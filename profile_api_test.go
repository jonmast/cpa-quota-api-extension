package main

import (
	"encoding/json"
	"math"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

// profileRuntime starts a runtime backed by a temp database and hands back a
// seed function that inserts rolled-up rows through a second connection to the
// same file (WAL mode allows this), so tests exercise the endpoint through
// handleManagement like the other API tests.
func profileRuntime(t *testing.T) (*runtimeState, func(provider, authIndex, dayType string, hour int, date string, tokens, seen, tokened int64)) {
	t.Helper()
	cfg := defaultConfig()
	cfg.DatabasePath = filepath.Join(t.TempDir(), "health.db")
	r := newRuntime(&fakeHost{})
	t.Cleanup(r.shutdown)
	r.applyConfig(cfg)
	seeder, err := openHealthStore(cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(seeder.close)
	seed := func(provider, authIndex, dayType string, hour int, date string, tokens, seen, tokened int64) {
		t.Helper()
		if _, err := seeder.db.Exec(`INSERT INTO usage_buckets(provider,auth_index,day_type,hour,date,token_sum,records_seen,records_tokened) VALUES(?,?,?,?,?,?,?,?)`,
			provider, authIndex, dayType, hour, date, tokens, seen, tokened); err != nil {
			t.Fatal(err)
		}
	}
	return r, seed
}

func fetchProfile(t *testing.T, r *runtimeState) profileResponse {
	t.Helper()
	resp := r.handleManagement(managementRequest{Method: http.MethodGet, Path: profileRoute})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.StatusCode, resp.Body)
	}
	var payload profileResponse
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func recentDate(daysAgo int) string {
	return time.Now().UTC().AddDate(0, 0, -daysAgo).Format(bucketDateLayout)
}

func weightSum(buckets []profileBucket) float64 {
	sum := 0.0
	for _, bucket := range buckets {
		sum += bucket.Weight
	}
	return sum
}

func bucketAt(t *testing.T, profile providerProfile, dayType string, hour int) profileBucket {
	t.Helper()
	for _, bucket := range profile.Buckets {
		if bucket.DayType == dayType && bucket.Hour == hour {
			return bucket
		}
	}
	t.Fatalf("bucket (%s,%d) missing", dayType, hour)
	return profileBucket{}
}

func TestProfileRollsUpAcrossAuthIndexes(t *testing.T) {
	r, seed := profileRuntime(t)
	// Two auth indexes land in the same (weekday, 9) cell; a third row is a
	// weekend cell. All must merge into one provider-level profile.
	seed("claude", "auth-a", dayTypeWeekday, 9, recentDate(3), 100, 2, 2)
	seed("claude", "auth-b", dayTypeWeekday, 9, recentDate(3), 50, 2, 1)
	seed("claude", "auth-a", dayTypeWeekend, 14, recentDate(1), 50, 1, 1)
	payload := fetchProfile(t, r)
	if len(payload.Providers) != 1 || payload.ObservationDays != 2 {
		t.Fatalf("payload = %#v", payload)
	}
	profile := payload.Providers["claude"]
	if profile.TotalTokens != 200 || profile.Observations != 5 {
		t.Fatalf("profile = %#v", profile)
	}
	if math.Abs(profile.TokenCoverage-0.8) > 1e-9 {
		t.Fatalf("token_coverage = %f", profile.TokenCoverage)
	}
	if math.Abs(profile.WeekendShare-0.25) > 1e-9 {
		t.Fatalf("weekend_share = %f", profile.WeekendShare)
	}
	merged := bucketAt(t, profile, dayTypeWeekday, 9)
	if merged.Tokens != 150 || merged.N != 4 || math.Abs(merged.Weight-0.75) > 1e-9 {
		t.Fatalf("merged bucket = %#v", merged)
	}
	if got := []int{9, 14}; len(profile.PeakHours) != 2 || profile.PeakHours[0] != got[0] || profile.PeakHours[1] != got[1] {
		t.Fatalf("peak_hours = %v", profile.PeakHours)
	}
	if len(profile.Buckets) != profileBucketCount || math.Abs(weightSum(profile.Buckets)-1.0) > 1e-9 {
		t.Fatalf("buckets = %d sum = %f", len(profile.Buckets), weightSum(profile.Buckets))
	}
}

func TestProfileWeightsUniformWithoutTokenDetail(t *testing.T) {
	r, seed := profileRuntime(t)
	// A provider whose records never carry token detail (coverage 0) gets the
	// uniform shape: 1/48 per cell, still summing to 1.0.
	seed("copilot", "auth-c", dayTypeWeekday, 11, recentDate(2), 0, 4, 0)
	profile := fetchProfile(t, r).Providers["copilot"]
	if profile.TotalTokens != 0 || profile.Observations != 4 || profile.TokenCoverage != 0 {
		t.Fatalf("profile = %#v", profile)
	}
	if math.Abs(weightSum(profile.Buckets)-1.0) > 1e-9 {
		t.Fatalf("weight sum = %f", weightSum(profile.Buckets))
	}
	for _, bucket := range profile.Buckets {
		if math.Abs(bucket.Weight-1.0/profileBucketCount) > 1e-9 {
			t.Fatalf("bucket = %#v", bucket)
		}
	}
	if len(profile.PeakHours) != 0 || profile.WeekendShare != 0 {
		t.Fatalf("profile = %#v", profile)
	}
}

func TestProfileConfidenceGradesByDistinctDays(t *testing.T) {
	r, seed := profileRuntime(t)
	providers := map[string]int{"p6": 6, "p7": 7, "p20": 20, "p21": 21}
	for provider, days := range providers {
		for day := 0; day < days; day++ {
			seed(provider, "a", dayTypeWeekday, 10, recentDate(day), 10, 1, 1)
		}
	}
	payload := fetchProfile(t, r)
	want := map[string]string{"p6": "low", "p7": "medium", "p20": "medium", "p21": "high"}
	for provider, grade := range want {
		if got := payload.Providers[provider].Confidence; got != grade {
			t.Fatalf("%s confidence = %s, want %s", provider, got, grade)
		}
	}
	if payload.ObservationDays != 21 {
		t.Fatalf("observation_days = %d", payload.ObservationDays)
	}
}

func TestProfileColdStartIsWellFormed(t *testing.T) {
	r, _ := profileRuntime(t)
	resp := r.handleManagement(managementRequest{Method: http.MethodGet, Path: profileRoute})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.StatusCode, resp.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(resp.Body, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["providers"]) != "{}" {
		t.Fatalf("providers = %s", raw["providers"])
	}
	payload := fetchProfile(t, r)
	if payload.ObservationDays != 0 || payload.GeneratedAt.IsZero() {
		t.Fatalf("payload = %#v", payload)
	}
	scheme := payload.BucketScheme
	if scheme.BucketCount != 48 || scheme.HoursPerDay != 24 || scheme.Timezone == "" || scheme.RetentionDays != profileRetentionDays || len(scheme.DayTypes) != 2 {
		t.Fatalf("bucket_scheme = %#v", scheme)
	}
}

func TestProfileSchemeReportsConfiguredTimezone(t *testing.T) {
	cfg := defaultConfig()
	cfg.DatabasePath = filepath.Join(t.TempDir(), "health.db")
	cfg.ProfileTimezone = "America/New_York"
	r := newRuntime(&fakeHost{})
	defer r.shutdown()
	r.applyConfig(cfg)
	if got := fetchProfile(t, r).BucketScheme.Timezone; got != "America/New_York" {
		t.Fatalf("timezone = %s", got)
	}
}

func TestProfileUnavailableWithoutDatabase(t *testing.T) {
	r := newRuntime(&fakeHost{})
	defer r.shutdown()
	cfg := defaultConfig()
	cfg.DatabasePath = ""
	r.applyConfig(cfg)
	if resp := r.handleManagement(managementRequest{Method: http.MethodGet, Path: profileRoute}); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
