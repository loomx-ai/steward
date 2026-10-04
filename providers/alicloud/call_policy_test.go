package alicloud

import (
	"errors"
	"testing"
	"time"
)

// Throttling retries longer and backs off from a second; other transient
// failures keep their short retries.
func TestThrottledQueriesBackOffLongerThanTransientFailures(t *testing.T) {
	throttled := &APIError{Code: "Throttling.User", Message: "slow down", StatusCode: 429}
	transient := errors.New("connection reset")
	if queryRetryLimit(cloudProductQueryRetryCount, throttled) != throttledQueryRetryCount || queryRetryLimit(cloudProductQueryRetryCount, transient) != cloudProductQueryRetryCount {
		t.Fatal("retry limits")
	}
	within := func(got, want time.Duration) bool { return got >= want*3/4 && got <= want*5/4 }
	for attempt, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 20 * time.Second} {
		if got := queryRetryDelay(attempt, throttled); !within(got, want) {
			t.Errorf("throttled attempt %d delay %s, want about %s", attempt, got, want)
		}
	}
	if got := queryRetryDelay(0, transient); !within(got, cloudProductQueryRetryDelay) {
		t.Errorf("transient delay %s", got)
	}
}
