package main

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return location
}

func openProfileStore(t *testing.T) (*healthStore, pluginConfig) {
	t.Helper()
	cfg := defaultConfig()
	cfg.DatabasePath = filepath.Join(t.TempDir(), "health.db")
	cfg.profileLoc = time.UTC
	store, err := openHealthStore(cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.close)
	return store, cfg
}

func TestUsageRecordDecodingKeepsModelAndTokenDetail(t *testing.T) {
	raw := []byte(`{"Provider":"Claude","Model":" claude-opus-4 ","AuthIndex":"a","RequestedAt":"2026-08-03T12:00:00Z","Failed":false,"Detail":{"InputTokens":100,"OutputTokens":40,"ReasoningTokens":10,"CachedTokens":900,"CacheReadTokens":900,"CacheCreationTokens":50,"TotalTokens":1100}}`)
	var record usageRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	event, ok := sanitizeUsageRecord(record, time.Now())
	if !ok {
		t.Fatal("record rejected")
	}
	if event.Model != "claude-opus-4" || !event.TokensSeen {
		t.Fatalf("event = %#v", event)
	}
	// Uncached sum is input + output + reasoning; cache counters are excluded (ADR 0004).
	if event.UncachedTokens != 150 {
		t.Fatalf("uncached = %d, want 150", event.UncachedTokens)
	}
	encoded, _ := json.Marshal(event)
	if containsSensitiveJSON(encoded) {
		t.Fatalf("sanitized event leaked sensitive data: %s", encoded)
	}
}

func TestUsageRecordWithoutDetailHasNoTokens(t *testing.T) {
	event, ok := sanitizeUsageRecord(usageRecord{Provider: "copilot", AuthIndex: "a", RequestedAt: time.Now()}, time.Now())
	if !ok || event.TokensSeen || event.UncachedTokens != 0 {
		t.Fatalf("event = %#v ok = %v", event, ok)
	}
	// Negative counters must not produce a negative shape signal.
	event, _ = sanitizeUsageRecord(usageRecord{AuthIndex: "a", Detail: usageDetail{InputTokens: -5, OutputTokens: 7}}, time.Now())
	if event.UncachedTokens != 7 || !event.TokensSeen {
		t.Fatalf("event = %#v", event)
	}
}

func TestBucketAssignmentAcrossTimezoneAndDSTBoundaries(t *testing.T) {
	berlin := mustLocation(t, "Europe/Berlin")
	losAngeles := mustLocation(t, "America/Los_Angeles")
	tests := []struct {
		name string
		at   time.Time
		loc  *time.Location
		want bucketKey
	}{
		{"weekday utc", time.Date(2026, 8, 3, 10, 15, 0, 0, time.UTC), time.UTC, bucketKey{dayTypeWeekday, 10, "2026-08-03"}},
		{"weekend utc", time.Date(2026, 8, 8, 10, 15, 0, 0, time.UTC), time.UTC, bucketKey{dayTypeWeekend, 10, "2026-08-08"}},
		// Saturday 00:30 UTC is still Friday evening in Los Angeles: the day
		// type flips across the timezone boundary.
		{"utc weekend is la weekday", time.Date(2026, 8, 29, 0, 30, 0, 0, time.UTC), losAngeles, bucketKey{dayTypeWeekday, 17, "2026-08-28"}},
		{"same instant in utc", time.Date(2026, 8, 29, 0, 30, 0, 0, time.UTC), time.UTC, bucketKey{dayTypeWeekend, 0, "2026-08-29"}},
		// Berlin springs forward 2026-03-29 02:00 CET -> 03:00 CEST: hour 2
		// never occurs; the instant after the jump lands on wall-clock hour 3.
		{"before spring forward", time.Date(2026, 3, 29, 0, 30, 0, 0, time.UTC), berlin, bucketKey{dayTypeWeekend, 1, "2026-03-29"}},
		{"after spring forward", time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC), berlin, bucketKey{dayTypeWeekend, 3, "2026-03-29"}},
		// Berlin falls back 2026-10-25 03:00 CEST -> 02:00 CET: two distinct
		// UTC hours both map to wall-clock hour 2.
		{"fall back first pass", time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC), berlin, bucketKey{dayTypeWeekend, 2, "2026-10-25"}},
		{"fall back second pass", time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC), berlin, bucketKey{dayTypeWeekend, 2, "2026-10-25"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bucketFor(tt.at, tt.loc); got != tt.want {
				t.Fatalf("bucketFor = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// recentUTC returns the most recent instant at hour:15 UTC on the wanted
// weekday, looking back from now. Store tests must use recent instants because
// record() prunes rows older than the retention window against the real clock.
func recentUTC(now time.Time, want time.Weekday, hour int) time.Time {
	day := now.UTC()
	for day.Weekday() != want {
		day = day.AddDate(0, 0, -1)
	}
	return time.Date(day.Year(), day.Month(), day.Day(), hour, 15, 0, 0, time.UTC)
}

func TestUsageEventsAggregateIntoDayGrainBuckets(t *testing.T) {
	store, cfg := openProfileStore(t)
	at := recentUTC(time.Now(), time.Monday, 10)
	saturday := recentUTC(time.Now(), time.Saturday, 10)
	tokened := healthEvent{Provider: "claude", AuthIndex: "a", At: at, Success: true, TokensSeen: true, UncachedTokens: 150}
	if err := store.record(tokened, cfg); err != nil {
		t.Fatal(err)
	}
	tokened.UncachedTokens = 50
	tokened.At = at.Add(20 * time.Minute)
	if err := store.record(tokened, cfg); err != nil {
		t.Fatal(err)
	}
	// An event without token detail still counts toward records_seen.
	if err := store.record(healthEvent{Provider: "claude", AuthIndex: "a", At: at.Add(30 * time.Minute), Success: true}, cfg); err != nil {
		t.Fatal(err)
	}
	// Failures represent no consumption and must not touch bucket rows.
	if err := store.record(healthEvent{Provider: "claude", AuthIndex: "a", At: at.Add(40 * time.Minute), StatusCode: 429, FailureClass: "rate_limited"}, cfg); err != nil {
		t.Fatal(err)
	}
	// A weekend event lands in its own row.
	if err := store.record(healthEvent{Provider: "claude", AuthIndex: "a", At: saturday, Success: true, TokensSeen: true, UncachedTokens: 30}, cfg); err != nil {
		t.Fatal(err)
	}
	rows, err := store.usageBuckets()
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows = %#v err = %v", rows, err)
	}
	byDayType := map[string]usageBucketRow{}
	for _, row := range rows {
		byDayType[row.DayType] = row
	}
	weekday, weekend := byDayType[dayTypeWeekday], byDayType[dayTypeWeekend]
	if weekday != (usageBucketRow{Provider: "claude", AuthIndex: "a", DayType: dayTypeWeekday, Hour: 10, Date: at.Format(bucketDateLayout), TokenSum: 200, RecordsSeen: 3, RecordsTokened: 2}) {
		t.Fatalf("weekday row = %#v", weekday)
	}
	if weekend != (usageBucketRow{Provider: "claude", AuthIndex: "a", DayType: dayTypeWeekend, Hour: 10, Date: saturday.Format(bucketDateLayout), TokenSum: 30, RecordsSeen: 1, RecordsTokened: 1}) {
		t.Fatalf("weekend row = %#v", weekend)
	}
}

func TestProfileTimezoneControlsBucketAssignmentAtWriteTime(t *testing.T) {
	store, cfg := openProfileStore(t)
	cfg.profileLoc = mustLocation(t, "America/Los_Angeles")
	// Shortly after midnight UTC on a Saturday it is still Friday evening in
	// Los Angeles: the row must carry the weekday day type and Friday's date.
	at := recentUTC(time.Now(), time.Saturday, 0)
	event := healthEvent{Provider: "codex", AuthIndex: "b", At: at, Success: true, TokensSeen: true, UncachedTokens: 10}
	if err := store.record(event, cfg); err != nil {
		t.Fatal(err)
	}
	rows, err := store.usageBuckets()
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %#v err = %v", rows, err)
	}
	friday := at.AddDate(0, 0, -1).Format(bucketDateLayout)
	// 00:15 UTC is 16:15 or 17:15 the previous evening depending on DST.
	if rows[0].DayType != dayTypeWeekday || rows[0].Date != friday || (rows[0].Hour != 16 && rows[0].Hour != 17) {
		t.Fatalf("row = %#v, want weekday %s hour 16 or 17", rows[0], friday)
	}
}

func TestUsageBucketPruningDropsRowsPastRetention(t *testing.T) {
	store, cfg := openProfileStore(t)
	now := time.Now().UTC()
	stale := now.AddDate(0, 0, -(profileRetentionDays + 10)).Format(bucketDateLayout)
	edge := now.AddDate(0, 0, -profileRetentionDays).Format(bucketDateLayout)
	for _, date := range []string{stale, edge} {
		if _, err := store.db.Exec(`INSERT INTO usage_buckets(provider,auth_index,day_type,hour,date,token_sum,records_seen,records_tokened) VALUES('claude','a','weekday',9,?,100,1,1)`, date); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.record(healthEvent{Provider: "claude", AuthIndex: "a", At: now, Success: true, TokensSeen: true, UncachedTokens: 5}, cfg); err != nil {
		t.Fatal(err)
	}
	if err := store.prune(cfg); err != nil {
		t.Fatal(err)
	}
	rows, err := store.usageBuckets()
	if err != nil {
		t.Fatal(err)
	}
	dates := map[string]bool{}
	for _, row := range rows {
		dates[row.Date] = true
	}
	if dates[stale] || !dates[edge] || len(rows) != 2 {
		t.Fatalf("rows after prune = %#v", rows)
	}
}

func TestProfileTimezoneConfigKnob(t *testing.T) {
	cfg, err := decodeLifecycleConfig([]byte(`{"config_yaml":` + jsonQuote("profile-timezone: America/New_York") + `}`))
	if err != nil || cfg.ProfileTimezone != "America/New_York" || cfg.profileLocation().String() != "America/New_York" {
		t.Fatalf("cfg = %#v err = %v", cfg, err)
	}
	if _, err := decodeLifecycleConfig([]byte(`{"config_yaml":` + jsonQuote("profile-timezone: Not/AZone") + `}`)); err == nil {
		t.Fatal("invalid timezone accepted")
	}
	if defaultConfig().profileLocation() != time.Local {
		t.Fatal("default profile timezone is not server local")
	}
}

func TestMigrationToV5PreservesExistingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "health.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	// A pre-v5 database: existing tables with data, no usage_buckets, version 4.
	if _, err = db.Exec(`CREATE TABLE observations (auth_index TEXT PRIMARY KEY, provider TEXT NOT NULL, last_status_code INTEGER NOT NULL DEFAULT 0, last_failure_at TEXT, last_success_at TEXT, retry_at TEXT, failure_count INTEGER NOT NULL DEFAULT 0, last_event_at TEXT);
		INSERT INTO observations(auth_index,provider,last_status_code) VALUES('a','claude',429);
		PRAGMA user_version = 4`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openHealthStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 5 {
		t.Fatalf("version = %d err = %v", version, err)
	}
	var status int
	if err := store.db.QueryRow(`SELECT last_status_code FROM observations WHERE auth_index='a'`).Scan(&status); err != nil || status != 429 {
		t.Fatalf("existing data lost: status = %d err = %v", status, err)
	}
	if rows, err := store.usageBuckets(); err != nil || len(rows) != 0 {
		t.Fatalf("usage_buckets = %#v err = %v", rows, err)
	}
}
