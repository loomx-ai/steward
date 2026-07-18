package execution

import (
	"errors"
	"fmt"
)

var ErrInvalidTransition = errors.New("invalid action transition")

var actionTransitions = map[ActionStatus]map[ActionStatus]struct{}{
	ActionPending: {
		ActionIntentPersisted: {},
	},
	ActionIntentPersisted: {
		ActionInvoking: {},
		ActionSkipped:  {},
	},
	ActionInvoking: {
		ActionWaiting:     {},
		ActionReadingBack: {},
		ActionSkipped:     {},
		ActionFailed:      {},
	},
	ActionWaiting: {
		ActionReadingBack: {},
		ActionSkipped:     {},
		ActionFailed:      {},
	},
	ActionReadingBack: {
		ActionSucceeded:   {},
		ActionSkipped:     {},
		ActionFailed:      {},
		ActionReconciling: {},
	},
	ActionReconciling: {
		ActionSucceeded: {},
		ActionFailed:    {},
	},
}

func (a *ActionAttempt) Transition(next ActionStatus) error {
	allowed, ok := actionTransitions[a.Status]
	if !ok {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, a.Status, next)
	}
	if _, ok := allowed[next]; !ok {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, a.Status, next)
	}
	a.Status = next
	return nil
}
