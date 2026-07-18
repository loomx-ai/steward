package execution_test

import (
	"errors"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
)

func TestActionPersistsIntentBeforeProviderCall(t *testing.T) {
	attempt := execution.ActionAttempt{Status: execution.ActionPending}
	if err := attempt.Transition(execution.ActionInvoking); !errors.Is(err, execution.ErrInvalidTransition) {
		t.Fatalf("pending action must pass through intent_persisted, got %v", err)
	}
	if err := attempt.Transition(execution.ActionIntentPersisted); err != nil {
		t.Fatal(err)
	}
	if err := attempt.Transition(execution.ActionInvoking); err != nil {
		t.Fatal(err)
	}
}

func TestActionCannotLeaveTerminalState(t *testing.T) {
	attempt := execution.ActionAttempt{Status: execution.ActionSucceeded}
	if err := attempt.Transition(execution.ActionReconciling); !errors.Is(err, execution.ErrInvalidTransition) {
		t.Fatalf("terminal action must not transition, got %v", err)
	}
}

func TestActionCanSkipFromPersistedIntent(t *testing.T) {
	attempt := execution.ActionAttempt{Status: execution.ActionIntentPersisted}
	if err := attempt.Transition(execution.ActionSkipped); err != nil {
		t.Fatal(err)
	}
	if err := attempt.Transition(execution.ActionInvoking); !errors.Is(err, execution.ErrInvalidTransition) {
		t.Fatalf("skipped action must be terminal, got %v", err)
	}
}

func TestActionCanReconcileAfterReadback(t *testing.T) {
	attempt := execution.ActionAttempt{Status: execution.ActionReadingBack}
	if err := attempt.Transition(execution.ActionReconciling); err != nil {
		t.Fatal(err)
	}
	if err := attempt.Transition(execution.ActionFailed); err != nil {
		t.Fatal(err)
	}
}
