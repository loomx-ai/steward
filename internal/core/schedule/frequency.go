package schedule

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	// Schedules name IANA zones; embed the database so minimal hosts without
	// zoneinfo (static Linux builds, Windows) can still resolve them.
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
)

type FrequencyKind string

const (
	FrequencyHourly  FrequencyKind = "hourly"
	FrequencyDaily   FrequencyKind = "daily"
	FrequencyWeekly  FrequencyKind = "weekly"
	FrequencyMonthly FrequencyKind = "monthly"
	FrequencyCron    FrequencyKind = "cron"
)

// HourlyIntervals divide a day evenly, so "every N hours from HH:MM" lands on
// the same times every day.
var HourlyIntervals = []int{1, 2, 3, 4, 6, 8, 12}

// Frequency is when a schedule runs, in its own time zone.
type Frequency struct {
	Kind FrequencyKind `json:"kind"`
	// EveryHours is the interval for hourly schedules.
	EveryHours int `json:"every_hours,omitempty"`
	// Time is HH:MM. Hourly schedules use it as the first run of the day.
	Time string `json:"time,omitempty"`
	// Weekdays use 0 for Sunday through 6 for Saturday.
	Weekdays []int `json:"weekdays,omitempty"`
	// MonthDay is 1–28 so every month has it.
	MonthDay int    `json:"month_day,omitempty"`
	Cron     string `json:"cron,omitempty"`
	Timezone string `json:"timezone"`
}

var (
	ErrFrequencyInvalid = errors.New("schedule frequency is invalid")
	ErrIntervalTooShort = errors.New("schedule runs more often than allowed")
)

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// Compiled is a validated frequency that can answer when it runs next.
type Compiled struct {
	schedule cron.Schedule
	location *time.Location
}

func (c Compiled) Next(after time.Time) time.Time {
	return c.schedule.Next(after.In(c.location)).UTC()
}

// Upcoming lists the next count run times after the given time.
func (c Compiled) Upcoming(after time.Time, count int) []time.Time {
	result := make([]time.Time, 0, count)
	for len(result) < count {
		after = c.Next(after)
		if after.IsZero() {
			break
		}
		result = append(result, after)
	}
	return result
}

// ShortestInterval looks a year ahead for the closest two consecutive runs.
func (c Compiled) ShortestInterval(from time.Time) time.Duration {
	shortest := time.Duration(0)
	previous := c.Next(from)
	limit := from.AddDate(1, 0, 0)
	for steps := 0; steps < 2000 && !previous.IsZero() && previous.Before(limit); steps++ {
		next := c.Next(previous)
		if next.IsZero() {
			break
		}
		if gap := next.Sub(previous); shortest == 0 || gap < shortest {
			shortest = gap
		}
		previous = next
	}
	return shortest
}

// Compile validates the frequency and enforces the minimum time between runs.
func (f Frequency) Compile(minInterval time.Duration, now time.Time) (Compiled, error) {
	expression, err := f.expression()
	if err != nil {
		return Compiled{}, err
	}
	if strings.TrimSpace(f.Timezone) == "" {
		return Compiled{}, fmt.Errorf("%w: a time zone is required", ErrFrequencyInvalid)
	}
	location, err := time.LoadLocation(f.Timezone)
	if err != nil {
		return Compiled{}, fmt.Errorf("%w: unknown time zone %q", ErrFrequencyInvalid, f.Timezone)
	}
	parsed, err := cronParser.Parse(expression)
	if err != nil {
		return Compiled{}, fmt.Errorf("%w: %v", ErrFrequencyInvalid, err)
	}
	compiled := Compiled{schedule: parsed, location: location}
	if compiled.Next(now).IsZero() {
		return Compiled{}, fmt.Errorf("%w: the schedule never runs", ErrFrequencyInvalid)
	}
	if shortest := compiled.ShortestInterval(now); minInterval > 0 && shortest > 0 && shortest < minInterval {
		return Compiled{}, fmt.Errorf("%w: runs can be %s apart, the minimum is %s", ErrIntervalTooShort, shortest, minInterval)
	}
	return compiled, nil
}

func (f Frequency) expression() (string, error) {
	invalid := func(format string, values ...any) (string, error) {
		return "", fmt.Errorf("%w: "+format, append([]any{ErrFrequencyInvalid}, values...)...)
	}
	if f.Kind == FrequencyCron {
		expression := strings.Join(strings.Fields(f.Cron), " ")
		if len(strings.Fields(expression)) != 5 {
			return invalid("a cron expression needs five fields: minute hour day-of-month month day-of-week")
		}
		return expression, nil
	}
	hour, minute, err := clock(f.Time)
	if err != nil {
		return invalid("time must be HH:MM")
	}
	switch f.Kind {
	case FrequencyHourly:
		if !slices.Contains(HourlyIntervals, f.EveryHours) {
			return invalid("every_hours must be one of %v", HourlyIntervals)
		}
		return fmt.Sprintf("%d %d/%d * * *", minute, hour%f.EveryHours, f.EveryHours), nil
	case FrequencyDaily:
		return fmt.Sprintf("%d %d * * *", minute, hour), nil
	case FrequencyWeekly:
		if len(f.Weekdays) == 0 {
			return invalid("pick at least one weekday")
		}
		days := slices.Clone(f.Weekdays)
		slices.Sort(days)
		days = slices.Compact(days)
		parts := make([]string, 0, len(days))
		for _, day := range days {
			if day < 0 || day > 6 {
				return invalid("weekdays must be 0 (Sunday) to 6 (Saturday)")
			}
			parts = append(parts, strconv.Itoa(day))
		}
		return fmt.Sprintf("%d %d * * %s", minute, hour, strings.Join(parts, ",")), nil
	case FrequencyMonthly:
		if f.MonthDay < 1 || f.MonthDay > 28 {
			return invalid("month_day must be 1 to 28")
		}
		return fmt.Sprintf("%d %d %d * *", minute, hour, f.MonthDay), nil
	default:
		return invalid("unknown kind %q", f.Kind)
	}
}

func clock(value string) (int, int, error) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(value))
	if err != nil {
		return 0, 0, err
	}
	return parsed.Hour(), parsed.Minute(), nil
}
