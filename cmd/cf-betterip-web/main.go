package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cf-betterip-ser/internal/geodb"
)

type App struct {
	store        *Store
	sessions     *SessionStore
	tasks        *TaskManager
	dataDir      string
	geoMu        sync.RWMutex
	geoLocations []geodb.Location
	geoDatabase  GeoDatabaseStatus
}

type Store struct {
	path  string
	mu    sync.Mutex
	state AppState
}

type AppState struct {
	Admin     *AdminConfig   `json:"admin,omitempty"`
	Settings  Settings       `json:"settings"`
	Runs      []RunRecord    `json:"runs,omitempty"`
	Results   []IPTestResult `json:"results,omitempty"`
	UpdatedAt string         `json:"updated_at"`
}

type AdminConfig struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
	CreatedAt    string `json:"created_at"`
}

type Settings struct {
	CloudflareAPIToken   string       `json:"cloudflare_api_token,omitempty"`
	CloudflareAccountID  string       `json:"cloudflare_account_id,omitempty"`
	CloudflareZoneID     string       `json:"cloudflare_zone_id,omitempty"`
	RecordName           string       `json:"record_name,omitempty"`
	DNSTargetMode        string       `json:"dns_target_mode,omitempty"`
	IPv4Target           TargetConfig `json:"ipv4_target"`
	IPv6Target           TargetConfig `json:"ipv6_target"`
	IPv4Enabled          bool         `json:"ipv4_enabled"`
	IPv6Enabled          bool         `json:"ipv6_enabled"`
	IPv4Count            int          `json:"ipv4_count"`
	IPv6Count            int          `json:"ipv6_count"`
	UseTLS               bool         `json:"use_tls"`
	BandwidthMbps        int          `json:"bandwidth_mbps"`
	RTTConcurrency       int          `json:"rtt_concurrency"`
	LocationMode         string       `json:"location_mode,omitempty"`
	LocationCountry      string       `json:"location_country,omitempty"`
	LocationRegion       string       `json:"location_region,omitempty"`
	LocationCity         string       `json:"location_city,omitempty"`
	ScheduleEnabled      bool         `json:"schedule_enabled"`
	ScheduleMode         string       `json:"schedule_mode,omitempty"`
	ScheduleIntervalDays int          `json:"schedule_interval_days"`
	ScheduleTime         string       `json:"schedule_time,omitempty"`
}

type TargetConfig struct {
	RecordName          string `json:"record_name,omitempty"`
	CredentialMode      string `json:"credential_mode,omitempty"`
	CloudflareAPIToken  string `json:"cloudflare_api_token,omitempty"`
	CloudflareAccountID string `json:"cloudflare_account_id,omitempty"`
	CloudflareZoneID    string `json:"cloudflare_zone_id,omitempty"`
}

type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]string
}

type TaskManager struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

type RunRecord struct {
	ID              string   `json:"id"`
	Trigger         string   `json:"trigger"`
	Status          string   `json:"status"`
	Mode            string   `json:"mode"`
	Stage           string   `json:"stage,omitempty"`
	Progress        int      `json:"progress"`
	UpdatedIPCount  int      `json:"updated_ip_count"`
	SyncedIPCount   int      `json:"synced_ip_count"`
	RequiredIPCount int      `json:"required_ip_count"`
	DNSStatus       string   `json:"dns_status,omitempty"`
	StartedAt       string   `json:"started_at"`
	FinishedAt      string   `json:"finished_at,omitempty"`
	Summary         string   `json:"summary,omitempty"`
	Logs            []RunLog `json:"logs"`
}

type RunLog struct {
	At      string `json:"at"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type IPTestResult struct {
	RunID                   string `json:"run_id"`
	IP                      string `json:"ip"`
	IPVersion               int    `json:"ip_version"`
	RecordType              string `json:"record_type"`
	Protocol                string `json:"protocol"`
	ConfiguredBandwidthMbps int    `json:"configured_bandwidth_mbps"`
	MeasuredBandwidthMbps   int    `json:"measured_bandwidth_mbps"`
	PeakSpeedKBps           int    `json:"peak_speed_kbps"`
	RTTMs                   int    `json:"rtt_ms"`
	DataCenter              string `json:"data_center"`
	DataCenterCode          string `json:"data_center_code,omitempty"`
	DataCenterCountry       string `json:"data_center_country,omitempty"`
	DataCenterRegion        string `json:"data_center_region,omitempty"`
	DataCenterCity          string `json:"data_center_city,omitempty"`
	DurationSeconds         int    `json:"duration_seconds"`
	SelectedForDNS          bool   `json:"selected_for_dns"`
	CloudflareSynced        bool   `json:"cloudflare_synced"`
	TestedAt                string `json:"tested_at"`
}

type PageData struct {
	Title               string
	Flash               string
	Error               string
	Username            string
	Settings            Settings
	TokenMasked         string
	HasAdmin            bool
	DNSTargetModeLabel  string
	IPv4RecordName      string
	IPv6RecordName      string
	IPv4CredentialLabel string
	IPv6CredentialLabel string
	IPv4TokenMasked     string
	IPv6TokenMasked     string
	ScheduleSummary     string
	LocationSummary     string
	NextRunAt           string
	RecentRuns          []RunRecord
	HasRunningRun       bool
	Stats               DashboardStats
	CurrentRun          *RunRecord
	LatestRun           *RunRecord
	ConfigTestResults   []ConfigTestResult
	CanResumeRun        bool
	LatestResultSummary IPResultSummary
	LatestIPv4Results   []IPResultView
	LatestIPv6Results   []IPResultView
	TodayResultSummary  IPResultSummary
	TodayIPv4Results    []IPResultView
	TodayIPv6Results    []IPResultView
	GeoCountries        []GeoChoice
	GeoRegions          []GeoChoice
	GeoCities           []GeoChoice
	GeoLocations        []geodb.Location
	GeoDatabase         GeoDatabaseStatus
}

type GeoLocation struct {
	IATA    string  `json:"iata"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	Country string  `json:"cca2"`
	Region  string  `json:"region"`
	City    string  `json:"city"`
}

type GeoChoice struct {
	Value    string
	Label    string
	Selected bool
}

type GeoDatabaseStatus struct {
	LocationCount int
	GeoFeedCount  int
	UpdatedAt     string
	Ready         bool
}

type DashboardStats struct {
	ProductStatus     string
	ProductStatusText string
	ProductStatusHint string
	TodayUpdatedIPs   int
	TodaySyncedIPs    int
	TodayTaskCount    int
	ExpectedIPCount   int
	CurrentStage      string
	CurrentProgress   int
	LastDNSStatus     string
	ConfigReady       bool
	ConfigHint        string
}

type IPResultSummary struct {
	Title            string
	Total            int
	IPv4Count        int
	IPv6Count        int
	SyncedCount      int
	BestIP           string
	BestDataCenter   string
	BestMeasuredMbps int
	BestPeakKBps     int
	BestRTTMs        int
}

type IPResultView struct {
	Index                   int
	RunID                   string
	IP                      string
	Family                  string
	RecordType              string
	Protocol                string
	ConfiguredBandwidthMbps int
	MeasuredBandwidthMbps   int
	PeakSpeedKBps           int
	RTTMs                   int
	DataCenter              string
	DataCenterCode          string
	DataCenterCountry       string
	DataCenterRegion        string
	DurationSeconds         int
	SyncedText              string
	TestedAt                string
}

type ConfigTestTarget struct {
	Label      string
	RecordName string
	APIToken   string
	ZoneID     string
}

type ConfigTestResult struct {
	Label       string
	RecordName  string
	TestName    string
	Success     bool
	Message     string
	CreatedID   string
	CompletedAt string
}

type DNSSyncTarget struct {
	Label      string
	RecordName string
	RecordType string
	APIToken   string
	ZoneID     string
	IPs        []string
}

type CloudflareDNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

func main() {
	listen := flag.String("listen", envOrDefault("LISTEN_ADDR", ":18080"), "HTTP listen address")
	dataDir := flag.String("data-dir", envOrDefault("DATA_DIR", "./data"), "data directory")
	flag.Parse()

	store, err := NewStore(filepath.Join(*dataDir, "app_state.json"))
	if err != nil {
		log.Fatal(err)
	}

	dataCenterLocations := loadGeoLocations(*dataDir)
	geoEntries := loadGeoDatabase(*dataDir)
	app := &App{
		store:        store,
		sessions:     &SessionStore{sessions: make(map[string]string)},
		tasks:        &TaskManager{cancels: make(map[string]context.CancelFunc)},
		dataDir:      *dataDir,
		geoLocations: geodb.Locations(geoEntries),
		geoDatabase:  readGeoDatabaseStatus(*dataDir, len(dataCenterLocations)),
	}
	go app.schedulerLoop()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", app.healthz)
	mux.HandleFunc("/setup", app.setup)
	mux.HandleFunc("/login", app.login)
	mux.HandleFunc("/logout", app.logout)
	mux.HandleFunc("/settings", app.requireAuth(app.settings))
	mux.HandleFunc("/settings/test", app.requireAuth(app.testSettings))
	mux.HandleFunc("/settings/geo-refresh", app.requireAuth(app.refreshGeoDatabase))
	mux.HandleFunc("/runs/start", app.requireAuth(app.startRun))
	mux.HandleFunc("/runs/resume", app.requireAuth(app.resumeRun))
	mux.HandleFunc("/runs/stop", app.requireAuth(app.stopRun))
	mux.HandleFunc("/runs/delete", app.requireAuth(app.deleteRun))
	mux.HandleFunc("/api/runs", app.requireAuth(app.runsAPI))
	mux.HandleFunc("/run", app.requireAuth(app.runPage))
	mux.HandleFunc("/dashboard", app.requireAuth(app.dashboard))
	mux.HandleFunc("/", app.root)

	log.Printf("cf-betterip web listening on %s, data=%s", *listen, *dataDir)
	if err := http.ListenAndServe(*listen, mux); err != nil {
		log.Fatal(err)
	}
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

const (
	locationsSourceURL   = "https://www.baipiao.eu.org/cloudflare/locations"
	cloudflareGeoFeedURL = "https://api.cloudflare.com/local-ip-ranges.csv"
)

func loadGeoLocations(dataDir string) []GeoLocation {
	path := filepath.Join(dataDir, "locations.json")
	if data, err := os.ReadFile(path); err == nil {
		if locations := parseGeoLocations(data); len(locations) > 0 {
			return locations
		}
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(locationsSourceURL)
	if err != nil {
		log.Printf("load location options failed: %v", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("load location options failed: HTTP %d", resp.StatusCode)
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		log.Printf("read location options failed: %v", err)
		return nil
	}
	locations := parseGeoLocations(data)
	if len(locations) == 0 {
		log.Printf("load location options failed: response contains no valid locations")
		return nil
	}
	if err := os.MkdirAll(dataDir, 0755); err == nil {
		if err := atomicWriteFile(path, data); err != nil {
			log.Printf("cache location options failed: %v", err)
		}
	}
	return locations
}

func parseGeoLocations(data []byte) []GeoLocation {
	var locations []GeoLocation
	if err := json.Unmarshal(data, &locations); err != nil {
		return nil
	}
	result := locations[:0]
	for _, loc := range locations {
		loc.IATA = strings.ToUpper(strings.TrimSpace(loc.IATA))
		loc.Country = strings.ToUpper(strings.TrimSpace(loc.Country))
		loc.Region = strings.TrimSpace(loc.Region)
		loc.City = strings.TrimSpace(loc.City)
		if loc.IATA == "" || loc.Country == "" || loc.City == "" {
			continue
		}
		result = append(result, loc)
	}
	return result
}

func validateGeoFeed(data []byte) (int, error) {
	entries, err := geodb.Parse(bytes.NewReader(data))
	if err != nil {
		return 0, fmt.Errorf("解析 GeoFeed 失败: %w", err)
	}
	count := len(entries)
	if count < 1000 {
		return 0, fmt.Errorf("GeoFeed 记录数异常: %d", count)
	}
	return count, nil
}

func loadGeoDatabase(dataDir string) []geodb.Entry {
	path := filepath.Join(dataDir, "local-ip-ranges.csv")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		seedCandidates := []string{
			strings.TrimSpace(os.Getenv("BETTER_CF_GEO_DB_SEED")),
			filepath.Join("database", "local-ip-ranges.csv"),
		}
		for _, seedPath := range seedCandidates {
			if seedPath == "" {
				continue
			}
			seed, readErr := os.ReadFile(seedPath)
			if readErr != nil {
				continue
			}
			if _, validateErr := validateGeoFeed(seed); validateErr != nil {
				log.Printf("ignore invalid GeoFeed seed %s: %v", seedPath, validateErr)
				continue
			}
			if writeErr := atomicWriteFile(path, seed); writeErr != nil {
				log.Printf("seed GeoFeed database failed: %v", writeErr)
			} else {
				log.Printf("seeded GeoFeed database from %s", seedPath)
			}
			break
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	entries, err := geodb.Parse(file)
	if err != nil {
		log.Printf("load GeoFeed database failed: %v", err)
		return nil
	}
	return entries
}

func downloadData(client *http.Client, sourceURL string, maxBytes int64) ([]byte, error) {
	resp, err := client.Get(sourceURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("下载内容超过 %d 字节上限", maxBytes)
	}
	return data, nil
}

func atomicWriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".geo-update-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Chmod(0644); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	ok = true
	return nil
}

func readGeoDatabaseStatus(dataDir string, locationCount int) GeoDatabaseStatus {
	status := GeoDatabaseStatus{LocationCount: locationCount}
	path := filepath.Join(dataDir, "local-ip-ranges.csv")
	data, err := os.ReadFile(path)
	if err != nil {
		return status
	}
	count, err := validateGeoFeed(data)
	if err != nil {
		return status
	}
	status.GeoFeedCount = count
	status.Ready = count > 0
	if info, err := os.Stat(path); err == nil {
		status.UpdatedAt = info.ModTime().Format("2006-01-02 15:04:05")
	}
	return status
}

func (a *App) geoSnapshot() ([]geodb.Location, GeoDatabaseStatus) {
	a.geoMu.RLock()
	defer a.geoMu.RUnlock()
	locations := append([]geodb.Location(nil), a.geoLocations...)
	return locations, a.geoDatabase
}

func (a *App) updateGeoDatabase() (GeoDatabaseStatus, error) {
	client := &http.Client{Timeout: 45 * time.Second}
	geoFeedData, err := downloadData(client, cloudflareGeoFeedURL, 16*1024*1024)
	if err != nil {
		return GeoDatabaseStatus{}, fmt.Errorf("下载 Cloudflare GeoFeed 失败: %w", err)
	}
	geoFeedCount, err := validateGeoFeed(geoFeedData)
	if err != nil {
		return GeoDatabaseStatus{}, err
	}
	geoEntries, err := geodb.Parse(bytes.NewReader(geoFeedData))
	if err != nil {
		return GeoDatabaseStatus{}, fmt.Errorf("解析 GeoFeed 失败: %w", err)
	}
	if err := atomicWriteFile(filepath.Join(a.dataDir, "local-ip-ranges.csv"), geoFeedData); err != nil {
		return GeoDatabaseStatus{}, fmt.Errorf("更新 GeoFeed 失败: %w", err)
	}
	_, currentStatus := a.geoSnapshot()
	locationCount := currentStatus.LocationCount
	if locationsData, downloadErr := downloadData(client, locationsSourceURL, 8*1024*1024); downloadErr == nil {
		if locations := parseGeoLocations(locationsData); len(locations) >= 100 {
			if writeErr := atomicWriteFile(filepath.Join(a.dataDir, "locations.json"), locationsData); writeErr == nil {
				locationCount = len(locations)
			}
		}
	}
	status := GeoDatabaseStatus{
		LocationCount: locationCount,
		GeoFeedCount:  geoFeedCount,
		UpdatedAt:     time.Now().Format("2006-01-02 15:04:05"),
		Ready:         true,
	}
	a.geoMu.Lock()
	a.geoLocations = geodb.Locations(geoEntries)
	a.geoDatabase = status
	a.geoMu.Unlock()
	return status, nil
}

func runTimeout() time.Duration {
	hours := clampInt(parseInt(os.Getenv("BETTER_CF_RUN_TIMEOUT_HOURS"), 3), 1, 72)
	return time.Duration(hours) * time.Hour
}

func familyNoResultTimeout() time.Duration {
	minutes := clampInt(parseInt(os.Getenv("BETTER_CF_FAMILY_TIMEOUT_MINUTES"), 30), 5, 1440)
	return time.Duration(minutes) * time.Minute
}

func formatDuration(duration time.Duration) string {
	if duration%time.Hour == 0 {
		return fmt.Sprintf("%d 小时", int(duration/time.Hour))
	}
	if duration%time.Minute == 0 {
		return fmt.Sprintf("%d 分钟", int(duration/time.Minute))
	}
	return duration.String()
}

func NewStore(path string) (*Store, error) {
	store := &Store{path: path}
	store.state.Settings = defaultSettings()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &store.state); err != nil {
			return nil, err
		}
		store.applyDefaults()
		if store.recoverInterruptedRuns() {
			_ = store.saveLocked()
		}
		return store, nil
	} else if errors.Is(err, os.ErrNotExist) {
		store.state.UpdatedAt = nowString()
		return store, nil
	} else {
		return nil, err
	}
}

func defaultSettings() Settings {
	return Settings{
		DNSTargetMode:        "single",
		IPv4Enabled:          true,
		IPv6Enabled:          true,
		IPv4Count:            10,
		IPv6Count:            10,
		UseTLS:               true,
		BandwidthMbps:        100,
		RTTConcurrency:       50,
		LocationMode:         "any",
		ScheduleMode:         "daily",
		ScheduleIntervalDays: 1,
		ScheduleTime:         "06:00",
	}
}

func (s *Store) applyDefaults() {
	if s.state.Settings.DNSTargetMode == "" {
		s.state.Settings.DNSTargetMode = "single"
	}
	if s.state.Settings.DNSTargetMode != "single" && s.state.Settings.DNSTargetMode != "split" {
		s.state.Settings.DNSTargetMode = "single"
	}
	if s.state.Settings.IPv4Target.CredentialMode == "" {
		s.state.Settings.IPv4Target.CredentialMode = "shared"
	}
	if s.state.Settings.IPv6Target.CredentialMode == "" {
		s.state.Settings.IPv6Target.CredentialMode = "shared"
	}
	if !s.state.Settings.IPv4Enabled && s.state.Settings.IPv4Count > 0 {
		s.state.Settings.IPv4Enabled = true
	}
	if !s.state.Settings.IPv6Enabled && s.state.Settings.IPv6Count > 0 {
		s.state.Settings.IPv6Enabled = true
	}
	s.state.Settings.IPv4Count = clampInt(s.state.Settings.IPv4Count, 0, 50)
	s.state.Settings.IPv6Count = clampInt(s.state.Settings.IPv6Count, 0, 50)
	if !s.state.Settings.IPv4Enabled {
		s.state.Settings.IPv4Count = 0
	}
	if !s.state.Settings.IPv6Enabled {
		s.state.Settings.IPv6Count = 0
	}
	if s.state.Settings.BandwidthMbps == 0 {
		s.state.Settings.BandwidthMbps = 100
	}
	if s.state.Settings.RTTConcurrency == 0 {
		s.state.Settings.RTTConcurrency = 50
	}
	s.state.Settings.LocationMode = normalizeLocationMode(s.state.Settings.LocationMode)
	s.state.Settings.LocationCountry = strings.ToUpper(strings.TrimSpace(s.state.Settings.LocationCountry))
	s.state.Settings.LocationRegion = strings.TrimSpace(s.state.Settings.LocationRegion)
	s.state.Settings.LocationCity = strings.TrimSpace(s.state.Settings.LocationCity)
	if s.state.Settings.LocationCountry == "" && s.state.Settings.LocationRegion == "" && s.state.Settings.LocationCity == "" {
		s.state.Settings.LocationMode = "any"
	}
	if s.state.Settings.ScheduleMode == "" {
		s.state.Settings.ScheduleMode = "daily"
	}
	if s.state.Settings.ScheduleMode != "hourly" && s.state.Settings.ScheduleMode != "daily" && s.state.Settings.ScheduleMode != "every_n_days" {
		s.state.Settings.ScheduleMode = "daily"
	}
	if s.state.Settings.ScheduleIntervalDays == 0 {
		s.state.Settings.ScheduleIntervalDays = 1
	}
	if s.state.Settings.ScheduleTime == "" {
		s.state.Settings.ScheduleTime = "06:00"
	}
	for i := range s.state.Runs {
		if strings.Contains(s.state.Runs[i].Summary, "任务框架执行完成") {
			s.state.Runs[i].Status = "failed"
			s.state.Runs[i].Stage = "旧占位任务已废弃"
			s.state.Runs[i].Progress = 0
			s.state.Runs[i].UpdatedIPCount = 0
			s.state.Runs[i].SyncedIPCount = 0
			s.state.Runs[i].DNSStatus = "failed"
			s.state.Runs[i].Summary = "这是一条旧占位任务记录，没有真实扫描或 DNS 同步结果，已废弃。"
		}
	}
}

func (s *Store) recoverInterruptedRuns() bool {
	changed := false
	for i := range s.state.Runs {
		if s.state.Runs[i].Status != "running" {
			continue
		}
		s.state.Runs[i].Status = "failed"
		s.state.Runs[i].Stage = "服务重启中断，可继续执行"
		s.state.Runs[i].DNSStatus = "pending"
		s.state.Runs[i].Summary = "任务在服务重启时中断，已保存的 IP 结果可以通过“继续执行”续接。"
		s.state.Runs[i].FinishedAt = nowString()
		s.state.Runs[i].Logs = append(s.state.Runs[i].Logs, RunLog{
			At:      nowString(),
			Level:   "warn",
			Message: "服务启动时检测到任务仍在 running，已标记为可续接状态。",
		})
		changed = true
	}
	return changed
}

func (s *Store) snapshot() AppState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Store) saveLocked() error {
	s.state.UpdatedAt = nowString()
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) createAdmin(username, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Admin != nil {
		return errors.New("admin already exists")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	s.state.Admin = &AdminConfig{
		Username:     username,
		PasswordHash: hash,
		CreatedAt:    nowString(),
	}
	return s.saveLocked()
}

func (s *Store) updateSettings(next Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Settings = next
	s.applyDefaults()
	return s.saveLocked()
}

func (s *Store) createRun(trigger string, settings Settings) (RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := RunRecord{
		ID:              fmt.Sprintf("%d", time.Now().UnixNano()),
		Trigger:         trigger,
		Status:          "running",
		Mode:            "force_refresh",
		Stage:           "准备执行",
		Progress:        5,
		RequiredIPCount: requiredIPCount(settings),
		DNSStatus:       "pending",
		StartedAt:       nowString(),
		Summary:         runSummary(trigger, settings),
		Logs: []RunLog{{
			At:      nowString(),
			Level:   "info",
			Message: "任务已创建，等待后台执行。",
		}},
	}
	s.state.Runs = append([]RunRecord{run}, s.state.Runs...)
	if len(s.state.Runs) > 50 {
		s.state.Runs = s.state.Runs[:50]
	}
	return run, s.saveLocked()
}

func (s *Store) updateRunProgress(id, stage string, progress, updatedIPs, syncedIPs int, dnsStatus string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Runs {
		if s.state.Runs[i].ID == id {
			s.state.Runs[i].Stage = stage
			if progress >= 0 {
				s.state.Runs[i].Progress = clampInt(progress, 0, 100)
			}
			if updatedIPs >= 0 {
				s.state.Runs[i].UpdatedIPCount = updatedIPs
			}
			if syncedIPs >= 0 {
				s.state.Runs[i].SyncedIPCount = syncedIPs
			}
			if dnsStatus != "" {
				s.state.Runs[i].DNSStatus = dnsStatus
			}
			_ = s.saveLocked()
			return
		}
	}
}

func (s *Store) appendRunLog(id, level, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Runs {
		if s.state.Runs[i].ID == id {
			s.state.Runs[i].Logs = append(s.state.Runs[i].Logs, RunLog{
				At:      nowString(),
				Level:   level,
				Message: message,
			})
			_ = s.saveLocked()
			return
		}
	}
}

func (s *Store) finishRun(id, status, summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Runs {
		if s.state.Runs[i].ID == id {
			s.state.Runs[i].Status = status
			s.state.Runs[i].Stage = "完成"
			s.state.Runs[i].Progress = 100
			s.state.Runs[i].FinishedAt = nowString()
			s.state.Runs[i].Summary = summary
			s.state.Runs[i].Logs = append(s.state.Runs[i].Logs, RunLog{
				At:      nowString(),
				Level:   "info",
				Message: "任务结束：" + status,
			})
			_ = s.saveLocked()
			return
		}
	}
}

func (s *Store) cancelRun(id, summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Runs {
		if s.state.Runs[i].ID == id && s.state.Runs[i].Status == "running" {
			s.state.Runs[i].Status = "canceled"
			s.state.Runs[i].Stage = "已停止"
			s.state.Runs[i].FinishedAt = nowString()
			s.state.Runs[i].Summary = summary
			s.state.Runs[i].Logs = append(s.state.Runs[i].Logs, RunLog{
				At:      nowString(),
				Level:   "warn",
				Message: "任务已停止。",
			})
			_ = s.saveLocked()
			return
		}
	}
}

func (s *Store) deleteRun(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := false
	nextRuns := s.state.Runs[:0]
	for _, run := range s.state.Runs {
		if run.ID == id {
			removed = true
			continue
		}
		nextRuns = append(nextRuns, run)
	}
	s.state.Runs = nextRuns
	if removed {
		nextResults := s.state.Results[:0]
		for _, result := range s.state.Results {
			if result.RunID == id {
				continue
			}
			nextResults = append(nextResults, result)
		}
		s.state.Results = nextResults
		_ = s.saveLocked()
	}
	return removed
}

func (s *Store) addIPResult(result IPTestResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Results = append([]IPTestResult{result}, s.state.Results...)
	if len(s.state.Results) > 1000 {
		s.state.Results = s.state.Results[:1000]
	}
	_ = s.saveLocked()
}

func (s *Store) markRunResultsSynced(runID string, ips map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Results {
		if s.state.Results[i].RunID == runID && ips[s.state.Results[i].IP] {
			s.state.Results[i].CloudflareSynced = true
		}
	}
	_ = s.saveLocked()
}

func nowString() string {
	return time.Now().Format(time.RFC3339)
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	iterations := 120000
	hash := stretchPassword([]byte(password), salt, iterations)
	return fmt.Sprintf("sha256$%d$%s$%s",
		iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	actual := stretchPassword([]byte(password), salt, iterations)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func stretchPassword(password, salt []byte, iterations int) []byte {
	h := sha256.New()
	h.Write(salt)
	h.Write(password)
	sum := h.Sum(nil)
	for i := 1; i < iterations; i++ {
		h.Reset()
		h.Write(sum)
		h.Write(password)
		h.Write(salt)
		sum = h.Sum(nil)
	}
	return sum
}

func (s *SessionStore) create(username string) (string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	s.mu.Lock()
	s.sessions[token] = username
	s.mu.Unlock()
	return token, nil
}

func (s *SessionStore) get(token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	username, ok := s.sessions[token]
	return username, ok
}

func (s *SessionStore) delete(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

func (t *TaskManager) register(id string, cancel context.CancelFunc) {
	t.mu.Lock()
	t.cancels[id] = cancel
	t.mu.Unlock()
}

func (t *TaskManager) unregister(id string) {
	t.mu.Lock()
	delete(t.cancels, id)
	t.mu.Unlock()
}

func (t *TaskManager) cancel(id string) bool {
	t.mu.Lock()
	cancel, ok := t.cancels[id]
	t.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

func (a *App) root(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if a.store.snapshot().Admin == nil {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	if _, ok := a.currentUser(r); !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

func (a *App) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ok")
}

func (a *App) setup(w http.ResponseWriter, r *http.Request) {
	state := a.store.snapshot()
	if state.Admin != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if r.Method == http.MethodPost {
		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")
		confirm := r.FormValue("confirm_password")
		if username == "" || password == "" {
			a.render(w, setupTemplate, PageData{Title: "首次初始化", Error: "用户名和密码不能为空"})
			return
		}
		if password != confirm {
			a.render(w, setupTemplate, PageData{Title: "首次初始化", Error: "两次输入的密码不一致"})
			return
		}
		if err := a.store.createAdmin(username, password); err != nil {
			a.render(w, setupTemplate, PageData{Title: "首次初始化", Error: err.Error()})
			return
		}
		http.Redirect(w, r, "/login?flash=setup_ok", http.StatusFound)
		return
	}
	a.render(w, setupTemplate, PageData{Title: "首次初始化"})
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	state := a.store.snapshot()
	if state.Admin == nil {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	flash := ""
	if r.URL.Query().Get("flash") == "setup_ok" {
		flash = "管理员已创建，请登录。"
	}
	if r.Method == http.MethodPost {
		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")
		if username != state.Admin.Username || !verifyPassword(state.Admin.PasswordHash, password) {
			a.render(w, loginTemplate, PageData{Title: "登录", Error: "用户名或密码错误"})
			return
		}
		token, err := a.sessions.create(username)
		if err != nil {
			a.render(w, loginTemplate, PageData{Title: "登录", Error: "创建会话失败"})
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "cfbs_session",
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   86400,
		})
		http.Redirect(w, r, "/dashboard", http.StatusFound)
		return
	}
	a.render(w, loginTemplate, PageData{Title: "登录", Flash: flash})
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("cfbs_session"); err == nil {
		a.sessions.delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "cfbs_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (a *App) dashboard(w http.ResponseWriter, r *http.Request) {
	state := a.store.snapshot()
	user, _ := a.currentUser(r)
	data := a.pageData("Dashboard", user, state.Settings)
	data.HasAdmin = state.Admin != nil
	data.RecentRuns = recentRuns(state.Runs, 8)
	data.HasRunningRun = hasRunningRun(state.Runs)
	data.Stats = buildDashboardStats(state)
	data.CurrentRun = currentRun(state.Runs)
	data.LatestRun = latestRun(state.Runs)
	data.CanResumeRun = canResumeRun(state)
	fillResultPanels(&data, state)
	a.render(w, dashboardTemplate, data)
}

func (a *App) pageData(title, username string, settings Settings) PageData {
	geoLocations, geoDatabase := a.geoSnapshot()
	data := PageData{
		Title:               title,
		Username:            username,
		Settings:            settings,
		TokenMasked:         maskToken(settings.CloudflareAPIToken),
		DNSTargetModeLabel:  dnsTargetModeLabel(settings.DNSTargetMode),
		IPv4RecordName:      effectiveRecordName(settings, "ipv4"),
		IPv6RecordName:      effectiveRecordName(settings, "ipv6"),
		IPv4CredentialLabel: credentialLabel(settings.IPv4Target.CredentialMode),
		IPv6CredentialLabel: credentialLabel(settings.IPv6Target.CredentialMode),
		IPv4TokenMasked:     maskToken(effectiveToken(settings, settings.IPv4Target)),
		IPv6TokenMasked:     maskToken(effectiveToken(settings, settings.IPv6Target)),
		ScheduleSummary:     scheduleSummary(settings),
		LocationSummary:     locationFilterSummary(settings),
		NextRunAt:           nextRunText(settings),
		GeoLocations:        geoLocations,
		GeoDatabase:         geoDatabase,
	}
	data.GeoCountries, data.GeoRegions, data.GeoCities = buildGeoChoices(geoLocations, settings)
	return data
}

func (a *App) refreshGeoDatabase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	state := a.store.snapshot()
	user, _ := a.currentUser(r)
	status, err := a.updateGeoDatabase()
	data := a.pageData("配置", user, state.Settings)
	if err != nil {
		data.Error = "地区数据库更新失败：" + err.Error()
	} else {
		data.Flash = fmt.Sprintf("地区数据库已更新：%d 个机房，%d 条 Cloudflare IP 地理记录。", status.LocationCount, status.GeoFeedCount)
	}
	a.render(w, settingsTemplate, data)
}

func buildGeoChoices(locations []geodb.Location, settings Settings) ([]GeoChoice, []GeoChoice, []GeoChoice) {
	countries := make(map[string]bool)
	regions := make(map[string]bool)
	cities := make(map[string]bool)
	for _, loc := range locations {
		countries[loc.Country] = true
		if settings.LocationCountry != "" && !strings.EqualFold(settings.LocationCountry, loc.Country) {
			continue
		}
		if loc.Region != "" {
			regions[loc.Region] = true
		}
		if settings.LocationRegion != "" && !strings.EqualFold(settings.LocationRegion, loc.Region) {
			continue
		}
		if loc.City != "" {
			cities[loc.City] = true
		}
	}
	return geoChoices(countries, settings.LocationCountry), geoChoices(regions, settings.LocationRegion), geoChoices(cities, settings.LocationCity)
}

func geoChoices(values map[string]bool, selected string) []GeoChoice {
	if strings.TrimSpace(selected) != "" {
		values[selected] = true
	}
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	sort.Slice(keys, func(i, j int) bool { return strings.ToLower(keys[i]) < strings.ToLower(keys[j]) })
	choices := make([]GeoChoice, 0, len(keys))
	for _, value := range keys {
		choices = append(choices, GeoChoice{Value: value, Label: value, Selected: strings.EqualFold(value, selected)})
	}
	return choices
}

func (a *App) settings(w http.ResponseWriter, r *http.Request) {
	state := a.store.snapshot()
	user, _ := a.currentUser(r)
	data := a.pageData("配置", user, state.Settings)
	if r.Method == http.MethodPost {
		next := state.Settings
		if token := strings.TrimSpace(r.FormValue("cloudflare_api_token")); token != "" {
			next.CloudflareAPIToken = token
		}
		next.CloudflareAccountID = strings.TrimSpace(r.FormValue("cloudflare_account_id"))
		next.CloudflareZoneID = strings.TrimSpace(r.FormValue("cloudflare_zone_id"))
		next.RecordName = strings.TrimSpace(r.FormValue("record_name"))
		next.DNSTargetMode = normalizeTargetMode(r.FormValue("dns_target_mode"))
		next.IPv4Target.RecordName = strings.TrimSpace(r.FormValue("ipv4_record_name"))
		next.IPv4Target.CredentialMode = normalizeCredentialMode(r.FormValue("ipv4_credential_mode"))
		if token := strings.TrimSpace(r.FormValue("ipv4_cloudflare_api_token")); token != "" {
			next.IPv4Target.CloudflareAPIToken = token
		}
		next.IPv4Target.CloudflareAccountID = strings.TrimSpace(r.FormValue("ipv4_cloudflare_account_id"))
		next.IPv4Target.CloudflareZoneID = strings.TrimSpace(r.FormValue("ipv4_cloudflare_zone_id"))
		next.IPv6Target.RecordName = strings.TrimSpace(r.FormValue("ipv6_record_name"))
		next.IPv6Target.CredentialMode = normalizeCredentialMode(r.FormValue("ipv6_credential_mode"))
		if token := strings.TrimSpace(r.FormValue("ipv6_cloudflare_api_token")); token != "" {
			next.IPv6Target.CloudflareAPIToken = token
		}
		next.IPv6Target.CloudflareAccountID = strings.TrimSpace(r.FormValue("ipv6_cloudflare_account_id"))
		next.IPv6Target.CloudflareZoneID = strings.TrimSpace(r.FormValue("ipv6_cloudflare_zone_id"))
		next.IPv4Enabled = r.FormValue("ipv4_enabled") == "on"
		next.IPv6Enabled = r.FormValue("ipv6_enabled") == "on"
		next.IPv4Count = clampInt(parseInt(r.FormValue("ipv4_count"), 10), 0, 50)
		next.IPv6Count = clampInt(parseInt(r.FormValue("ipv6_count"), 10), 0, 50)
		if !next.IPv4Enabled || next.IPv4Count == 0 {
			next.IPv4Enabled = false
			next.IPv4Count = 0
		}
		if !next.IPv6Enabled || next.IPv6Count == 0 {
			next.IPv6Enabled = false
			next.IPv6Count = 0
		}
		next.UseTLS = r.FormValue("use_tls") == "on"
		next.BandwidthMbps = clampInt(parseInt(r.FormValue("bandwidth_mbps"), 100), 1, 10000)
		next.RTTConcurrency = clampInt(parseInt(r.FormValue("rtt_concurrency"), 50), 1, 100)
		next.LocationMode = normalizeLocationMode(r.FormValue("location_mode"))
		next.LocationCountry = strings.ToUpper(strings.TrimSpace(r.FormValue("location_country")))
		next.LocationRegion = strings.TrimSpace(r.FormValue("location_region"))
		next.LocationCity = strings.TrimSpace(r.FormValue("location_city"))
		if next.LocationCountry == "" && next.LocationRegion == "" && next.LocationCity == "" {
			next.LocationMode = "any"
		}
		next.ScheduleEnabled = r.FormValue("schedule_enabled") == "on"
		next.ScheduleMode = normalizeScheduleMode(r.FormValue("schedule_mode"))
		next.ScheduleIntervalDays = clampInt(parseInt(r.FormValue("schedule_interval_days"), 1), 1, 365)
		next.ScheduleTime = strings.TrimSpace(r.FormValue("schedule_time"))
		if err := a.store.updateSettings(next); err != nil {
			data.Error = err.Error()
			a.render(w, settingsTemplate, data)
			return
		}
		data = a.pageData("配置", user, next)
		data.Flash = "配置已保存。"
	}
	a.render(w, settingsTemplate, data)
}

func (a *App) testSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/settings", http.StatusFound)
		return
	}
	state := a.store.snapshot()
	user, _ := a.currentUser(r)
	data := a.pageData("配置", user, state.Settings)
	targets := buildConfigTestTargets(state.Settings)
	if len(targets) == 0 {
		data.Error = "请先保存 Cloudflare Token、Zone ID 和目标域名，再测试配置。"
		a.render(w, settingsTemplate, data)
		return
	}
	for _, target := range targets {
		data.ConfigTestResults = append(data.ConfigTestResults, testCloudflareTarget(target))
	}
	data.Flash = "配置测试已完成。"
	a.render(w, settingsTemplate, data)
}

func (a *App) runPage(w http.ResponseWriter, r *http.Request) {
	state := a.store.snapshot()
	user, _ := a.currentUser(r)
	data := a.pageData("运行", user, state.Settings)
	data.RecentRuns = recentRuns(state.Runs, 20)
	data.HasRunningRun = hasRunningRun(state.Runs)
	data.Stats = buildDashboardStats(state)
	data.CurrentRun = currentRun(state.Runs)
	data.LatestRun = latestRun(state.Runs)
	data.CanResumeRun = canResumeRun(state)
	fillResultPanels(&data, state)
	a.render(w, runTemplate, data)
}

func (a *App) startRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/run", http.StatusFound)
		return
	}
	state := a.store.snapshot()
	if hasRunningRun(state.Runs) {
		http.Redirect(w, r, "/run", http.StatusFound)
		return
	}
	run, err := a.store.createRun("manual", state.Settings)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go a.executeRun(run.ID, state.Settings)
	http.Redirect(w, r, "/run", http.StatusFound)
}

func (a *App) resumeRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/run", http.StatusFound)
		return
	}
	state := a.store.snapshot()
	if hasRunningRun(state.Runs) {
		http.Redirect(w, r, "/run", http.StatusFound)
		return
	}
	source, seed := latestResumableRun(state)
	if source == nil || len(seed) == 0 {
		http.Redirect(w, r, "/run", http.StatusFound)
		return
	}
	run, err := a.store.createRun("resume", state.Settings)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go a.executeRunWithSeed(run.ID, state.Settings, source.ID, seed)
	http.Redirect(w, r, "/run", http.StatusFound)
}

func (a *App) stopRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/run", http.StatusFound)
		return
	}
	id := strings.TrimSpace(r.FormValue("id"))
	if id == "" {
		http.Redirect(w, r, "/run", http.StatusFound)
		return
	}
	if a.tasks.cancel(id) {
		a.store.updateRunProgress(id, "正在停止", -1, -1, -1, "pending")
		a.store.appendRunLog(id, "warn", "收到手动停止请求，正在终止当前测速进程。")
	} else {
		a.store.cancelRun(id, "任务已手动停止。")
	}
	http.Redirect(w, r, "/run", http.StatusFound)
}

func (a *App) deleteRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/run", http.StatusFound)
		return
	}
	id := strings.TrimSpace(r.FormValue("id"))
	if id == "" {
		http.Redirect(w, r, "/run", http.StatusFound)
		return
	}
	_ = a.tasks.cancel(id)
	a.store.deleteRun(id)
	http.Redirect(w, r, "/run", http.StatusFound)
}

func (a *App) runsAPI(w http.ResponseWriter, r *http.Request) {
	state := a.store.snapshot()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(recentRuns(state.Runs, 20))
}

func (a *App) executeRun(id string, settings Settings) {
	a.executeRunWithSeed(id, settings, "", nil)
}

func (a *App) executeRunWithSeed(id string, settings Settings, sourceRunID string, seed []IPTestResult) {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout())
	a.tasks.register(id, cancel)
	defer func() {
		cancel()
		a.tasks.unregister(id)
	}()

	required := requiredIPCount(settings)
	a.store.updateRunProgress(id, "读取配置", 5, 0, 0, "pending")
	trigger := "manual"
	if sourceRunID != "" {
		trigger = "resume"
	}
	a.store.appendRunLog(id, "info", "开始真实执行："+runSummary(trigger, settings))
	a.store.appendRunLog(id, "info", fmt.Sprintf("任务保护：整体最长运行 %s；单个协议族 %s 无新增有效 IP 将自动失败。", formatDuration(runTimeout()), formatDuration(familyNoResultTimeout())))
	if required <= 0 {
		a.store.finishRun(id, "failed", "目标数量为 0，没有需要扫描或写入的 IP。")
		return
	}

	results := make([]IPTestResult, 0, required)
	seen := make(map[string]bool)
	if len(seed) > 0 {
		a.store.appendRunLog(id, "info", fmt.Sprintf("从任务 %s 续接，载入已保存结果 %d 个。", sourceRunID, len(seed)))
		for _, item := range seed {
			if len(results) >= required || seen[item.IP] {
				continue
			}
			item.RunID = id
			item.CloudflareSynced = false
			item.TestedAt = nowString()
			seen[item.IP] = true
			results = append(results, item)
			a.store.addIPResult(item)
		}
		a.store.updateRunProgress(id, "续接已保存结果", 8, len(results), 0, "pending")
	}
	existingV4 := countFamily(results, 4)
	existingV6 := countFamily(results, 6)
	ipv4TargetCount := activeIPv4Count(settings)
	ipv6TargetCount := activeIPv6Count(settings)
	if ipv4TargetCount > 0 {
		remaining := ipv4TargetCount - existingV4
		if remaining > 0 {
			a.store.appendRunLog(id, "info", fmt.Sprintf("开始 IPv4 扫描：还需 %d 个，总目标 %d 个。", remaining, ipv4TargetCount))
			v4, err := a.collectFamilyResults(ctx, id, settings, 4, remaining, seen, len(results), required)
			results = append(results, v4...)
			if err != nil {
				a.finishRunFromError(id, err)
				return
			}
		} else {
			a.store.appendRunLog(id, "info", fmt.Sprintf("IPv4 已满足：%d/%d。", existingV4, ipv4TargetCount))
		}
	} else {
		a.store.appendRunLog(id, "info", "IPv4 未启用或数量为 0，跳过 IPv4 扫描与 A 记录同步。")
	}
	if ipv6TargetCount > 0 {
		remaining := ipv6TargetCount - existingV6
		if remaining > 0 {
			a.store.appendRunLog(id, "info", fmt.Sprintf("开始 IPv6 扫描：还需 %d 个，总目标 %d 个。", remaining, ipv6TargetCount))
			v6, err := a.collectFamilyResults(ctx, id, settings, 6, remaining, seen, len(results), required)
			results = append(results, v6...)
			if err != nil {
				a.finishRunFromError(id, err)
				return
			}
		} else {
			a.store.appendRunLog(id, "info", fmt.Sprintf("IPv6 已满足：%d/%d。", existingV6, ipv6TargetCount))
		}
	} else {
		a.store.appendRunLog(id, "info", "IPv6 未启用或数量为 0，跳过 IPv6 扫描与 AAAA 记录同步。")
	}

	if len(results) != required {
		a.store.finishRun(id, "failed", fmt.Sprintf("扫描结果数量不足：需要 %d 个，实际 %d 个；未执行 DNS 更新。", required, len(results)))
		return
	}

	a.store.updateRunProgress(id, "准备同步 DNS", 82, len(results), 0, "pending")
	a.store.appendRunLog(id, "info", "扫描结果已全部保存，开始一次性替换 Cloudflare DNS。")
	synced, err := syncResultsToCloudflare(settings, results, func(message string) {
		a.store.appendRunLog(id, "info", message)
	})
	if err != nil {
		a.store.updateRunProgress(id, "DNS 同步失败", 92, len(results), synced, "failed")
		a.store.finishRun(id, "failed", "扫描完成但 DNS 同步失败："+err.Error())
		return
	}
	a.store.updateRunProgress(id, "DNS 已确认", 100, len(results), synced, "confirmed")
	syncedMap := make(map[string]bool)
	for _, result := range results {
		syncedMap[result.IP] = true
	}
	a.store.markRunResultsSynced(id, syncedMap)
	a.store.finishRun(id, "succeeded", fmt.Sprintf("完成：扫描 %d 个 IP，写入 Cloudflare %d 个。", len(results), synced))
}

func (a *App) finishRunFromError(id string, err error) {
	if errors.Is(err, context.Canceled) {
		a.store.finishRun(id, "canceled", "任务已手动停止。")
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		a.store.finishRun(id, "failed", fmt.Sprintf("任务超过整体运行上限 %s，已自动停止；请检查配置、网络或降低目标数量。", formatDuration(runTimeout())))
		return
	}
	a.store.finishRun(id, "failed", err.Error())
}

func (a *App) schedulerLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		state := a.store.snapshot()
		if !shouldStartScheduledRun(state, time.Now()) {
			continue
		}
		run, err := a.store.createRun("scheduled", state.Settings)
		if err != nil {
			log.Printf("scheduled run create failed: %v", err)
			continue
		}
		go a.executeRun(run.ID, state.Settings)
	}
}

func (a *App) collectFamilyResults(ctx context.Context, id string, settings Settings, ipVersion, targetCount int, seen map[string]bool, existingCount, requiredCount int) ([]IPTestResult, error) {
	results := make([]IPTestResult, 0, targetCount)
	noResultTimeout := familyNoResultTimeout()
	lastResultAt := time.Now()
	for attempt := 1; len(results) < targetCount; attempt++ {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		stage := fmt.Sprintf("扫描 IPv%d %d/%d", ipVersion, len(results)+1, targetCount)
		progress := 10 + ((existingCount + len(results)) * 65 / requiredCount)
		a.store.updateRunProgress(id, stage, progress, existingCount+len(results), 0, "pending")
		a.store.appendRunLog(id, "info", fmt.Sprintf("IPv%d 第 %d 次尝试，目标收集 %d/%d。", ipVersion, attempt, len(results), targetCount))

		resultDeadline := lastResultAt.Add(noResultTimeout)
		attemptCtx, cancel := context.WithDeadline(ctx, resultDeadline)
		result, output, err := runBetterIPScan(attemptCtx, settings, ipVersion, func(message string) {
			a.store.appendRunLog(id, "info", message)
		})
		cancel()
		if output != "" {
			a.store.appendRunLog(id, "info", trimForLog(output, 1200))
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return results, ctxErr
			}
			if errors.Is(err, context.Canceled) {
				return results, err
			}
			if errors.Is(err, context.DeadlineExceeded) || time.Now().After(resultDeadline) {
				return results, fmt.Errorf("IPv%d 连续 %s 没有新增有效 IP，已自动停止该任务；如果 VPS 不支持 IPv%d，请把 IPv%d 数量设置为 0 后重新执行", ipVersion, formatDuration(noResultTimeout), ipVersion, ipVersion)
			}
			a.store.appendRunLog(id, "error", fmt.Sprintf("IPv%d 第 %d 次尝试失败：%v", ipVersion, attempt, err))
			continue
		}
		if result.IP == "" {
			a.store.appendRunLog(id, "error", "未能从脚本输出中解析到优选 IP。")
			continue
		}
		if seen[result.IP] {
			a.store.appendRunLog(id, "info", "跳过重复 IP："+result.IP)
			continue
		}
		seen[result.IP] = true
		result.RunID = id
		result.IPVersion = ipVersion
		if ipVersion == 6 {
			result.RecordType = "AAAA"
		} else {
			result.RecordType = "A"
		}
		if settings.UseTLS {
			result.Protocol = "TLS"
		} else {
			result.Protocol = "HTTP"
		}
		result.ConfiguredBandwidthMbps = settings.BandwidthMbps
		result.SelectedForDNS = true
		result.TestedAt = nowString()
		results = append(results, result)
		lastResultAt = time.Now()
		a.store.addIPResult(result)
		a.store.updateRunProgress(id, stage, progress, existingCount+len(results), 0, "pending")
		a.store.appendRunLog(id, "info", fmt.Sprintf("已保存 IPv%d 结果：%s，实测 %d Mbps，峰值 %d kB/s，RTT %d ms，机房 %s。",
			ipVersion, result.IP, result.MeasuredBandwidthMbps, result.PeakSpeedKBps, result.RTTMs, result.DataCenter))
	}
	return results, nil
}

func runBetterIPScan(ctx context.Context, settings Settings, ipVersion int, onLog func(string)) (IPTestResult, string, error) {
	bin, err := findScannerBinary()
	if err != nil {
		return IPTestResult{}, "", err
	}
	menu := "1"
	if ipVersion == 4 && !settings.UseTLS {
		menu = "2"
	}
	if ipVersion == 6 && settings.UseTLS {
		menu = "3"
	}
	if ipVersion == 6 && !settings.UseTLS {
		menu = "4"
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin)
	cmd.Dir = scannerWorkDir(bin)
	cmd.Env = append(os.Environ(),
		"BETTER_CF_LOCATION_MODE="+normalizeLocationMode(settings.LocationMode),
		"BETTER_CF_LOCATION_COUNTRY="+strings.TrimSpace(settings.LocationCountry),
		"BETTER_CF_LOCATION_REGION="+strings.TrimSpace(settings.LocationRegion),
		"BETTER_CF_LOCATION_CITY="+strings.TrimSpace(settings.LocationCity),
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return IPTestResult{}, "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return IPTestResult{}, "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return IPTestResult{}, "", err
	}
	if err := cmd.Start(); err != nil {
		return IPTestResult{}, "", err
	}

	outputCh := make(chan string, 64)
	readPipe := func(r io.Reader) {
		buf := make([]byte, 256)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				outputCh <- string(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}
	go readPipe(stdout)
	go readPipe(stderr)

	var builder strings.Builder
	wroteMenu := false
	wroteBandwidth := false
	wroteRTT := false
	wroteExit := false
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	startedAt := time.Now()
	lastOutputAt := startedAt
	lastLoggedLen := 0
	idleLimit := 3 * time.Minute

	writeLine := func(value interface{}) error {
		_, err := io.WriteString(stdin, fmt.Sprintf("%v\n", value))
		return err
	}

	var waitErr error
loop:
	for {
		select {
		case chunk := <-outputCh:
			builder.WriteString(chunk)
			lastOutputAt = time.Now()
			output := builder.String()
			if !wroteMenu && strings.Contains(output, "请选择菜单") {
				if err := writeLine(menu); err != nil {
					waitErr = err
					break loop
				}
				wroteMenu = true
			}
			if wroteMenu && !wroteBandwidth && strings.Contains(output, "请设置期望的带宽大小") {
				if err := writeLine(settings.BandwidthMbps); err != nil {
					waitErr = err
					break loop
				}
				wroteBandwidth = true
			}
			if wroteBandwidth && !wroteRTT && strings.Contains(output, "请设置 RTT 测试进程数") {
				if err := writeLine(settings.RTTConcurrency); err != nil {
					waitErr = err
					break loop
				}
				wroteRTT = true
			}
			if wroteRTT && !wroteExit && strings.Contains(output, "总计用时:") && strings.Count(output, "请选择菜单") >= 2 {
				_ = writeLine("0")
				wroteExit = true
			}
		case <-heartbeat.C:
			output := builder.String()
			if len(output) > lastLoggedLen {
				delta := output[lastLoggedLen:]
				lastLoggedLen = len(output)
				if strings.TrimSpace(delta) != "" {
					onLog("脚本实时输出：" + trimForLog(delta, 900))
				}
			} else {
				idleFor := time.Since(lastOutputAt)
				onLog(fmt.Sprintf("脚本仍在运行，累计 %d 秒；最近 %d 秒没有新输出。", int(time.Since(startedAt).Seconds()), int(idleFor.Seconds())))
				if idleFor >= idleLimit {
					_ = cmd.Process.Kill()
					waitErr = fmt.Errorf("脚本超过 %d 秒没有任何输出，判定为卡住，终止本次尝试并续接下一次", int(idleLimit.Seconds()))
					break loop
				}
			}
		case err := <-done:
			waitErr = err
			break loop
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			waitErr = ctx.Err()
			break loop
		}
	}
	_ = stdin.Close()
	drainUntil := time.After(300 * time.Millisecond)
drain:
	for {
		select {
		case chunk := <-outputCh:
			builder.WriteString(chunk)
		case <-drainUntil:
			break drain
		}
	}
	output := builder.String()
	if waitErr != nil {
		return IPTestResult{}, output, waitErr
	}
	result, err := parseBetterIPOutput(output)
	return result, output, err
}

func findScannerBinary() (string, error) {
	candidates := []string{
		strings.TrimSpace(os.Getenv("SCANNER_BIN")),
		"/root/cf-betterip/better-cloudflare-ip",
		"../better-cloudflare-ip",
		"./better-cloudflare-ip",
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			if filepath.IsAbs(candidate) {
				return candidate, nil
			}
			abs, err := filepath.Abs(candidate)
			if err != nil {
				return candidate, nil
			}
			return abs, nil
		}
	}
	return "", errors.New("找不到 better-cloudflare-ip 可执行文件，请确认 /root/cf-betterip/better-cloudflare-ip 存在")
}

func scannerWorkDir(bin string) string {
	dir := filepath.Dir(bin)
	if dir == "" || dir == "." {
		return "."
	}
	return dir
}

func parseBetterIPOutput(output string) (IPTestResult, error) {
	result := IPTestResult{}
	result.IP = firstMatch(output, `优选 IP:\s*([^\s]+)`)
	result.MeasuredBandwidthMbps = atoi(firstMatch(output, `实测带宽:\s*(\d+)\s*Mbps`))
	result.PeakSpeedKBps = atoi(firstMatch(output, `峰值速度:\s*(\d+)\s*kB/s`))
	result.RTTMs = atoi(firstMatch(output, `往返延迟:\s*(\d+)\s*毫秒`))
	result.DataCenter = strings.TrimSpace(firstMatch(output, `数据中心:\s*([^\n\r]+)`))
	result.DataCenterCode = strings.TrimSpace(firstMatch(output, `数据中心代码:\s*([^\n\r]+)`))
	result.DataCenterCountry = strings.TrimSpace(firstMatch(output, `数据中心国家:\s*([^\n\r]+)`))
	result.DataCenterRegion = strings.TrimSpace(firstMatch(output, `数据中心区域:\s*([^\n\r]+)`))
	result.DataCenterCity = strings.TrimSpace(firstMatch(output, `数据中心城市:\s*([^\n\r]+)`))
	result.DurationSeconds = atoi(firstMatch(output, `总计用时:\s*(\d+)\s*秒`))
	if result.IP == "" {
		return result, errors.New("未解析到优选 IP")
	}
	return result, nil
}

func firstMatch(text, pattern string) string {
	re := regexp.MustCompile(pattern)
	matches := re.FindStringSubmatch(text)
	if len(matches) < 2 {
		return ""
	}
	return strings.TrimSpace(matches[1])
}

func atoi(raw string) int {
	value, _ := strconv.Atoi(strings.TrimSpace(raw))
	return value
}

func trimForLog(text string, max int) string {
	text = strings.TrimSpace(text)
	if len(text) <= max {
		return text
	}
	return text[len(text)-max:]
}

func (a *App) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.store.snapshot().Admin == nil {
			http.Redirect(w, r, "/setup", http.StatusFound)
			return
		}
		if _, ok := a.currentUser(r); !ok {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next(w, r)
	}
}

func (a *App) currentUser(r *http.Request) (string, bool) {
	cookie, err := r.Cookie("cfbs_session")
	if err != nil || cookie.Value == "" {
		return "", false
	}
	return a.sessions.get(cookie.Value)
}

func (a *App) render(w http.ResponseWriter, page string, data PageData) {
	tpl := template.Must(template.New("layout").Parse(layoutTemplate + runsTemplate + resultTemplate + page))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tpl.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func parseInt(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return value
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func maskToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return "未配置"
	}
	if len(token) <= 10 {
		return "已配置"
	}
	return token[:4] + "..." + token[len(token)-4:]
}

func normalizeTargetMode(raw string) string {
	if raw == "split" {
		return "split"
	}
	return "single"
}

func normalizeCredentialMode(raw string) string {
	if raw == "custom" {
		return "custom"
	}
	return "shared"
}

func normalizeLocationMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "strict":
		return "strict"
	case "prefer":
		return "prefer"
	default:
		return "any"
	}
}

func locationFilterSummary(settings Settings) string {
	if normalizeLocationMode(settings.LocationMode) == "any" || (settings.LocationCountry == "" && settings.LocationRegion == "" && settings.LocationCity == "") {
		return "全局随机"
	}
	parts := make([]string, 0, 4)
	if settings.LocationMode == "strict" {
		parts = append(parts, "严格地区")
	} else {
		parts = append(parts, "地区优先")
	}
	if settings.LocationCountry != "" {
		parts = append(parts, settings.LocationCountry)
	}
	if settings.LocationRegion != "" {
		parts = append(parts, settings.LocationRegion)
	}
	if settings.LocationCity != "" {
		parts = append(parts, settings.LocationCity)
	}
	return strings.Join(parts, " / ")
}

func activeIPv4Count(settings Settings) int {
	if !settings.IPv4Enabled || settings.IPv4Count <= 0 {
		return 0
	}
	return settings.IPv4Count
}

func activeIPv6Count(settings Settings) int {
	if !settings.IPv6Enabled || settings.IPv6Count <= 0 {
		return 0
	}
	return settings.IPv6Count
}

func requiredIPCount(settings Settings) int {
	return activeIPv4Count(settings) + activeIPv6Count(settings)
}

func dnsTargetModeLabel(mode string) string {
	if mode == "split" {
		return "IPv4 / IPv6 分离域名"
	}
	return "单域名"
}

func credentialLabel(mode string) string {
	if mode == "custom" {
		return "独立凭据"
	}
	return "继承统一凭据"
}

func effectiveRecordName(settings Settings, family string) string {
	if settings.DNSTargetMode != "split" {
		return settings.RecordName
	}
	if family == "ipv6" {
		return settings.IPv6Target.RecordName
	}
	return settings.IPv4Target.RecordName
}

func effectiveToken(settings Settings, target TargetConfig) string {
	if target.CredentialMode == "custom" {
		return target.CloudflareAPIToken
	}
	return settings.CloudflareAPIToken
}

func effectiveZoneID(settings Settings, target TargetConfig) string {
	if target.CredentialMode == "custom" {
		return target.CloudflareZoneID
	}
	return settings.CloudflareZoneID
}

func buildConfigTestTargets(settings Settings) []ConfigTestTarget {
	var targets []ConfigTestTarget
	if settings.DNSTargetMode == "split" {
		if settings.IPv4Target.RecordName != "" || effectiveToken(settings, settings.IPv4Target) != "" || effectiveZoneID(settings, settings.IPv4Target) != "" {
			targets = append(targets, ConfigTestTarget{
				Label:      "IPv4 A 目标",
				RecordName: settings.IPv4Target.RecordName,
				APIToken:   effectiveToken(settings, settings.IPv4Target),
				ZoneID:     effectiveZoneID(settings, settings.IPv4Target),
			})
		}
		if settings.IPv6Target.RecordName != "" || effectiveToken(settings, settings.IPv6Target) != "" || effectiveZoneID(settings, settings.IPv6Target) != "" {
			targets = append(targets, ConfigTestTarget{
				Label:      "IPv6 AAAA 目标",
				RecordName: settings.IPv6Target.RecordName,
				APIToken:   effectiveToken(settings, settings.IPv6Target),
				ZoneID:     effectiveZoneID(settings, settings.IPv6Target),
			})
		}
		return targets
	}
	if settings.RecordName != "" || settings.CloudflareAPIToken != "" || settings.CloudflareZoneID != "" {
		targets = append(targets, ConfigTestTarget{
			Label:      "单域名目标",
			RecordName: settings.RecordName,
			APIToken:   settings.CloudflareAPIToken,
			ZoneID:     settings.CloudflareZoneID,
		})
	}
	return targets
}

func testCloudflareTarget(target ConfigTestTarget) ConfigTestResult {
	result := ConfigTestResult{
		Label:       target.Label,
		RecordName:  target.RecordName,
		CompletedAt: nowString(),
	}
	target.RecordName = strings.TrimSuffix(strings.TrimSpace(target.RecordName), ".")
	target.APIToken = strings.TrimSpace(target.APIToken)
	target.ZoneID = strings.TrimSpace(target.ZoneID)
	if target.APIToken == "" {
		result.Message = "缺少 API Token。"
		return result
	}
	if target.ZoneID == "" {
		result.Message = "缺少 Zone ID。"
		return result
	}
	if target.RecordName == "" {
		result.Message = "缺少目标域名。"
		return result
	}
	result.TestName = "_cf-betterip-test." + target.RecordName

	client := &http.Client{Timeout: 20 * time.Second}
	if err := cloudflareRequest(client, http.MethodGet, "https://api.cloudflare.com/client/v4/zones/"+target.ZoneID, target.APIToken, nil, nil); err != nil {
		result.Message = "Zone 访问失败：" + err.Error()
		return result
	}

	recordID, err := createCloudflareTXT(client, target)
	if err != nil {
		result.Message = "临时 TXT 写入失败：" + err.Error()
		return result
	}
	result.CreatedID = recordID
	if err := cloudflareRequest(client, http.MethodDelete, "https://api.cloudflare.com/client/v4/zones/"+target.ZoneID+"/dns_records/"+recordID, target.APIToken, nil, nil); err != nil {
		result.Message = "临时 TXT 已写入，但删除失败：" + err.Error()
		return result
	}
	result.Success = true
	result.Message = "测试通过：Zone 可访问，临时 TXT 可创建并已删除。"
	return result
}

func createCloudflareTXT(client *http.Client, target ConfigTestTarget) (string, error) {
	body := map[string]interface{}{
		"type":    "TXT",
		"name":    "_cf-betterip-test." + strings.TrimSuffix(target.RecordName, "."),
		"content": "cf-betterip-test-" + strconv.FormatInt(time.Now().Unix(), 10),
		"ttl":     60,
	}
	var parsed struct {
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	err := cloudflareRequest(client, http.MethodPost, "https://api.cloudflare.com/client/v4/zones/"+target.ZoneID+"/dns_records", target.APIToken, body, &parsed)
	if err != nil {
		return "", err
	}
	if parsed.Result.ID == "" {
		return "", errors.New("Cloudflare 未返回临时记录 ID")
	}
	return parsed.Result.ID, nil
}

func syncResultsToCloudflare(settings Settings, results []IPTestResult, logf func(string)) (int, error) {
	targets, err := buildDNSSyncTargets(settings, results)
	if err != nil {
		return 0, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	totalSynced := 0
	for _, target := range targets {
		if len(target.IPs) == 0 {
			continue
		}
		if target.RecordName == "" {
			return totalSynced, fmt.Errorf("%s 缺少目标域名", target.Label)
		}
		if target.APIToken == "" {
			return totalSynced, fmt.Errorf("%s 缺少 API Token", target.Label)
		}
		if target.ZoneID == "" {
			return totalSynced, fmt.Errorf("%s 缺少 Zone ID", target.Label)
		}
		logf(fmt.Sprintf("%s：查询旧 %s 记录。", target.Label, target.RecordType))
		oldRecords, err := listCloudflareDNSRecords(client, target)
		if err != nil {
			return totalSynced, err
		}
		for _, record := range oldRecords {
			logf(fmt.Sprintf("%s：删除旧记录 %s -> %s。", target.Label, record.Type, record.Content))
			if err := cloudflareRequest(client, http.MethodDelete, "https://api.cloudflare.com/client/v4/zones/"+target.ZoneID+"/dns_records/"+record.ID, target.APIToken, nil, nil); err != nil {
				return totalSynced, err
			}
		}
		for _, ip := range target.IPs {
			logf(fmt.Sprintf("%s：创建 %s %s -> %s。", target.Label, target.RecordType, target.RecordName, ip))
			if _, err := createCloudflareAddressRecord(client, target, ip); err != nil {
				return totalSynced, err
			}
		}
		verified, err := listCloudflareDNSRecords(client, target)
		if err != nil {
			return totalSynced, err
		}
		if !sameContents(verified, target.IPs) {
			return totalSynced, fmt.Errorf("%s 反查确认失败：Cloudflare 记录与目标 IP 不一致", target.Label)
		}
		totalSynced += len(target.IPs)
		logf(fmt.Sprintf("%s：已确认写入 %d 条 %s 记录。", target.Label, len(target.IPs), target.RecordType))
	}
	return totalSynced, nil
}

func buildDNSSyncTargets(settings Settings, results []IPTestResult) ([]DNSSyncTarget, error) {
	var ipv4, ipv6 []string
	for _, result := range results {
		if result.IPVersion == 6 {
			ipv6 = append(ipv6, result.IP)
		} else {
			ipv4 = append(ipv4, result.IP)
		}
	}
	if settings.DNSTargetMode == "split" {
		return []DNSSyncTarget{
			{
				Label:      "IPv4 A 目标",
				RecordName: settings.IPv4Target.RecordName,
				RecordType: "A",
				APIToken:   effectiveToken(settings, settings.IPv4Target),
				ZoneID:     effectiveZoneID(settings, settings.IPv4Target),
				IPs:        ipv4,
			},
			{
				Label:      "IPv6 AAAA 目标",
				RecordName: settings.IPv6Target.RecordName,
				RecordType: "AAAA",
				APIToken:   effectiveToken(settings, settings.IPv6Target),
				ZoneID:     effectiveZoneID(settings, settings.IPv6Target),
				IPs:        ipv6,
			},
		}, nil
	}
	return []DNSSyncTarget{
		{
			Label:      "单域名 IPv4 A",
			RecordName: settings.RecordName,
			RecordType: "A",
			APIToken:   settings.CloudflareAPIToken,
			ZoneID:     settings.CloudflareZoneID,
			IPs:        ipv4,
		},
		{
			Label:      "单域名 IPv6 AAAA",
			RecordName: settings.RecordName,
			RecordType: "AAAA",
			APIToken:   settings.CloudflareAPIToken,
			ZoneID:     settings.CloudflareZoneID,
			IPs:        ipv6,
		},
	}, nil
}

func listCloudflareDNSRecords(client *http.Client, target DNSSyncTarget) ([]CloudflareDNSRecord, error) {
	query := url.Values{}
	query.Set("name", target.RecordName)
	query.Set("type", target.RecordType)
	var parsed struct {
		Result []CloudflareDNSRecord `json:"result"`
	}
	err := cloudflareRequest(client, http.MethodGet, "https://api.cloudflare.com/client/v4/zones/"+target.ZoneID+"/dns_records?"+query.Encode(), target.APIToken, nil, &parsed)
	if err != nil {
		return nil, err
	}
	var safe []CloudflareDNSRecord
	for _, record := range parsed.Result {
		if strings.EqualFold(record.Name, target.RecordName) && record.Type == target.RecordType {
			safe = append(safe, record)
		}
	}
	return safe, nil
}

func createCloudflareAddressRecord(client *http.Client, target DNSSyncTarget, ip string) (string, error) {
	body := map[string]interface{}{
		"type":    target.RecordType,
		"name":    target.RecordName,
		"content": ip,
		"ttl":     1,
		"proxied": false,
	}
	var parsed struct {
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	err := cloudflareRequest(client, http.MethodPost, "https://api.cloudflare.com/client/v4/zones/"+target.ZoneID+"/dns_records", target.APIToken, body, &parsed)
	if err != nil {
		return "", err
	}
	if parsed.Result.ID == "" {
		return "", errors.New("Cloudflare 未返回记录 ID")
	}
	return parsed.Result.ID, nil
}

func sameContents(records []CloudflareDNSRecord, expected []string) bool {
	if len(records) != len(expected) {
		return false
	}
	actual := make(map[string]int)
	for _, record := range records {
		actual[record.Content]++
	}
	for _, ip := range expected {
		actual[ip]--
		if actual[ip] < 0 {
			return false
		}
	}
	for _, count := range actual {
		if count != 0 {
			return false
		}
	}
	return true
}

func cloudflareRequest(client *http.Client, method, url, token string, body interface{}, out interface{}) error {
	var payload *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(data)
	} else {
		payload = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var parsed struct {
		Success bool `json:"success"`
		Errors  []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !parsed.Success {
		if len(parsed.Errors) > 0 {
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, parsed.Errors[0].Message)
		}
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return err
		}
	}
	return nil
}

func normalizeScheduleMode(raw string) string {
	switch raw {
	case "hourly", "daily", "every_n_days":
		return raw
	default:
		return "daily"
	}
}

func scheduleSummary(settings Settings) string {
	if !settings.ScheduleEnabled {
		return "未启用"
	}
	switch settings.ScheduleMode {
	case "hourly":
		return "每小时运行一次"
	case "every_n_days":
		return fmt.Sprintf("每 %d 天 %s 运行一次", settings.ScheduleIntervalDays, settings.ScheduleTime)
	default:
		return "每天 " + settings.ScheduleTime + " 运行一次"
	}
}

func nextRunText(settings Settings) string {
	if !settings.ScheduleEnabled {
		return "未计划"
	}
	now := time.Now()
	switch settings.ScheduleMode {
	case "hourly":
		return now.Add(time.Hour).Format("2006-01-02 15:04")
	case "every_n_days":
		return nextTimeAt(now, settings.ScheduleTime).AddDate(0, 0, settings.ScheduleIntervalDays-1).Format("2006-01-02 15:04")
	default:
		return nextTimeAt(now, settings.ScheduleTime).Format("2006-01-02 15:04")
	}
}

func nextTimeAt(now time.Time, hhmm string) time.Time {
	hour, minute := 6, 0
	parts := strings.Split(hhmm, ":")
	if len(parts) == 2 {
		hour = clampInt(parseInt(parts[0], 6), 0, 23)
		minute = clampInt(parseInt(parts[1], 0), 0, 59)
	}
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func recentRuns(runs []RunRecord, limit int) []RunRecord {
	if len(runs) <= limit {
		return runs
	}
	return runs[:limit]
}

func currentRun(runs []RunRecord) *RunRecord {
	for i := range runs {
		if runs[i].Status == "running" {
			return &runs[i]
		}
	}
	return nil
}

func latestRun(runs []RunRecord) *RunRecord {
	if len(runs) == 0 {
		return nil
	}
	return &runs[0]
}

func hasRunningRun(runs []RunRecord) bool {
	for _, run := range runs {
		if run.Status == "running" {
			return true
		}
	}
	return false
}

func canResumeRun(state AppState) bool {
	if hasRunningRun(state.Runs) {
		return false
	}
	run, seed := latestResumableRun(state)
	return run != nil && len(seed) > 0
}

func latestResumableRun(state AppState) (*RunRecord, []IPTestResult) {
	if len(state.Runs) == 0 || len(state.Results) == 0 {
		return nil, nil
	}
	for i := range state.Runs {
		run := &state.Runs[i]
		if run.Status == "running" {
			continue
		}
		required := requiredIPCount(state.Settings)
		if required <= 0 {
			continue
		}
		seed := seedResultsForRun(state.Results, run.ID, state.Settings)
		if len(seed) == 0 {
			continue
		}
		if len(seed) < required || run.SyncedIPCount < required || run.DNSStatus != "confirmed" {
			return run, seed
		}
	}
	return nil, nil
}

func seedResultsForRun(results []IPTestResult, runID string, settings Settings) []IPTestResult {
	seed := make([]IPTestResult, 0, requiredIPCount(settings))
	seen := make(map[string]bool)
	v4Count := 0
	v6Count := 0
	ipv4TargetCount := activeIPv4Count(settings)
	ipv6TargetCount := activeIPv6Count(settings)
	for _, result := range results {
		if result.RunID != runID || result.IP == "" || !result.SelectedForDNS {
			continue
		}
		if seen[result.IP] {
			continue
		}
		if result.IPVersion == 6 {
			if v6Count >= ipv6TargetCount {
				continue
			}
			v6Count++
		} else {
			if v4Count >= ipv4TargetCount {
				continue
			}
			v4Count++
		}
		seen[result.IP] = true
		seed = append(seed, result)
	}
	return seed
}

func countFamily(results []IPTestResult, ipVersion int) int {
	count := 0
	for _, result := range results {
		if result.IPVersion == ipVersion {
			count++
		}
	}
	return count
}

func fillResultPanels(data *PageData, state AppState) {
	latest := latestRunResults(state)
	data.LatestResultSummary = buildIPResultSummary("最近同步结果", latest)
	data.LatestIPv4Results = filterIPResultViews(latest, 4)
	data.LatestIPv6Results = filterIPResultViews(latest, 6)

	today := todayResults(state)
	data.TodayResultSummary = buildIPResultSummary("今天测试结果", today)
	data.TodayIPv4Results = filterIPResultViews(today, 4)
	data.TodayIPv6Results = filterIPResultViews(today, 6)
}

func latestRunResults(state AppState) []IPResultView {
	for _, run := range state.Runs {
		views := resultViewsForRun(state.Results, run.ID)
		if len(views) > 0 {
			return views
		}
	}
	return nil
}

func todayResults(state AppState) []IPResultView {
	start := localDayStart(time.Now())
	var results []IPTestResult
	for _, result := range state.Results {
		testedAt, err := time.Parse(time.RFC3339, result.TestedAt)
		if err != nil || testedAt.Before(start) {
			continue
		}
		if result.IP == "" || !result.SelectedForDNS {
			continue
		}
		results = append(results, result)
	}
	return buildIPResultViews(results)
}

func resultViewsForRun(results []IPTestResult, runID string) []IPResultView {
	var selected []IPTestResult
	for _, result := range results {
		if result.RunID != runID || result.IP == "" || !result.SelectedForDNS {
			continue
		}
		selected = append(selected, result)
	}
	return buildIPResultViews(selected)
}

func buildIPResultViews(results []IPTestResult) []IPResultView {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].IPVersion != results[j].IPVersion {
			return results[i].IPVersion < results[j].IPVersion
		}
		if results[i].MeasuredBandwidthMbps != results[j].MeasuredBandwidthMbps {
			return results[i].MeasuredBandwidthMbps > results[j].MeasuredBandwidthMbps
		}
		if results[i].PeakSpeedKBps != results[j].PeakSpeedKBps {
			return results[i].PeakSpeedKBps > results[j].PeakSpeedKBps
		}
		return results[i].RTTMs < results[j].RTTMs
	})
	views := make([]IPResultView, 0, len(results))
	for i, result := range results {
		family := "IPv4"
		if result.IPVersion == 6 {
			family = "IPv6"
		}
		syncedText := "未同步"
		if result.CloudflareSynced {
			syncedText = "已同步"
		}
		views = append(views, IPResultView{
			Index:                   i + 1,
			RunID:                   result.RunID,
			IP:                      result.IP,
			Family:                  family,
			RecordType:              result.RecordType,
			Protocol:                result.Protocol,
			ConfiguredBandwidthMbps: result.ConfiguredBandwidthMbps,
			MeasuredBandwidthMbps:   result.MeasuredBandwidthMbps,
			PeakSpeedKBps:           result.PeakSpeedKBps,
			RTTMs:                   result.RTTMs,
			DataCenter:              result.DataCenter,
			DataCenterCode:          result.DataCenterCode,
			DataCenterCountry:       result.DataCenterCountry,
			DataCenterRegion:        result.DataCenterRegion,
			DurationSeconds:         result.DurationSeconds,
			SyncedText:              syncedText,
			TestedAt:                result.TestedAt,
		})
	}
	return views
}

func filterIPResultViews(results []IPResultView, ipVersion int) []IPResultView {
	family := "IPv4"
	if ipVersion == 6 {
		family = "IPv6"
	}
	var filtered []IPResultView
	for _, result := range results {
		if result.Family == family {
			filtered = append(filtered, result)
		}
	}
	for i := range filtered {
		filtered[i].Index = i + 1
	}
	return filtered
}

func buildIPResultSummary(title string, results []IPResultView) IPResultSummary {
	summary := IPResultSummary{Title: title, Total: len(results)}
	for _, result := range results {
		if result.Family == "IPv6" {
			summary.IPv6Count++
		} else {
			summary.IPv4Count++
		}
		if result.SyncedText == "已同步" {
			summary.SyncedCount++
		}
		if result.MeasuredBandwidthMbps > summary.BestMeasuredMbps {
			summary.BestIP = result.IP
			summary.BestDataCenter = result.DataCenter
			summary.BestMeasuredMbps = result.MeasuredBandwidthMbps
			summary.BestPeakKBps = result.PeakSpeedKBps
			summary.BestRTTMs = result.RTTMs
		}
	}
	return summary
}

func localDayStart(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

func buildDashboardStats(state AppState) DashboardStats {
	settings := state.Settings
	expected := requiredIPCount(settings)
	stats := DashboardStats{
		ExpectedIPCount: expected,
		ConfigReady:     isConfigReady(settings),
		ConfigHint:      configHint(settings),
		CurrentStage:    "空闲",
		CurrentProgress: 0,
		LastDNSStatus:   "未同步",
	}
	todayStart := localDayStart(time.Now())
	for _, run := range state.Runs {
		startedAt, err := time.Parse(time.RFC3339, run.StartedAt)
		if err == nil && !startedAt.Before(todayStart) {
			stats.TodayTaskCount++
			stats.TodayUpdatedIPs += run.UpdatedIPCount
			stats.TodaySyncedIPs += run.SyncedIPCount
		}
	}
	if current := currentRun(state.Runs); current != nil {
		stats.ProductStatus = "running"
		stats.ProductStatusText = "正在执行"
		stats.ProductStatusHint = current.Stage
		stats.CurrentStage = current.Stage
		stats.CurrentProgress = current.Progress
		stats.LastDNSStatus = dnsStatusText(current.DNSStatus)
		return stats
	}
	if latest := latestRun(state.Runs); latest != nil {
		stats.CurrentStage = latest.Stage
		stats.CurrentProgress = latest.Progress
		stats.LastDNSStatus = dnsStatusText(latest.DNSStatus)
		if latest.Status == "succeeded" && latest.SyncedIPCount >= expected && expected > 0 {
			stats.ProductStatus = "synced"
			stats.ProductStatusText = "已同步"
			stats.ProductStatusHint = "Cloudflare 记录数量满足目标"
		} else if latest.Status == "succeeded" {
			stats.ProductStatus = "needs_attention"
			stats.ProductStatusText = "未完成同步"
			stats.ProductStatusHint = "已有任务完成，但写入数量未达到目标"
		} else {
			stats.ProductStatus = latest.Status
			stats.ProductStatusText = "需要查看"
			stats.ProductStatusHint = latest.Summary
		}
		return stats
	}
	if stats.ConfigReady {
		stats.ProductStatus = "ready"
		stats.ProductStatusText = "已就绪"
		stats.ProductStatusHint = "可以立即执行第一次任务"
	} else {
		stats.ProductStatus = "setup"
		stats.ProductStatusText = "待配置"
		stats.ProductStatusHint = stats.ConfigHint
	}
	return stats
}

func isConfigReady(settings Settings) bool {
	if requiredIPCount(settings) == 0 {
		return false
	}
	if activeIPv4Count(settings) > 0 && effectiveRecordName(settings, "ipv4") == "" {
		return false
	}
	if activeIPv6Count(settings) > 0 && effectiveRecordName(settings, "ipv6") == "" {
		return false
	}
	if settings.CloudflareAPIToken == "" && settings.IPv4Target.CloudflareAPIToken == "" && settings.IPv6Target.CloudflareAPIToken == "" {
		return false
	}
	return true
}

func configHint(settings Settings) string {
	if requiredIPCount(settings) == 0 {
		return "至少启用 IPv4 或 IPv6"
	}
	if activeIPv4Count(settings) > 0 && effectiveRecordName(settings, "ipv4") == "" {
		return "先配置 IPv4 目标域名"
	}
	if activeIPv6Count(settings) > 0 && effectiveRecordName(settings, "ipv6") == "" {
		return "先配置 IPv6 目标域名"
	}
	if settings.CloudflareAPIToken == "" && settings.IPv4Target.CloudflareAPIToken == "" && settings.IPv6Target.CloudflareAPIToken == "" {
		return "先配置 Cloudflare Token"
	}
	return "基础配置已完成"
}

func dnsStatusText(status string) string {
	switch status {
	case "confirmed":
		return "已确认"
	case "partial":
		return "部分同步"
	case "failed":
		return "同步失败"
	case "pending":
		return "待同步"
	default:
		return "未同步"
	}
}

func runSummary(trigger string, settings Settings) string {
	return fmt.Sprintf("%s / %s / IPv4:%d IPv6:%d / %s / %d Mbps / RTT:%d",
		triggerLabel(trigger),
		dnsTargetModeLabel(settings.DNSTargetMode),
		activeIPv4Count(settings),
		activeIPv6Count(settings),
		locationFilterSummary(settings),
		settings.BandwidthMbps,
		settings.RTTConcurrency,
	)
}

func triggerLabel(trigger string) string {
	if trigger == "scheduled" {
		return "定时执行"
	}
	if trigger == "resume" {
		return "继续执行"
	}
	return "立即执行"
}

func shouldStartScheduledRun(state AppState, now time.Time) bool {
	settings := state.Settings
	if !settings.ScheduleEnabled || hasRunningRun(state.Runs) {
		return false
	}
	switch settings.ScheduleMode {
	case "hourly":
		if now.Minute() != 0 {
			return false
		}
		return !hasScheduledRunSince(state.Runs, now.Truncate(time.Hour))
	case "every_n_days":
		if now.Format("15:04") != settings.ScheduleTime {
			return false
		}
		last, ok := lastScheduledRunTime(state.Runs)
		if !ok {
			return true
		}
		return now.Sub(last) >= time.Duration(settings.ScheduleIntervalDays)*24*time.Hour
	default:
		if now.Format("15:04") != settings.ScheduleTime {
			return false
		}
		return !hasScheduledRunSince(state.Runs, time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()))
	}
}

func hasScheduledRunSince(runs []RunRecord, since time.Time) bool {
	for _, run := range runs {
		if run.Trigger != "scheduled" {
			continue
		}
		startedAt, err := time.Parse(time.RFC3339, run.StartedAt)
		if err == nil && !startedAt.Before(since) {
			return true
		}
	}
	return false
}

func lastScheduledRunTime(runs []RunRecord) (time.Time, bool) {
	for _, run := range runs {
		if run.Trigger != "scheduled" {
			continue
		}
		startedAt, err := time.Parse(time.RFC3339, run.StartedAt)
		if err == nil {
			return startedAt, true
		}
	}
	return time.Time{}, false
}

const layoutTemplate = `
{{define "layout"}}
<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Title}} - CF BetterIP DNS Sync</title>
  <style>
    :root { color-scheme: light; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    body { margin: 0; background: #f7f8fa; color: #1f2937; }
    header { background: #111827; color: white; padding: 14px 22px; display: flex; justify-content: space-between; align-items: center; }
    header a { color: #e5e7eb; text-decoration: none; margin-right: 14px; }
    main { max-width: 1060px; margin: 28px auto; padding: 0 18px; }
    .panel { background: white; border: 1px solid #e5e7eb; border-radius: 8px; padding: 22px; margin-bottom: 18px; }
    h1 { font-size: 24px; margin: 0 0 16px; }
    h2 { font-size: 18px; margin: 0 0 12px; }
    label { display: block; font-weight: 600; margin: 14px 0 6px; }
    input[type="text"], input[type="password"], input[type="number"], input[type="time"], select { width: 100%; box-sizing: border-box; border: 1px solid #d1d5db; border-radius: 6px; padding: 10px 12px; font-size: 15px; background: white; }
    button, .button { background: #2563eb; color: white; border: 0; border-radius: 6px; padding: 10px 14px; font-size: 15px; cursor: pointer; text-decoration: none; display: inline-block; }
    button.secondary { background: #4b5563; }
    button.danger { background: #b91c1c; }
    button.ghost { background: #eef2f7; color: #1f2937; }
    .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 14px; }
    .dashboard-grid { display: grid; grid-template-columns: minmax(0, 1.5fr) minmax(280px, .8fr); gap: 18px; align-items: start; }
    .status-band { background: #102033; color: #fff; border-radius: 8px; padding: 22px; margin-bottom: 18px; }
    .status-band strong { display: block; font-size: 28px; margin-top: 6px; }
    .status-band p { margin: 8px 0 0; color: #d1d5db; }
    .kpi-grid { display: grid; grid-template-columns: repeat(4, minmax(140px, 1fr)); gap: 12px; margin-bottom: 18px; }
    .kpi { background: white; border: 1px solid #e5e7eb; border-radius: 8px; padding: 16px; }
    .kpi span { display: block; color: #6b7280; font-size: 13px; }
    .kpi strong { display: block; font-size: 26px; margin-top: 6px; }
    .metric { border: 1px solid #e5e7eb; border-radius: 8px; padding: 14px; background: #fbfdff; }
    .metric span { display: block; color: #6b7280; font-size: 13px; }
    .metric strong { display: block; margin-top: 6px; font-size: 18px; overflow-wrap: anywhere; }
    .subsection { border-top: 1px solid #e5e7eb; padding-top: 16px; }
    .flash { background: #ecfdf5; border: 1px solid #a7f3d0; color: #065f46; padding: 10px 12px; border-radius: 6px; margin-bottom: 14px; }
    .error { background: #fef2f2; border: 1px solid #fecaca; color: #991b1b; padding: 10px 12px; border-radius: 6px; margin-bottom: 14px; }
    .muted { color: #6b7280; }
    .row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
    .checkbox { display: flex; gap: 8px; align-items: center; margin-top: 14px; }
    details { border: 1px solid #e5e7eb; border-radius: 8px; padding: 12px; margin-top: 10px; background: #fff; }
    summary { cursor: pointer; font-weight: 700; }
    pre.log { white-space: pre-wrap; overflow-wrap: anywhere; background: #0f172a; color: #e5e7eb; border-radius: 8px; padding: 12px; line-height: 1.5; max-height: 360px; overflow-y: auto; overscroll-behavior: contain; }
    .progress { height: 10px; border-radius: 999px; background: #e5e7eb; overflow: hidden; margin: 10px 0; }
    .progress > div { height: 100%; background: #2563eb; }
    .compact-list { margin: 0; padding: 0; list-style: none; }
    .compact-list li { display: flex; justify-content: space-between; gap: 12px; border-bottom: 1px solid #edf0f3; padding: 10px 0; }
    .compact-list li:last-child { border-bottom: 0; }
    .table-wrap { overflow-x: auto; border: 1px solid #e5e7eb; border-radius: 8px; margin-top: 10px; }
    table { width: 100%; border-collapse: collapse; font-size: 13px; min-width: 880px; background: #fff; }
    th, td { padding: 10px 12px; border-bottom: 1px solid #edf0f3; text-align: left; vertical-align: middle; }
    th { background: #f9fafb; color: #6b7280; font-weight: 700; white-space: nowrap; }
    td { color: #1f2937; }
    tr:last-child td { border-bottom: 0; }
    .ip-cell { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; overflow-wrap: anywhere; }
    .section-title { margin: 16px 0 6px; font-size: 15px; }
    .status-running { color: #b45309; }
    .status-succeeded { color: #047857; }
    .status-failed { color: #b91c1c; }
    .status-canceled { color: #6b7280; }
    code { background: #f3f4f6; padding: 2px 5px; border-radius: 4px; }
    @media (max-width: 820px) { .dashboard-grid, .kpi-grid { grid-template-columns: 1fr; } }
  </style>
  {{if .HasRunningRun}}<script>
    (function(){
      function nearBottom(el) {
        return el.scrollHeight - el.scrollTop - el.clientHeight < 24;
      }
      function scrollLogsToBottom() {
        if (sessionStorage.getItem("cfbsManualLogScroll") === "1") return;
        document.querySelectorAll("details[open] pre.log").forEach(function(el){
          el.scrollTop = el.scrollHeight;
        });
      }
      window.addEventListener("load", function(){
        document.querySelectorAll("pre.log").forEach(function(el){
          el.addEventListener("scroll", function(){
            if (nearBottom(el)) {
              sessionStorage.removeItem("cfbsManualLogScroll");
            } else {
              sessionStorage.setItem("cfbsManualLogScroll", "1");
            }
          });
        });
        scrollLogsToBottom();
        setTimeout(function(){ window.location.reload(); }, 4000);
      });
    })();
  </script>{{end}}
</head>
<body>
  <header>
    <div><strong>CF BetterIP DNS Sync</strong></div>
    {{if .Username}}<nav>
      <a href="/dashboard">Dashboard</a>
      <a href="/settings">配置</a>
      <a href="/run">运行</a>
      <form action="/logout" method="post" style="display:inline"><button class="secondary" type="submit">退出</button></form>
    </nav>{{end}}
  </header>
  <main>
    {{if .Flash}}<div class="flash">{{.Flash}}</div>{{end}}
    {{if .Error}}<div class="error">{{.Error}}</div>{{end}}
    {{template "content" .}}
  </main>
</body>
</html>
{{end}}
`

const setupTemplate = `
{{define "content"}}
<section class="panel">
  <h1>首次初始化管理员</h1>
  <p class="muted">第一次使用需要创建一个管理员账号。创建后才能配置 Cloudflare 和执行任务。</p>
  <form method="post">
    <label>管理员用户名</label>
    <input type="text" name="username" autocomplete="username" required>
    <label>管理员密码</label>
    <input type="password" name="password" autocomplete="new-password" required>
    <label>确认密码</label>
    <input type="password" name="confirm_password" autocomplete="new-password" required>
    <p><button type="submit">创建管理员</button></p>
  </form>
</section>
{{end}}
`

const loginTemplate = `
{{define "content"}}
<section class="panel">
  <h1>管理员登录</h1>
  <form method="post">
    <label>用户名</label>
    <input type="text" name="username" autocomplete="username" required>
    <label>密码</label>
    <input type="password" name="password" autocomplete="current-password" required>
    <p><button type="submit">登录</button></p>
  </form>
</section>
{{end}}
`

const dashboardTemplate = `
{{define "content"}}
<section class="status-band">
  <span>当前状态</span>
  <strong>{{.Stats.ProductStatusText}}</strong>
  <p>{{.Stats.ProductStatusHint}}</p>
  <div class="row" style="margin-top:16px">
    <form action="/runs/start" method="post" style="display:inline"><button type="submit">立即执行</button></form>
    {{if .CanResumeRun}}<form action="/runs/resume" method="post" style="display:inline"><button type="submit">继续执行</button></form>{{end}}
    {{if .CurrentRun}}<form action="/runs/stop" method="post" style="display:inline"><input type="hidden" name="id" value="{{.CurrentRun.ID}}"><button class="danger" type="submit">停止任务</button></form>{{end}}
    <a class="button" href="/run">查看日志</a>
    <a class="button" href="/settings">配置</a>
  </div>
</section>

<section class="kpi-grid">
  <div class="kpi"><span>今日更新 IP</span><strong>{{.Stats.TodayUpdatedIPs}}</strong></div>
  <div class="kpi"><span>今日写入 DNS</span><strong>{{.Stats.TodaySyncedIPs}} / {{.Stats.ExpectedIPCount}}</strong></div>
  <div class="kpi"><span>今日任务</span><strong>{{.Stats.TodayTaskCount}}</strong></div>
  <div class="kpi"><span>DNS 状态</span><strong>{{.Stats.LastDNSStatus}}</strong></div>
</section>

<div class="dashboard-grid">
  <section class="panel">
    <h1>执行看板</h1>
    {{if .CurrentRun}}
      <p class="muted">当前阶段：{{.CurrentRun.Stage}}</p>
      <div class="progress"><div style="width: {{.CurrentRun.Progress}}%"></div></div>
      <div class="grid">
        <div class="metric"><span>已发现可用 IP</span><strong>{{.CurrentRun.UpdatedIPCount}} / {{.CurrentRun.RequiredIPCount}}</strong></div>
        <div class="metric"><span>已写入 Cloudflare</span><strong>{{.CurrentRun.SyncedIPCount}} / {{.CurrentRun.RequiredIPCount}}</strong></div>
        <div class="metric"><span>触发方式</span><strong>{{if eq .CurrentRun.Trigger "scheduled"}}定时{{else if eq .CurrentRun.Trigger "resume"}}续接{{else}}手动{{end}}</strong></div>
      </div>
    {{else if .LatestRun}}
      <p class="muted">最近一次：{{.LatestRun.Stage}}</p>
      <div class="progress"><div style="width: {{.LatestRun.Progress}}%"></div></div>
      <div class="grid">
        <div class="metric"><span>更新 IP</span><strong>{{.LatestRun.UpdatedIPCount}} / {{.LatestRun.RequiredIPCount}}</strong></div>
        <div class="metric"><span>写入 DNS</span><strong>{{.LatestRun.SyncedIPCount}} / {{.LatestRun.RequiredIPCount}}</strong></div>
        <div class="metric"><span>结果</span><strong>{{.LatestRun.Status}}</strong></div>
      </div>
    {{else}}
      <p class="muted">还没有执行记录。</p>
    {{end}}
  </section>

  <section class="panel">
    <h2>目标概览</h2>
    <ul class="compact-list">
      <li><span>IPv4 A</span><strong>{{if .Settings.IPv4Enabled}}{{if .IPv4RecordName}}{{.IPv4RecordName}}{{else}}未配置{{end}}{{else}}未启用{{end}}</strong></li>
      <li><span>IPv6 AAAA</span><strong>{{if .Settings.IPv6Enabled}}{{if .IPv6RecordName}}{{.IPv6RecordName}}{{else}}未配置{{end}}{{else}}未启用{{end}}</strong></li>
      <li><span>地区筛选</span><strong>{{.LocationSummary}}</strong></li>
      <li><span>计划</span><strong>{{.ScheduleSummary}}</strong></li>
      <li><span>下次</span><strong>{{.NextRunAt}}</strong></li>
      <li><span>配置</span><strong>{{if .Stats.ConfigReady}}可执行{{else}}{{.Stats.ConfigHint}}{{end}}</strong></li>
    </ul>
  </section>
</div>

{{template "ipResultPanel" .}}

<section class="panel">
  <h2>最近任务</h2>
  {{template "runs" .}}
</section>
{{end}}
`

const resultTemplate = `
{{define "ipResultPanel"}}
<section class="panel">
  <div class="row" style="justify-content:space-between; align-items:flex-start">
    <div>
      <h2>IP 结果看板</h2>
      <p class="muted">优先展示最近一次写入 Cloudflare 的结果；需要排查时，可以展开今天全部测试数据。</p>
    </div>
  </div>

  {{if .LatestResultSummary.Total}}
    <div class="grid">
      <div class="metric"><span>最近同步 IP</span><strong>{{.LatestResultSummary.Total}}</strong></div>
      <div class="metric"><span>IPv4 / IPv6</span><strong>{{.LatestResultSummary.IPv4Count}} / {{.LatestResultSummary.IPv6Count}}</strong></div>
      <div class="metric"><span>Cloudflare 确认</span><strong>{{.LatestResultSummary.SyncedCount}} / {{.LatestResultSummary.Total}}</strong></div>
      <div class="metric"><span>最佳实测</span><strong>{{.LatestResultSummary.BestMeasuredMbps}} Mbps</strong></div>
    </div>
    <details open>
      <summary>最近同步结果 · {{.LatestResultSummary.Total}} 个 IP · 最佳 {{.LatestResultSummary.BestIP}} / {{.LatestResultSummary.BestDataCenter}}</summary>
      <h3 class="section-title">IPv4 A · {{.LatestResultSummary.IPv4Count}} 个</h3>
      {{template "ipResultTable" .LatestIPv4Results}}
      <h3 class="section-title">IPv6 AAAA · {{.LatestResultSummary.IPv6Count}} 个</h3>
      {{template "ipResultTable" .LatestIPv6Results}}
    </details>
  {{else}}
    <p class="muted">还没有可展示的 IP 测试结果。</p>
  {{end}}

  {{if .TodayResultSummary.Total}}
    <details>
      <summary>今天全部测试结果 · {{.TodayResultSummary.Total}} 个 IP · 已同步 {{.TodayResultSummary.SyncedCount}} 个</summary>
      <div class="grid" style="margin-top:12px">
        <div class="metric"><span>今日 IPv4</span><strong>{{.TodayResultSummary.IPv4Count}}</strong></div>
        <div class="metric"><span>今日 IPv6</span><strong>{{.TodayResultSummary.IPv6Count}}</strong></div>
        <div class="metric"><span>今日最佳实测</span><strong>{{.TodayResultSummary.BestMeasuredMbps}} Mbps</strong></div>
        <div class="metric"><span>今日最佳峰值</span><strong>{{.TodayResultSummary.BestPeakKBps}} kB/s</strong></div>
      </div>
      <h3 class="section-title">今日 IPv4 A</h3>
      {{template "ipResultTable" .TodayIPv4Results}}
      <h3 class="section-title">今日 IPv6 AAAA</h3>
      {{template "ipResultTable" .TodayIPv6Results}}
    </details>
  {{end}}
</section>
{{end}}

{{define "ipResultTable"}}
<div class="table-wrap">
  <table>
    <thead>
      <tr>
        <th>#</th>
        <th>IP</th>
        <th>协议</th>
        <th>实测带宽</th>
        <th>峰值速度</th>
        <th>RTT</th>
        <th>机房</th>
        <th>耗时</th>
        <th>DNS</th>
        <th>测试时间</th>
      </tr>
    </thead>
    <tbody>
      {{range .}}
        <tr>
          <td>{{.Index}}</td>
          <td class="ip-cell">{{.IP}}</td>
          <td>{{.Protocol}} / {{.RecordType}}</td>
          <td>{{.MeasuredBandwidthMbps}} Mbps</td>
          <td>{{.PeakSpeedKBps}} kB/s</td>
          <td>{{.RTTMs}} ms</td>
          <td>{{.DataCenter}}{{if .DataCenterCode}} ({{.DataCenterCode}}){{end}}{{if .DataCenterRegion}}<br><span class="muted">{{.DataCenterRegion}}</span>{{end}}</td>
          <td>{{.DurationSeconds}} 秒</td>
          <td>{{.SyncedText}}</td>
          <td>{{.TestedAt}}</td>
        </tr>
      {{else}}
        <tr><td colspan="10" class="muted">暂无数据</td></tr>
      {{end}}
    </tbody>
  </table>
</div>
{{end}}
`

const settingsTemplate = `
{{define "content"}}
	<section class="panel">
	  <h1>项目配置</h1>
	  <div class="subsection" style="border-top:0; padding-top:0; margin-bottom:18px">
	    <h2>配置检查</h2>
	    <p class="muted">保存配置后，点击测试。系统会创建一个临时 TXT 记录并立刻删除，用来确认 Token、Zone ID 和 DNS 写入权限都正确。</p>
	    <form method="post" action="/settings/test" class="row">
	      <button type="submit">测试 Cloudflare 写入</button>
	      <span class="muted">只操作 <code>_cf-betterip-test.&lt;目标域名&gt;</code>，不会修改正式 A / AAAA。</span>
	    </form>
	    {{if .ConfigTestResults}}
	      <div class="grid" style="margin-top:14px">
	        {{range .ConfigTestResults}}
	          <div class="metric">
	            <span>{{.Label}} · {{if .Success}}测试通过{{else}}测试失败{{end}}</span>
	            <strong>{{if .RecordName}}{{.RecordName}}{{else}}未配置域名{{end}}</strong>
	            <p class="muted">{{.Message}}</p>
	            {{if .TestName}}<p class="muted">临时记录：<code>{{.TestName}}</code></p>{{end}}
	          </div>
	        {{end}}
	      </div>
	    {{end}}
	  </div>
	  <form method="post">
    <h2>统一 Cloudflare 凭据</h2>
    <p class="muted">默认情况下 IPv4 和 IPv6 都继承这里的 Cloudflare 配置。只有需要分账号、分 Zone 或分 Token 时，再在下面开启独立凭据。</p>
    <label>统一 API Token <span class="muted">当前：{{.TokenMasked}}</span></label>
    <input type="password" name="cloudflare_api_token" placeholder="留空表示保留现有 Token">
    <label>统一 Account ID</label>
    <input type="text" name="cloudflare_account_id" value="{{.Settings.CloudflareAccountID}}">
    <label>统一 Zone ID</label>
    <input type="text" name="cloudflare_zone_id" value="{{.Settings.CloudflareZoneID}}">

    <h2 style="margin-top:22px">DNS 目标策略</h2>
    <label>目标模式</label>
    <div class="row">
      <label class="checkbox"><input type="radio" name="dns_target_mode" value="single" {{if eq .Settings.DNSTargetMode "single"}}checked{{end}}> 单域名：IPv4 和 IPv6 都写入同一个域名</label>
      <label class="checkbox"><input type="radio" name="dns_target_mode" value="split" {{if eq .Settings.DNSTargetMode "split"}}checked{{end}}> 分离域名：IPv4 和 IPv6 分开写入</label>
    </div>
    <label>单域名目标完整域名</label>
    <input type="text" name="record_name" value="{{.Settings.RecordName}}" placeholder="speed.123go.eu.org">
    <p class="muted">单域名模式下会把 IPv4 A 和 IPv6 AAAA 都写入这个域名。</p>

    <div class="grid">
      <div class="subsection">
        <h2>IPv4 A 目标</h2>
        <label>IPv4 目标完整域名</label>
        <input type="text" name="ipv4_record_name" value="{{.Settings.IPv4Target.RecordName}}" placeholder="ipv4-speed.123go.eu.org">
        <label>IPv4 凭据模式</label>
        <label class="checkbox"><input type="radio" name="ipv4_credential_mode" value="shared" {{if ne .Settings.IPv4Target.CredentialMode "custom"}}checked{{end}}> 继承统一凭据</label>
        <label class="checkbox"><input type="radio" name="ipv4_credential_mode" value="custom" {{if eq .Settings.IPv4Target.CredentialMode "custom"}}checked{{end}}> 使用独立凭据</label>
        <label>IPv4 独立 API Token <span class="muted">当前：{{.IPv4TokenMasked}}</span></label>
        <input type="password" name="ipv4_cloudflare_api_token" placeholder="留空表示保留现有 Token">
        <label>IPv4 独立 Account ID</label>
        <input type="text" name="ipv4_cloudflare_account_id" value="{{.Settings.IPv4Target.CloudflareAccountID}}">
        <label>IPv4 独立 Zone ID</label>
        <input type="text" name="ipv4_cloudflare_zone_id" value="{{.Settings.IPv4Target.CloudflareZoneID}}">
      </div>
      <div class="subsection">
        <h2>IPv6 AAAA 目标</h2>
        <label>IPv6 目标完整域名</label>
        <input type="text" name="ipv6_record_name" value="{{.Settings.IPv6Target.RecordName}}" placeholder="ipv6-speed.123go.eu.org">
        <label>IPv6 凭据模式</label>
        <label class="checkbox"><input type="radio" name="ipv6_credential_mode" value="shared" {{if ne .Settings.IPv6Target.CredentialMode "custom"}}checked{{end}}> 继承统一凭据</label>
        <label class="checkbox"><input type="radio" name="ipv6_credential_mode" value="custom" {{if eq .Settings.IPv6Target.CredentialMode "custom"}}checked{{end}}> 使用独立凭据</label>
        <label>IPv6 独立 API Token <span class="muted">当前：{{.IPv6TokenMasked}}</span></label>
        <input type="password" name="ipv6_cloudflare_api_token" placeholder="留空表示保留现有 Token">
        <label>IPv6 独立 Account ID</label>
        <input type="text" name="ipv6_cloudflare_account_id" value="{{.Settings.IPv6Target.CloudflareAccountID}}">
        <label>IPv6 独立 Zone ID</label>
        <input type="text" name="ipv6_cloudflare_zone_id" value="{{.Settings.IPv6Target.CloudflareZoneID}}">
      </div>
    </div>

    <h2 style="margin-top:22px">地区筛选</h2>
    <p class="muted">先从 Cloudflare IP 地区网段数据库中按国家、区域和城市筛选 CIDR，再从这些网段中生成 IPv4 / IPv6 候选 IP 测速。<code>CF-RAY</code> 只用于展示实际响应机房。</p>
    <div class="row" style="margin-bottom:12px">
      {{if .GeoDatabase.Ready}}
        <span class="muted">数据库已就绪：{{.GeoDatabase.GeoFeedCount}} 条 IP 网段，更新于 {{.GeoDatabase.UpdatedAt}}</span>
      {{else}}
        <span class="muted">地区 IP 网段数据库尚未下载，请先点击更新。</span>
      {{end}}
      <button type="submit" class="ghost" formaction="/settings/geo-refresh" formmethod="post">更新地区 IP 数据库</button>
    </div>
    <div class="row">
      <label class="checkbox"><input type="radio" name="location_mode" value="any" {{if eq .Settings.LocationMode "any"}}checked{{end}}> 全局随机</label>
      <label class="checkbox"><input type="radio" name="location_mode" value="prefer" {{if eq .Settings.LocationMode "prefer"}}checked{{end}}> 地区网段优先，10 分钟后回退全局</label>
      <label class="checkbox"><input type="radio" name="location_mode" value="strict" {{if eq .Settings.LocationMode "strict"}}checked{{end}}> 仅测试所选地区网段</label>
    </div>
    <div class="grid">
      <div>
        <label for="location-country">国家 / 地区</label>
        <select id="location-country" name="location_country">
          <option value="">所有国家</option>
          {{range .GeoCountries}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}
        </select>
      </div>
      <div>
        <label for="location-region">区域 / 数据中心代码</label>
        <select id="location-region" name="location_region">
          <option value="">所有区域</option>
          {{range .GeoRegions}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}
        </select>
      </div>
      <div>
        <label for="location-city">城市</label>
        <select id="location-city" name="location_city">
          <option value="">所有城市</option>
          {{range .GeoCities}}<option value="{{.Value}}" {{if .Selected}}selected{{end}}>{{.Label}}</option>{{end}}
        </select>
      </div>
    </div>
    <div id="geo-location-source" hidden>
      {{range .GeoLocations}}<span data-country="{{.Country}}" data-region="{{.Region}}" data-city="{{.City}}"></span>{{end}}
    </div>

    <h2 style="margin-top:22px">扫描参数</h2>
    <div class="grid">
      <div>
        <label class="checkbox"><input type="checkbox" name="ipv4_enabled" {{if .Settings.IPv4Enabled}}checked{{end}}> 启用 IPv4 扫描与 A 记录同步</label>
        <label>IPv4 写入数量</label>
        <input type="number" name="ipv4_count" min="0" max="50" value="{{.Settings.IPv4Count}}">
      </div>
      <div>
        <label class="checkbox"><input type="checkbox" name="ipv6_enabled" {{if .Settings.IPv6Enabled}}checked{{end}}> 启用 IPv6 扫描与 AAAA 记录同步</label>
        <label>IPv6 写入数量</label>
        <input type="number" name="ipv6_count" min="0" max="50" value="{{.Settings.IPv6Count}}">
      </div>
      <div>
        <label>设置带宽 Mbps</label>
        <input type="number" name="bandwidth_mbps" min="1" max="10000" value="{{.Settings.BandwidthMbps}}">
      </div>
      <div>
        <label>RTT 并发数</label>
        <input type="number" name="rtt_concurrency" min="1" max="100" value="{{.Settings.RTTConcurrency}}">
      </div>
      <div>
        <label>定时运行时间</label>
        <input type="time" name="schedule_time" value="{{.Settings.ScheduleTime}}">
      </div>
    </div>
    <label class="checkbox"><input type="checkbox" name="use_tls" {{if .Settings.UseTLS}}checked{{end}}> 使用 TLS 测速</label>

    <h2 style="margin-top:22px">定时任务</h2>
    <label class="checkbox"><input type="checkbox" name="schedule_enabled" {{if .Settings.ScheduleEnabled}}checked{{end}}> 启用定时任务</label>
    <label>定时类型</label>
    <div class="row">
      <label class="checkbox"><input type="radio" name="schedule_mode" value="hourly" {{if eq .Settings.ScheduleMode "hourly"}}checked{{end}}> 每小时</label>
      <label class="checkbox"><input type="radio" name="schedule_mode" value="daily" {{if eq .Settings.ScheduleMode "daily"}}checked{{end}}> 每天固定时间</label>
      <label class="checkbox"><input type="radio" name="schedule_mode" value="every_n_days" {{if eq .Settings.ScheduleMode "every_n_days"}}checked{{end}}> 每 N 天固定时间</label>
    </div>
    <div class="grid">
      <div>
        <label>每天运行时间</label>
        <input type="time" name="schedule_time" value="{{.Settings.ScheduleTime}}">
      </div>
      <div>
        <label>间隔天数</label>
        <input type="number" name="schedule_interval_days" min="1" max="365" value="{{.Settings.ScheduleIntervalDays}}">
      </div>
    </div>
    <p class="muted">当前策略：{{.ScheduleSummary}}；下一次计划：{{.NextRunAt}}</p>
    <p class="row"><button type="submit">保存配置</button><a class="button" href="/dashboard">返回 Dashboard</a></p>
  </form>
</section>
<script>
  (function () {
    var country = document.getElementById("location-country");
    var region = document.getElementById("location-region");
    var city = document.getElementById("location-city");
    var source = Array.prototype.map.call(document.querySelectorAll("#geo-location-source span"), function (node) {
      return { country: node.dataset.country, region: node.dataset.region, city: node.dataset.city };
    });
    if (!country || !region || !city || source.length === 0) return;

    function valuesFor(field, countryValue, regionValue) {
      var seen = {};
      return source.filter(function (item) {
        return (!countryValue || item.country === countryValue) && (!regionValue || item.region === regionValue);
      }).map(function (item) { return item[field]; }).filter(function (value) {
        if (!value || seen[value]) return false;
        seen[value] = true;
        return true;
      }).sort(function (a, b) { return a.localeCompare(b); });
    }

    function replaceOptions(select, values, emptyLabel, preferred) {
      select.innerHTML = "";
      var empty = document.createElement("option");
      empty.value = "";
      empty.textContent = emptyLabel;
      select.appendChild(empty);
      values.forEach(function (value) {
        var option = document.createElement("option");
        option.value = value;
        option.textContent = value;
        select.appendChild(option);
      });
      select.value = values.indexOf(preferred) >= 0 ? preferred : "";
    }

    function refresh(resetRegion, resetCity) {
      var selectedRegion = resetRegion ? "" : region.value;
      var regions = valuesFor("region", country.value, "");
      replaceOptions(region, regions, "所有区域", selectedRegion);
      var selectedCity = resetCity ? "" : city.value;
      var cities = valuesFor("city", country.value, region.value);
      replaceOptions(city, cities, "所有城市", selectedCity);
    }

    country.addEventListener("change", function () { refresh(true, true); });
    region.addEventListener("change", function () { refresh(false, true); });
  })();
</script>
{{end}}
`

const runTemplate = `
{{define "content"}}
<section class="panel">
  <h1>任务控制台</h1>
  <div class="row" style="margin-bottom:16px">
    <form action="/runs/start" method="post" style="display:inline"><button type="submit">立即执行</button></form>
    {{if .CanResumeRun}}<form action="/runs/resume" method="post" style="display:inline"><button type="submit">继续执行</button></form>{{end}}
    {{if .CurrentRun}}<form action="/runs/stop" method="post" style="display:inline"><input type="hidden" name="id" value="{{.CurrentRun.ID}}"><button class="danger" type="submit">停止任务</button></form>{{end}}
    <a class="button" href="/settings">调整定时设置</a>
  </div>
  {{if .CurrentRun}}
    <p class="muted">当前阶段：{{.CurrentRun.Stage}}</p>
    <div class="progress"><div style="width: {{.CurrentRun.Progress}}%"></div></div>
  {{else if .LatestRun}}
    <p class="muted">最近阶段：{{.LatestRun.Stage}}</p>
    <div class="progress"><div style="width: {{.LatestRun.Progress}}%"></div></div>
  {{end}}
  <div class="grid">
    <div class="metric"><span>今日更新 IP</span><strong>{{.Stats.TodayUpdatedIPs}}</strong></div>
    <div class="metric"><span>今日写入 DNS</span><strong>{{.Stats.TodaySyncedIPs}} / {{.Stats.ExpectedIPCount}}</strong></div>
    <div class="metric"><span>今日任务</span><strong>{{.Stats.TodayTaskCount}}</strong></div>
    <div class="metric"><span>定时策略</span><strong>{{.ScheduleSummary}}</strong></div>
    <div class="metric"><span>地区筛选</span><strong>{{.LocationSummary}}</strong></div>
  </div>
</section>
{{template "ipResultPanel" .}}
<section class="panel">
  <h2>运行日志</h2>
  {{template "runs" .}}
</section>
{{end}}
`

const runsTemplate = `
{{define "runs"}}
  {{if .RecentRuns}}
    {{range .RecentRuns}}
      <details {{if eq .Status "running"}}open{{end}}>
        <summary>
          <span class="status-{{.Status}}">{{.Status}}</span>
          · {{if eq .Trigger "scheduled"}}定时执行{{else if eq .Trigger "resume"}}继续执行{{else}}立即执行{{end}}
          · {{.StartedAt}}
          · {{.Stage}}
        </summary>
        <div class="progress"><div style="width: {{.Progress}}%"></div></div>
        <ul class="compact-list">
          <li><span>更新 IP</span><strong>{{.UpdatedIPCount}} / {{.RequiredIPCount}}</strong></li>
          <li><span>写入 DNS</span><strong>{{.SyncedIPCount}} / {{.RequiredIPCount}}</strong></li>
          <li><span>摘要</span><strong>{{.Summary}}</strong></li>
        </ul>
        <div class="row" style="margin:12px 0">
          {{if eq .Status "running"}}
            <form action="/runs/stop" method="post" style="display:inline"><input type="hidden" name="id" value="{{.ID}}"><button class="danger" type="submit">停止任务</button></form>
          {{end}}
          <form action="/runs/delete" method="post" style="display:inline"><input type="hidden" name="id" value="{{.ID}}"><button class="ghost" type="submit">删除记录</button></form>
        </div>
        <pre class="log">{{range .Logs}}[{{.At}}] [{{.Level}}] {{.Message}}
{{end}}</pre>
      </details>
    {{end}}
  {{else}}
    <p class="muted">还没有执行记录。</p>
  {{end}}
{{end}}
`
