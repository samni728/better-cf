package searchmemory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"strings"
	"time"
)

type PrefixInsight struct {
	Prefix      string
	Attempts    int
	Successes   int
	Failures    int
	SuccessRate float64
	LastSeenAt  string
}

type PortInsight struct {
	Scheme       string
	Port         int
	Attempts     int
	Successes    int
	SuccessRate  float64
	AvgLatencyMs int
}

type HTTPOnlyInsight struct {
	Prefix        string
	HTTPIPs       int
	HTTPAttempts  int
	HTTPSAttempts int
	LastSeenAt    string
}

type ManualPriorityInsight struct {
	Prefix       string
	SeedIP       string
	NarrowPrefix string
}

type ProfileInsight struct {
	ID                 string
	Profile            Profile
	CreatedAt          string
	LastUsedAt         string
	Summary            Summary
	RecentUniqueIPs    int
	RecentPrefixes     int
	CoveragePercent    float64
	SuccessfulPrefixes []PrefixInsight
	CoolingPrefixes    []string
	ManualPrefixes     []string
	ManualPriorities   []ManualPriorityInsight
	Budget             CandidateBudget
	Ports              []PortInsight
	HTTPOnlyPrefixes   []HTTPOnlyInsight
}

func (s *Store) RecommendBudget(ctx context.Context, profileID string, version int, now time.Time) (CandidateBudget, error) {
	defaultBudget := CandidateBudget{Exact: 15, Narrow: 30, Wide: 20, Global: 35}
	rows, err := s.db.QueryContext(ctx, `SELECT candidate_source,
		COUNT(*), SUM(CASE WHEN outcome='true_success' THEN 1 ELSE 0 END)
		FROM ip_observations
		WHERE profile_id=? AND ip_version=? AND outcome IN ('true_success','true_failure') AND tested_at>=?
		GROUP BY candidate_source`, profileID, version, now.Add(-7*24*time.Hour).UTC().Format(time.RFC3339Nano))
	if err != nil {
		return defaultBudget, err
	}
	defer rows.Close()
	type sourceStat struct{ attempts, successes int }
	stats := map[string]sourceStat{}
	totalAttempts := 0
	for rows.Next() {
		var source string
		var stat sourceStat
		if err := rows.Scan(&source, &stat.attempts, &stat.successes); err != nil {
			return defaultBudget, err
		}
		source = normalizeSource(source)
		stats[source] = stat
		totalAttempts += stat.attempts
	}
	if totalAttempts == 0 {
		return defaultBudget, nil
	}

	sources := []string{"exact", "narrow", "wide", "global"}
	floors := map[string]int{"exact": 10, "narrow": 20, "wide": 15, "global": 15}
	scores := make(map[string]float64, len(sources))
	scoreTotal := 0.0
	for _, source := range sources {
		stat := stats[source]
		// Beta 平滑避免样本很少时一次成功就吃掉全部预算。
		score := float64(stat.successes+1) / float64(stat.attempts+4)
		scores[source] = score
		scoreTotal += score
	}
	budgetMap := make(map[string]int, len(sources))
	remaining := 40
	allocated := 0
	for _, source := range sources {
		extra := int(math.Floor(float64(remaining) * scores[source] / scoreTotal))
		budgetMap[source] = floors[source] + extra
		allocated += extra
	}
	for i := 0; allocated < remaining; i++ {
		budgetMap[sources[i%len(sources)]]++
		allocated++
	}
	return CandidateBudget{Exact: budgetMap["exact"], Narrow: budgetMap["narrow"], Wide: budgetMap["wide"], Global: budgetMap["global"]}, nil
}

func (s *Store) AddManualPrefix(ctx context.Context, profileID string, version int, raw string) (string, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
	if err != nil || (version == 4) != prefix.Addr().Is4() {
		return "", errors.New("优先网段格式或 IP 版本不正确")
	}
	wantBits := 32
	if version == 4 {
		wantBits = 16
	}
	if prefix.Bits() != wantBits {
		return "", fmt.Errorf("IPv%d 手动优先网段必须使用 /%d", version, wantBits)
	}
	seedIP := ""
	if addr := prefix.Addr(); addr != prefix.Masked().Addr() {
		seedIP = addr.String()
	}
	prefix = prefix.Masked()
	var rawProfile string
	if err := s.db.QueryRowContext(ctx, `SELECT config_json FROM search_profiles WHERE id=?`, profileID).Scan(&rawProfile); errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("搜索记忆配置档案不存在，请刷新设置页")
	} else if err != nil {
		return "", err
	}
	var profile Profile
	if err := json.Unmarshal([]byte(rawProfile), &profile); err != nil || profile.IPVersion != version {
		return "", errors.New("搜索记忆配置档案与 IP 版本不匹配")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO manual_prefixes(profile_id,prefix,seed_ip,created_at) VALUES(?,?,?,?)
		ON CONFLICT(profile_id,prefix) DO UPDATE SET seed_ip=CASE WHEN excluded.seed_ip<>'' THEN excluded.seed_ip ELSE manual_prefixes.seed_ip END, created_at=excluded.created_at`, profileID, prefix.String(), seedIP, time.Now().UTC().Format(time.RFC3339Nano))
	return prefix.String(), err
}

func (s *Store) RemoveManualPrefix(ctx context.Context, profileID, raw string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM manual_prefixes WHERE profile_id=? AND prefix=?`, profileID, strings.TrimSpace(raw))
	return err
}

func (s *Store) ClearProfile(ctx context.Context, profileID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM port_observations WHERE observation_id IN (SELECT id FROM ip_observations WHERE profile_id=?)`, profileID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ip_observations WHERE profile_id=?`, profileID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM manual_prefixes WHERE profile_id=?`, profileID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM search_profiles WHERE id=?`, profileID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListProfileInsights(ctx context.Context, now time.Time) ([]ProfileInsight, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,config_json,created_at,last_used_at FROM search_profiles ORDER BY last_used_at DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type profileRow struct{ id, raw, created, used string }
	var profiles []profileRow
	for rows.Next() {
		var item profileRow
		if err := rows.Scan(&item.id, &item.raw, &item.created, &item.used); err != nil {
			return nil, err
		}
		profiles = append(profiles, item)
	}
	result := make([]ProfileInsight, 0, len(profiles))
	for _, item := range profiles {
		var profile Profile
		if json.Unmarshal([]byte(item.raw), &profile) != nil {
			continue
		}
		insight, err := s.ProfileInsight(ctx, item.id, profile, now)
		if err != nil {
			return nil, err
		}
		insight.CreatedAt, insight.LastUsedAt = item.created, item.used
		result = append(result, insight)
	}
	return result, nil
}

func (s *Store) ProfileInsight(ctx context.Context, profileID string, profile Profile, now time.Time) (ProfileInsight, error) {
	result := ProfileInsight{ID: profileID, Profile: profile}
	var err error
	result.Summary, err = s.Summary(ctx, profileID, profile.IPVersion, now)
	if err != nil {
		return result, err
	}
	result.Budget, err = s.RecommendBudget(ctx, profileID, profile.IPVersion, now)
	if err != nil {
		return result, err
	}
	cutoff := now.Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT ip), COUNT(DISTINCT prefix_narrow)
		FROM ip_observations WHERE profile_id=? AND ip_version=?
		AND outcome IN ('true_success','true_failure','scan_success','bandwidth_pass','bandwidth_fail','region_match') AND tested_at>=?`, profileID, profile.IPVersion, cutoff).
		Scan(&result.RecentUniqueIPs, &result.RecentPrefixes); err != nil {
		return result, err
	}
	if result.RecentPrefixes > 0 {
		result.CoveragePercent = math.Min(100, float64(result.RecentUniqueIPs)*100/float64(result.RecentPrefixes*256))
	}
	result.SuccessfulPrefixes, err = s.successfulPrefixes(ctx, profileID, profile.IPVersion, cutoff)
	if err != nil {
		return result, err
	}
	memory, err := s.Candidates(ctx, profileID, profile.IPVersion, now)
	if err != nil {
		return result, err
	}
	result.CoolingPrefixes = memory.ExcludePrefixes
	result.ManualPrefixes = memory.ManualPrefixes
	for _, prefix := range memory.ManualPrefixes {
		item := ManualPriorityInsight{Prefix: prefix}
		for _, seed := range memory.ManualSeedIPs {
			addr, addrErr := netip.ParseAddr(seed)
			parent, prefixErr := netip.ParsePrefix(prefix)
			if addrErr == nil && prefixErr == nil && parent.Contains(addr) {
				item.SeedIP = seed
				bits := 48
				if addr.Is4() {
					bits = 24
				}
				item.NarrowPrefix = netip.PrefixFrom(addr, bits).Masked().String()
				break
			}
		}
		result.ManualPriorities = append(result.ManualPriorities, item)
	}
	result.Ports, err = s.portInsights(ctx, profileID, cutoff)
	if err != nil {
		return result, err
	}
	if profile.HTTPEnabled && profile.HTTPSEnabled {
		result.HTTPOnlyPrefixes, err = s.httpOnlyPrefixes(ctx, profileID, cutoff)
	}
	return result, err
}

func (s *Store) successfulPrefixes(ctx context.Context, profileID string, version int, cutoff string) ([]PrefixInsight, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT prefix_narrow,COUNT(*),
		SUM(CASE WHEN outcome='true_success' THEN 1 ELSE 0 END),
		SUM(CASE WHEN outcome='true_failure' THEN 1 ELSE 0 END),MAX(tested_at)
		FROM ip_observations WHERE profile_id=? AND ip_version=? AND outcome IN ('true_success','true_failure') AND tested_at>=?
		GROUP BY prefix_narrow HAVING SUM(CASE WHEN outcome='true_success' THEN 1 ELSE 0 END)>0
		ORDER BY SUM(CASE WHEN outcome='true_success' THEN 1 ELSE 0 END) DESC,MAX(tested_at) DESC LIMIT 20`, profileID, version, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PrefixInsight
	for rows.Next() {
		var item PrefixInsight
		if err := rows.Scan(&item.Prefix, &item.Attempts, &item.Successes, &item.Failures, &item.LastSeenAt); err != nil {
			return nil, err
		}
		if item.Attempts > 0 {
			item.SuccessRate = float64(item.Successes) * 100 / float64(item.Attempts)
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *Store) portInsights(ctx context.Context, profileID, cutoff string) ([]PortInsight, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.scheme,p.port,COUNT(*),SUM(p.success),
		AVG(CASE WHEN p.success=1 THEN p.latency_ms END)
		FROM port_observations p JOIN ip_observations i ON i.id=p.observation_id
		WHERE i.profile_id=? AND i.tested_at>=? GROUP BY p.scheme,p.port ORDER BY p.scheme,p.port`, profileID, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PortInsight
	for rows.Next() {
		var item PortInsight
		var avg sql.NullFloat64
		if err := rows.Scan(&item.Scheme, &item.Port, &item.Attempts, &item.Successes, &avg); err != nil {
			return nil, err
		}
		if item.Attempts > 0 {
			item.SuccessRate = float64(item.Successes) * 100 / float64(item.Attempts)
		}
		if avg.Valid {
			item.AvgLatencyMs = int(math.Round(avg.Float64))
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *Store) httpOnlyPrefixes(ctx context.Context, profileID, cutoff string) ([]HTTPOnlyInsight, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT i.prefix_narrow,
		COUNT(DISTINCT CASE WHEN p.scheme='HTTP' AND p.success=1 THEN i.ip END),
		SUM(CASE WHEN p.scheme='HTTP' THEN 1 ELSE 0 END),
		SUM(CASE WHEN p.scheme='HTTPS' THEN 1 ELSE 0 END),MAX(i.tested_at)
		FROM ip_observations i JOIN port_observations p ON p.observation_id=i.id
		WHERE i.profile_id=? AND i.tested_at>=? GROUP BY i.prefix_narrow
		HAVING SUM(CASE WHEN p.scheme='HTTP' AND p.success=1 THEN 1 ELSE 0 END)>0
		AND SUM(CASE WHEN p.scheme='HTTPS' AND p.success=1 THEN 1 ELSE 0 END)=0
		AND SUM(CASE WHEN p.scheme='HTTPS' THEN 1 ELSE 0 END)>0
		ORDER BY COUNT(DISTINCT CASE WHEN p.scheme='HTTP' AND p.success=1 THEN i.ip END) DESC LIMIT 20`, profileID, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []HTTPOnlyInsight
	for rows.Next() {
		var item HTTPOnlyInsight
		if err := rows.Scan(&item.Prefix, &item.HTTPIPs, &item.HTTPAttempts, &item.HTTPSAttempts, &item.LastSeenAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}
