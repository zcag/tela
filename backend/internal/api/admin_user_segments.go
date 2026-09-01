package api

import "time"

// admin_user_segments.go — the lifecycle label on each row of the admin People
// table. Counts alone make you derive "who is slipping away" and "who never got
// started" by eye, every single time; this does it once, server-side, so the
// same definition drives the column, the filter and the docs.
//
// Two deliberate choices:
//
//   - Segments read the LAST 30 DAYS regardless of the selected window. They
//     describe the person, not the view — otherwise switching to "all time"
//     would silently promote every long-dormant account to Power.
//   - Recency outranks volume. Someone who wrote 200 pages and hasn't appeared
//     in six weeks is Churned, not Power. That is the whole point of looking.

const (
	segNever   = "never"   // signed up, never did anything
	segChurned = "churned" // was active once, silent since
	segDabbler = "dabbler" // 1-3 active days in the last 30
	segRegular = "regular" // 4-11
	segPower   = "power"   // 12+ — roughly every other working day

	segChurnDays   = 30
	segPowerDays   = 12
	segRegularDays = 4
)

// classifySegment labels one account from its metrics plus lastActive (the most
// recent session touch; nil = never signed in). Edits are part of the
// "ever active" test on purpose: a sync or API user writes without ever holding
// a browser session, and filing them as "never started" would be flatly wrong.
func classifySegment(m *adminUserMetrics, lastActive *string, now time.Time) string {
	if m.Edits == 0 && m.DaysActive == 0 && lastActive == nil {
		return segNever
	}
	// Nothing in the last 30 days of activity, and no recent session either.
	quiet := m.Days30 == 0
	if quiet && lastActive != nil {
		if t, err := time.Parse("2006-01-02 15:04:05", *lastActive); err == nil {
			quiet = now.Sub(t) > segChurnDays*24*time.Hour
		}
	}
	if quiet {
		return segChurned
	}
	switch {
	case m.Days30 >= segPowerDays:
		return segPower
	case m.Days30 >= segRegularDays:
		return segRegular
	default:
		// Includes the account whose only recent trace is a sign-in — which is
		// exactly what a dabbler is.
		return segDabbler
	}
}
