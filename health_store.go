package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const healthSchemaVersion = 5

type healthStore struct{ db *sql.DB }
type healthIncident struct {
	ID         int64      `json:"id"`
	Provider   string     `json:"provider"`
	AuthIndex  string     `json:"auth_index"`
	State      string     `json:"state"`
	StatusCode int        `json:"status_code,omitempty"`
	OpenedAt   time.Time  `json:"opened_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}
type healthHistory struct {
	ID         int64          `json:"id"`
	RecordedAt time.Time      `json:"recorded_at"`
	Total      int            `json:"total"`
	Routable   int            `json:"routable"`
	Lost       int            `json:"lost"`
	Degraded   int            `json:"degraded"`
	ByState    map[string]int `json:"by_state"`
	ByProvider map[string]int `json:"by_provider"`
}

type incidentFilter struct {
	Provider   string
	AuthIndex  string
	State      string
	StatusCode int
	From       time.Time
	To         time.Time
}

type historyFilter struct {
	From time.Time
	To   time.Time
}

func openHealthStore(path string) (*healthStore, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}
		_ = os.Chmod(dir, 0o750)
	}
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	store := &healthStore{db: db}
	if err = store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		_ = os.Chmod(candidate, 0o600)
	}
	return store, nil
}

func columnExists(tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func addColumn(tx *sql.Tx, table, column, definition string) error {
	exists, err := columnExists(tx, table, column)
	if err != nil || exists {
		return err
	}
	_, err = tx.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + definition)
	return err
}

func (s *healthStore) migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS observations (
		auth_index TEXT PRIMARY KEY, provider TEXT NOT NULL, last_status_code INTEGER NOT NULL DEFAULT 0,
		last_failure_at TEXT, last_success_at TEXT, retry_at TEXT, failure_count INTEGER NOT NULL DEFAULT 0,
		last_event_at TEXT
	);
	CREATE TABLE IF NOT EXISTS failure_events (
		id INTEGER PRIMARY KEY, auth_index TEXT NOT NULL, provider TEXT NOT NULL, occurred_at TEXT NOT NULL,
		status_code INTEGER NOT NULL DEFAULT 0, failure_class TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS failure_events_account_time ON failure_events(auth_index, occurred_at DESC);
	CREATE TABLE IF NOT EXISTS incidents (
		id INTEGER PRIMARY KEY, provider TEXT NOT NULL, auth_index TEXT NOT NULL, state TEXT NOT NULL,
		status_code INTEGER NOT NULL DEFAULT 0, opened_at TEXT NOT NULL, resolved_at TEXT
	);
	CREATE INDEX IF NOT EXISTS incidents_opened ON incidents(opened_at DESC);
	CREATE INDEX IF NOT EXISTS incidents_filters ON incidents(provider, auth_index, status_code, opened_at DESC);
	CREATE TABLE IF NOT EXISTS health_history (
		id INTEGER PRIMARY KEY, recorded_at TEXT NOT NULL, total INTEGER NOT NULL, routable INTEGER NOT NULL,
		lost INTEGER NOT NULL, degraded INTEGER NOT NULL, by_state_json TEXT NOT NULL DEFAULT '{}',
		by_provider_json TEXT NOT NULL DEFAULT '{}'
	);
	CREATE INDEX IF NOT EXISTS health_history_recorded ON health_history(recorded_at DESC);
	CREATE TABLE IF NOT EXISTS alert_state (name TEXT PRIMARY KEY, value TEXT NOT NULL);
	CREATE TABLE IF NOT EXISTS usage_buckets (
		provider TEXT NOT NULL, auth_index TEXT NOT NULL, day_type TEXT NOT NULL, hour INTEGER NOT NULL,
		date TEXT NOT NULL, token_sum INTEGER NOT NULL DEFAULT 0, records_seen INTEGER NOT NULL DEFAULT 0,
		records_tokened INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY(provider, auth_index, day_type, hour, date)
	);
	CREATE INDEX IF NOT EXISTS usage_buckets_date ON usage_buckets(date);`); err != nil {
		return err
	}
	// v0.2.0 databases may have the original observations table without these columns.
	if err = addColumn(tx, "observations", "failure_count", "failure_count INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err = addColumn(tx, "observations", "last_event_at", "last_event_at TEXT"); err != nil {
		return err
	}
	if err = addColumn(tx, "health_history", "by_state_json", "by_state_json TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return err
	}
	if err = addColumn(tx, "health_history", "by_provider_json", "by_provider_json TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE observations SET last_event_at=COALESCE(last_success_at,last_failure_at) WHERE last_event_at IS NULL`); err != nil {
		return err
	}
	// Existing alert state predates the explicit baseline marker.
	if _, err = tx.Exec(`INSERT OR IGNORE INTO alert_state(name,value) SELECT 'pool_initialized','1' WHERE EXISTS (SELECT 1 FROM alert_state WHERE name='pool_breach')`); err != nil {
		return err
	}
	if _, err = tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, healthSchemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *healthStore) close() {
	if s != nil && s.db != nil {
		_ = s.db.Close()
	}
}
func timeText(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}
func scanTime(raw sql.NullString) time.Time {
	if !raw.Valid {
		return time.Time{}
	}
	value, _ := time.Parse(time.RFC3339Nano, raw.String)
	return value
}

// record always stores a sanitized failure event, but only lets the newest event alter current state.
func (s *healthStore) record(event healthEvent, cfg pluginConfig) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at := timeText(event.At)
	if !event.Success {
		if _, err = tx.Exec(`INSERT INTO failure_events(auth_index,provider,occurred_at,status_code,failure_class) VALUES(?,?,?,?,?)`, event.AuthIndex, event.Provider, at, event.StatusCode, event.FailureClass); err != nil {
			return err
		}
	}
	// Successful requests feed the usage profile at write time, bucketed in the
	// profile timezone. Failures represent no consumption and would depress the
	// token-coverage denominator, so they never touch bucket rows. Events
	// without token detail still increment records_seen — that asymmetry is the
	// token-coverage input (CONTEXT.md: token coverage).
	if event.Success {
		key := bucketFor(event.At, cfg.profileLocation())
		tokened := 0
		if event.TokensSeen {
			tokened = 1
		}
		if _, err = tx.Exec(`INSERT INTO usage_buckets(provider,auth_index,day_type,hour,date,token_sum,records_seen,records_tokened) VALUES(?,?,?,?,?,?,1,?)
			ON CONFLICT(provider,auth_index,day_type,hour,date) DO UPDATE SET token_sum=usage_buckets.token_sum+excluded.token_sum,records_seen=usage_buckets.records_seen+1,records_tokened=usage_buckets.records_tokened+excluded.records_tokened`,
			event.Provider, event.AuthIndex, key.DayType, key.Hour, key.Date, event.UncachedTokens, tokened); err != nil {
			return err
		}
	}
	var last sql.NullString
	_ = tx.QueryRow(`SELECT last_event_at FROM observations WHERE auth_index=?`, event.AuthIndex).Scan(&last)
	newest := !last.Valid || scanTime(last).IsZero() || !event.At.Before(scanTime(last))
	if newest && event.Success {
		_, err = tx.Exec(`INSERT INTO observations(auth_index,provider,last_success_at,last_event_at,failure_count) VALUES(?,?,?,?,0)
			ON CONFLICT(auth_index) DO UPDATE SET provider=excluded.provider,last_success_at=excluded.last_success_at,last_event_at=excluded.last_event_at,retry_at=NULL,failure_count=0`, event.AuthIndex, event.Provider, at, at)
		if err == nil {
			_, err = tx.Exec(`UPDATE incidents SET resolved_at=? WHERE auth_index=? AND resolved_at IS NULL`, at, event.AuthIndex)
		}
	}
	if newest && !event.Success {
		_, err = tx.Exec(`INSERT INTO observations(auth_index,provider,last_status_code,last_failure_at,retry_at,last_event_at,failure_count) VALUES(?,?,?,?,?,?,1)
			ON CONFLICT(auth_index) DO UPDATE SET provider=excluded.provider,last_status_code=excluded.last_status_code,last_failure_at=excluded.last_failure_at,retry_at=excluded.retry_at,last_event_at=excluded.last_event_at,failure_count=observations.failure_count+1`, event.AuthIndex, event.Provider, event.StatusCode, at, timeText(event.RetryAt), at)
		if err == nil {
			result, updateErr := tx.Exec(`UPDATE incidents SET provider=?,state=?,status_code=? WHERE auth_index=? AND resolved_at IS NULL`, event.Provider, event.FailureClass, event.StatusCode, event.AuthIndex)
			if updateErr != nil {
				err = updateErr
			} else if count, _ := result.RowsAffected(); count == 0 {
				_, err = tx.Exec(`INSERT INTO incidents(provider,auth_index,state,status_code,opened_at) VALUES(?,?,?,?,?)`, event.Provider, event.AuthIndex, event.FailureClass, event.StatusCode, at)
			}
		}
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return s.prune(cfg)
}

func (s *healthStore) observation(authIndex string, window time.Time) (healthObservation, error) {
	var status int
	var failure, success, retry sql.NullString
	err := s.db.QueryRow(`SELECT last_status_code,last_failure_at,last_success_at,retry_at FROM observations WHERE auth_index=?`, authIndex).Scan(&status, &failure, &success, &retry)
	if err == sql.ErrNoRows {
		return healthObservation{}, nil
	}
	if err != nil {
		return healthObservation{}, err
	}
	var count int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM failure_events WHERE auth_index=? AND occurred_at>=? AND (? IS NULL OR occurred_at>?)`, authIndex, timeText(window), timeText(scanTime(success)), timeText(scanTime(success))).Scan(&count); err != nil {
		return healthObservation{}, err
	}
	return healthObservation{LastStatusCode: status, LastFailureAt: scanTime(failure), LastSuccessAt: scanTime(success), RetryAt: scanTime(retry), RecentFailures: count}, nil
}
func (s *healthStore) saveHistory(snapshot healthSnapshot, cfg pluginConfig) error {
	byState, _ := json.Marshal(snapshot.ByState)
	byProvider, _ := json.Marshal(snapshot.ByProvider)
	_, err := s.db.Exec(`INSERT INTO health_history(recorded_at,total,routable,lost,degraded,by_state_json,by_provider_json) VALUES(?,?,?,?,?,?,?)`, timeText(snapshot.GeneratedAt), snapshot.Capacity.Total, snapshot.Capacity.Routable, snapshot.Capacity.Lost, snapshot.Capacity.Degraded, string(byState), string(byProvider))
	if err != nil {
		return err
	}
	return s.prune(cfg)
}
func (s *healthStore) prune(cfg pluginConfig) error {
	if _, err := s.db.Exec(`DELETE FROM failure_events WHERE occurred_at<?`, timeText(time.Now().Add(-cfg.IncidentRetention))); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM incidents WHERE id NOT IN (SELECT id FROM incidents WHERE COALESCE(resolved_at,opened_at)>=? ORDER BY id DESC LIMIT ?)`, timeText(time.Now().Add(-cfg.IncidentRetention)), cfg.IncidentMaxRows); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM health_history WHERE id NOT IN (SELECT id FROM health_history WHERE recorded_at>=? ORDER BY id DESC LIMIT ?)`, timeText(time.Now().Add(-cfg.HistoryRetention)), cfg.HistoryMaxRows); err != nil {
		return err
	}
	// Day-grain profile rows age out at ~30 days (ADR 0004). Dates are
	// YYYY-MM-DD in the profile timezone, so lexical comparison is ordering.
	profileCutoff := time.Now().In(cfg.profileLocation()).AddDate(0, 0, -profileRetentionDays).Format(bucketDateLayout)
	_, err := s.db.Exec(`DELETE FROM usage_buckets WHERE date<?`, profileCutoff)
	return err
}

func (s *healthStore) incidentsFiltered(filter incidentFilter, limit int, before int64) ([]healthIncident, error) {
	args := []any{}
	where := []string{"1=1"}
	if filter.Provider != "" {
		where = append(where, "provider=?")
		args = append(args, filter.Provider)
	}
	if filter.AuthIndex != "" {
		where = append(where, "auth_index=?")
		args = append(args, filter.AuthIndex)
	}
	if filter.State != "" {
		where = append(where, "state=?")
		args = append(args, filter.State)
	}
	if filter.StatusCode != 0 {
		where = append(where, "status_code=?")
		args = append(args, filter.StatusCode)
	}
	if !filter.From.IsZero() {
		where = append(where, "opened_at>=?")
		args = append(args, timeText(filter.From))
	}
	if !filter.To.IsZero() {
		where = append(where, "opened_at<=?")
		args = append(args, timeText(filter.To))
	}
	if before > 0 {
		where = append(where, "id<?")
		args = append(args, before)
	}
	args = append(args, limit+1)
	rows, err := s.db.Query(`SELECT id,provider,auth_index,state,status_code,opened_at,resolved_at FROM incidents WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []healthIncident
	for rows.Next() {
		var item healthIncident
		var opened, resolved sql.NullString
		if err := rows.Scan(&item.ID, &item.Provider, &item.AuthIndex, &item.State, &item.StatusCode, &opened, &resolved); err != nil {
			return nil, err
		}
		item.OpenedAt = scanTime(opened)
		if t := scanTime(resolved); !t.IsZero() {
			item.ResolvedAt = &t
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (s *healthStore) historyFiltered(filter historyFilter, limit int, before int64) ([]healthHistory, error) {
	where := []string{"1=1"}
	args := []any{}
	if !filter.From.IsZero() {
		where = append(where, "recorded_at>=?")
		args = append(args, timeText(filter.From))
	}
	if !filter.To.IsZero() {
		where = append(where, "recorded_at<=?")
		args = append(args, timeText(filter.To))
	}
	if before > 0 {
		where = append(where, "id<?")
		args = append(args, before)
	}
	args = append(args, limit+1)
	rows, err := s.db.Query(`SELECT id,recorded_at,total,routable,lost,degraded,by_state_json,by_provider_json FROM health_history WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []healthHistory
	for rows.Next() {
		var item healthHistory
		var recorded sql.NullString
		var byState, byProvider string
		if err := rows.Scan(&item.ID, &recorded, &item.Total, &item.Routable, &item.Lost, &item.Degraded, &byState, &byProvider); err != nil {
			return nil, err
		}
		item.RecordedAt = scanTime(recorded)
		_ = json.Unmarshal([]byte(byState), &item.ByState)
		_ = json.Unmarshal([]byte(byProvider), &item.ByProvider)
		out = append(out, item)
	}
	return out, rows.Err()
}

// updateAlertState persists the active pool state before delivery. Pending state is
// cleared only after a successful webhook response, so failed deliveries retry.
func (s *healthStore) updateAlertState(state string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	read := func(name string) (string, error) {
		var value string
		err := tx.QueryRow(`SELECT value FROM alert_state WHERE name=?`, name).Scan(&value)
		if err == sql.ErrNoRows {
			return "", nil
		}
		return value, err
	}
	initialized, err := read("pool_initialized")
	if err != nil {
		return err
	}
	previous, err := read("pool_breach")
	if err != nil {
		return err
	}
	set := func(name, value string) error {
		_, err := tx.Exec(`INSERT INTO alert_state(name,value) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET value=excluded.value`, name, value)
		return err
	}
	if initialized == "" {
		if err = set("pool_initialized", "1"); err != nil {
			return err
		}
		if state == "breach" {
			if err = set("pool_breach_pending", "1"); err != nil {
				return err
			}
		}
	} else if previous != state {
		if state == "breach" {
			if err = set("pool_breach_pending", "1"); err != nil {
				return err
			}
			if err = set("pool_recovery_pending", ""); err != nil {
				return err
			}
		} else {
			if err = set("pool_recovery_pending", "1"); err != nil {
				return err
			}
			if err = set("pool_breach_pending", ""); err != nil {
				return err
			}
		}
	}
	if err = set("pool_breach", state); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *healthStore) currentAlertState() (string, error) {
	return s.alert("pool_breach")
}

func (s *healthStore) nextAlert(cooldown time.Duration, now time.Time) (string, bool, error) {
	state, err := s.alert("pool_breach")
	if err != nil || state == "" {
		return "", false, err
	}
	if state == "healthy" {
		pending, err := s.alert("pool_recovery_pending")
		return "recovery", err == nil && pending == "1", err
	}
	pending, err := s.alert("pool_breach_pending")
	if err != nil || pending == "1" {
		return "breach", err == nil, err
	}
	lastRaw, err := s.alert("pool_alert_at")
	if err != nil || lastRaw == "" {
		return "breach", err == nil, err
	}
	last, parseErr := time.Parse(time.RFC3339Nano, lastRaw)
	return "breach", parseErr != nil || !now.Before(last.Add(cooldown)), nil
}

func (s *healthStore) recordAlertDelivery(event string, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO alert_state(name,value) VALUES('pool_alert_at',?) ON CONFLICT(name) DO UPDATE SET value=excluded.value`, now.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if event == "breach" {
		_, err = tx.Exec(`UPDATE alert_state SET value='' WHERE name='pool_breach_pending' AND (SELECT value FROM alert_state WHERE name='pool_breach')='breach'`)
	} else {
		_, err = tx.Exec(`UPDATE alert_state SET value='' WHERE name='pool_recovery_pending' AND (SELECT value FROM alert_state WHERE name='pool_breach')='healthy'`)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *healthStore) String() string { return fmt.Sprintf("healthStore") }
func (s *healthStore) alert(name string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM alert_state WHERE name=?`, name).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}
func (s *healthStore) setAlert(name, value string) error {
	_, err := s.db.Exec(`INSERT INTO alert_state(name,value) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET value=excluded.value`, name, value)
	return err
}
func (s *healthStore) incidents(provider, state string, limit int, before int64) ([]healthIncident, error) {
	return s.incidentsFiltered(incidentFilter{Provider: provider, State: state}, limit, before)
}
func (s *healthStore) history(limit int, before int64) ([]healthHistory, error) {
	return s.historyFiltered(historyFilter{}, limit, before)
}
func opaqueCursor(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d", id)))
}
