package main

import (
	"math"
	"testing"
	"time"
)

// Fixed instants for projection math: the cycle is the calendar week
// 2026-08-31 (Monday) 00:00 UTC → 2026-09-07 (Monday) 00:00 UTC.
var (
	cycleStart = time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	cycleReset = time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
)

func sevenDayWindow(used float64) quotaWindow {
	reset := cycleReset
	return quotaWindow{ID: "seven_day", UsedPercent: &used, ResetAt: &reset, WindowSeconds: 7 * 24 * 60 * 60}
}

// calendarRows synthesizes day-grain bucket rows by walking the same calendar
// hours the profile rollup counts occurrences over, so a constant tokensAt
// value yields exactly that per-occurrence rate.
func calendarRows(provider string, now time.Time, loc *time.Location, tokensAt func(local time.Time) int64) []usageBucketRow {
	var rows []usageBucketRow
	for t := profileRetentionStart(now, loc); t.Before(now); t = t.Add(time.Hour) {
		tokens := tokensAt(t.In(loc))
		if tokens <= 0 {
			continue
		}
		key := bucketFor(t, loc)
		rows = append(rows, usageBucketRow{Provider: provider, AuthIndex: "a", DayType: key.DayType, Hour: key.Hour, Date: key.Date, TokenSum: tokens, RecordsSeen: 1, RecordsTokened: 1})
	}
	return rows
}

func TestUniformBasisCollapsesToNaiveClockRatio(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC) // half the cycle elapsed
	cases := []struct {
		name           string
		used           float64
		wantVerdict    string
		wantExhaustion *time.Time
	}{
		{"on track", 40, "on_track", nil},
		{"tight lower bound", 45, "tight", nil},
		{"exactly 100 resets before crossing", 50, "tight", nil},
		{"will exhaust", 60, "will_exhaust", utcMinute(2026, 9, 5, 20, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projection := computeWindowProjection(sevenDayWindow(tc.used), nil, now, time.UTC)
			if projection == nil {
				t.Fatal("projection = nil")
			}
			if projection.Basis != "uniform" || projection.Confidence != "low" {
				t.Fatalf("basis = %q, confidence = %q", projection.Basis, projection.Confidence)
			}
			if projection.ElapsedFraction != 0.5 {
				t.Fatalf("elapsed_fraction = %v, want 0.5", projection.ElapsedFraction)
			}
			// Uniform must collapse exactly to the naive clock method.
			if projection.ExpectedFraction != projection.ElapsedFraction {
				t.Fatalf("expected_fraction = %v, elapsed_fraction = %v; want exact equality", projection.ExpectedFraction, projection.ElapsedFraction)
			}
			if projection.ProjectedUsedPercent != projection.NaiveProjectedPercent {
				t.Fatalf("projected = %v, naive = %v; want exact equality", projection.ProjectedUsedPercent, projection.NaiveProjectedPercent)
			}
			if projection.NaiveProjectedPercent != tc.used*2 {
				t.Fatalf("naive = %v, want %v", projection.NaiveProjectedPercent, tc.used*2)
			}
			if projection.Verdict != tc.wantVerdict {
				t.Fatalf("verdict = %q, want %q", projection.Verdict, tc.wantVerdict)
			}
			if tc.wantExhaustion == nil {
				if projection.ProjectedExhaustionAt != nil {
					t.Fatalf("exhaustion = %v, want nil", projection.ProjectedExhaustionAt)
				}
			} else if projection.ProjectedExhaustionAt == nil || !within(projection.ProjectedExhaustionAt.Sub(*tc.wantExhaustion), time.Second) {
				t.Fatalf("exhaustion = %v, want %v", projection.ProjectedExhaustionAt, tc.wantExhaustion)
			}
		})
	}
}

func utcMinute(year int, month time.Month, day, hour, minute int) *time.Time {
	instant := time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
	return &instant
}

func within(d, tolerance time.Duration) bool {
	if d < 0 {
		d = -d
	}
	return d <= tolerance
}

func TestProjectionOmittedForSlidingWindowsAndCreditPools(t *testing.T) {
	used := 40.0
	rollingReset := time.Date(2026, 9, 5, 3, 0, 0, 0, time.UTC)
	weekly := sevenDayWindow(used)
	weekly.ID = "weekly"
	sevenDay := sevenDayWindow(used)
	accounts := []accountQuota{
		{Provider: openCodeGoProvider, Windows: []quotaWindow{
			{ID: "rolling", UsedPercent: &used, ResetAt: &rollingReset, WindowSeconds: 18000},
			weekly,
		}},
		{Provider: "claude", Windows: []quotaWindow{
			sevenDay,
			{ID: "five_hour", UsedPercent: &used, WindowSeconds: 18000}, // no reset: start not derivable
			{ID: "extra", UsedPercent: &used},
		}},
	}
	cfg := defaultConfig()
	cfg.profileLoc = time.UTC

	attachProjections(accounts, nil, cfg, time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC))

	if projection := accounts[0].Windows[0].Projection; projection != nil {
		t.Fatalf("opencode-go rolling projection = %#v, want nil (sliding window)", projection)
	}
	if accounts[0].Windows[1].Projection == nil {
		t.Fatal("opencode-go weekly projection missing")
	}
	if accounts[1].Windows[0].Projection == nil {
		t.Fatal("claude seven_day projection missing")
	}
	if projection := accounts[1].Windows[1].Projection; projection != nil {
		t.Fatalf("claude five_hour without reset projection = %#v, want nil", projection)
	}
	if projection := accounts[1].Windows[2].Projection; projection != nil {
		t.Fatalf("claude extra projection = %#v, want nil (credit pool)", projection)
	}
}

// skewedProfile expects twice the consumption on weekday hours vs weekend
// hours, so its expected fraction visibly diverges from the clock.
func skewedProfile(seen, tokened int64, distinctDays int) *usageProfile {
	profile := &usageProfile{recordsSeen: seen, recordsTokened: tokened, distinctDays: distinctDays}
	for hour := 0; hour < 24; hour++ {
		profile.rates[0][hour] = 2
		profile.rates[1][hour] = 1
	}
	return profile
}

func TestTokenCoverageGuardAndColdStartForceUniformBasis(t *testing.T) {
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC) // Saturday 00:00: 5 of 7 days elapsed
	cases := []struct {
		name      string
		profile   *usageProfile
		wantBasis string
	}{
		{"coverage below half", skewedProfile(20, 9, 25), "uniform"},
		{"coverage at half passes", skewedProfile(20, 10, 25), "profile"},
		{"cold start below record floor", skewedProfile(19, 19, 25), "uniform"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projection := computeWindowProjection(sevenDayWindow(40), tc.profile, now, time.UTC)
			if projection == nil {
				t.Fatal("projection = nil")
			}
			if projection.Basis != tc.wantBasis {
				t.Fatalf("basis = %q, want %q", projection.Basis, tc.wantBasis)
			}
			// Degradation stays visible: the guard downgrades the basis, not
			// the confidence grade, which still reflects observed history.
			if projection.Confidence != "high" {
				t.Fatalf("confidence = %q, want high", projection.Confidence)
			}
			if tc.wantBasis == "uniform" {
				if projection.ExpectedFraction != projection.ElapsedFraction || projection.ProjectedUsedPercent != projection.NaiveProjectedPercent {
					t.Fatalf("uniform projection did not collapse to naive: %#v", projection)
				}
				return
			}
			// 120 weekday hours at rate 2 elapsed, of 240 weekday-hour units
			// + 48 weekend-hour units in the cycle.
			wantExpected := 240.0 / 288.0
			if math.Abs(projection.ExpectedFraction-wantExpected) > 1e-9 {
				t.Fatalf("expected_fraction = %v, want %v", projection.ExpectedFraction, wantExpected)
			}
			if projection.ExpectedFraction == projection.ElapsedFraction {
				t.Fatal("profile basis unexpectedly equals the clock ratio")
			}
		})
	}
}

func TestPerOccurrenceRatesNotDisplayWeights(t *testing.T) {
	// A user with a perfectly flat schedule: the same tokens every calendar
	// hour, weekday and weekend alike. The retention window still observes
	// ~22 weekday vs ~8 weekend days, so raw cell sums (display weights) are
	// weekday-inflated; per-occurrence rates must cancel that imbalance and
	// reproduce the clock ratio.
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC) // Saturday 00:00
	rows := calendarRows("claude", now, time.UTC, func(time.Time) int64 { return 100 })
	profiles := buildUsageProfiles(rows, now, time.UTC)

	projection := computeWindowProjection(sevenDayWindow(40), profiles["claude"], now, time.UTC)
	if projection == nil || projection.Basis != "profile" {
		t.Fatalf("projection = %#v, want profile basis", projection)
	}
	wantFlat := 5.0 / 7.0
	if math.Abs(projection.ExpectedFraction-wantFlat) > 1e-9 {
		t.Fatalf("expected_fraction = %v, want %v (flat schedule)", projection.ExpectedFraction, wantFlat)
	}

	// The wrong method: walk raw cell sums over the same cycle.
	var sums [2][24]float64
	for _, row := range rows {
		index, _ := dayTypeIndexByName(row.DayType)
		sums[index][row.Hour] += float64(row.TokenSum)
	}
	displayWeight := integrateRates(&sums, cycleStart, now, time.UTC) / integrateRates(&sums, cycleStart, cycleReset, time.UTC)
	if math.Abs(displayWeight-wantFlat) < 0.05 {
		t.Fatalf("display-weight fraction = %v: fixture lost its weekday/weekend imbalance", displayWeight)
	}
	if math.Abs(projection.ExpectedFraction-displayWeight) < 0.05 {
		t.Fatalf("expected_fraction = %v tracks display weights (%v)", projection.ExpectedFraction, displayWeight)
	}
}

func workdayTokens(local time.Time) int64 {
	if dayTypeIndexOf(local) == 0 && local.Hour() >= 9 && local.Hour() < 17 {
		return 1000
	}
	return 0
}

func TestWeekdayConcentratedProfileBeatsClockMidWorkday(t *testing.T) {
	// Wednesday 13:00: 20 of the cycle's 40 working hours are behind us, but
	// only 61 of its 168 wall-clock hours.
	now := time.Date(2026, 9, 2, 13, 0, 0, 0, time.UTC)
	rows := calendarRows("claude", now, time.UTC, workdayTokens)
	profiles := buildUsageProfiles(rows, now, time.UTC)

	projection := computeWindowProjection(sevenDayWindow(40), profiles["claude"], now, time.UTC)
	if projection == nil || projection.Basis != "profile" {
		t.Fatalf("projection = %#v, want profile basis", projection)
	}
	if math.Abs(projection.ExpectedFraction-0.5) > 1e-9 {
		t.Fatalf("expected_fraction = %v, want 0.5", projection.ExpectedFraction)
	}
	if projection.ExpectedFraction <= projection.ElapsedFraction {
		t.Fatalf("expected_fraction = %v not above elapsed_fraction = %v", projection.ExpectedFraction, projection.ElapsedFraction)
	}
	if math.Abs(projection.ProjectedUsedPercent-80) > 1e-9 {
		t.Fatalf("projected = %v, want 80", projection.ProjectedUsedPercent)
	}
	// The naive clock sees the same 40% as runaway pace; the profile knows it
	// is a normal workday and stays on_track while naive would exhaust.
	if projection.NaiveProjectedPercent <= 100 || projection.Verdict != "on_track" {
		t.Fatalf("naive = %v, verdict = %q", projection.NaiveProjectedPercent, projection.Verdict)
	}
	if projection.ProjectedExhaustionAt != nil {
		t.Fatalf("exhaustion = %v, want nil", projection.ProjectedExhaustionAt)
	}
}

func TestProjectedExhaustionWalksForwardThroughWorkingHours(t *testing.T) {
	now := time.Date(2026, 9, 2, 13, 0, 0, 0, time.UTC) // Wednesday 13:00
	rows := calendarRows("claude", now, time.UTC, workdayTokens)
	profiles := buildUsageProfiles(rows, now, time.UTC)

	projection := computeWindowProjection(sevenDayWindow(55), profiles["claude"], now, time.UTC)
	if projection == nil || projection.Verdict != "will_exhaust" {
		t.Fatalf("projection = %#v, want will_exhaust", projection)
	}
	if math.Abs(projection.ProjectedUsedPercent-110) > 1e-9 {
		t.Fatalf("projected = %v, want 110", projection.ProjectedUsedPercent)
	}
	// 16363.6 expected units remain to 100%: 4 working hours Wednesday, 8
	// Thursday, then 4.3636 into Friday's workday — nights and the weekend
	// contribute nothing, so the crossing lands Friday 13:21:49.
	want := time.Date(2026, 9, 4, 13, 21, 49, 0, time.UTC)
	if projection.ProjectedExhaustionAt == nil || !within(projection.ProjectedExhaustionAt.Sub(want), 2*time.Second) {
		t.Fatalf("exhaustion = %v, want ~%v", projection.ProjectedExhaustionAt, want)
	}
	if !projection.ProjectedExhaustionAt.Before(cycleReset) {
		t.Fatalf("exhaustion %v not before reset %v", projection.ProjectedExhaustionAt, cycleReset)
	}
}

func TestConfidenceGradedByDistinctDaysNotRecords(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		days int
		want string
	}{{3, "low"}, {7, "medium"}, {20, "medium"}, {21, "high"}}
	for _, tc := range cases {
		// Huge record count, few distinct days: the grade follows the days.
		profile := &usageProfile{recordsSeen: 10000, recordsTokened: 10000, distinctDays: tc.days}
		projection := computeWindowProjection(sevenDayWindow(40), profile, now, time.UTC)
		if projection == nil || projection.Confidence != tc.want {
			t.Fatalf("distinctDays %d: projection = %#v, want confidence %q", tc.days, projection, tc.want)
		}
	}
}

func TestAttachProjectionsRollsUpStoredBuckets(t *testing.T) {
	store, cfg := openProfileStore(t)
	now := time.Now().UTC()
	at := recentUTC(now, time.Monday, 10)
	for i := 0; i < 20; i++ {
		event := healthEvent{Provider: "claude", AuthIndex: "claude-1", At: at.Add(time.Duration(i) * time.Minute), Success: true, TokensSeen: true, UncachedTokens: 500}
		if err := store.record(event, cfg); err != nil {
			t.Fatal(err)
		}
	}
	used := 40.0
	reset := now.Add(84 * time.Hour)
	accounts := []accountQuota{{Provider: "claude", AuthIndex: "claude-1", Windows: []quotaWindow{
		{ID: "seven_day", UsedPercent: &used, ResetAt: &reset, WindowSeconds: 7 * 24 * 60 * 60},
	}}}

	attachProjections(accounts, store, cfg, now)

	projection := accounts[0].Windows[0].Projection
	if projection == nil {
		t.Fatal("projection missing")
	}
	if projection.Basis != "profile" {
		t.Fatalf("basis = %q, want profile (20 fully-tokened records)", projection.Basis)
	}
	if projection.Confidence != "low" {
		t.Fatalf("confidence = %q, want low (single observed day)", projection.Confidence)
	}
}

func TestProjectionSurfacesThroughManagementHandler(t *testing.T) {
	host := newFakeHost().
		withEntry(hostAuthFileEntry{AuthIndex: "claude-1", Name: "claude.json", Provider: "claude"}).
		withCredential("claude-1", `{"access_token":"sk-ant-oat-test"}`).
		withJSON(claudeQuotaURL, claudeFullPayload)

	account := accountByProvider(t, quotaSnapshotJSON(t, host, nil), "claude")

	for _, id := range []string{"five_hour", "seven_day"} {
		window := windowByID(t, account, id)
		if window.Projection == nil {
			t.Fatalf("%s projection missing", id)
		}
		// No health store is configured in this harness: cold start, uniform.
		if window.Projection.Basis != "uniform" || window.Projection.Confidence != "low" {
			t.Fatalf("%s projection = %#v", id, window.Projection)
		}
		if window.Projection.ExpectedFraction != window.Projection.ElapsedFraction {
			t.Fatalf("%s uniform projection did not collapse to clock ratio: %#v", id, window.Projection)
		}
	}
	extra := windowByID(t, account, "extra")
	if extra.Projection != nil {
		t.Fatalf("extra projection = %#v, want nil", extra.Projection)
	}
}
