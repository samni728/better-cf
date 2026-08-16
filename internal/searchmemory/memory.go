package searchmemory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 2

type Store struct {
	db *sql.DB
}

type Profile struct {
	IPVersion     int
	LocationMode  string
	Country       string
	Region        string
	City          string
	BandwidthMbps int
	MaxRTTMs      int
	HTTPEnabled   bool
	HTTPNodeHash  string
	HTTPSEnabled  bool
	HTTPSNodeHash string
	TestURL       string
	NetworkLabel  string
}

type PortObservation struct {
	Scheme     string
	Port       int
	Success    bool
	LatencyMs  int
	ErrorClass string
}

type Observation struct {
	RunID             string
	ProfileID         string
	IP                string
	IPVersion         int
	Outcome           string
	ErrorClass        string
	CandidateSource   string
	DataCenterCountry string
	DataCenterCode    string
	RTTMs             int
	BandwidthMbps     int
	TestedAt          time.Time
	Ports             []PortObservation
}

type LegacyObservation struct {
	IP                string
	IPVersion         int
	DataCenterCountry string
	DataCenterCode    string
	RTTMs             int
	BandwidthMbps     int
	TestedAt          time.Time
}

type CandidateMemory struct {
	SuccessIPs      []string
	HintPrefixes    []string
	ManualPrefixes  []string
	ExcludeIPs      []string
	ExcludePrefixes []string
	Budget          CandidateBudget
}

type CandidateBudget struct {
	Exact  int
	Narrow int
	Wide   int
	Global int
}

type Summary struct {
	Observations    int
	Successes       int
	Failures        int
	HotSuccesses    int
	RegionMatches   int
	BandwidthPasses int
	BandwidthFails  int
	CoolingIPs      int
	CoolingPrefixes int
	LastObservedAt  string
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS schema_meta (version INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS search_profiles (
			id TEXT PRIMARY KEY,
			config_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			last_used_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS ip_observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id TEXT NOT NULL DEFAULT '',
			profile_id TEXT NOT NULL,
			ip TEXT NOT NULL,
			ip_version INTEGER NOT NULL,
			prefix_narrow TEXT NOT NULL,
			prefix_wide TEXT NOT NULL,
			outcome TEXT NOT NULL,
			error_class TEXT NOT NULL DEFAULT '',
			candidate_source TEXT NOT NULL DEFAULT 'global',
			dc_country TEXT NOT NULL DEFAULT '',
			dc_code TEXT NOT NULL DEFAULT '',
			rtt_ms INTEGER NOT NULL DEFAULT 0,
			bandwidth_mbps INTEGER NOT NULL DEFAULT 0,
			tested_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS port_observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			observation_id INTEGER NOT NULL,
			scheme TEXT NOT NULL,
			port INTEGER NOT NULL,
			success INTEGER NOT NULL,
			latency_ms INTEGER NOT NULL DEFAULT 0,
			error_class TEXT NOT NULL DEFAULT '',
			FOREIGN KEY(observation_id) REFERENCES ip_observations(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS manual_prefixes (
			profile_id TEXT NOT NULL,
			prefix TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY(profile_id, prefix)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ip_memory_profile_ip_time ON ip_observations(profile_id, ip, tested_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_ip_memory_profile_prefix_time ON ip_observations(profile_id, prefix_narrow, tested_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_ip_memory_profile_outcome_time ON ip_observations(profile_id, outcome, tested_at DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_legacy_unique ON ip_observations(profile_id, ip, tested_at)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	var version int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM schema_meta LIMIT 1`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err = s.db.ExecContext(ctx, `INSERT INTO schema_meta(version) VALUES (?)`, schemaVersion); err != nil {
			return err
		}
		version = schemaVersion
		err = nil
	}
	if err != nil {
		return err
	}
	if version == 1 {
		if err := s.addColumnIfMissing(ctx, "ip_observations", "candidate_source", `ALTER TABLE ip_observations ADD COLUMN candidate_source TEXT NOT NULL DEFAULT 'global'`); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE schema_meta SET version=?`, schemaVersion); err != nil {
			return err
		}
		version = schemaVersion
	}
	if version != schemaVersion {
		return fmt.Errorf("unsupported search memory schema %d", version)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_ip_memory_profile_source_time ON ip_observations(profile_id, candidate_source, tested_at DESC)`); err != nil {
		return err
	}
	return nil
}

func (s *Store) addColumnIfMissing(ctx context.Context, table, column, statement string) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	_, err = s.db.ExecContext(ctx, statement)
	return err
}

func ProfileID(profile Profile) (string, error) {
	data, err := json.Marshal(profile)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16]), nil
}

func SecretFingerprint(raw string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	return hex.EncodeToString(sum[:16])
}

func (s *Store) EnsureProfile(ctx context.Context, profile Profile) (string, error) {
	id, err := ProfileID(profile)
	if err != nil {
		return "", err
	}
	data, _ := json.Marshal(profile)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `INSERT INTO search_profiles(id, config_json, created_at, last_used_at)
		VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET last_used_at=excluded.last_used_at`, id, string(data), now, now)
	return id, err
}

func prefixes(ip string, version int) (string, string, error) {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return "", "", err
	}
	if version == 4 {
		return netip.PrefixFrom(addr, 24).Masked().String(), netip.PrefixFrom(addr, 16).Masked().String(), nil
	}
	return netip.PrefixFrom(addr, 48).Masked().String(), netip.PrefixFrom(addr, 32).Masked().String(), nil
}

func (s *Store) Record(ctx context.Context, observation Observation) error {
	narrow, wide, err := prefixes(observation.IP, observation.IPVersion)
	if err != nil {
		return err
	}
	if observation.TestedAt.IsZero() {
		observation.TestedAt = time.Now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO ip_observations
		(run_id,profile_id,ip,ip_version,prefix_narrow,prefix_wide,outcome,error_class,candidate_source,dc_country,dc_code,rtt_ms,bandwidth_mbps,tested_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, observation.RunID, observation.ProfileID, observation.IP, observation.IPVersion,
		narrow, wide, observation.Outcome, observation.ErrorClass, normalizeSource(observation.CandidateSource), observation.DataCenterCountry, observation.DataCenterCode,
		observation.RTTMs, observation.BandwidthMbps, observation.TestedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	for _, port := range observation.Ports {
		_, err = tx.ExecContext(ctx, `INSERT INTO port_observations(observation_id,scheme,port,success,latency_ms,error_class) VALUES(?,?,?,?,?,?)`,
			id, port.Scheme, port.Port, boolInt(port.Success), port.LatencyMs, port.ErrorClass)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func normalizeSource(value string) string {
	switch strings.TrimSpace(value) {
	case "exact", "narrow", "wide", "global":
		return strings.TrimSpace(value)
	default:
		return "global"
	}
}

func (s *Store) ImportLegacy(ctx context.Context, items []LegacyObservation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range items {
		narrow, wide, err := prefixes(item.IP, item.IPVersion)
		if err != nil {
			continue
		}
		testedAt := item.TestedAt
		if testedAt.IsZero() {
			testedAt = time.Now()
		}
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO ip_observations
			(run_id,profile_id,ip,ip_version,prefix_narrow,prefix_wide,outcome,error_class,candidate_source,dc_country,dc_code,rtt_ms,bandwidth_mbps,tested_at)
			VALUES('', 'legacy-scan-v1', ?, ?, ?, ?, 'scan_success_unverified', '', 'global', ?, ?, ?, ?, ?)`,
			item.IP, item.IPVersion, narrow, wide, item.DataCenterCountry, item.DataCenterCode, item.RTTMs, item.BandwidthMbps, testedAt.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Candidates(ctx context.Context, profileID string, version int, now time.Time) (CandidateMemory, error) {
	var memory CandidateMemory
	memory.Budget = CandidateBudget{Exact: 15, Narrow: 30, Wide: 20, Global: 35}
	manualRows, err := s.db.QueryContext(ctx, `SELECT prefix FROM manual_prefixes WHERE profile_id=? ORDER BY created_at DESC`, profileID)
	if err != nil {
		return memory, err
	}
	for manualRows.Next() {
		var prefix string
		if manualRows.Scan(&prefix) == nil {
			memory.ManualPrefixes = append(memory.ManualPrefixes, prefix)
			memory.HintPrefixes = append(memory.HintPrefixes, prefix)
		}
	}
	manualRows.Close()
	sevenDays := now.Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	threeDays := now.Add(-3 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	oneDay := now.Add(-24 * time.Hour).UTC().Format(time.RFC3339Nano)
	sixHours := now.Add(-6 * time.Hour).UTC().Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx, `SELECT ip, MAX(tested_at) latest FROM ip_observations
		WHERE profile_id=? AND ip_version=? AND outcome IN ('true_success','scan_success','bandwidth_pass') AND tested_at>=?
		AND NOT EXISTS (SELECT 1 FROM ip_observations f WHERE f.profile_id=ip_observations.profile_id
			AND f.ip=ip_observations.ip AND f.outcome='true_failure' AND f.tested_at>ip_observations.tested_at)
		GROUP BY ip ORDER BY latest DESC LIMIT 100`, profileID, version, sevenDays)
	if err != nil {
		return memory, err
	}
	for rows.Next() {
		var ip, at string
		if rows.Scan(&ip, &at) == nil {
			memory.SuccessIPs = append(memory.SuccessIPs, ip)
		}
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `SELECT prefix_narrow, prefix_wide,
		SUM(CASE outcome WHEN 'true_success' THEN 12 WHEN 'scan_success' THEN 10 WHEN 'bandwidth_pass' THEN 8 WHEN 'bandwidth_fail' THEN 3 WHEN 'true_failure' THEN 2 WHEN 'region_match' THEN 1 ELSE 0 END) score,
		SUM(CASE WHEN tested_at>=? THEN CASE outcome WHEN 'true_success' THEN 12 WHEN 'scan_success' THEN 10 WHEN 'bandwidth_pass' THEN 8 WHEN 'bandwidth_fail' THEN 3 WHEN 'true_failure' THEN 2 WHEN 'region_match' THEN 1 ELSE 0 END ELSE 0 END) hot_score,
		MAX(tested_at) latest FROM ip_observations WHERE profile_id=? AND ip_version=? AND tested_at>=?
		AND outcome IN ('true_success','scan_success','bandwidth_pass','bandwidth_fail','true_failure','region_match')
		GROUP BY prefix_narrow,prefix_wide HAVING score>0 ORDER BY hot_score DESC,score DESC,latest DESC LIMIT 20`, threeDays, profileID, version, sevenDays)
	if err != nil {
		return memory, err
	}
	prefixScore := make(map[string]int)
	for rows.Next() {
		var narrow, wide, latest string
		var score, hotScore int
		if rows.Scan(&narrow, &wide, &score, &hotScore, &latest) == nil {
			// 地区命中即可保留父网段线索；带宽和真连接越靠后，优先级越高。
			prefixScore[narrow] += score*3 + hotScore*2
			prefixScore[wide] += score + hotScore
		}
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `SELECT f.ip FROM ip_observations f
		WHERE f.profile_id=? AND f.ip_version=? AND f.outcome='bandwidth_fail' AND f.tested_at>=?
		AND NOT EXISTS (SELECT 1 FROM ip_observations s2 WHERE s2.profile_id=f.profile_id AND s2.ip=f.ip
			AND s2.outcome IN ('bandwidth_pass','scan_success','true_success') AND s2.tested_at>f.tested_at)
		GROUP BY f.ip LIMIT 2000`, profileID, version, sixHours)
	if err != nil {
		return memory, err
	}
	for rows.Next() {
		var ip string
		if rows.Scan(&ip) == nil {
			memory.ExcludeIPs = append(memory.ExcludeIPs, ip)
		}
	}
	rows.Close()
	type scored struct {
		value string
		score int
	}
	var scoredPrefixes []scored
	for value, score := range prefixScore {
		scoredPrefixes = append(scoredPrefixes, scored{value, score})
	}
	sort.Slice(scoredPrefixes, func(i, j int) bool { return scoredPrefixes[i].score > scoredPrefixes[j].score })
	for _, item := range scoredPrefixes {
		memory.HintPrefixes = append(memory.HintPrefixes, item.value)
	}

	rows, err = s.db.QueryContext(ctx, `SELECT f.ip FROM ip_observations f
		WHERE f.profile_id=? AND f.ip_version=? AND f.outcome='true_failure' AND f.tested_at>=?
		AND NOT EXISTS (SELECT 1 FROM ip_observations s2 WHERE s2.profile_id=f.profile_id AND s2.ip=f.ip AND s2.outcome='true_success' AND s2.tested_at>f.tested_at)
		GROUP BY f.ip LIMIT 2000`, profileID, version, threeDays)
	if err != nil {
		return memory, err
	}
	for rows.Next() {
		var ip string
		if rows.Scan(&ip) == nil {
			memory.ExcludeIPs = append(memory.ExcludeIPs, ip)
		}
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `SELECT f.prefix_narrow FROM ip_observations f
		WHERE f.profile_id=? AND f.ip_version=? AND f.outcome='true_failure' AND f.tested_at>=?
		AND NOT EXISTS (SELECT 1 FROM ip_observations s2 WHERE s2.profile_id=f.profile_id AND s2.prefix_narrow=f.prefix_narrow AND s2.outcome='true_success' AND s2.tested_at>=?)
		GROUP BY f.prefix_narrow HAVING COUNT(DISTINCT f.ip)>=8 LIMIT 200`, profileID, version, oneDay, oneDay)
	if err != nil {
		return memory, err
	}
	for rows.Next() {
		var prefix string
		if rows.Scan(&prefix) == nil {
			memory.ExcludePrefixes = append(memory.ExcludePrefixes, prefix)
		}
	}
	rows.Close()
	if budget, err := s.RecommendBudget(ctx, profileID, version, now); err == nil {
		memory.Budget = budget
	}
	return memory, nil
}

func (s *Store) Prune(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM port_observations WHERE observation_id IN
		(SELECT id FROM ip_observations WHERE tested_at < ?)`, now.Add(-7*24*time.Hour).UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM ip_observations WHERE tested_at < ? AND outcome!='true_success'`, now.Add(-30*24*time.Hour).UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) Summary(ctx context.Context, profileID string, version int, now time.Time) (Summary, error) {
	var result Summary
	row := s.db.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN outcome='true_success' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN outcome='true_failure' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN outcome='true_success' AND tested_at>=? THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN outcome='region_match' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN outcome='bandwidth_pass' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN outcome='bandwidth_fail' THEN 1 ELSE 0 END),0),
		MAX(tested_at) FROM ip_observations WHERE profile_id=?`, now.Add(-3*24*time.Hour).UTC().Format(time.RFC3339Nano), profileID)
	var last sql.NullString
	if err := row.Scan(&result.Observations, &result.Successes, &result.Failures, &result.HotSuccesses, &result.RegionMatches, &result.BandwidthPasses, &result.BandwidthFails, &last); err != nil {
		return result, err
	}
	if last.Valid {
		result.LastObservedAt = last.String
	}
	memory, err := s.Candidates(ctx, profileID, version, now)
	if err == nil {
		result.CoolingIPs = len(memory.ExcludeIPs)
		result.CoolingPrefixes = len(memory.ExcludePrefixes)
	}
	return result, err
}
