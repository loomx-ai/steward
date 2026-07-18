package execution

import (
	"testing"
	"time"
)

func TestActiveDurationMergesParallelRunsAndExcludesWaits(t *testing.T) {
	base := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	at := func(offset time.Duration) *time.Time {
		value := base.Add(offset)
		return &value
	}
	jobs := []Job{
		{RunIntervals: []JobRunInterval{
			{StartedAt: base, FinishedAt: at(10 * time.Second)},
			{StartedAt: base.Add(30 * time.Second), FinishedAt: at(40 * time.Second)},
		}},
		{RunIntervals: []JobRunInterval{
			{StartedAt: base.Add(5 * time.Second), FinishedAt: at(15 * time.Second)},
			{StartedAt: base.Add(35 * time.Second), FinishedAt: at(45 * time.Second)},
		}},
	}

	got, recorded := ActiveDuration(jobs, base.Add(time.Minute))
	if !recorded || got != 30*time.Second {
		t.Fatalf("ActiveDuration() = %s, %v; want 30s, true", got, recorded)
	}
}

func TestActiveDurationIncludesOpenRunningInterval(t *testing.T) {
	base := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	got, recorded := ActiveDuration([]Job{{
		Status:       JobRunning,
		RunIntervals: []JobRunInterval{{StartedAt: base}},
	}}, base.Add(7*time.Second))
	if !recorded || got != 7*time.Second {
		t.Fatalf("ActiveDuration() = %s, %v; want 7s, true", got, recorded)
	}
}
