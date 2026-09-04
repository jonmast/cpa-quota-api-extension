package main

import (
	"time"
	// Embed the IANA timezone database so profile-timezone resolution works
	// even when the runtime image ships without tzdata.
	_ "time/tzdata"
)

const (
	dayTypeWeekday = "weekday"
	dayTypeWeekend = "weekend"
	// profileRetentionDays bounds day-grain bucket history at ~30 days so a
	// schedule change washes out instead of lingering for months (ADR 0004).
	profileRetentionDays = 30
	bucketDateLayout     = "2006-01-02"
)

// bucketKey locates one day-grain usage-profile row: the (day type, hour) cell
// plus the calendar date it was observed on, all in the profile timezone.
type bucketKey struct {
	DayType string
	Hour    int
	Date    string
}

// bucketFor assigns an instant to its profile bucket (CONTEXT.md: bucket, day
// type). Assignment uses wall-clock fields in loc, so DST transitions land on
// the hour the user actually experienced, and a UTC instant near midnight can
// belong to a different day type than its UTC calendar day.
func bucketFor(at time.Time, loc *time.Location) bucketKey {
	local := at.In(loc)
	dayType := dayTypeWeekday
	if weekday := local.Weekday(); weekday == time.Saturday || weekday == time.Sunday {
		dayType = dayTypeWeekend
	}
	return bucketKey{DayType: dayType, Hour: local.Hour(), Date: local.Format(bucketDateLayout)}
}

// --- GET /v1/profile -------------------------------------------------------

// profileResponse is the wire shape of GET /v1/profile: the learned usage
// profile rolled up to provider level across auth indexes (per-auth breakdown
// deliberately out of scope).
type profileResponse struct {
	GeneratedAt     time.Time                  `json:"generated_at"`
	BucketScheme    profileBucketScheme        `json:"bucket_scheme"`
	ObservationDays int                        `json:"observation_days"`
	Providers       map[string]providerProfile `json:"providers"`
}

type profileBucketScheme struct {
	DayTypes      []string `json:"day_types"`
	HoursPerDay   int      `json:"hours_per_day"`
	BucketCount   int      `json:"bucket_count"`
	Timezone      string   `json:"timezone"`
	RetentionDays int      `json:"retention_days"`
}

type providerProfile struct {
	TotalTokens  int64 `json:"total_tokens"`
	Observations int64 `json:"observations"`
	// TokenCoverage is the fraction of the provider's usage records that
	// carried token counts (CONTEXT.md: token coverage).
	TokenCoverage float64 `json:"token_coverage"`
	// Confidence grades by distinct observed days, not event counts:
	// low < 7, medium 7-20, high > 20.
	Confidence string          `json:"confidence"`
	Buckets    []profileBucket `json:"buckets"`
	// PeakHours lists up to three hours-of-day with the highest token sums
	// across both day types, busiest first; empty without token data.
	PeakHours []int `json:"peak_hours"`
	// WeekendShare is the fraction of uncached tokens observed in weekend
	// buckets — the profile's answer to the clock's assumed 2/7 (~28.6%).
	// Zero when no token data has been observed.
	WeekendShare float64 `json:"weekend_share"`
}

type profileBucket struct {
	DayType string `json:"day_type"`
	Hour    int    `json:"hour"`
	// Weight is the bucket's share of the provider's uncached tokens,
	// normalized to sum to 1.0 across the 48 cells; uniform (1/48) when the
	// provider has no token data. Presentational only — projection math uses
	// per-occurrence rates instead (ADR 0004).
	Weight float64 `json:"weight"`
	Tokens int64   `json:"tokens"`
	N      int64   `json:"n"`
}

// profileCell aggregates one (day type, hour) cell across dates and auth indexes.
type profileCell struct {
	Tokens  int64
	Seen    int64
	Tokened int64
}

type providerRollup struct {
	Cells        map[bucketKey]profileCell // keyed by DayType+Hour; Date left empty
	DistinctDays int
}

// profileRollup aggregates usage_buckets to provider level: per-cell sums
// across dates and auth indexes, per-provider distinct observed days, and the
// overall distinct-day count.
func (s *healthStore) profileRollup() (map[string]*providerRollup, int, error) {
	rollup := map[string]*providerRollup{}
	rows, err := s.db.Query(`SELECT provider,day_type,hour,SUM(token_sum),SUM(records_seen),SUM(records_tokened) FROM usage_buckets GROUP BY provider,day_type,hour`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var provider, dayType string
		var hour int
		var cell profileCell
		if err := rows.Scan(&provider, &dayType, &hour, &cell.Tokens, &cell.Seen, &cell.Tokened); err != nil {
			return nil, 0, err
		}
		entry := rollup[provider]
		if entry == nil {
			entry = &providerRollup{Cells: map[bucketKey]profileCell{}}
			rollup[provider] = entry
		}
		entry.Cells[bucketKey{DayType: dayType, Hour: hour}] = cell
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	days, err := s.db.Query(`SELECT provider,COUNT(DISTINCT date) FROM usage_buckets GROUP BY provider`)
	if err != nil {
		return nil, 0, err
	}
	defer days.Close()
	for days.Next() {
		var provider string
		var count int
		if err := days.Scan(&provider, &count); err != nil {
			return nil, 0, err
		}
		if entry := rollup[provider]; entry != nil {
			entry.DistinctDays = count
		}
	}
	if err := days.Err(); err != nil {
		return nil, 0, err
	}
	var observationDays int
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT date) FROM usage_buckets`).Scan(&observationDays); err != nil {
		return nil, 0, err
	}
	return rollup, observationDays, nil
}

func confidenceGrade(distinctDays int) string {
	switch {
	case distinctDays < 7:
		return "low"
	case distinctDays <= 20:
		return "medium"
	default:
		return "high"
	}
}

const profileBucketCount = 48

func buildProviderProfile(rollup *providerRollup) providerProfile {
	profile := providerProfile{Confidence: confidenceGrade(rollup.DistinctDays), Buckets: make([]profileBucket, 0, profileBucketCount), PeakHours: []int{}}
	var tokenedRecords, weekendTokens int64
	hourTokens := make([]int64, 24)
	for _, cell := range rollup.Cells {
		profile.Observations += cell.Seen
		tokenedRecords += cell.Tokened
	}
	if profile.Observations > 0 {
		profile.TokenCoverage = float64(tokenedRecords) / float64(profile.Observations)
	}
	for _, dayType := range []string{dayTypeWeekday, dayTypeWeekend} {
		for hour := 0; hour < 24; hour++ {
			cell := rollup.Cells[bucketKey{DayType: dayType, Hour: hour}]
			profile.TotalTokens += cell.Tokens
			hourTokens[hour] += cell.Tokens
			if dayType == dayTypeWeekend {
				weekendTokens += cell.Tokens
			}
			profile.Buckets = append(profile.Buckets, profileBucket{DayType: dayType, Hour: hour, Tokens: cell.Tokens, N: cell.Seen})
		}
	}
	for i := range profile.Buckets {
		if profile.TotalTokens > 0 {
			profile.Buckets[i].Weight = float64(profile.Buckets[i].Tokens) / float64(profile.TotalTokens)
		} else {
			profile.Buckets[i].Weight = 1.0 / profileBucketCount
		}
	}
	if profile.TotalTokens > 0 {
		profile.WeekendShare = float64(weekendTokens) / float64(profile.TotalTokens)
		profile.PeakHours = peakHours(hourTokens)
	}
	return profile
}

// peakHours returns up to three hours-of-day with the highest token sums,
// busiest first; hours without tokens never qualify. Ties break toward the
// earlier hour.
func peakHours(hourTokens []int64) []int {
	peaks := []int{}
	for len(peaks) < 3 {
		best, bestTokens := -1, int64(0)
		for hour, tokens := range hourTokens {
			if tokens > bestTokens {
				best, bestTokens = hour, tokens
			}
		}
		if best < 0 {
			break
		}
		peaks = append(peaks, best)
		hourTokens[best] = 0
	}
	return peaks
}

func buildProfileResponse(store *healthStore, cfg pluginConfig, now time.Time) (profileResponse, error) {
	rollup, observationDays, err := store.profileRollup()
	if err != nil {
		return profileResponse{}, err
	}
	timezone := cfg.ProfileTimezone
	if timezone == "" {
		timezone = time.Local.String()
	}
	response := profileResponse{
		GeneratedAt: now,
		BucketScheme: profileBucketScheme{
			DayTypes:      []string{dayTypeWeekday, dayTypeWeekend},
			HoursPerDay:   24,
			BucketCount:   profileBucketCount,
			Timezone:      timezone,
			RetentionDays: profileRetentionDays,
		},
		ObservationDays: observationDays,
		Providers:       map[string]providerProfile{},
	}
	for provider, entry := range rollup {
		response.Providers[provider] = buildProviderProfile(entry)
	}
	return response, nil
}

func (r *runtimeState) profileEndpoint() managementResponse {
	r.healthOps.Lock()
	defer r.healthOps.Unlock()
	r.mu.Lock()
	store, cfg := r.healthStore, r.cfg
	r.mu.Unlock()
	if store == nil {
		return jsonError(503, "health_unavailable", "health monitoring is unavailable")
	}
	response, err := buildProfileResponse(store, cfg, time.Now().UTC())
	if err != nil {
		return jsonError(503, "health_unavailable", "health monitoring is unavailable")
	}
	return jsonResponse(200, response)
}

// usageBucketRow is one stored day-grain profile row.
type usageBucketRow struct {
	Provider       string
	AuthIndex      string
	DayType        string
	Hour           int
	Date           string
	TokenSum       int64
	RecordsSeen    int64
	RecordsTokened int64
}

func (s *healthStore) usageBuckets() ([]usageBucketRow, error) {
	rows, err := s.db.Query(`SELECT provider,auth_index,day_type,hour,date,token_sum,records_seen,records_tokened FROM usage_buckets ORDER BY provider,auth_index,date,day_type,hour`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []usageBucketRow
	for rows.Next() {
		var row usageBucketRow
		if err := rows.Scan(&row.Provider, &row.AuthIndex, &row.DayType, &row.Hour, &row.Date, &row.TokenSum, &row.RecordsSeen, &row.RecordsTokened); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
