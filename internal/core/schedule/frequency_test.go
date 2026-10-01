package schedule

import (
	"errors"
	"testing"
	"time"
)

func TestFrequencyKindsRunAtTheirLocalTimes(t *testing.T) {
	// 2026-10-01 20:00 in Shanghai.
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	shanghai, _ := time.LoadLocation("Asia/Shanghai")
	at := func(month time.Month, day, hour, minute int) time.Time {
		return time.Date(2026, month, day, hour, minute, 0, 0, shanghai).UTC()
	}
	for name, test := range map[string]struct {
		frequency Frequency
		want      []time.Time
	}{
		"every two hours from 00:30":  {Frequency{Kind: FrequencyHourly, EveryHours: 2, Time: "00:30", Timezone: "Asia/Shanghai"}, []time.Time{at(10, 1, 20, 30), at(10, 1, 22, 30), at(10, 2, 0, 30)}},
		"anchor after the first slot": {Frequency{Kind: FrequencyHourly, EveryHours: 6, Time: "15:00", Timezone: "Asia/Shanghai"}, []time.Time{at(10, 1, 21, 0), at(10, 2, 3, 0), at(10, 2, 9, 0)}},
		"daily":                       {Frequency{Kind: FrequencyDaily, Time: "03:12", Timezone: "Asia/Shanghai"}, []time.Time{at(10, 2, 3, 12), at(10, 3, 3, 12), at(10, 4, 3, 12)}},
		"weekly on Monday and Friday": {Frequency{Kind: FrequencyWeekly, Weekdays: []int{5, 1, 1}, Time: "04:00", Timezone: "Asia/Shanghai"}, []time.Time{at(10, 2, 4, 0), at(10, 5, 4, 0), at(10, 9, 4, 0)}},
		"monthly":                     {Frequency{Kind: FrequencyMonthly, MonthDay: 1, Time: "02:00", Timezone: "Asia/Shanghai"}, []time.Time{at(11, 1, 2, 0), at(12, 1, 2, 0), time.Date(2027, 1, 1, 2, 0, 0, 0, shanghai).UTC()}},
		"quarterly cron":              {Frequency{Kind: FrequencyCron, Cron: "0 2 1 */3 *", Timezone: "Asia/Shanghai"}, []time.Time{time.Date(2027, 1, 1, 2, 0, 0, 0, shanghai).UTC(), time.Date(2027, 4, 1, 2, 0, 0, 0, shanghai).UTC(), time.Date(2027, 7, 1, 2, 0, 0, 0, shanghai).UTC()}},
	} {
		t.Run(name, func(t *testing.T) {
			compiled, err := test.frequency.Compile(time.Hour, now)
			if err != nil {
				t.Fatal(err)
			}
			got := compiled.Upcoming(now, 3)
			for index := range test.want {
				if !got[index].Equal(test.want[index]) {
					t.Fatalf("Upcoming() = %v, want %v", got, test.want)
				}
			}
		})
	}
}

func TestFrequencyRejectsInvalidAndTooFrequentSchedules(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for name, test := range map[string]struct {
		frequency Frequency
		min       time.Duration
		want      error
	}{
		"descriptor":          {Frequency{Kind: FrequencyCron, Cron: "@every 1m", Timezone: "UTC"}, time.Hour, ErrFrequencyInvalid},
		"six fields":          {Frequency{Kind: FrequencyCron, Cron: "0 0 2 * * *", Timezone: "UTC"}, time.Hour, ErrFrequencyInvalid},
		"bad time":            {Frequency{Kind: FrequencyDaily, Time: "25:00", Timezone: "UTC"}, time.Hour, ErrFrequencyInvalid},
		"bad interval":        {Frequency{Kind: FrequencyHourly, EveryHours: 5, Time: "00:00", Timezone: "UTC"}, time.Hour, ErrFrequencyInvalid},
		"no weekdays":         {Frequency{Kind: FrequencyWeekly, Time: "00:00", Timezone: "UTC"}, time.Hour, ErrFrequencyInvalid},
		"day 31":              {Frequency{Kind: FrequencyMonthly, MonthDay: 31, Time: "00:00", Timezone: "UTC"}, time.Hour, ErrFrequencyInvalid},
		"unknown zone":        {Frequency{Kind: FrequencyDaily, Time: "00:00", Timezone: "Mars/Olympus"}, time.Hour, ErrFrequencyInvalid},
		"missing zone":        {Frequency{Kind: FrequencyDaily, Time: "00:00"}, time.Hour, ErrFrequencyInvalid},
		"every minute":        {Frequency{Kind: FrequencyCron, Cron: "* * * * *", Timezone: "UTC"}, time.Hour, ErrIntervalTooShort},
		"hourly under 6h":     {Frequency{Kind: FrequencyHourly, EveryHours: 4, Time: "00:00", Timezone: "UTC"}, 6 * time.Hour, ErrIntervalTooShort},
		"cron bunching runs":  {Frequency{Kind: FrequencyCron, Cron: "0 1,2 * * *", Timezone: "UTC"}, 6 * time.Hour, ErrIntervalTooShort},
		"never (February 30)": {Frequency{Kind: FrequencyCron, Cron: "0 0 30 2 *", Timezone: "UTC"}, time.Hour, ErrFrequencyInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := test.frequency.Compile(test.min, now); !errors.Is(err, test.want) {
				t.Fatalf("Compile() error = %v, want %v", err, test.want)
			}
		})
	}
	if _, err := (Frequency{Kind: FrequencyHourly, EveryHours: 6, Time: "00:00", Timezone: "UTC"}).Compile(6*time.Hour, now); err != nil {
		t.Fatalf("every six hours must satisfy a six-hour minimum: %v", err)
	}
}
