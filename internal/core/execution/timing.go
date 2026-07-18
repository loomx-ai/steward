package execution

import (
	"sort"
	"time"
)

// ActiveDuration merges worker run intervals across jobs so parallel work is
// counted once. The boolean reports whether timing data has been recorded.
func ActiveDuration(jobs []Job, now time.Time) (time.Duration, bool) {
	type interval struct {
		start time.Time
		end   time.Time
	}
	intervals := make([]interval, 0)
	for _, job := range jobs {
		for _, run := range job.RunIntervals {
			if run.StartedAt.IsZero() {
				continue
			}
			end := now
			if run.FinishedAt != nil {
				end = *run.FinishedAt
			} else if job.Status != JobRunning {
				end = job.UpdatedAt
			}
			if end.Before(run.StartedAt) {
				end = run.StartedAt
			}
			intervals = append(intervals, interval{start: run.StartedAt, end: end})
		}
	}
	if len(intervals) == 0 {
		return 0, false
	}
	sort.Slice(intervals, func(i, j int) bool {
		if intervals[i].start.Equal(intervals[j].start) {
			return intervals[i].end.Before(intervals[j].end)
		}
		return intervals[i].start.Before(intervals[j].start)
	})
	start, end := intervals[0].start, intervals[0].end
	var total time.Duration
	for _, current := range intervals[1:] {
		if !current.start.After(end) {
			if current.end.After(end) {
				end = current.end
			}
			continue
		}
		total += end.Sub(start)
		start, end = current.start, current.end
	}
	return total + end.Sub(start), true
}

func LatestJobUpdate(jobs []Job, fallback time.Time) time.Time {
	latest := fallback
	for _, job := range jobs {
		if job.UpdatedAt.After(latest) {
			latest = job.UpdatedAt
		}
	}
	return latest
}
