package main

import "time"

// Projection of fixed-cycle quota windows (ADR 0004): the provider-reported
// used percent supplies the level, the usage profile's per-occurrence rates
// supply the shape, and walking those rates over the cycle's actual calendar
// hours yields the expected fraction that replaces the naive wall-clock ratio.

const (
	basisProfile = "profile"
	basisUniform = "uniform"

	verdictOnTrack     = "on_track"
	verdictTight       = "tight"
	verdictWillExhaust = "will_exhaust"

	confidenceLow    = "low"
	confidenceMedium = "medium"
	confidenceHigh   = "high"

	// projectionMinRecords is the cold-start floor: below this many observed
	// usage records the profile has no standing and the basis stays uniform.
	projectionMinRecords = 20
	// projectionMinCoverageNum/Den express the token-coverage guard: fewer
	// than half the records carrying token detail forces the uniform basis so
	// degradation is visible rather than silent (CONTEXT.md: token coverage).
	projectionMinCoverageNum = 1
	projectionMinCoverageDen = 2

	// Confidence grades by distinct observed days — not event counts
	// (CONTEXT.md: confidence). A week of days earns medium; three weeks,
	// enough to see both day types several times over, earns high.
	confidenceMediumDays = 7
	confidenceHighDays   = 21
)

// slidingWindowKeys marks provider windows that measure a trailing period with
// no fixed cycle start (CONTEXT.md: sliding window). They are never projected
// even though they may carry a duration and a release instant.
var slidingWindowKeys = map[string]bool{
	openCodeGoProvider + "/rolling": true,
}

// uniformRates is the flat shape backing the uniform basis: every calendar
// hour expects the same consumption, which collapses the projection exactly to
// the naive clock-ratio method.
var uniformRates = func() [2][24]float64 {
	var rates [2][24]float64
	for d := range rates {
		for h := range rates[d] {
			rates[d][h] = 1
		}
	}
	return rates
}()

// usageProfile is the provider-level rollup of day-grain usage buckets: a
// per-occurrence rate for each (day type, hour) cell plus the evidence
// counters that gate the basis and grade confidence.
type usageProfile struct {
	rates          [2][24]float64
	recordsSeen    int64
	recordsTokened int64
	distinctDays   int
}

// eligible reports whether the profile clears the cold-start floor and the
// token-coverage guard; failing either forces the uniform basis.
func (p *usageProfile) eligible() bool {
	return p.recordsSeen >= projectionMinRecords &&
		p.recordsTokened*projectionMinCoverageDen >= p.recordsSeen*projectionMinCoverageNum
}

func (p *usageProfile) confidence() string {
	switch {
	case p.distinctDays >= confidenceHighDays:
		return confidenceHigh
	case p.distinctDays >= confidenceMediumDays:
		return confidenceMedium
	default:
		return confidenceLow
	}
}

func dayTypeIndexOf(local time.Time) int {
	if weekday := local.Weekday(); weekday == time.Saturday || weekday == time.Sunday {
		return 1
	}
	return 0
}

func dayTypeIndexByName(name string) (int, bool) {
	switch name {
	case dayTypeWeekday:
		return 0, true
	case dayTypeWeekend:
		return 1, true
	}
	return 0, false
}

// profileRetentionStart is the earliest instant day-grain rows can cover:
// local midnight of the oldest retained date (the prune cutoff keeps whole
// dates, so coverage starts at that date's midnight in the profile timezone).
func profileRetentionStart(now time.Time, loc *time.Location) time.Time {
	local := now.In(loc).AddDate(0, 0, -profileRetentionDays)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
}

// slotOccurrences counts how many times each (day type, hour) slot elapsed in
// the retention window ending at now — the denominator of the per-occurrence
// rate, which includes zero-usage slots by construction (CONTEXT.md:
// per-occurrence rate). Walking real instants keeps DST days at their true
// 23/25 hour lengths.
func slotOccurrences(now time.Time, loc *time.Location) [2][24]float64 {
	var occurrences [2][24]float64
	for t := profileRetentionStart(now, loc); t.Before(now); t = t.Add(time.Hour) {
		local := t.In(loc)
		occurrences[dayTypeIndexOf(local)][local.Hour()]++
	}
	return occurrences
}

// buildUsageProfiles rolls stored day-grain bucket rows up to provider level
// (ADR 0004: profiles roll up at read time). Rates divide each cell's token
// sum by its calendar occurrences — never by observed rows — so imbalanced
// weekday/weekend observation counts cannot skew the shape the way raw
// display weights would.
func buildUsageProfiles(rows []usageBucketRow, now time.Time, loc *time.Location) map[string]*usageProfile {
	if len(rows) == 0 {
		return nil
	}
	occurrences := slotOccurrences(now, loc)
	profiles := make(map[string]*usageProfile)
	sums := make(map[string]*[2][24]float64)
	days := make(map[string]map[string]bool)
	for _, row := range rows {
		dayType, ok := dayTypeIndexByName(row.DayType)
		if !ok || row.Hour < 0 || row.Hour > 23 {
			continue
		}
		profile := profiles[row.Provider]
		if profile == nil {
			profile = &usageProfile{}
			profiles[row.Provider] = profile
			sums[row.Provider] = &[2][24]float64{}
			days[row.Provider] = make(map[string]bool)
		}
		sums[row.Provider][dayType][row.Hour] += float64(row.TokenSum)
		profile.recordsSeen += row.RecordsSeen
		profile.recordsTokened += row.RecordsTokened
		days[row.Provider][row.Date] = true
	}
	for provider, profile := range profiles {
		profile.distinctDays = len(days[provider])
		for dayType := range profile.rates {
			for hour := range profile.rates[dayType] {
				if occurrences[dayType][hour] > 0 {
					profile.rates[dayType][hour] = sums[provider][dayType][hour] / occurrences[dayType][hour]
				}
			}
		}
	}
	return profiles
}

// nextWallHour is the first wall-clock hour boundary in loc after t.
func nextWallHour(t time.Time, loc *time.Location) time.Time {
	local := t.In(loc)
	next := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, loc).Add(time.Hour)
	if !next.After(t) {
		// Defensive: never stall the walk on a pathological zone transition.
		next = t.Add(time.Hour)
	}
	return next
}

// integrateRates integrates the per-occurrence rate over [from, to) by walking
// the interval's actual calendar hours in loc, weighting partial hours by
// their elapsed fraction.
func integrateRates(rates *[2][24]float64, from, to time.Time, loc *time.Location) float64 {
	total := 0.0
	for t := from; t.Before(to); {
		next := nextWallHour(t, loc)
		if next.After(to) {
			next = to
		}
		local := t.In(loc)
		total += rates[dayTypeIndexOf(local)][local.Hour()] * next.Sub(t).Hours()
		t = next
	}
	return total
}

// projectedExhaustion runs the rate walk forward from `from` until `needed`
// more expected-consumption units accrue, returning the crossing instant, or
// nil when the cycle resets first.
func projectedExhaustion(rates *[2][24]float64, from, resetAt time.Time, loc *time.Location, needed float64) *time.Time {
	if needed <= 0 {
		crossing := from
		return &crossing
	}
	for t := from; t.Before(resetAt); {
		next := nextWallHour(t, loc)
		if next.After(resetAt) {
			next = resetAt
		}
		local := t.In(loc)
		rate := rates[dayTypeIndexOf(local)][local.Hour()]
		segment := rate * next.Sub(t).Hours()
		if segment >= needed && rate > 0 {
			crossing := t.Add(time.Duration(needed / rate * float64(time.Hour)))
			return &crossing
		}
		needed -= segment
		t = next
	}
	return nil
}

func verdictFor(projected float64) string {
	switch {
	case projected > 100:
		return verdictWillExhaust
	case projected >= 90:
		return verdictTight
	default:
		return verdictOnTrack
	}
}

// computeWindowProjection projects one fixed-cycle window. It returns nil when
// the window has no derivable cycle start (missing duration or reset) or no
// reported level. The uniform basis sets expected_fraction to the elapsed
// fraction itself, so the projection collapses exactly to the naive
// clock-ratio method — there is no separate insufficient-data path (ADR 0004).
func computeWindowProjection(window quotaWindow, profile *usageProfile, now time.Time, loc *time.Location) *windowProjection {
	if window.UsedPercent == nil || window.WindowSeconds <= 0 || window.ResetAt == nil {
		return nil
	}
	resetAt := window.ResetAt.UTC()
	start := resetAt.Add(-time.Duration(window.WindowSeconds) * time.Second)
	clamped := now
	if clamped.Before(start) {
		clamped = start
	}
	if clamped.After(resetAt) {
		clamped = resetAt
	}
	elapsedFraction := float64(clamped.Sub(start)) / float64(resetAt.Sub(start))
	used := *window.UsedPercent

	basis := basisUniform
	confidence := confidenceLow
	rates := &uniformRates
	expectedFraction := elapsedFraction
	if profile != nil {
		confidence = profile.confidence()
		if profile.eligible() {
			if total := integrateRates(&profile.rates, start, resetAt, loc); total > 0 {
				basis = basisProfile
				rates = &profile.rates
				expectedFraction = integrateRates(&profile.rates, start, clamped, loc) / total
			}
		}
	}

	// used × (expected cycle total) / (expected so far); when nothing was
	// expected yet the pace is unknowable and the level itself is reported.
	projected := used
	if expectedFraction > 0 {
		projected = used / expectedFraction
	}
	naive := used
	if elapsedFraction > 0 {
		naive = used / elapsedFraction
	}

	projection := &windowProjection{
		ElapsedFraction:       elapsedFraction,
		ExpectedFraction:      expectedFraction,
		ProjectedUsedPercent:  projected,
		NaiveProjectedPercent: naive,
		Verdict:               verdictFor(projected),
		Confidence:            confidence,
		Basis:                 basis,
	}
	if projected > 100 && used > 0 {
		// Consumption is modeled as proportional to the expected-consumption
		// integral, so 100% falls where the integral reaches (100/used) times
		// its value at now; walk the remainder forward from now.
		soFar := integrateRates(rates, start, clamped, loc)
		needed := soFar * (100 - used) / used
		projection.ProjectedExhaustionAt = projectedExhaustion(rates, clamped, resetAt, loc, needed)
	}
	return projection
}

// attachProjections decorates every fixed-cycle window carrying a derivable
// cycle start with an additive projection. Sliding windows and credit pools
// are left untouched; a nil store (health disabled) degrades every basis to
// uniform rather than dropping the object.
func attachProjections(accounts []accountQuota, store *healthStore, cfg pluginConfig, now time.Time) {
	loc := cfg.profileLocation()
	var profiles map[string]*usageProfile
	if store != nil {
		if rows, err := store.usageBuckets(); err == nil {
			profiles = buildUsageProfiles(rows, now, loc)
		}
	}
	for accountIndex := range accounts {
		account := &accounts[accountIndex]
		for windowIndex := range account.Windows {
			window := &account.Windows[windowIndex]
			if slidingWindowKeys[account.Provider+"/"+window.ID] {
				continue
			}
			window.Projection = computeWindowProjection(*window, profiles[account.Provider], now, loc)
		}
	}
}
