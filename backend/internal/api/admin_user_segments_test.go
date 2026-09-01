package api

import (
	"testing"
	"time"
)

func TestClassifySegment(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	ts := func(daysAgo int) *string {
		s := now.AddDate(0, 0, -daysAgo).Format("2006-01-02 15:04:05")
		return &s
	}

	cases := []struct {
		name       string
		m          adminUserMetrics
		lastActive *string
		want       string
	}{
		{"signed up and vanished", adminUserMetrics{}, nil, segNever},
		{"signed in once, nothing since", adminUserMetrics{}, ts(90), segChurned},
		{"one look this month", adminUserMetrics{Days30: 2, DaysActive: 2}, ts(3), segDabbler},
		{"in most weeks", adminUserMetrics{Days30: 6, DaysActive: 20}, ts(1), segRegular},
		{"lives in it", adminUserMetrics{Days30: 18, DaysActive: 60}, ts(0), segPower},
		// Recency outranks volume: a prolific author who stopped is churned, not
		// power — that's the row worth acting on.
		{"prolific but gone", adminUserMetrics{Edits: 4000, DaysActive: 40, Days30: 0}, ts(60), segChurned},
		// A sync/API writer never holds a session, so "never signed in" must not
		// be read as "never started".
		{"writes over sync only", adminUserMetrics{Edits: 300, DaysActive: 9, Days30: 9}, nil, segRegular},
		// A session touch with no recorded activity is still a person showing up.
		{"signed in, did nothing visible", adminUserMetrics{}, ts(2), segDabbler},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifySegment(&c.m, c.lastActive, now); got != c.want {
				t.Fatalf("classifySegment = %q, want %q (metrics %+v)", got, c.want, c.m)
			}
		})
	}
}

// The sparkline, the trend delta and the cohort grid all index into `weeks` by
// position, so the axis must be dense, Monday-aligned and end on the current week.
func TestWeekAxis(t *testing.T) {
	// A Thursday — the axis should end on that week's Monday.
	now := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	axis, idx := weekAxis(now)
	if len(axis) != adminUserWeeks {
		t.Fatalf("axis length = %d, want %d", len(axis), adminUserWeeks)
	}
	if axis[len(axis)-1] != "2026-08-31" {
		t.Fatalf("last bucket = %q, want the Monday of the current week (2026-08-31)", axis[len(axis)-1])
	}
	for i, d := range axis {
		day, err := time.Parse("2006-01-02", d)
		if err != nil {
			t.Fatalf("bucket %d not a date: %q", i, d)
		}
		if day.Weekday() != time.Monday {
			t.Fatalf("bucket %d (%s) is a %s, want Monday — Postgres date_trunc('week') is Monday-based", i, d, day.Weekday())
		}
		if idx[d] != i {
			t.Fatalf("idx[%s] = %d, want %d", d, idx[d], i)
		}
	}
}
