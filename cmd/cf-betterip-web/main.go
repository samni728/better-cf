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
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cf-betterip-ser/internal/geodb"
	"cf-betterip-ser/internal/searchmemory"
)

type App struct {
	store        *Store
	searchMemory *searchmemory.Store
	sessions     *SessionStore
	tasks        *TaskManager
	dataDir      string
	geoMu        sync.RWMutex
	geoLocations []GeoLocation
	geoDatabase  GeoDatabaseStatus
}

const (
	appVersion               = "v1.3.0"
	repositoryURL            = "https://github.com/samni728/better-cf"
	scannerObservationPrefix = "@@BETTER_CF_OBSERVATION@@"
)

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
	DNSConfigVersion      int                          `json:"dns_config_version,omitempty"`
	CloudflareCredentials []CloudflareCredentialConfig `json:"cloudflare_credentials,omitempty"`
	DNSTargets            []DNSTargetConfig            `json:"dns_targets,omitempty"`
	ManualDNSTargets      []ManualDNSTargetConfig      `json:"manual_dns_targets,omitempty"`
	// Legacy DNS fields are retained for one-way v1 -> v2 migration only.
	CloudflareAPIToken      string       `json:"cloudflare_api_token,omitempty"`
	CloudflareAccountID     string       `json:"cloudflare_account_id,omitempty"`
	CloudflareZoneID        string       `json:"cloudflare_zone_id,omitempty"`
	RecordName              string       `json:"record_name,omitempty"`
	DNSTargetMode           string       `json:"dns_target_mode,omitempty"`
	IPv4Target              TargetConfig `json:"ipv4_target"`
	IPv6Target              TargetConfig `json:"ipv6_target"`
	IPv4Enabled             bool         `json:"ipv4_enabled"`
	IPv6Enabled             bool         `json:"ipv6_enabled"`
	IPv4Count               int          `json:"ipv4_count"`
	IPv6Count               int          `json:"ipv6_count"`
	UseTLS                  bool         `json:"use_tls"`
	BandwidthMbps           int          `json:"bandwidth_mbps"`
	RTTConcurrency          int          `json:"rtt_concurrency"`
	MaxRTTMs                int          `json:"max_rtt_ms"`
	TrueConnectionIPv4      bool         `json:"true_connection_ipv4,omitempty"`
	TrueConnectionIPv6      bool         `json:"true_connection_ipv6,omitempty"`
	TrueConnectionHTTP      bool         `json:"true_connection_http,omitempty"`
	TrueConnectionHTTPS     bool         `json:"true_connection_https,omitempty"`
	TrueConnectionHTTPNode  string       `json:"true_connection_http_node,omitempty"`
	TrueConnectionHTTPSNode string       `json:"true_connection_https_node,omitempty"`
	TrueConnectionTestURL   string       `json:"true_connection_test_url,omitempty"`
	SearchNetworkLabel      string       `json:"search_network_label,omitempty"`
	LocationMode            string       `json:"location_mode,omitempty"`
	LocationCountry         string       `json:"location_country,omitempty"`
	LocationRegion          string       `json:"location_region,omitempty"`
	LocationCity            string       `json:"location_city,omitempty"`
	ScheduleEnabled         bool         `json:"schedule_enabled"`
	ScheduleMode            string       `json:"schedule_mode,omitempty"`
	ScheduleIntervalDays    int          `json:"schedule_interval_days"`
	ScheduleTime            string       `json:"schedule_time,omitempty"`
}

type CloudflareCredentialConfig struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	AuthType  string `json:"auth_type"`
	APIToken  string `json:"api_token,omitempty"`
	APIKey    string `json:"api_key,omitempty"`
	Email     string `json:"email,omitempty"`
	AccountID string `json:"account_id,omitempty"`
}

type DNSTargetConfig struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	RootDomain   string `json:"root_domain"`
	ZoneID       string `json:"zone_id"`
	RecordName   string `json:"record_name"`
	RecordFamily string `json:"record_family"`
	CredentialID string `json:"credential_id"`
	Enabled      bool   `json:"enabled"`
}

type ManualDNSTargetConfig struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	RootDomain    string `json:"root_domain"`
	ZoneID        string `json:"zone_id"`
	RecordName    string `json:"record_name"`
	CredentialID  string `json:"credential_id"`
	LastUpdatedAt string `json:"last_updated_at,omitempty"`
	LastIPv4Count int    `json:"last_ipv4_count,omitempty"`
	LastIPv6Count int    `json:"last_ipv6_count,omitempty"`
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
	ID                      string                `json:"id"`
	Trigger                 string                `json:"trigger"`
	Status                  string                `json:"status"`
	Mode                    string                `json:"mode"`
	Stage                   string                `json:"stage,omitempty"`
	Progress                int                   `json:"progress"`
	UpdatedIPCount          int                   `json:"updated_ip_count"`
	SyncedIPCount           int                   `json:"synced_ip_count"`
	RequiredIPCount         int                   `json:"required_ip_count"`
	RequiredDNSRecordCount  int                   `json:"required_dns_record_count,omitempty"`
	PlannedDNSTargetCount   int                   `json:"planned_dns_target_count,omitempty"`
	ConfirmedDNSTargetCount int                   `json:"confirmed_dns_target_count,omitempty"`
	DNSStatus               string                `json:"dns_status,omitempty"`
	ConfigSnapshot          *Settings             `json:"config_snapshot,omitempty"`
	SearchPlanSnapshot      []RunSearchFamilyPlan `json:"search_plan_snapshot,omitempty"`
	DNSTargetResults        []DNSTargetSyncResult `json:"dns_target_results,omitempty"`
	StartedAt               string                `json:"started_at"`
	FinishedAt              string                `json:"finished_at,omitempty"`
	Summary                 string                `json:"summary,omitempty"`
	Logs                    []RunLog              `json:"logs"`
	Plan                    RunPlanView           `json:"-"`
}

type RunPlanTargetView struct {
	Name        string
	RecordName  string
	RecordsText string
	Reason      string
}

type RunPlanView struct {
	Available          bool
	ScanText           string
	TrueConnectionText string
	DNSHeadline        string
	IPv4RecordCount    int
	IPv6RecordCount    int
	ActiveTargets      []RunPlanTargetView
	SkippedTargets     []RunPlanTargetView
	SearchFamilies     []RunSearchFamilyPlan
}

type RunSearchFamilyPlan struct {
	IPVersion          int                          `json:"ip_version"`
	Available          bool                         `json:"available"`
	ManualPrefixes     []string                     `json:"manual_prefixes,omitempty"`
	ManualSeedIPs      []string                     `json:"manual_seed_ips,omitempty"`
	ManualHintPrefixes []string                     `json:"manual_hint_prefixes,omitempty"`
	ManualQuotaPercent int                          `json:"manual_quota_percent,omitempty"`
	ExactIPCount       int                          `json:"exact_ip_count"`
	NarrowHintCount    int                          `json:"narrow_hint_count"`
	WideHintCount      int                          `json:"wide_hint_count"`
	CoolingIPCount     int                          `json:"cooling_ip_count"`
	CoolingPrefixCount int                          `json:"cooling_prefix_count"`
	Budget             searchmemory.CandidateBudget `json:"budget"`
}

type GeoFilterStats struct {
	DataCenterCount int
	Codes           string
}

type RunLog struct {
	At      string `json:"at"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type IPTestResult struct {
	RunID                   string                     `json:"run_id"`
	IP                      string                     `json:"ip"`
	IPVersion               int                        `json:"ip_version"`
	RecordType              string                     `json:"record_type"`
	Protocol                string                     `json:"protocol"`
	ConfiguredBandwidthMbps int                        `json:"configured_bandwidth_mbps"`
	MeasuredBandwidthMbps   int                        `json:"measured_bandwidth_mbps"`
	PeakSpeedKBps           int                        `json:"peak_speed_kbps"`
	RTTMs                   int                        `json:"rtt_ms"`
	TrueConnectionTested    bool                       `json:"true_connection_tested,omitempty"`
	TrueConnectionPorts     []TrueConnectionPortResult `json:"true_connection_ports,omitempty"`
	CandidateSource         string                     `json:"candidate_source,omitempty"`
	DataCenter              string                     `json:"data_center"`
	DataCenterCode          string                     `json:"data_center_code,omitempty"`
	DataCenterCountry       string                     `json:"data_center_country,omitempty"`
	DataCenterRegion        string                     `json:"data_center_region,omitempty"`
	DataCenterCity          string                     `json:"data_center_city,omitempty"`
	DurationSeconds         int                        `json:"duration_seconds"`
	SelectedForDNS          bool                       `json:"selected_for_dns"`
	CloudflareSynced        bool                       `json:"cloudflare_synced"`
	ConfirmedDNSTargets     int                        `json:"confirmed_dns_targets,omitempty"`
	PlannedDNSTargets       int                        `json:"planned_dns_targets,omitempty"`
	TestedAt                string                     `json:"tested_at"`
}

type scannerStageObservation struct {
	Stage             string `json:"stage"`
	IP                string `json:"ip"`
	IPVersion         int    `json:"ip_version"`
	DataCenterCode    string `json:"dc_code,omitempty"`
	DataCenterCountry string `json:"dc_country,omitempty"`
	DataCenterRegion  string `json:"dc_region,omitempty"`
	DataCenterCity    string `json:"dc_city,omitempty"`
	RTTMs             int    `json:"rtt_ms,omitempty"`
	MaxRTTMs          int    `json:"max_rtt_ms,omitempty"`
	PeakSpeedKBps     int    `json:"peak_speed_kbps,omitempty"`
}

type TrueConnectionPortResult struct {
	Scheme    string `json:"scheme"`
	Port      int    `json:"port"`
	LatencyMs int    `json:"latency_ms"`
}

type PageData struct {
	Title                  string
	Flash                  string
	Error                  string
	Username               string
	Settings               Settings
	HasAdmin               bool
	CloudflareCredentials  []CloudflareCredentialView
	DNSTargets             []DNSTargetView
	ManualDNSTargets       []ManualDNSTargetView
	DNSTargetSummary       string
	ExpectedDNSRecordCount int
	ScheduleSummary        string
	LocationSummary        string
	NextRunAt              string
	RecentRuns             []RunRecord
	HasRunningRun          bool
	Stats                  DashboardStats
	CurrentRun             *RunRecord
	LatestRun              *RunRecord
	ConfigTestResults      []ConfigTestResult
	CanResumeRun           bool
	LatestResultSummary    IPResultSummary
	LatestIPv4Results      []IPResultView
	LatestIPv6Results      []IPResultView
	TodayResultSummary     IPResultSummary
	TodayIPv4Results       []IPResultView
	TodayIPv6Results       []IPResultView
	GeoCountries           []GeoChoice
	GeoRegions             []GeoChoice
	GeoCities              []GeoChoice
	GeoLocations           []GeoLocation
	GeoDatabase            GeoDatabaseStatus
	GeoFilterStats         GeoFilterStats
	FamilyNoResultLimit    string
	SearchMemoryIPv4       searchmemory.Summary
	SearchMemoryIPv6       searchmemory.Summary
	SearchMemoryProfiles   []SearchMemoryProfileView
	AppVersion             string
	RepositoryURL          string
}

type SearchMemoryProfileView struct {
	Insight   searchmemory.ProfileInsight
	Label     string
	ModeLabel string
	Current   bool
}

type CloudflareCredentialView struct {
	Config       CloudflareCredentialConfig
	SecretStatus string
}

type DNSTargetView struct {
	Config         DNSTargetConfig
	CredentialName string
	FamilyLabel    string
}

type ManualDNSTargetView struct {
	Config         ManualDNSTargetConfig
	CredentialName string
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
	TrueConnectionText      string
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
	RootDomain string
	RecordName string
	Credential CloudflareCredentialConfig
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
	TargetID   string
	Label      string
	RootDomain string
	RecordName string
	RecordType string
	Credential CloudflareCredentialConfig
	ZoneID     string
	IPs        []string
}

type DNSSyncReport struct {
	ConfirmedRecords int
	ConfirmedTargets int
	TotalTargets     int
	TargetResults    []DNSTargetSyncResult
	ConfirmedIPs     map[string]bool
	ConfirmedByIP    map[string]int
	PlannedByIP      map[string]int
}

type DNSTargetSyncResult struct {
	TargetID         string `json:"target_id"`
	TargetName       string `json:"target_name"`
	Status           string `json:"status"`
	ConfirmedRecords int    `json:"confirmed_records"`
	PlannedRecords   int    `json:"planned_records"`
	Error            string `json:"error,omitempty"`
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
	memory, err := searchmemory.Open(filepath.Join(*dataDir, "search_memory.sqlite"))
	if err != nil {
		log.Fatal(err)
	}
	defer memory.Close()
	if err := importLegacySearchMemory(memory, store.snapshot().Results); err != nil {
		log.Printf("import legacy search memory failed: %v", err)
	}
	if err := memory.Prune(context.Background(), time.Now()); err != nil {
		log.Printf("prune search memory failed: %v", err)
	}

	dataCenterLocations := loadGeoLocations(*dataDir)
	_ = loadGeoDatabase(*dataDir) // 保留 GeoFeed 缓存用于数据库更新/研究，不再作为可测候选池。
	app := &App{
		store:        store,
		searchMemory: memory,
		sessions:     &SessionStore{sessions: make(map[string]string)},
		tasks:        &TaskManager{cancels: make(map[string]context.CancelFunc)},
		dataDir:      *dataDir,
		geoLocations: dataCenterLocations,
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
	mux.HandleFunc("/manual-dns/targets/add", app.requireAuth(app.addManualDNSTarget))
	mux.HandleFunc("/manual-dns/targets/update", app.requireAuth(app.updateManualDNSTarget))
	mux.HandleFunc("/manual-dns/targets/clear", app.requireAuth(app.clearManualDNSTarget))
	mux.HandleFunc("/manual-dns/targets/delete", app.requireAuth(app.deleteManualDNSTarget))
	mux.HandleFunc("/search-memory/prefix/add", app.requireAuth(app.addSearchMemoryPrefix))
	mux.HandleFunc("/search-memory/prefix/delete", app.requireAuth(app.deleteSearchMemoryPrefix))
	mux.HandleFunc("/search-memory/profile/clear", app.requireAuth(app.clearSearchMemoryProfile))
	mux.HandleFunc("/runs/start", app.requireAuth(app.startRun))
	mux.HandleFunc("/runs/resume", app.requireAuth(app.resumeRun))
	mux.HandleFunc("/runs/stop", app.requireAuth(app.stopRun))
	mux.HandleFunc("/runs/delete", app.requireAuth(app.deleteRun))
	mux.HandleFunc("/api/runs", app.requireAuth(app.runsAPI))
	mux.HandleFunc("/run", app.requireAuth(app.runPage))
	mux.HandleFunc("/history", app.requireAuth(app.historyPage))
	mux.HandleFunc("/run/detail", app.requireAuth(app.runDetailPage))
	mux.HandleFunc("/results", app.requireAuth(app.resultsPage))
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
	status := GeoDatabaseStatus{LocationCount: locationCount, Ready: locationCount > 0}
	if info, err := os.Stat(filepath.Join(dataDir, "locations.json")); err == nil {
		status.UpdatedAt = info.ModTime().Format("2006-01-02 15:04:05")
	}
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
	if status.UpdatedAt == "" {
		if info, err := os.Stat(path); err == nil {
			status.UpdatedAt = info.ModTime().Format("2006-01-02 15:04:05")
		}
	}
	return status
}

func (a *App) geoSnapshot() ([]GeoLocation, GeoDatabaseStatus) {
	a.geoMu.RLock()
	defer a.geoMu.RUnlock()
	locations := append([]GeoLocation(nil), a.geoLocations...)
	return locations, a.geoDatabase
}

func (a *App) geoFilterStats(settings Settings) GeoFilterStats {
	locations, _ := a.geoSnapshot()
	return calculateGeoFilterStats(locations, settings)
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
	if err := atomicWriteFile(filepath.Join(a.dataDir, "local-ip-ranges.csv"), geoFeedData); err != nil {
		return GeoDatabaseStatus{}, fmt.Errorf("更新 GeoFeed 失败: %w", err)
	}
	_, currentStatus := a.geoSnapshot()
	locationCount := currentStatus.LocationCount
	var dataCenterLocations []GeoLocation
	if locationsData, downloadErr := downloadData(client, locationsSourceURL, 8*1024*1024); downloadErr == nil {
		if locations := parseGeoLocations(locationsData); len(locations) >= 100 {
			if writeErr := atomicWriteFile(filepath.Join(a.dataDir, "locations.json"), locationsData); writeErr == nil {
				locationCount = len(locations)
				dataCenterLocations = locations
			}
		}
	}
	if len(dataCenterLocations) == 0 {
		dataCenterLocations = loadGeoLocations(a.dataDir)
		locationCount = len(dataCenterLocations)
	}
	if locationCount == 0 {
		return GeoDatabaseStatus{}, fmt.Errorf("更新 GeoFeed 成功，但 Cloudflare 响应机房数据不可用")
	}
	status := GeoDatabaseStatus{
		LocationCount: locationCount,
		GeoFeedCount:  geoFeedCount,
		UpdatedAt:     time.Now().Format("2006-01-02 15:04:05"),
		Ready:         locationCount > 0,
	}
	a.geoMu.Lock()
	a.geoLocations = dataCenterLocations
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
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(path); err == nil {
		var loaded AppState
		if err := json.Unmarshal(data, &loaded); err != nil {
			return nil, err
		}
		legacyDNSConfig := loaded.Settings.DNSConfigVersion < 2
		store.state = loaded
		settingsBeforeDefaults := cloneSettings(store.state.Settings)
		store.applyDefaults()
		settingsChanged := !reflect.DeepEqual(settingsBeforeDefaults, store.state.Settings)
		if legacyDNSConfig {
			backupPath := path + ".pre-v2.bak"
			if _, statErr := os.Stat(backupPath); errors.Is(statErr, os.ErrNotExist) {
				if writeErr := os.WriteFile(backupPath, data, 0600); writeErr != nil {
					return nil, fmt.Errorf("备份旧 DNS 配置失败: %w", writeErr)
				}
			} else if statErr != nil {
				return nil, fmt.Errorf("检查旧 DNS 配置备份失败: %w", statErr)
			}
		}
		recoveredRuns := store.recoverInterruptedRuns()
		if legacyDNSConfig || settingsChanged || recoveredRuns {
			if err := store.saveLocked(); err != nil {
				return nil, fmt.Errorf("保存升级后的应用状态失败: %w", err)
			}
		}
		return store, nil
	} else if errors.Is(err, os.ErrNotExist) {
		store.state.Settings = defaultSettings()
		store.state.UpdatedAt = nowString()
		return store, nil
	} else {
		return nil, err
	}
}

func defaultSettings() Settings {
	return Settings{
		DNSConfigVersion:      2,
		IPv4Enabled:           true,
		IPv6Enabled:           true,
		IPv4Count:             10,
		IPv6Count:             10,
		UseTLS:                true,
		BandwidthMbps:         100,
		RTTConcurrency:        50,
		MaxRTTMs:              200,
		TrueConnectionTestURL: "https://www.google.com/generate_204",
		SearchNetworkLabel:    "213 VPS",
		LocationMode:          "any",
		ScheduleMode:          "daily",
		ScheduleIntervalDays:  1,
		ScheduleTime:          "06:00",
	}
}

func (s *Store) applyDefaults() {
	if s.state.Settings.DNSConfigVersion < 2 {
		if len(s.state.Settings.CloudflareCredentials) > 0 || len(s.state.Settings.DNSTargets) > 0 {
			s.state.Settings.DNSConfigVersion = 2
		} else {
			migrateLegacyDNSConfig(&s.state.Settings)
		}
	}
	clearLegacyDNSFields(&s.state.Settings)
	normalizeDNSConfig(&s.state.Settings)
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
	if s.state.Settings.MaxRTTMs == 0 {
		s.state.Settings.MaxRTTMs = 200
	}
	s.state.Settings.MaxRTTMs = clampInt(s.state.Settings.MaxRTTMs, 10, 2000)
	if strings.TrimSpace(s.state.Settings.TrueConnectionTestURL) == "" {
		s.state.Settings.TrueConnectionTestURL = "https://www.google.com/generate_204"
	}
	if strings.TrimSpace(s.state.Settings.SearchNetworkLabel) == "" {
		s.state.Settings.SearchNetworkLabel = "213 VPS"
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
	state := s.state
	state.Settings = cloneSettings(s.state.Settings)
	state.Runs = cloneRunRecords(s.state.Runs)
	return state
}

func cloneSettings(settings Settings) Settings {
	cloned := settings
	cloned.CloudflareCredentials = append([]CloudflareCredentialConfig(nil), settings.CloudflareCredentials...)
	cloned.DNSTargets = append([]DNSTargetConfig(nil), settings.DNSTargets...)
	cloned.ManualDNSTargets = append([]ManualDNSTargetConfig(nil), settings.ManualDNSTargets...)
	return cloned
}

func cloneRunRecords(runs []RunRecord) []RunRecord {
	cloned := append([]RunRecord(nil), runs...)
	for i := range cloned {
		cloned[i].Logs = append([]RunLog(nil), runs[i].Logs...)
		cloned[i].DNSTargetResults = append([]DNSTargetSyncResult(nil), runs[i].DNSTargetResults...)
		cloned[i].SearchPlanSnapshot = append([]RunSearchFamilyPlan(nil), runs[i].SearchPlanSnapshot...)
		for j := range cloned[i].SearchPlanSnapshot {
			cloned[i].SearchPlanSnapshot[j].ManualPrefixes = append([]string(nil), runs[i].SearchPlanSnapshot[j].ManualPrefixes...)
			cloned[i].SearchPlanSnapshot[j].ManualSeedIPs = append([]string(nil), runs[i].SearchPlanSnapshot[j].ManualSeedIPs...)
			cloned[i].SearchPlanSnapshot[j].ManualHintPrefixes = append([]string(nil), runs[i].SearchPlanSnapshot[j].ManualHintPrefixes...)
		}
		if runs[i].ConfigSnapshot != nil {
			snapshot := cloneSettings(*runs[i].ConfigSnapshot)
			cloned[i].ConfigSnapshot = &snapshot
		}
	}
	return cloned
}

func sanitizedRunSettings(settings Settings) Settings {
	snapshot := cloneSettings(settings)
	for i := range snapshot.CloudflareCredentials {
		snapshot.CloudflareCredentials[i].APIToken = ""
		snapshot.CloudflareCredentials[i].APIKey = ""
	}
	snapshot.CloudflareAPIToken = ""
	snapshot.IPv4Target.CloudflareAPIToken = ""
	snapshot.IPv6Target.CloudflareAPIToken = ""
	snapshot.ManualDNSTargets = nil
	snapshot.TrueConnectionHTTPNode = ""
	snapshot.TrueConnectionHTTPSNode = ""
	return snapshot
}

func hydrateRunSettings(snapshot Settings, current Settings) Settings {
	hydrated := cloneSettings(snapshot)
	currentCredentials := make(map[string]CloudflareCredentialConfig, len(current.CloudflareCredentials))
	for _, credential := range current.CloudflareCredentials {
		currentCredentials[credential.ID] = credential
	}
	for i := range hydrated.CloudflareCredentials {
		if currentCredential, ok := currentCredentials[hydrated.CloudflareCredentials[i].ID]; ok {
			hydrated.CloudflareCredentials[i].APIToken = currentCredential.APIToken
			hydrated.CloudflareCredentials[i].APIKey = currentCredential.APIKey
		}
	}
	// 节点分享链接包含 UUID 等敏感信息，不写入任务快照；续接时按当前已保存配置解析。
	hydrated.TrueConnectionHTTPNode = current.TrueConnectionHTTPNode
	hydrated.TrueConnectionHTTPSNode = current.TrueConnectionHTTPSNode
	return hydrated
}

func settingsForFailedForm(candidate Settings, persisted Settings) Settings {
	safe := cloneSettings(candidate)
	persistedCredentials := make(map[string]CloudflareCredentialConfig, len(persisted.CloudflareCredentials))
	for _, credential := range persisted.CloudflareCredentials {
		persistedCredentials[credential.ID] = credential
	}
	for i := range safe.CloudflareCredentials {
		safe.CloudflareCredentials[i].APIToken = ""
		safe.CloudflareCredentials[i].APIKey = ""
		if old, ok := persistedCredentials[safe.CloudflareCredentials[i].ID]; ok {
			safe.CloudflareCredentials[i].APIToken = old.APIToken
			safe.CloudflareCredentials[i].APIKey = old.APIKey
		}
	}
	return safe
}

func migrateLegacyDNSConfig(settings *Settings) {
	settings.DNSConfigVersion = 2
	credentials := make([]CloudflareCredentialConfig, 0, 3)
	targets := make([]DNSTargetConfig, 0, 2)
	sharedID := "credential-legacy-shared"
	if settings.CloudflareAPIToken != "" || settings.CloudflareAccountID != "" || settings.RecordName != "" || settings.CloudflareZoneID != "" {
		credentials = append(credentials, CloudflareCredentialConfig{
			ID: sharedID, Name: "原统一 Cloudflare 凭据", AuthType: "api_token",
			APIToken: settings.CloudflareAPIToken, AccountID: settings.CloudflareAccountID,
		})
	}
	credentialFor := func(id, name string, target TargetConfig) string {
		if normalizeCredentialMode(target.CredentialMode) != "custom" {
			return sharedID
		}
		credentials = append(credentials, CloudflareCredentialConfig{
			ID: id, Name: name, AuthType: "api_token",
			APIToken: target.CloudflareAPIToken, AccountID: target.CloudflareAccountID,
		})
		return id
	}
	if normalizeTargetMode(settings.DNSTargetMode) == "split" {
		if settings.IPv4Target.RecordName != "" || settings.IPv4Target.CloudflareZoneID != "" {
			zoneID := settings.CloudflareZoneID
			if normalizeCredentialMode(settings.IPv4Target.CredentialMode) == "custom" {
				zoneID = settings.IPv4Target.CloudflareZoneID
			}
			targets = append(targets, DNSTargetConfig{ID: "target-legacy-ipv4", Name: "原 IPv4 目标", ZoneID: zoneID, RecordName: settings.IPv4Target.RecordName, RecordFamily: "ipv4", CredentialID: credentialFor("credential-legacy-ipv4", "原 IPv4 独立凭据", settings.IPv4Target), Enabled: true})
		}
		if settings.IPv6Target.RecordName != "" || settings.IPv6Target.CloudflareZoneID != "" {
			zoneID := settings.CloudflareZoneID
			if normalizeCredentialMode(settings.IPv6Target.CredentialMode) == "custom" {
				zoneID = settings.IPv6Target.CloudflareZoneID
			}
			targets = append(targets, DNSTargetConfig{ID: "target-legacy-ipv6", Name: "原 IPv6 目标", ZoneID: zoneID, RecordName: settings.IPv6Target.RecordName, RecordFamily: "ipv6", CredentialID: credentialFor("credential-legacy-ipv6", "原 IPv6 独立凭据", settings.IPv6Target), Enabled: true})
		}
	} else if settings.RecordName != "" || settings.CloudflareZoneID != "" {
		targets = append(targets, DNSTargetConfig{ID: "target-legacy-single", Name: "原单域名目标", ZoneID: settings.CloudflareZoneID, RecordName: settings.RecordName, RecordFamily: "both", CredentialID: sharedID, Enabled: true})
	}
	settings.CloudflareCredentials = credentials
	settings.DNSTargets = targets
	// The pre-v2 backup is the rollback source. Do not retain duplicate secrets
	// in the active state after a successful one-way migration.
	clearLegacyDNSFields(settings)
}

func clearLegacyDNSFields(settings *Settings) {
	settings.CloudflareAPIToken = ""
	settings.CloudflareAccountID = ""
	settings.CloudflareZoneID = ""
	settings.RecordName = ""
	settings.DNSTargetMode = ""
	settings.IPv4Target = TargetConfig{}
	settings.IPv6Target = TargetConfig{}
}

func normalizeDNSConfig(settings *Settings) {
	settings.DNSConfigVersion = 2
	credentialIDs := make(map[string]bool)
	for i := range settings.CloudflareCredentials {
		credential := &settings.CloudflareCredentials[i]
		credential.ID = ensureConfigID(credential.ID, "credential", credentialIDs)
		credential.Name = strings.TrimSpace(credential.Name)
		if credential.Name == "" {
			credential.Name = fmt.Sprintf("Cloudflare 凭据 %d", i+1)
		}
		credential.AuthType = normalizeAuthType(credential.AuthType)
		credential.APIToken = strings.TrimSpace(credential.APIToken)
		credential.APIKey = strings.TrimSpace(credential.APIKey)
		credential.Email = strings.TrimSpace(credential.Email)
		credential.AccountID = strings.TrimSpace(credential.AccountID)
	}
	targetIDs := make(map[string]bool)
	for i := range settings.DNSTargets {
		target := &settings.DNSTargets[i]
		target.ID = ensureConfigID(target.ID, "target", targetIDs)
		target.Name = strings.TrimSpace(target.Name)
		target.RootDomain = normalizeDomain(target.RootDomain)
		target.RecordName = normalizeDomain(target.RecordName)
		target.ZoneID = strings.TrimSpace(target.ZoneID)
		target.RecordFamily = normalizeRecordFamily(target.RecordFamily)
		target.CredentialID = strings.TrimSpace(target.CredentialID)
		if target.Name == "" {
			target.Name = target.RecordName
		}
	}
	manualTargetIDs := make(map[string]bool)
	for i := range settings.ManualDNSTargets {
		target := &settings.ManualDNSTargets[i]
		target.ID = ensureConfigID(target.ID, "manual-target", manualTargetIDs)
		target.Name = strings.TrimSpace(target.Name)
		target.RootDomain = normalizeDomain(target.RootDomain)
		target.RecordName = normalizeDomain(target.RecordName)
		target.ZoneID = strings.TrimSpace(target.ZoneID)
		target.CredentialID = strings.TrimSpace(target.CredentialID)
		if target.Name == "" {
			target.Name = target.RecordName
		}
	}
}

func ensureConfigID(raw, prefix string, used map[string]bool) string {
	raw = strings.TrimSpace(raw)
	valid, _ := regexp.MatchString(`^[A-Za-z0-9_-]{1,64}$`, raw)
	if valid && !used[raw] {
		used[raw] = true
		return raw
	}
	for {
		buf := make([]byte, 8)
		if _, err := rand.Read(buf); err != nil {
			raw = fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
		} else {
			raw = prefix + "-" + base64.RawURLEncoding.EncodeToString(buf)
		}
		if !used[raw] {
			used[raw] = true
			return raw
		}
	}
}

func normalizeDomain(raw string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
}

func normalizeAuthType(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), "global_api_key") {
		return "global_api_key"
	}
	return "api_token"
}

func normalizeRecordFamily(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "ipv4", "ipv6":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return "both"
	}
}

func parseDNSConfigForm(form url.Values, previous Settings) (Settings, error) {
	next := cloneSettings(previous)
	previousCredentials := make(map[string]CloudflareCredentialConfig)
	for _, credential := range previous.CloudflareCredentials {
		previousCredentials[credential.ID] = credential
	}
	credentialIDs := form["credential_id"]
	if len(credentialIDs) > 20 {
		return next, errors.New("Cloudflare 凭据最多允许配置 20 份")
	}
	seenCredentialIDs := make(map[string]bool)
	credentials := make([]CloudflareCredentialConfig, 0, len(credentialIDs))
	for _, rawID := range credentialIDs {
		id := strings.TrimSpace(rawID)
		if !validConfigID(id) || seenCredentialIDs[id] {
			return next, errors.New("Cloudflare 凭据包含无效或重复的内部 ID，请刷新页面后重试")
		}
		seenCredentialIDs[id] = true
		credential := CloudflareCredentialConfig{
			ID:        id,
			Name:      strings.TrimSpace(form.Get("credential_name_" + id)),
			AuthType:  normalizeAuthType(form.Get("credential_auth_type_" + id)),
			Email:     strings.TrimSpace(form.Get("credential_email_" + id)),
			AccountID: strings.TrimSpace(form.Get("credential_account_id_" + id)),
		}
		if old, ok := previousCredentials[id]; ok {
			credential.APIToken = old.APIToken
			credential.APIKey = old.APIKey
		}
		if secret := strings.TrimSpace(form.Get("credential_api_token_" + id)); secret != "" {
			credential.APIToken = secret
		}
		if secret := strings.TrimSpace(form.Get("credential_api_key_" + id)); secret != "" {
			credential.APIKey = secret
		}
		credentials = append(credentials, credential)
	}

	targetIDs := form["target_id"]
	if len(targetIDs) > 30 {
		return next, errors.New("DNS 写入目标最多允许配置 30 个")
	}
	seenTargetIDs := make(map[string]bool)
	targets := make([]DNSTargetConfig, 0, len(targetIDs))
	for _, rawID := range targetIDs {
		id := strings.TrimSpace(rawID)
		if !validConfigID(id) || seenTargetIDs[id] {
			return next, errors.New("DNS 目标包含无效或重复的内部 ID，请刷新页面后重试")
		}
		seenTargetIDs[id] = true
		target := DNSTargetConfig{
			ID:           id,
			Name:         strings.TrimSpace(form.Get("target_name_" + id)),
			RootDomain:   normalizeDomain(form.Get("target_root_domain_" + id)),
			ZoneID:       strings.TrimSpace(form.Get("target_zone_id_" + id)),
			RecordName:   normalizeDomain(form.Get("target_record_name_" + id)),
			RecordFamily: normalizeRecordFamily(form.Get("target_record_family_" + id)),
			CredentialID: strings.TrimSpace(form.Get("target_credential_id_" + id)),
			Enabled:      form.Get("target_enabled_"+id) == "on",
		}
		if target.Name == "" {
			target.Name = target.RecordName
		}
		targets = append(targets, target)
	}
	next.DNSConfigVersion = 2
	next.CloudflareCredentials = credentials
	next.DNSTargets = targets
	return next, nil
}

func validConfigID(id string) bool {
	valid, _ := regexp.MatchString(`^[A-Za-z0-9_-]{1,64}$`, id)
	return valid
}

func validateDNSConfig(settings Settings) error {
	if err := validateTrueConnectionSettings(settings); err != nil {
		return err
	}
	credentials := make(map[string]CloudflareCredentialConfig)
	for _, credential := range settings.CloudflareCredentials {
		if !validConfigID(credential.ID) || credentials[credential.ID].ID != "" {
			return errors.New("Cloudflare 凭据 ID 无效或重复")
		}
		if strings.TrimSpace(credential.Name) == "" {
			return errors.New("每份 Cloudflare 凭据都需要填写名称")
		}
		credentials[credential.ID] = credential
	}
	bindings := make(map[string]string)
	targetIDs := make(map[string]bool)
	for _, target := range settings.DNSTargets {
		if !validConfigID(target.ID) || targetIDs[target.ID] {
			return errors.New("DNS 目标 ID 无效或重复")
		}
		targetIDs[target.ID] = true
		credential, credentialExists := credentials[target.CredentialID]
		if !credentialExists {
			return fmt.Errorf("%s：引用的 Cloudflare 凭据不存在，请先重新绑定再删除凭据", target.Name)
		}
		if !target.Enabled {
			continue
		}
		if strings.TrimSpace(target.Name) == "" {
			return errors.New("每个已启用的 DNS 目标都需要填写名称")
		}
		legacyRootPending := target.RootDomain == "" && strings.HasPrefix(target.ID, "target-legacy-")
		if !legacyRootPending && !isValidDomainName(target.RootDomain) {
			return fmt.Errorf("%s：根域名格式不正确", target.Name)
		}
		if !isValidDomainName(target.RecordName) {
			return fmt.Errorf("%s：目标完整域名格式不正确", target.Name)
		}
		if !legacyRootPending && target.RecordName != target.RootDomain && !strings.HasSuffix(target.RecordName, "."+target.RootDomain) {
			return fmt.Errorf("%s：目标域名 %s 不属于根域名 %s", target.Name, target.RecordName, target.RootDomain)
		}
		if strings.TrimSpace(target.ZoneID) == "" {
			return fmt.Errorf("%s：缺少 Cloudflare Zone ID", target.Name)
		}
		if !credentialExists {
			return fmt.Errorf("%s：请选择一份有效的 Cloudflare 凭据", target.Name)
		}
		if err := validateCredentialForUse(credential); err != nil {
			return fmt.Errorf("%s 使用的凭据“%s”不可用：%w", target.Name, credential.Name, err)
		}
		for _, recordType := range targetRecordTypes(target) {
			key := strings.ToLower(target.ZoneID) + "|" + target.RecordName + "|" + recordType
			if previousName, exists := bindings[key]; exists {
				return fmt.Errorf("DNS 目标“%s”和“%s”重复操作同一个 %s 记录，请合并或停用其中一个", previousName, target.Name, recordType)
			}
			bindings[key] = target.Name
		}
	}
	if len(settings.ManualDNSTargets) > 30 {
		return errors.New("手动 DNS 目标最多允许配置 30 个")
	}
	manualIDs := make(map[string]bool)
	for _, target := range settings.ManualDNSTargets {
		if !validConfigID(target.ID) || manualIDs[target.ID] {
			return errors.New("手动 DNS 目标 ID 无效或重复")
		}
		manualIDs[target.ID] = true
		credential, ok := credentials[target.CredentialID]
		if !ok {
			return fmt.Errorf("手动目标“%s”：引用的 Cloudflare 凭据不存在", target.Name)
		}
		if strings.TrimSpace(target.Name) == "" {
			return errors.New("每个手动 DNS 目标都需要填写名称")
		}
		if !isValidDomainName(target.RootDomain) {
			return fmt.Errorf("手动目标“%s”：根域名格式不正确", target.Name)
		}
		if !isValidDomainName(target.RecordName) || strings.Contains(target.RecordName, "*") {
			return fmt.Errorf("手动目标“%s”：目标完整域名格式不正确", target.Name)
		}
		if target.RecordName == target.RootDomain || !strings.HasSuffix(target.RecordName, "."+target.RootDomain) {
			return fmt.Errorf("手动目标“%s”：必须使用根域名 %s 下的具体子域名", target.Name, target.RootDomain)
		}
		if strings.TrimSpace(target.ZoneID) == "" {
			return fmt.Errorf("手动目标“%s”：缺少 Cloudflare Zone ID", target.Name)
		}
		if err := validateCredentialForUse(credential); err != nil {
			return fmt.Errorf("手动目标“%s”使用的凭据不可用：%w", target.Name, err)
		}
		for _, recordType := range []string{"A", "AAAA"} {
			key := strings.ToLower(target.ZoneID) + "|" + target.RecordName + "|" + recordType
			if previousName, exists := bindings[key]; exists {
				return fmt.Errorf("手动目标“%s”与目标“%s”都会操作 %s 的 %s 记录，不能同时配置", target.Name, previousName, target.RecordName, recordType)
			}
			bindings[key] = target.Name
		}
	}
	return nil
}

func validateCredentialForUse(credential CloudflareCredentialConfig) error {
	if normalizeAuthType(credential.AuthType) == "global_api_key" {
		if strings.TrimSpace(credential.Email) == "" {
			return errors.New("Global API Key 模式缺少 Cloudflare 账号邮箱")
		}
		if strings.TrimSpace(credential.APIKey) == "" {
			return errors.New("缺少 Global API Key")
		}
		return nil
	}
	if strings.TrimSpace(credential.APIToken) == "" {
		return errors.New("缺少 API Token")
	}
	return nil
}

func isValidDomainName(name string) bool {
	name = normalizeDomain(name)
	if name == "" || len(name) > 253 || strings.Contains(name, "*") || !strings.Contains(name, ".") {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

func targetRecordTypes(target DNSTargetConfig) []string {
	switch normalizeRecordFamily(target.RecordFamily) {
	case "ipv4":
		return []string{"A"}
	case "ipv6":
		return []string{"AAAA"}
	default:
		return []string{"A", "AAAA"}
	}
}

func targetSupportsFamily(target DNSTargetConfig, family int) bool {
	if !target.Enabled {
		return false
	}
	recordFamily := normalizeRecordFamily(target.RecordFamily)
	if family == 6 {
		return recordFamily == "ipv6" || recordFamily == "both"
	}
	return recordFamily == "ipv4" || recordFamily == "both"
}

func credentialByID(settings Settings, id string) (CloudflareCredentialConfig, bool) {
	for _, credential := range settings.CloudflareCredentials {
		if credential.ID == id {
			return credential, true
		}
	}
	return CloudflareCredentialConfig{}, false
}

func buildCredentialViews(settings Settings) []CloudflareCredentialView {
	views := make([]CloudflareCredentialView, 0, len(settings.CloudflareCredentials))
	for _, credential := range settings.CloudflareCredentials {
		status := "未配置"
		if credential.AuthType == "global_api_key" && credential.APIKey != "" {
			status = "已保存"
		} else if credential.AuthType != "global_api_key" && credential.APIToken != "" {
			status = "已保存"
		}
		views = append(views, CloudflareCredentialView{Config: credential, SecretStatus: status})
	}
	return views
}

func buildDNSTargetViews(settings Settings) []DNSTargetView {
	views := make([]DNSTargetView, 0, len(settings.DNSTargets))
	for _, target := range settings.DNSTargets {
		credentialName := "未选择凭据"
		if credential, ok := credentialByID(settings, target.CredentialID); ok {
			credentialName = credential.Name
		}
		views = append(views, DNSTargetView{Config: target, CredentialName: credentialName, FamilyLabel: recordFamilyLabel(target.RecordFamily)})
	}
	return views
}

func buildManualDNSTargetViews(settings Settings) []ManualDNSTargetView {
	views := make([]ManualDNSTargetView, 0, len(settings.ManualDNSTargets))
	for _, target := range settings.ManualDNSTargets {
		credentialName := "未选择凭据"
		if credential, ok := credentialByID(settings, target.CredentialID); ok {
			credentialName = credential.Name
		}
		views = append(views, ManualDNSTargetView{Config: target, CredentialName: credentialName})
	}
	return views
}

func recordFamilyLabel(family string) string {
	switch normalizeRecordFamily(family) {
	case "ipv4":
		return "仅 IPv4 A"
	case "ipv6":
		return "仅 IPv6 AAAA"
	default:
		return "IPv4 A + IPv6 AAAA"
	}
}

func enabledDNSTargetCount(settings Settings) int {
	count := 0
	for _, target := range settings.DNSTargets {
		if target.Enabled {
			count++
		}
	}
	return count
}

func plannedDNSTargetCount(settings Settings) int {
	count := 0
	for _, target := range settings.DNSTargets {
		if (activeIPv4Count(settings) > 0 && targetSupportsFamily(target, 4)) || (activeIPv6Count(settings) > 0 && targetSupportsFamily(target, 6)) {
			count++
		}
	}
	return count
}

func dnsBindingCounts(settings Settings) (int, int) {
	var ipv4Targets, ipv6Targets int
	for _, target := range settings.DNSTargets {
		if targetSupportsFamily(target, 4) {
			ipv4Targets++
		}
		if targetSupportsFamily(target, 6) {
			ipv6Targets++
		}
	}
	return ipv4Targets, ipv6Targets
}

func requiredDNSRecordCount(settings Settings) int {
	ipv4Targets, ipv6Targets := dnsBindingCounts(settings)
	return activeIPv4Count(settings)*ipv4Targets + activeIPv6Count(settings)*ipv6Targets
}

func dnsTargetSummary(settings Settings) string {
	ipv4Targets, ipv6Targets := dnsBindingCounts(settings)
	return fmt.Sprintf("全部配置：%d 个已启用目标（支持 A：%d，支持 AAAA：%d）", enabledDNSTargetCount(settings), ipv4Targets, ipv6Targets)
}

func runDNSPlanCounts(settings Settings) (ipv4Records, ipv6Records int) {
	ipv4Targets, ipv6Targets := dnsBindingCounts(settings)
	return activeIPv4Count(settings) * ipv4Targets, activeIPv6Count(settings) * ipv6Targets
}

func runDNSPlanSummary(settings Settings) string {
	ipv4Records, ipv6Records := runDNSPlanCounts(settings)
	return fmt.Sprintf("本轮 DNS：%d 个域名目标，%d 条 A，%d 条 AAAA", plannedDNSTargetCount(settings), ipv4Records, ipv6Records)
}

func buildRunPlan(settings Settings) RunPlanView {
	plan := RunPlanView{Available: true}
	ipv4Count := activeIPv4Count(settings)
	ipv6Count := activeIPv6Count(settings)
	if ipv4Count > 0 {
		plan.ScanText = fmt.Sprintf("IPv4：%d 个", ipv4Count)
	} else {
		plan.ScanText = "IPv4：不执行"
	}
	if ipv6Count > 0 {
		plan.ScanText += fmt.Sprintf("；IPv6：%d 个", ipv6Count)
	} else {
		plan.ScanText += "；IPv6：不执行"
	}

	plan.TrueConnectionText = trueConnectionPlanText(settings)
	plan.IPv4RecordCount, plan.IPv6RecordCount = runDNSPlanCounts(settings)
	plan.DNSHeadline = fmt.Sprintf("%d 个域名目标；%d 条 A；%d 条 AAAA", plannedDNSTargetCount(settings), plan.IPv4RecordCount, plan.IPv6RecordCount)
	for _, target := range settings.DNSTargets {
		if !target.Enabled {
			continue
		}
		var records, skipped []string
		if targetSupportsFamily(target, 4) {
			if ipv4Count > 0 {
				records = append(records, fmt.Sprintf("%d 条 A", ipv4Count))
			} else {
				skipped = append(skipped, "IPv4 未启用，不写 A")
			}
		}
		if targetSupportsFamily(target, 6) {
			if ipv6Count > 0 {
				records = append(records, fmt.Sprintf("%d 条 AAAA", ipv6Count))
			} else {
				skipped = append(skipped, "IPv6 未启用，不写 AAAA")
			}
		}
		item := RunPlanTargetView{Name: target.Name, RecordName: target.RecordName}
		if len(records) > 0 {
			item.RecordsText = strings.Join(append(records, skipped...), "；")
			plan.ActiveTargets = append(plan.ActiveTargets, item)
		} else {
			item.Reason = strings.Join(skipped, "；")
			plan.SkippedTargets = append(plan.SkippedTargets, item)
		}
	}
	return plan
}

func trueConnectionPlanText(settings Settings) string {
	var families []string
	if settings.TrueConnectionIPv4 {
		families = append(families, "IPv4")
	}
	if settings.TrueConnectionIPv6 {
		families = append(families, "IPv6")
	}
	if len(families) == 0 {
		return "关闭；入选只依据 Cloudflare 响应、RTT 与带宽"
	}
	var protocols []string
	if settings.TrueConnectionHTTP {
		protocols = append(protocols, "HTTP（7 个端口）")
	}
	if settings.TrueConnectionHTTPS {
		protocols = append(protocols, "HTTPS（6 个端口）")
	}
	familyText := strings.Join(families, " + ")
	if len(families) == 1 {
		familyText = "仅 " + familyText
	}
	return familyText + "；" + strings.Join(protocols, " + ")
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
	s.state.Settings = cloneSettings(next)
	s.applyDefaults()
	return s.saveLocked()
}

func (s *Store) addManualDNSTarget(target ManualDNSTargetConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneSettings(s.state.Settings)
	if len(next.ManualDNSTargets) >= 30 {
		return errors.New("手动 DNS 目标最多允许配置 30 个")
	}
	next.ManualDNSTargets = append(next.ManualDNSTargets, target)
	normalizeDNSConfig(&next)
	if err := validateDNSConfig(next); err != nil {
		return err
	}
	s.state.Settings = next
	return s.saveLocked()
}

func (s *Store) updateManualDNSTargetStats(id string, ipv4Count, ipv6Count int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Settings.ManualDNSTargets {
		if s.state.Settings.ManualDNSTargets[i].ID != id {
			continue
		}
		s.state.Settings.ManualDNSTargets[i].LastIPv4Count = ipv4Count
		s.state.Settings.ManualDNSTargets[i].LastIPv6Count = ipv6Count
		s.state.Settings.ManualDNSTargets[i].LastUpdatedAt = nowString()
		return s.saveLocked()
	}
	return errors.New("手动 DNS 目标不存在")
}

func (s *Store) removeManualDNSTarget(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Settings.ManualDNSTargets {
		if s.state.Settings.ManualDNSTargets[i].ID != id {
			continue
		}
		s.state.Settings.ManualDNSTargets = append(s.state.Settings.ManualDNSTargets[:i], s.state.Settings.ManualDNSTargets[i+1:]...)
		return s.saveLocked()
	}
	return errors.New("手动 DNS 目标不存在")
}

func (s *Store) createRun(trigger string, settings Settings, geoStats GeoFilterStats) (RunRecord, error) {
	return s.createRunWithSearchPlan(trigger, settings, geoStats, nil)
}

func (s *Store) createRunWithSearchPlan(trigger string, settings Settings, geoStats GeoFilterStats, searchPlan []RunSearchFamilyPlan) (RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	configSnapshot := sanitizedRunSettings(settings)
	run := RunRecord{
		ID:                     fmt.Sprintf("%d", time.Now().UnixNano()),
		Trigger:                trigger,
		Status:                 "running",
		Mode:                   "force_refresh",
		Stage:                  "准备执行",
		Progress:               5,
		RequiredIPCount:        requiredIPCount(settings),
		RequiredDNSRecordCount: requiredDNSRecordCount(settings),
		PlannedDNSTargetCount:  plannedDNSTargetCount(settings),
		DNSStatus:              "pending",
		ConfigSnapshot:         &configSnapshot,
		SearchPlanSnapshot:     cloneRunSearchPlan(searchPlan),
		StartedAt:              nowString(),
		Summary:                runSummary(trigger, settings, geoStats),
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

func cloneRunSearchPlan(searchPlan []RunSearchFamilyPlan) []RunSearchFamilyPlan {
	cloned := append([]RunSearchFamilyPlan(nil), searchPlan...)
	for i := range cloned {
		cloned[i].ManualPrefixes = append([]string(nil), searchPlan[i].ManualPrefixes...)
		cloned[i].ManualSeedIPs = append([]string(nil), searchPlan[i].ManualSeedIPs...)
		cloned[i].ManualHintPrefixes = append([]string(nil), searchPlan[i].ManualHintPrefixes...)
	}
	return cloned
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

func (s *Store) updateRunDNSConfirmation(id string, confirmedRecords, confirmedTargets int, targetResults []DNSTargetSyncResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Runs {
		if s.state.Runs[i].ID != id {
			continue
		}
		s.state.Runs[i].SyncedIPCount = confirmedRecords
		s.state.Runs[i].ConfirmedDNSTargetCount = confirmedTargets
		s.state.Runs[i].DNSTargetResults = append([]DNSTargetSyncResult(nil), targetResults...)
		_ = s.saveLocked()
		return
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

func (s *Store) markRunResultsDNSStatus(runID string, confirmed, planned map[string]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Results {
		if s.state.Results[i].RunID == runID {
			ip := s.state.Results[i].IP
			s.state.Results[i].ConfirmedDNSTargets = confirmed[ip]
			s.state.Results[i].PlannedDNSTargets = planned[ip]
			s.state.Results[i].CloudflareSynced = planned[ip] > 0 && confirmed[ip] == planned[ip]
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
	geoStats := calculateGeoFilterStats(geoLocations, settings)
	data := PageData{
		Title:                  title,
		Username:               username,
		Settings:               settings,
		CloudflareCredentials:  buildCredentialViews(settings),
		DNSTargets:             buildDNSTargetViews(settings),
		ManualDNSTargets:       buildManualDNSTargetViews(settings),
		DNSTargetSummary:       dnsTargetSummary(settings),
		ExpectedDNSRecordCount: requiredDNSRecordCount(settings),
		ScheduleSummary:        scheduleSummary(settings),
		LocationSummary:        locationFilterSummaryWithStats(settings, geoStats),
		NextRunAt:              nextRunText(settings),
		GeoLocations:           geoLocations,
		GeoDatabase:            geoDatabase,
		GeoFilterStats:         geoStats,
		FamilyNoResultLimit:    formatDuration(familyNoResultTimeout()),
		AppVersion:             appVersion,
		RepositoryURL:          repositoryURL,
	}
	data.GeoCountries, data.GeoRegions, data.GeoCities = buildGeoChoices(geoLocations, settings)
	if a.searchMemory != nil {
		now := time.Now()
		if id, err := searchmemory.ProfileID(searchProfileForSettings(settings, 4)); err == nil {
			data.SearchMemoryIPv4, _ = a.searchMemory.Summary(context.Background(), id, 4, now)
		}
		if id, err := searchmemory.ProfileID(searchProfileForSettings(settings, 6)); err == nil {
			data.SearchMemoryIPv6, _ = a.searchMemory.Summary(context.Background(), id, 6, now)
		}
		if title == "配置" {
			data.SearchMemoryProfiles = a.searchMemoryProfileViews(settings, now)
		}
	}
	return data
}

func (a *App) searchMemoryProfileViews(settings Settings, now time.Time) []SearchMemoryProfileView {
	current := make(map[string]bool)
	for _, version := range []int{4, 6} {
		if id := a.ensureSearchProfile(settings, version); id != "" {
			current[id] = true
		}
	}
	insights, err := a.searchMemory.ListProfileInsights(context.Background(), now)
	if err != nil {
		log.Printf("list search memory profiles failed: %v", err)
		return nil
	}
	views := make([]SearchMemoryProfileView, 0, len(insights))
	for _, insight := range insights {
		protocols := "基础扫描"
		if insight.Profile.HTTPEnabled && insight.Profile.HTTPSEnabled {
			protocols = "HTTP + HTTPS"
		} else if insight.Profile.HTTPEnabled {
			protocols = "仅 HTTP"
		} else if insight.Profile.HTTPSEnabled {
			protocols = "仅 HTTPS"
		}
		location := strings.ToUpper(strings.TrimSpace(insight.Profile.Country))
		if location == "" {
			location = "全球"
		}
		network := strings.TrimSpace(insight.Profile.NetworkLabel)
		if network == "" {
			network = "未标记出口"
		}
		views = append(views, SearchMemoryProfileView{
			Insight: insight, Current: current[insight.ID], ModeLabel: protocols,
			Label: fmt.Sprintf("IPv%d · %s · %s · %s", insight.Profile.IPVersion, location, protocols, network),
		})
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].Current != views[j].Current {
			return views[i].Current
		}
		return views[i].Insight.LastUsedAt > views[j].Insight.LastUsedAt
	})
	return views
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

func buildGeoChoices(locations []GeoLocation, settings Settings) ([]GeoChoice, []GeoChoice, []GeoChoice) {
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

func calculateGeoFilterStats(locations []GeoLocation, settings Settings) GeoFilterStats {
	stats := GeoFilterStats{}
	var codes []string
	for _, loc := range locations {
		if settings.LocationCountry != "" && !strings.EqualFold(settings.LocationCountry, loc.Country) {
			continue
		}
		if settings.LocationRegion != "" && !strings.EqualFold(settings.LocationRegion, loc.Region) {
			continue
		}
		if settings.LocationCity != "" && !strings.EqualFold(settings.LocationCity, loc.City) {
			continue
		}
		stats.DataCenterCount++
		codes = append(codes, loc.IATA)
	}
	sort.Strings(codes)
	stats.Codes = strings.Join(codes, " / ")
	return stats
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
	if r.Method == http.MethodGet {
		data.Flash = strings.TrimSpace(r.URL.Query().Get("flash"))
		data.Error = strings.TrimSpace(r.URL.Query().Get("error"))
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			data.Error = "配置表单无法解析：" + err.Error()
			a.render(w, settingsTemplate, data)
			return
		}
		next, err := parseDNSConfigForm(r.Form, state.Settings)
		if err != nil {
			data.Error = err.Error()
			a.render(w, settingsTemplate, data)
			return
		}
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
		next.MaxRTTMs = clampInt(parseInt(r.FormValue("max_rtt_ms"), 200), 10, 2000)
		next.TrueConnectionIPv4 = r.FormValue("true_connection_ipv4") == "on"
		next.TrueConnectionIPv6 = r.FormValue("true_connection_ipv6") == "on"
		next.TrueConnectionHTTP = r.FormValue("true_connection_http") == "on"
		next.TrueConnectionHTTPS = r.FormValue("true_connection_https") == "on"
		if submitted := strings.TrimSpace(r.FormValue("true_connection_http_node")); submitted != "" {
			next.TrueConnectionHTTPNode = submitted
		}
		if submitted := strings.TrimSpace(r.FormValue("true_connection_https_node")); submitted != "" {
			next.TrueConnectionHTTPSNode = submitted
		}
		next.TrueConnectionTestURL = strings.TrimSpace(r.FormValue("true_connection_test_url"))
		if next.TrueConnectionTestURL == "" {
			next.TrueConnectionTestURL = "https://www.google.com/generate_204"
		}
		next.SearchNetworkLabel = strings.TrimSpace(r.FormValue("search_network_label"))
		if next.SearchNetworkLabel == "" {
			next.SearchNetworkLabel = "213 VPS"
		}
		next.LocationMode = normalizeLocationMode(r.FormValue("location_mode"))
		next.LocationCountry = strings.ToUpper(strings.TrimSpace(r.FormValue("location_country")))
		next.LocationRegion = strings.TrimSpace(r.FormValue("location_region"))
		next.LocationCity = strings.TrimSpace(r.FormValue("location_city"))
		if next.LocationCountry == "" && next.LocationRegion == "" && next.LocationCity == "" {
			next.LocationMode = "any"
		}
		geoStats := a.geoFilterStats(next)
		if next.LocationMode != "any" && geoStats.DataCenterCount == 0 {
			data = a.pageData("配置", user, settingsForFailedForm(next, state.Settings))
			data.Error = "所选地区在当前 Cloudflare 机房数据库中没有可匹配的实际响应机房，配置尚未保存。本次输入的新密钥也未保存，修正后请重新输入。"
			a.render(w, settingsTemplate, data)
			return
		}
		next.ScheduleEnabled = r.FormValue("schedule_enabled") == "on"
		next.ScheduleMode = normalizeScheduleMode(r.FormValue("schedule_mode"))
		next.ScheduleIntervalDays = clampInt(parseInt(r.FormValue("schedule_interval_days"), 1), 1, 365)
		next.ScheduleTime = strings.TrimSpace(r.FormValue("schedule_time"))
		if err := validateDNSConfig(next); err != nil {
			data = a.pageData("配置", user, settingsForFailedForm(next, state.Settings))
			data.Error = err.Error() + "。配置尚未保存，本次输入的新密钥需在修正后重新输入。"
			a.render(w, settingsTemplate, data)
			return
		}
		if err := a.store.updateSettings(next); err != nil {
			data.Error = err.Error()
			a.render(w, settingsTemplate, data)
			return
		}
		data = a.pageData("配置", user, next)
		data.Flash = "配置已保存；后续立即执行和定时任务将使用：" + data.LocationSummary
	}
	a.render(w, settingsTemplate, data)
}

func redirectSettingsMessage(w http.ResponseWriter, r *http.Request, flash string, err error) {
	query := url.Values{}
	if err != nil {
		query.Set("error", err.Error())
	} else {
		query.Set("flash", flash)
	}
	http.Redirect(w, r, "/settings?"+query.Encode(), http.StatusSeeOther)
}

func (a *App) addSearchMemoryPrefix(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		redirectSettingsMessage(w, r, "", err)
		return
	}
	profileID := strings.TrimSpace(r.FormValue("profile_id"))
	version := parseInt(r.FormValue("ip_version"), 0)
	prefix, err := a.searchMemory.AddManualPrefix(r.Context(), profileID, version, r.FormValue("prefix"))
	if err != nil {
		redirectSettingsMessage(w, r, "", err)
		return
	}
	redirectSettingsMessage(w, r, "已加入手动优先范围 "+prefix+"；如果输入含主机地址，系统会保存种子 IP，先精确复测并从对应窄网段深挖。", nil)
}

func (a *App) deleteSearchMemoryPrefix(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		redirectSettingsMessage(w, r, "", err)
		return
	}
	if err := a.searchMemory.RemoveManualPrefix(r.Context(), strings.TrimSpace(r.FormValue("profile_id")), r.FormValue("prefix")); err != nil {
		redirectSettingsMessage(w, r, "", err)
		return
	}
	redirectSettingsMessage(w, r, "手动优先网段已移除。", nil)
}

func (a *App) clearSearchMemoryProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		redirectSettingsMessage(w, r, "", err)
		return
	}
	if err := a.searchMemory.ClearProfile(r.Context(), strings.TrimSpace(r.FormValue("profile_id"))); err != nil {
		redirectSettingsMessage(w, r, "", err)
		return
	}
	redirectSettingsMessage(w, r, "该搜索记忆配置档案、手动网段和端口统计已全部清除。", nil)
}

func manualDNSTargetByID(settings Settings, id string) (ManualDNSTargetConfig, bool) {
	for _, target := range settings.ManualDNSTargets {
		if target.ID == id {
			return target, true
		}
	}
	return ManualDNSTargetConfig{}, false
}

func (a *App) addManualDNSTarget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	if err := r.ParseForm(); err != nil {
		redirectSettingsMessage(w, r, "", fmt.Errorf("手动 DNS 目标表单无法解析：%w", err))
		return
	}
	target := ManualDNSTargetConfig{
		Name:         strings.TrimSpace(r.FormValue("manual_name")),
		RootDomain:   normalizeDomain(r.FormValue("manual_root_domain")),
		ZoneID:       strings.TrimSpace(r.FormValue("manual_zone_id")),
		RecordName:   normalizeDomain(r.FormValue("manual_record_name")),
		CredentialID: strings.TrimSpace(r.FormValue("manual_credential_id")),
	}
	if err := a.store.addManualDNSTarget(target); err != nil {
		redirectSettingsMessage(w, r, "", err)
		return
	}
	redirectSettingsMessage(w, r, "手动 DNS 目标已添加。它不会参与自动扫描、定时任务或自动 DNS 更新。", nil)
}

func (a *App) updateManualDNSTarget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2*1024*1024)
	if err := r.ParseForm(); err != nil {
		redirectSettingsMessage(w, r, "", fmt.Errorf("导入内容无法解析：%w", err))
		return
	}
	settings := a.store.snapshot().Settings
	target, ok := manualDNSTargetByID(settings, strings.TrimSpace(r.FormValue("target_id")))
	if !ok {
		redirectSettingsMessage(w, r, "", errors.New("手动 DNS 目标不存在，请刷新页面"))
		return
	}
	shareLinks := r.FormValue("share_links")
	if strings.TrimSpace(shareLinks) == "" {
		shareLinks = r.FormValue("vmess_links") // 兼容已打开旧页面提交的字段名。
	}
	ipv4, ipv6, err := parseShareLinkIPList(shareLinks)
	if err != nil {
		redirectSettingsMessage(w, r, "", err)
		return
	}
	client := &http.Client{Timeout: 30 * time.Second}
	if err := replaceManualDNSRecords(client, settings, target, ipv4, ipv6, func(message string) { log.Printf("manual dns: %s", message) }); err != nil {
		redirectSettingsMessage(w, r, "", err)
		return
	}
	if err := a.store.updateManualDNSTargetStats(target.ID, len(ipv4), len(ipv6)); err != nil {
		redirectSettingsMessage(w, r, "", fmt.Errorf("DNS 已更新，但本地统计保存失败：%w", err))
		return
	}
	redirectSettingsMessage(w, r, fmt.Sprintf("手动目标 %s 已更新：IPv4 A %d 条，IPv6 AAAA %d 条。", target.RecordName, len(ipv4), len(ipv6)), nil)
}

func (a *App) clearManualDNSTarget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	settings := a.store.snapshot().Settings
	target, ok := manualDNSTargetByID(settings, strings.TrimSpace(r.FormValue("target_id")))
	if !ok {
		redirectSettingsMessage(w, r, "", errors.New("手动 DNS 目标不存在，请刷新页面"))
		return
	}
	client := &http.Client{Timeout: 30 * time.Second}
	if err := replaceManualDNSRecords(client, settings, target, nil, nil, func(message string) { log.Printf("manual dns: %s", message) }); err != nil {
		redirectSettingsMessage(w, r, "", err)
		return
	}
	if err := a.store.updateManualDNSTargetStats(target.ID, 0, 0); err != nil {
		redirectSettingsMessage(w, r, "", fmt.Errorf("DNS 已清空，但本地统计保存失败：%w", err))
		return
	}
	redirectSettingsMessage(w, r, "已删除 "+target.RecordName+" 下的全部 IPv4 A 和 IPv6 AAAA 记录；手动目标配置已保留，可随时再次导入。", nil)
}

func (a *App) deleteManualDNSTarget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	settings := a.store.snapshot().Settings
	target, ok := manualDNSTargetByID(settings, strings.TrimSpace(r.FormValue("target_id")))
	if !ok {
		redirectSettingsMessage(w, r, "", errors.New("手动 DNS 目标不存在，请刷新页面"))
		return
	}
	client := &http.Client{Timeout: 30 * time.Second}
	if err := replaceManualDNSRecords(client, settings, target, nil, nil, func(message string) { log.Printf("manual dns: %s", message) }); err != nil {
		redirectSettingsMessage(w, r, "", fmt.Errorf("未删除本地目标：先清空 Cloudflare A/AAAA 失败：%w", err))
		return
	}
	if err := a.store.removeManualDNSTarget(target.ID); err != nil {
		redirectSettingsMessage(w, r, "", fmt.Errorf("Cloudflare A/AAAA 已清空，但本地目标移除失败：%w", err))
		return
	}
	redirectSettingsMessage(w, r, "已清空 "+target.RecordName+" 的全部 A/AAAA 记录，并删除手动目标配置。", nil)
}

func (a *App) testSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/settings", http.StatusFound)
		return
	}
	state := a.store.snapshot()
	user, _ := a.currentUser(r)
	data := a.pageData("配置", user, state.Settings)
	if err := validateDNSConfig(state.Settings); err != nil {
		data.Error = "DNS 配置尚不可测试：" + err.Error()
		a.render(w, settingsTemplate, data)
		return
	}
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
	if running := currentRun(state.Runs); running != nil {
		data.RecentRuns = []RunRecord{*running}
	}
	data.HasRunningRun = hasRunningRun(state.Runs)
	data.Stats = buildDashboardStats(state)
	data.CurrentRun = currentRun(state.Runs)
	data.LatestRun = latestRun(state.Runs)
	data.CanResumeRun = canResumeRun(state)
	a.render(w, runTemplate, data)
}

func (a *App) historyPage(w http.ResponseWriter, r *http.Request) {
	state := a.store.snapshot()
	user, _ := a.currentUser(r)
	data := a.pageData("任务历史", user, state.Settings)
	data.RecentRuns = recentRuns(state.Runs, 100)
	data.HasRunningRun = hasRunningRun(state.Runs)
	a.render(w, historyTemplate, data)
}

func (a *App) runDetailPage(w http.ResponseWriter, r *http.Request) {
	state := a.store.snapshot()
	user, _ := a.currentUser(r)
	data := a.pageData("任务详情", user, state.Settings)
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	for _, run := range state.Runs {
		if run.ID == id {
			decorateRunPlan(&run)
			data.RecentRuns = []RunRecord{run}
			break
		}
	}
	if len(data.RecentRuns) == 0 {
		http.NotFound(w, r)
		return
	}
	data.HasRunningRun = data.RecentRuns[0].Status == "running"
	a.render(w, runDetailTemplate, data)
}

func (a *App) resultsPage(w http.ResponseWriter, r *http.Request) {
	state := a.store.snapshot()
	user, _ := a.currentUser(r)
	data := a.pageData("IP 结果", user, state.Settings)
	fillResultPanels(&data, state)
	a.render(w, resultsPageTemplate, data)
}

func (a *App) startRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/run", http.StatusFound)
		return
	}
	state := a.store.snapshot()
	if !isConfigReady(state.Settings) {
		user, _ := a.currentUser(r)
		data := a.pageData("配置", user, state.Settings)
		data.Error = "当前配置尚不能执行任务：" + configHint(state.Settings)
		a.render(w, settingsTemplate, data)
		return
	}
	if hasRunningRun(state.Runs) {
		http.Redirect(w, r, "/run", http.StatusFound)
		return
	}
	geoStats := a.geoFilterStats(state.Settings)
	searchPlan := a.buildRunSearchPlanSnapshot(r.Context(), state.Settings)
	run, err := a.store.createRunWithSearchPlan("manual", state.Settings, geoStats, searchPlan)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go a.executeRun(run.ID, state.Settings, "manual")
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
	resumeSettings := cloneSettings(state.Settings)
	if source.ConfigSnapshot != nil {
		resumeSettings = hydrateRunSettings(*source.ConfigSnapshot, state.Settings)
	}
	if !isConfigReady(resumeSettings) {
		user, _ := a.currentUser(r)
		data := a.pageData("配置", user, state.Settings)
		data.Error = "无法按原任务冻结配置续接：" + configHint(resumeSettings) + "。如果凭据已删除或更换，请先恢复同 ID 凭据。"
		a.render(w, settingsTemplate, data)
		return
	}
	geoStats := a.geoFilterStats(resumeSettings)
	searchPlan := a.buildRunSearchPlanSnapshot(r.Context(), resumeSettings)
	run, err := a.store.createRunWithSearchPlan("resume", resumeSettings, geoStats, searchPlan)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go a.executeRunWithSeed(run.ID, resumeSettings, "resume", source.ID, seed)
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

func (a *App) executeRun(id string, settings Settings, trigger string) {
	a.executeRunWithSeed(id, settings, trigger, "", nil)
}

func (a *App) executeRunWithSeed(id string, settings Settings, trigger, sourceRunID string, seed []IPTestResult) {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout())
	a.tasks.register(id, cancel)
	defer func() {
		cancel()
		a.tasks.unregister(id)
	}()

	required := requiredIPCount(settings)
	a.store.updateRunProgress(id, "读取配置", 5, 0, 0, "pending")
	geoStats := a.geoFilterStats(settings)
	a.store.appendRunLog(id, "info", "开始真实执行："+runSummary(trigger, settings, geoStats))
	a.store.appendRunLog(id, "info", "本次地区策略："+locationFilterSummaryWithStats(settings, geoStats))
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
			item.ConfirmedDNSTargets = 0
			item.PlannedDNSTargets = 0
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
	var familyFailures []string
	if ipv4TargetCount > 0 {
		remaining := ipv4TargetCount - existingV4
		if remaining > 0 {
			a.store.appendRunLog(id, "info", fmt.Sprintf("开始 IPv4 扫描：还需 %d 个，总目标 %d 个。", remaining, ipv4TargetCount))
			v4, err := a.collectFamilyResults(ctx, id, settings, 4, remaining, seen, len(results), required)
			results = append(results, v4...)
			if err != nil {
				if shouldAbortWholeRun(ctx, err) {
					a.finishRunFromError(id, err)
					return
				}
				familyFailures = append(familyFailures, err.Error())
				nextAction := "该任务未启用 IPv6，将统一判定最终状态"
				if ipv6TargetCount > 0 {
					nextAction = "继续执行 IPv6"
				}
				a.store.appendRunLog(id, "warn", fmt.Sprintf("IPv4 未收集满 %d 个，已保留当前 %d 个结果；不提前结束整个任务，%s。原因：%s", ipv4TargetCount, existingV4+len(v4), nextAction, err))
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
				if shouldAbortWholeRun(ctx, err) {
					a.finishRunFromError(id, err)
					return
				}
				familyFailures = append(familyFailures, err.Error())
				a.store.appendRunLog(id, "warn", fmt.Sprintf("IPv6 未收集满 %d 个，已保留当前 %d 个结果。原因：%s", ipv6TargetCount, existingV6+len(v6), err))
			}
		} else {
			a.store.appendRunLog(id, "info", fmt.Sprintf("IPv6 已满足：%d/%d。", existingV6, ipv6TargetCount))
		}
	} else {
		a.store.appendRunLog(id, "info", "IPv6 未启用或数量为 0，跳过 IPv6 扫描与 AAAA 记录同步。")
	}

	if len(results) != required {
		summary := fmt.Sprintf("所有已启用协议族已全部执行；IPv4 %d/%d，IPv6 %d/%d，共 %d/%d。数量不足，未执行 DNS 更新。",
			countFamily(results, 4), ipv4TargetCount, countFamily(results, 6), ipv6TargetCount, len(results), required)
		if len(familyFailures) > 0 {
			summary += " 失败原因：" + strings.Join(familyFailures, " | ")
		}
		a.store.finishRun(id, "failed", summary)
		return
	}

	a.store.updateRunProgress(id, "准备同步 DNS", 82, len(results), 0, "pending")
	a.store.appendRunLog(id, "info", "扫描结果已全部保存，开始一次性替换 Cloudflare DNS。")
	report, err := syncResultsToCloudflare(settings, results, func(message string) {
		a.store.appendRunLog(id, "info", message)
	})
	if err != nil {
		dnsStatus := "failed"
		stage := "DNS 同步失败"
		if report.ConfirmedRecords > 0 {
			dnsStatus = "partial"
			stage = "DNS 部分同步"
		}
		a.store.updateRunProgress(id, stage, 92, len(results), report.ConfirmedRecords, dnsStatus)
		a.store.updateRunDNSConfirmation(id, report.ConfirmedRecords, report.ConfirmedTargets, report.TargetResults)
		a.store.markRunResultsDNSStatus(id, report.ConfirmedByIP, report.PlannedByIP)
		a.store.finishRun(id, "failed", fmt.Sprintf("扫描完成，但 DNS 仅确认 %d/%d 个目标、%d/%d 条记录：%s", report.ConfirmedTargets, report.TotalTargets, report.ConfirmedRecords, requiredDNSRecordCount(settings), err))
		return
	}
	a.store.updateRunProgress(id, "DNS 已确认", 100, len(results), report.ConfirmedRecords, "confirmed")
	a.store.updateRunDNSConfirmation(id, report.ConfirmedRecords, report.ConfirmedTargets, report.TargetResults)
	a.store.markRunResultsDNSStatus(id, report.ConfirmedByIP, report.PlannedByIP)
	a.store.finishRun(id, "succeeded", fmt.Sprintf("完成：扫描 %d 个优选 IP，确认 %d/%d 个 DNS 目标、%d 条 Cloudflare DNS 记录。", len(results), report.ConfirmedTargets, report.TotalTargets, report.ConfirmedRecords))
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

func shouldAbortWholeRun(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled)
}

func (a *App) schedulerLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		state := a.store.snapshot()
		if !shouldStartScheduledRun(state, time.Now()) {
			continue
		}
		geoStats := a.geoFilterStats(state.Settings)
		searchPlan := a.buildRunSearchPlanSnapshot(context.Background(), state.Settings)
		run, err := a.store.createRunWithSearchPlan("scheduled", state.Settings, geoStats, searchPlan)
		if err != nil {
			log.Printf("scheduled run create failed: %v", err)
			continue
		}
		go a.executeRun(run.ID, state.Settings, "scheduled")
	}
}

func (a *App) collectFamilyResults(ctx context.Context, id string, settings Settings, ipVersion, targetCount int, seen map[string]bool, existingCount, requiredCount int) ([]IPTestResult, error) {
	results := make([]IPTestResult, 0, targetCount)
	noResultTimeout := familyNoResultTimeout()
	lastResultAt := time.Now()
	profileID := a.ensureSearchProfile(settings, ipVersion)
	memory := searchmemory.CandidateMemory{}
	failedPrefixesThisRun := make(map[string]int)
	var manualSeedIPsPending []string
	if profileID != "" {
		if loaded, err := a.searchMemory.Candidates(ctx, profileID, ipVersion, time.Now()); err == nil {
			memory = loaded
			manualSeedIPsPending = append([]string(nil), memory.ManualSeedIPs...)
			a.store.appendRunLog(id, "info", fmt.Sprintf("搜索记忆已加载：可复测的真连接/带宽候选 IP %d 个、实测地区优先网段 %d 个（手动 %d 个）、冷却 IP %d 个、冷却网段 %d 个；本轮预算 精确:%d%% /24或/48:%d%% /16或/32:%d%% 全局:%d%%。", len(memory.SuccessIPs), len(memory.HintPrefixes), len(memory.ManualPrefixes), len(memory.ExcludeIPs), len(memory.ExcludePrefixes), memory.Budget.Exact, memory.Budget.Narrow, memory.Budget.Wide, memory.Budget.Global))
			if len(memory.ManualPrefixes) > 0 {
				a.store.appendRunLog(id, "info", fmt.Sprintf("手动优先执行契约：父网段 %s；种子 IP %s；推导范围 %s。先精确复测种子，随后每批至少 40%% 候选来自手动范围；手动范围覆盖自动冷却，但仍须通过地区、RTT、带宽和真连接。", strings.Join(memory.ManualPrefixes, "、"), firstNonEmpty(strings.Join(memory.ManualSeedIPs, "、"), "未保存"), strings.Join(memory.ManualHintPrefixes, "、")))
			}
		} else {
			a.store.appendRunLog(id, "warn", "搜索记忆读取失败，本轮退回无记忆扫描："+err.Error())
		}
	}
	for attempt := 1; len(results) < targetCount; attempt++ {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		stage := fmt.Sprintf("扫描 IPv%d %d/%d", ipVersion, len(results)+1, targetCount)
		progress := 10 + ((existingCount + len(results)) * 65 / requiredCount)
		a.store.updateRunProgress(id, stage, progress, existingCount+len(results), 0, "pending")
		a.store.appendRunLog(id, "info", fmt.Sprintf("IPv%d 第 %d 次尝试，目标收集 %d/%d。", ipVersion, attempt, len(results), targetCount))
		hintSubnets := append([]string(nil), memory.HintPrefixes...)
		if len(hintSubnets) == 0 || !trueConnectionEnabledForFamily(settings, ipVersion) {
			hintSubnets = append(hintSubnets, a.regionalHintSubnets(settings, ipVersion)...)
		}
		historyIPs := append([]string(nil), memory.SuccessIPs...)
		if !trueConnectionEnabledForFamily(settings, ipVersion) {
			historyIPs = append(historyIPs, a.regionalHistoryIPs(settings, ipVersion, seen)...)
		}
		historyIPs = filterUnseenIPs(historyIPs, seen)
		if attempt == 1 && len(hintSubnets) > 0 {
			a.store.appendRunLog(id, "info", fmt.Sprintf("两阶段搜索开始：先复测 %d 个 IPv%d 历史候选，并按 %d 个实测线索从父网段 /16（IPv6 /32）广泛探索；一旦命中实际机房，再收窄到 /24（IPv6 /48）深挖。带宽候选合格后才进入 HTTP/HTTPS 真连接。", len(historyIPs), ipVersion, len(hintSubnets)))
		}

		resultDeadline := lastResultAt.Add(noResultTimeout)
		attemptCtx, cancel := context.WithDeadline(ctx, resultDeadline)
		result, output, err := runBetterIPScan(attemptCtx, settings, ipVersion, manualSeedIPsPending, memory.ManualHintPrefixes, historyIPs, hintSubnets, memory.ExcludeIPs, memory.ExcludePrefixes, memory.Budget, func(message string) {
			a.store.appendRunLog(id, "info", message)
		})
		manualSeedIPsPending = nil
		cancel()
		stageObservations := parseScannerStageObservations(output)
		learnedHints, stageCounts := a.recordScannerStageObservations(id, profileID, settings, ipVersion, historyIPs, hintSubnets, stageObservations)
		memory.HintPrefixes = appendUniqueStrings(memory.HintPrefixes, learnedHints...)
		if stageCounts["region_match"] > 0 {
			a.store.appendRunLog(id, "info", fmt.Sprintf("阶段记忆已保存：实测地区命中 %d 个，带宽达标 %d 个、未达标 %d 个；已将对应 /24 与 /16（IPv6 为 /48 与 /32）加入后续优先范围。", stageCounts["region_match"], stageCounts["bandwidth_pass"], stageCounts["bandwidth_fail"]))
		}
		visibleOutput := stripScannerObservationLines(output)
		if visibleOutput != "" {
			a.store.appendRunLog(id, "info", trimForLog(visibleOutput, 1200))
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return results, ctxErr
			}
			if errors.Is(err, context.Canceled) {
				return results, err
			}
			if errors.Is(err, context.DeadlineExceeded) || time.Now().After(resultDeadline) {
				detail := "请检查 VPS 的该协议族连通性、最大 RTT 和带宽门槛"
				if normalizeLocationMode(settings.LocationMode) != "any" {
					detail = "本任务按 CF-RAY 筛选实际响应机房；当前 VPS 路由可能无法到达所选机房，也可能是 RTT 或带宽未达标"
				}
				return results, fmt.Errorf("IPv%d 连续 %s 没有新增有效 IP，已停止该协议族并让整体任务继续；%s", ipVersion, formatDuration(noResultTimeout), detail)
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
		result.CandidateSource = candidateSourceForIP(result.IP, historyIPs, hintSubnets, ipVersion)
		if trueConnectionEnabledForFamily(settings, ipVersion) {
			a.store.appendRunLog(id, "info", fmt.Sprintf("阶段 1 已完成：%s 通过地区、RTT 与带宽门槛，已进入带宽候选池。开始阶段 2 真连接验证；将完整检测所选协议的全部 Cloudflare 端口。", result.IP))
			ports, portAttempts, trueErr := runTrueConnectionTests(ctx, settings, result.IP)
			if trueErr != nil {
				return results, fmt.Errorf("IPv%d 真连接测试无法继续：%w", ipVersion, trueErr)
			}
			a.store.appendRunLog(id, "info", "端口诊断："+formatTrueConnectionAttempts(portAttempts))
			result.TrueConnectionTested = true
			result.TrueConnectionPorts = ports
			outcome := "true_success"
			errorClass := ""
			if len(ports) == 0 {
				outcome = "true_failure"
				errorClass = dominantTrueConnectionError(portAttempts)
			}
			a.recordSearchObservation(id, profileID, result, outcome, errorClass, portAttempts)
			if len(ports) == 0 {
				memory.ExcludeIPs = appendUniqueStrings(memory.ExcludeIPs, result.IP)
				if hints := resultHintPrefixes(result.IP, ipVersion); len(hints) > 0 {
					failedPrefixesThisRun[hints[0]]++
					if failedPrefixesThisRun[hints[0]] >= 8 {
						memory.ExcludePrefixes = appendUniqueStrings(memory.ExcludePrefixes, hints[0])
						a.store.appendRunLog(id, "warn", "当前任务同一网段连续真连接失败，已把该网段加入冷却："+hints[0])
					}
				}
				a.store.appendRunLog(id, "warn", fmt.Sprintf("淘汰 %s：HTTP/HTTPS 真连接所选端口全部无有效响应，继续扫描下一个候选 IP。", result.IP))
				continue
			}
			memory.HintPrefixes = appendUniqueStrings(memory.HintPrefixes, resultHintPrefixes(result.IP, ipVersion)...)
			a.store.appendRunLog(id, "info", fmt.Sprintf("真连接通过 %s：%s。", result.IP, formatTrueConnectionPorts(ports)))
		}
		if !trueConnectionEnabledForFamily(settings, ipVersion) {
			a.recordSearchObservation(id, profileID, result, "scan_success", "", nil)
		}
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

func (a *App) regionalHintSubnets(settings Settings, ipVersion int) []string {
	if normalizeLocationMode(settings.LocationMode) == "any" || locationSelectionText(settings) == "" {
		return nil
	}
	state := a.store.snapshot()
	seen := make(map[string]bool)
	result := make([]string, 0, 20)
	for _, item := range state.Results {
		if item.IPVersion != ipVersion || !resultMatchesLocation(item, settings) {
			continue
		}
		addr, err := netip.ParseAddr(item.IP)
		if err != nil || (ipVersion == 4) != addr.Is4() {
			continue
		}
		bits := 48
		if ipVersion == 4 {
			bits = 24
		}
		subnet := netip.PrefixFrom(addr, bits).Masked().String()
		if seen[subnet] {
			continue
		}
		seen[subnet] = true
		result = append(result, subnet)
		if len(result) >= 20 {
			break
		}
	}
	return result
}

func (a *App) regionalHistoryIPs(settings Settings, ipVersion int, excluded map[string]bool) []string {
	if normalizeLocationMode(settings.LocationMode) == "any" || locationSelectionText(settings) == "" {
		return nil
	}
	state := a.store.snapshot()
	seen := make(map[string]bool)
	result := make([]string, 0, 50)
	for _, item := range state.Results {
		if item.IPVersion != ipVersion || excluded[item.IP] || !resultMatchesLocation(item, settings) {
			continue
		}
		addr, err := netip.ParseAddr(item.IP)
		if err != nil || (ipVersion == 4) != addr.Is4() {
			continue
		}
		value := addr.String()
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
		if len(result) >= 50 {
			break
		}
	}
	return result
}

func resultMatchesLocation(item IPTestResult, settings Settings) bool {
	if settings.LocationCountry != "" && !strings.EqualFold(settings.LocationCountry, item.DataCenterCountry) {
		return false
	}
	if settings.LocationRegion != "" && !strings.EqualFold(settings.LocationRegion, item.DataCenterRegion) {
		return false
	}
	if settings.LocationCity != "" && !strings.EqualFold(settings.LocationCity, item.DataCenterCity) {
		return false
	}
	return true
}

func runBetterIPScan(ctx context.Context, settings Settings, ipVersion int, manualSeedIPs, manualHintSubnets, historyIPs, hintSubnets, excludeIPs, excludeSubnets []string, budget searchmemory.CandidateBudget, onLog func(string)) (IPTestResult, string, error) {
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
		"BETTER_CF_MAX_RTT_MS="+strconv.Itoa(settings.MaxRTTMs),
		"BETTER_CF_LOCATION_MODE="+normalizeLocationMode(settings.LocationMode),
		"BETTER_CF_LOCATION_COUNTRY="+strings.TrimSpace(settings.LocationCountry),
		"BETTER_CF_LOCATION_REGION="+strings.TrimSpace(settings.LocationRegion),
		"BETTER_CF_LOCATION_CITY="+strings.TrimSpace(settings.LocationCity),
		"BETTER_CF_HINT_IPS="+strings.Join(historyIPs, ","),
		"BETTER_CF_HINT_SUBNETS="+strings.Join(hintSubnets, ","),
		"BETTER_CF_MANUAL_HINT_IPS="+strings.Join(manualSeedIPs, ","),
		"BETTER_CF_MANUAL_HINT_SUBNETS="+strings.Join(manualHintSubnets, ","),
		"BETTER_CF_EXCLUDE_IPS="+strings.Join(excludeIPs, ","),
		"BETTER_CF_EXCLUDE_SUBNETS="+strings.Join(excludeSubnets, ","),
		fmt.Sprintf("BETTER_CF_BUDGET_EXACT=%d", budget.Exact),
		fmt.Sprintf("BETTER_CF_BUDGET_NARROW=%d", budget.Narrow),
		fmt.Sprintf("BETTER_CF_BUDGET_WIDE=%d", budget.Wide),
		fmt.Sprintf("BETTER_CF_BUDGET_GLOBAL=%d", budget.Global),
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
				delta := stripScannerObservationLines(output[lastLoggedLen:])
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

func parseScannerStageObservations(output string) []scannerStageObservation {
	seen := make(map[string]bool)
	var result []scannerStageObservation
	for _, line := range strings.Split(output, "\n") {
		index := strings.Index(line, scannerObservationPrefix)
		if index < 0 {
			continue
		}
		raw := strings.TrimSpace(line[index+len(scannerObservationPrefix):])
		var item scannerStageObservation
		if json.Unmarshal([]byte(raw), &item) != nil || item.IP == "" || item.IPVersion == 0 {
			continue
		}
		switch item.Stage {
		case "region_match", "bandwidth_pass", "bandwidth_fail":
		default:
			continue
		}
		key := item.Stage + "|" + item.IP
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, item)
	}
	return result
}

func stripScannerObservationLines(output string) string {
	if !strings.Contains(output, scannerObservationPrefix) {
		return output
	}
	lines := strings.Split(output, "\n")
	visible := lines[:0]
	for _, line := range lines {
		if index := strings.Index(line, scannerObservationPrefix); index >= 0 {
			line = strings.TrimSpace(line[:index])
		}
		if strings.TrimSpace(line) != "" {
			visible = append(visible, line)
		}
	}
	return strings.Join(visible, "\n")
}

func (a *App) recordScannerStageObservations(runID, profileID string, settings Settings, ipVersion int, exactIPs, hints []string, items []scannerStageObservation) ([]string, map[string]int) {
	counts := map[string]int{"region_match": 0, "bandwidth_pass": 0, "bandwidth_fail": 0}
	var learnedHints []string
	for _, item := range items {
		if item.IPVersion != ipVersion {
			continue
		}
		candidate := IPTestResult{
			RunID: runID, IP: item.IP, IPVersion: ipVersion,
			DataCenterCode: item.DataCenterCode, DataCenterCountry: item.DataCenterCountry,
			DataCenterRegion: item.DataCenterRegion, DataCenterCity: item.DataCenterCity,
			RTTMs: item.RTTMs, PeakSpeedKBps: item.PeakSpeedKBps,
			MeasuredBandwidthMbps: item.PeakSpeedKBps / 128,
			CandidateSource:       candidateSourceForIP(item.IP, exactIPs, hints, ipVersion),
		}
		// “地区优先”回退全局后可能出现非目标机房，这些结果不能污染目标地区记忆。
		if normalizeLocationMode(settings.LocationMode) != "any" && !resultMatchesLocation(candidate, settings) {
			continue
		}
		a.recordSearchObservation(runID, profileID, candidate, item.Stage, "", nil)
		learnedHints = appendUniqueStrings(learnedHints, resultHintPrefixes(item.IP, ipVersion)...)
		counts[item.Stage]++
	}
	return learnedHints, counts
}

var (
	trueConnectionHTTPPorts   = []int{80, 8080, 8880, 2052, 2082, 2086, 2095}
	trueConnectionHTTPSPorts  = []int{443, 2053, 2083, 2087, 2096, 8443}
	trueConnectionLocalPortMu sync.Mutex
	trueConnectionLocalPorts  = make(map[int]bool)
)

type trueConnectionNode struct {
	Protocol        string
	OriginalAddress string
	ID              string
	AlterID         int
	Cipher          string
	Encryption      string
	Flow            string
	Network         string
	Host            string
	Path            string
	SNI             string
	Fingerprint     string
	ALPN            []string
	TLS             bool
	AllowInsecure   bool
}

type vmessShare struct {
	Address       string      `json:"add"`
	ID            string      `json:"id"`
	AlterID       interface{} `json:"aid"`
	Cipher        string      `json:"scy"`
	Network       string      `json:"net"`
	Host          string      `json:"host"`
	Path          string      `json:"path"`
	TLS           string      `json:"tls"`
	SNI           string      `json:"sni"`
	Fingerprint   string      `json:"fp"`
	ALPN          string      `json:"alpn"`
	AllowInsecure interface{} `json:"allowInsecure"`
}

func validateTrueConnectionSettings(settings Settings) error {
	enabled := settings.TrueConnectionIPv4 || settings.TrueConnectionIPv6
	if !enabled {
		return nil
	}
	if !settings.TrueConnectionHTTP && !settings.TrueConnectionHTTPS {
		return errors.New("已启用真连接测试，请至少勾选 HTTP 或 HTTPS")
	}
	if settings.TrueConnectionIPv4 && !settings.IPv4Enabled {
		return errors.New("已启用 IPv4 真连接测试，但 IPv4 扫描没有启用")
	}
	if settings.TrueConnectionIPv6 && !settings.IPv6Enabled {
		return errors.New("已启用 IPv6 真连接测试，但 IPv6 扫描没有启用")
	}
	if settings.TrueConnectionHTTP {
		if _, err := parseTrueConnectionNode(settings.TrueConnectionHTTPNode, false); err != nil {
			return fmt.Errorf("HTTP 真连接节点不可用：%w", err)
		}
	}
	if settings.TrueConnectionHTTPS {
		if _, err := parseTrueConnectionNode(settings.TrueConnectionHTTPSNode, true); err != nil {
			return fmt.Errorf("HTTPS 真连接节点不可用：%w", err)
		}
	}
	testURL, err := url.Parse(strings.TrimSpace(settings.TrueConnectionTestURL))
	if err != nil || testURL.Hostname() == "" || (testURL.Scheme != "http" && testURL.Scheme != "https") {
		return errors.New("真连接访问地址必须是完整的 http:// 或 https:// URL")
	}
	return nil
}

func parseTrueConnectionNode(raw string, expectTLS bool) (trueConnectionNode, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(raw), "vless://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.User == nil || strings.TrimSpace(parsed.User.Username()) == "" {
			return trueConnectionNode{}, errors.New("无法解析 vless:// 节点或缺少 UUID")
		}
		query := parsed.Query()
		security := strings.ToLower(strings.TrimSpace(query.Get("security")))
		node := trueConnectionNode{
			Protocol:        "vless",
			OriginalAddress: strings.TrimSpace(parsed.Hostname()),
			ID:              strings.TrimSpace(parsed.User.Username()),
			Encryption:      firstNonEmpty(query.Get("encryption"), "none"),
			Flow:            strings.TrimSpace(query.Get("flow")),
			Network:         normalizeTrueConnectionNetwork(query.Get("type")),
			Host:            strings.TrimSpace(query.Get("host")),
			Path:            strings.TrimSpace(query.Get("path")),
			SNI:             strings.TrimSpace(query.Get("sni")),
			Fingerprint:     strings.TrimSpace(query.Get("fp")),
			ALPN:            splitCommaValues(query.Get("alpn")),
			TLS:             security == "tls",
			AllowInsecure:   parseLooseBool(firstNonEmpty(query.Get("allowInsecure"), query.Get("insecure"))),
		}
		if node.Network == "" {
			return trueConnectionNode{}, errors.New("目前只支持 WS 或 TCP 传输")
		}
		if node.TLS != expectTLS {
			return trueConnectionNode{}, fmt.Errorf("节点是 %s，请提供一条 %s 节点", tlsLabel(node.TLS), tlsLabel(expectTLS))
		}
		return node, nil
	}
	if strings.HasPrefix(strings.ToLower(raw), "vmess://") {
		encoded := strings.TrimSpace(raw[len("vmess://"):])
		decoded, err := decodeBase64String(encoded)
		if err != nil {
			return trueConnectionNode{}, errors.New("vmess:// Base64 内容无法解析")
		}
		var share vmessShare
		if err := json.Unmarshal(decoded, &share); err != nil || strings.TrimSpace(share.ID) == "" {
			return trueConnectionNode{}, errors.New("vmess:// JSON 无效或缺少 UUID")
		}
		node := trueConnectionNode{
			Protocol:        "vmess",
			OriginalAddress: strings.TrimSpace(share.Address),
			ID:              strings.TrimSpace(share.ID),
			AlterID:         interfaceInt(share.AlterID),
			Cipher:          firstNonEmpty(strings.TrimSpace(share.Cipher), "auto"),
			Network:         normalizeTrueConnectionNetwork(share.Network),
			Host:            strings.TrimSpace(share.Host),
			Path:            strings.TrimSpace(share.Path),
			SNI:             strings.TrimSpace(share.SNI),
			Fingerprint:     strings.TrimSpace(share.Fingerprint),
			ALPN:            splitCommaValues(share.ALPN),
			TLS:             strings.EqualFold(strings.TrimSpace(share.TLS), "tls"),
			AllowInsecure:   interfaceBool(share.AllowInsecure),
		}
		if node.Network == "" {
			return trueConnectionNode{}, errors.New("目前只支持 WS 或 TCP 传输")
		}
		if node.TLS != expectTLS {
			return trueConnectionNode{}, fmt.Errorf("节点是 %s，请提供一条 %s 节点", tlsLabel(node.TLS), tlsLabel(expectTLS))
		}
		return node, nil
	}
	return trueConnectionNode{}, errors.New("只支持 vmess:// 或 vless:// 分享链接")
}

func normalizeTrueConnectionNetwork(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "tcp":
		return "tcp"
	case "ws", "websocket":
		return "ws"
	default:
		return ""
	}
}

func tlsLabel(enabled bool) string {
	if enabled {
		return "HTTPS/TLS"
	}
	return "HTTP/非 TLS"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func splitCommaValues(raw string) []string {
	var values []string
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func parseLooseBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func interfaceBool(value interface{}) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return parseLooseBool(typed)
	case float64:
		return typed != 0
	default:
		return false
	}
}

func interfaceInt(value interface{}) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case string:
		return atoi(typed)
	default:
		return 0
	}
}

func decodeBase64String(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	encodings := []*base64.Encoding{base64.RawStdEncoding, base64.StdEncoding, base64.RawURLEncoding, base64.URLEncoding}
	for _, encoding := range encodings {
		if decoded, err := encoding.DecodeString(raw); err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("invalid base64")
}

type trueConnectionVariant struct {
	Scheme string
	Port   int
	Node   trueConnectionNode
}

type trueConnectionPortAttempt struct {
	Scheme     string
	Port       int
	Success    bool
	LatencyMs  int
	ErrorClass string
}

func trueConnectionEnabledForFamily(settings Settings, ipVersion int) bool {
	if ipVersion == 6 {
		return settings.TrueConnectionIPv6
	}
	return settings.TrueConnectionIPv4
}

func searchProfileForSettings(settings Settings, ipVersion int) searchmemory.Profile {
	profile := searchmemory.Profile{
		IPVersion:     ipVersion,
		LocationMode:  normalizeLocationMode(settings.LocationMode),
		Country:       strings.ToUpper(strings.TrimSpace(settings.LocationCountry)),
		Region:        strings.TrimSpace(settings.LocationRegion),
		City:          strings.TrimSpace(settings.LocationCity),
		BandwidthMbps: settings.BandwidthMbps,
		MaxRTTMs:      settings.MaxRTTMs,
		TestURL:       strings.TrimSpace(settings.TrueConnectionTestURL),
		NetworkLabel:  strings.TrimSpace(settings.SearchNetworkLabel),
	}
	if trueConnectionEnabledForFamily(settings, ipVersion) {
		profile.HTTPEnabled = settings.TrueConnectionHTTP
		profile.HTTPSEnabled = settings.TrueConnectionHTTPS
		if profile.HTTPEnabled {
			profile.HTTPNodeHash = searchmemory.SecretFingerprint(settings.TrueConnectionHTTPNode)
		}
		if profile.HTTPSEnabled {
			profile.HTTPSNodeHash = searchmemory.SecretFingerprint(settings.TrueConnectionHTTPSNode)
		}
	}
	return profile
}

func (a *App) ensureSearchProfile(settings Settings, ipVersion int) string {
	if a.searchMemory == nil {
		return ""
	}
	id, err := a.searchMemory.EnsureProfile(context.Background(), searchProfileForSettings(settings, ipVersion))
	if err != nil {
		log.Printf("ensure search profile failed: %v", err)
		return ""
	}
	return id
}

func (a *App) buildRunSearchPlanSnapshot(ctx context.Context, settings Settings) []RunSearchFamilyPlan {
	versions := []int{4, 6}
	plans := make([]RunSearchFamilyPlan, 0, 2)
	for _, version := range versions {
		if (version == 4 && activeIPv4Count(settings) == 0) || (version == 6 && activeIPv6Count(settings) == 0) {
			continue
		}
		plan := RunSearchFamilyPlan{IPVersion: version}
		profileID := a.ensureSearchProfile(settings, version)
		if profileID == "" || a.searchMemory == nil {
			plans = append(plans, plan)
			continue
		}
		memory, err := a.searchMemory.Candidates(ctx, profileID, version, time.Now())
		if err != nil {
			log.Printf("build run search plan failed: %v", err)
			plans = append(plans, plan)
			continue
		}
		plan.Available = true
		plan.ManualPrefixes = append([]string(nil), memory.ManualPrefixes...)
		plan.ManualSeedIPs = append([]string(nil), memory.ManualSeedIPs...)
		plan.ManualHintPrefixes = append([]string(nil), memory.ManualHintPrefixes...)
		if len(memory.ManualPrefixes) > 0 {
			plan.ManualQuotaPercent = 40
		}
		plan.ExactIPCount = len(memory.SuccessIPs)
		plan.CoolingIPCount = len(memory.ExcludeIPs)
		plan.CoolingPrefixCount = len(memory.ExcludePrefixes)
		plan.Budget = memory.Budget
		narrowBits := 48
		if version == 4 {
			narrowBits = 24
		}
		for _, raw := range memory.HintPrefixes {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil {
				continue
			}
			if prefix.Bits() >= narrowBits {
				plan.NarrowHintCount++
			} else {
				plan.WideHintCount++
			}
		}
		plans = append(plans, plan)
	}
	return plans
}

func importLegacySearchMemory(memory *searchmemory.Store, results []IPTestResult) error {
	items := make([]searchmemory.LegacyObservation, 0, len(results))
	for _, result := range results {
		testedAt, _ := time.Parse(time.RFC3339, result.TestedAt)
		items = append(items, searchmemory.LegacyObservation{
			IP: result.IP, IPVersion: result.IPVersion, DataCenterCountry: result.DataCenterCountry,
			DataCenterCode: result.DataCenterCode, RTTMs: result.RTTMs, BandwidthMbps: result.MeasuredBandwidthMbps, TestedAt: testedAt,
		})
	}
	return memory.ImportLegacy(context.Background(), items)
}

func filterUnseenIPs(values []string, seen map[string]bool) []string {
	unique := make(map[string]bool)
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] || unique[value] {
			continue
		}
		unique[value] = true
		result = append(result, value)
	}
	return result
}

func appendUniqueStrings(values []string, additions ...string) []string {
	seen := make(map[string]bool, len(values)+len(additions))
	for _, value := range values {
		seen[value] = true
	}
	for _, value := range additions {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			values = append(values, value)
		}
	}
	return values
}

func resultHintPrefixes(ip string, version int) []string {
	addr, err := netip.ParseAddr(ip)
	if err != nil || (version == 4) != addr.Is4() {
		return nil
	}
	if version == 4 {
		return []string{netip.PrefixFrom(addr, 24).Masked().String(), netip.PrefixFrom(addr, 16).Masked().String()}
	}
	return []string{netip.PrefixFrom(addr, 48).Masked().String(), netip.PrefixFrom(addr, 32).Masked().String()}
}

func candidateSourceForIP(ip string, exactIPs, hints []string, version int) string {
	for _, exact := range exactIPs {
		if strings.TrimSpace(exact) == ip {
			return "exact"
		}
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return "global"
	}
	narrowBits := 48
	if version == 4 {
		narrowBits = 24
	}
	wideMatch := false
	for _, raw := range hints {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil || !prefix.Contains(addr) {
			continue
		}
		if prefix.Bits() >= narrowBits {
			return "narrow"
		}
		wideMatch = true
	}
	if wideMatch {
		return "wide"
	}
	return "global"
}

func dominantTrueConnectionError(attempts []trueConnectionPortAttempt) string {
	counts := make(map[string]int)
	best, bestCount := "all_ports_failed", 0
	for _, attempt := range attempts {
		if attempt.Success || attempt.ErrorClass == "" {
			continue
		}
		counts[attempt.ErrorClass]++
		if counts[attempt.ErrorClass] > bestCount {
			best, bestCount = attempt.ErrorClass, counts[attempt.ErrorClass]
		}
	}
	return best
}

func (a *App) recordSearchObservation(runID, profileID string, result IPTestResult, outcome, errorClass string, attempts []trueConnectionPortAttempt) {
	if a.searchMemory == nil || profileID == "" {
		return
	}
	ports := make([]searchmemory.PortObservation, 0, len(attempts))
	for _, attempt := range attempts {
		ports = append(ports, searchmemory.PortObservation{Scheme: attempt.Scheme, Port: attempt.Port, Success: attempt.Success, LatencyMs: attempt.LatencyMs, ErrorClass: attempt.ErrorClass})
	}
	err := a.searchMemory.Record(context.Background(), searchmemory.Observation{
		RunID: runID, ProfileID: profileID, IP: result.IP, IPVersion: result.IPVersion, Outcome: outcome,
		ErrorClass: errorClass, CandidateSource: result.CandidateSource, DataCenterCountry: result.DataCenterCountry, DataCenterCode: result.DataCenterCode,
		RTTMs: result.RTTMs, BandwidthMbps: result.MeasuredBandwidthMbps, TestedAt: time.Now(), Ports: ports,
	})
	if err != nil {
		log.Printf("record search observation failed: %v", err)
	}
}

func runTrueConnectionTests(ctx context.Context, settings Settings, candidateIP string) ([]TrueConnectionPortResult, []trueConnectionPortAttempt, error) {
	xrayBin, err := findXrayBinary()
	if err != nil {
		return nil, nil, err
	}
	variants := make([]trueConnectionVariant, 0, len(trueConnectionHTTPPorts)+len(trueConnectionHTTPSPorts))
	if settings.TrueConnectionHTTP {
		node, err := parseTrueConnectionNode(settings.TrueConnectionHTTPNode, false)
		if err != nil {
			return nil, nil, err
		}
		for _, port := range trueConnectionHTTPPorts {
			variants = append(variants, trueConnectionVariant{Scheme: "HTTP", Port: port, Node: node})
		}
	}
	if settings.TrueConnectionHTTPS {
		node, err := parseTrueConnectionNode(settings.TrueConnectionHTTPSNode, true)
		if err != nil {
			return nil, nil, err
		}
		for _, port := range trueConnectionHTTPSPorts {
			variants = append(variants, trueConnectionVariant{Scheme: "HTTPS", Port: port, Node: node})
		}
	}

	type portOutcome struct {
		result TrueConnectionPortResult
		err    error
	}
	jobs := make(chan trueConnectionVariant)
	outcomes := make(chan portOutcome, len(variants))
	workerCount := 4
	if len(variants) < workerCount {
		workerCount = len(variants)
	}
	var workers sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for variant := range jobs {
				latency, err := testTrueConnectionPort(ctx, xrayBin, variant.Node, candidateIP, variant.Port, settings.TrueConnectionTestURL)
				outcomes <- portOutcome{result: TrueConnectionPortResult{Scheme: variant.Scheme, Port: variant.Port, LatencyMs: latency}, err: err}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, variant := range variants {
			select {
			case jobs <- variant:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(outcomes)
	}()

	results := make([]TrueConnectionPortResult, 0, len(variants))
	attempts := make([]trueConnectionPortAttempt, 0, len(variants))
	for outcome := range outcomes {
		if outcome.err == nil {
			results = append(results, outcome.result)
		}
		attempts = append(attempts, trueConnectionPortAttempt{
			Scheme: outcome.result.Scheme, Port: outcome.result.Port, Success: outcome.err == nil,
			LatencyMs: outcome.result.LatencyMs, ErrorClass: classifyTrueConnectionError(outcome.err),
		})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Scheme != results[j].Scheme {
			return results[i].Scheme < results[j].Scheme
		}
		return results[i].Port < results[j].Port
	})
	if err := ctx.Err(); err != nil {
		return results, attempts, err
	}
	sort.Slice(attempts, func(i, j int) bool {
		if attempts[i].Scheme != attempts[j].Scheme {
			return attempts[i].Scheme < attempts[j].Scheme
		}
		return attempts[i].Port < attempts[j].Port
	})
	return results, attempts, nil
}

func classifyTrueConnectionError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, context.DeadlineExceeded), strings.Contains(message, "timeout"), strings.Contains(message, "deadline exceeded"):
		return "timeout"
	case strings.Contains(message, "connection refused"):
		return "connection_refused"
	case strings.Contains(message, "tls"), strings.Contains(message, "certificate"):
		return "tls_handshake"
	case strings.Contains(message, "websocket"), strings.Contains(message, "bad handshake"):
		return "ws_handshake"
	case strings.Contains(message, "eof"):
		return "eof"
	case strings.Contains(message, "http 4"), strings.Contains(message, "http 5"):
		return "test_http_error"
	default:
		return "proxy_error"
	}
}

func formatTrueConnectionAttempts(attempts []trueConnectionPortAttempt) string {
	parts := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		if attempt.Success {
			parts = append(parts, fmt.Sprintf("%s:%d=通过(%dms)", attempt.Scheme, attempt.Port, attempt.LatencyMs))
		} else {
			parts = append(parts, fmt.Sprintf("%s:%d=%s", attempt.Scheme, attempt.Port, attempt.ErrorClass))
		}
	}
	return strings.Join(parts, "；")
}

func testTrueConnectionPort(parent context.Context, xrayBin string, node trueConnectionNode, candidateIP string, port int, testURL string) (int, error) {
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	localPort, releaseLocalPort, err := reserveTrueConnectionLocalPort()
	if err != nil {
		return 0, err
	}
	defer releaseLocalPort()

	config, err := buildXrayTrueConnectionConfig(node, candidateIP, port, localPort)
	if err != nil {
		return 0, err
	}
	tempDir, err := os.MkdirTemp("", "cf-betterip-true-connect-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tempDir)
	configPath := filepath.Join(tempDir, "config.json")
	data, err := json.Marshal(config)
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return 0, err
	}

	cmd := exec.CommandContext(ctx, xrayBin, "run", "-config", configPath)
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("启动 Xray 真连接核心失败: %w", err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	defer func() {
		cancel()
		select {
		case <-waitCh:
		case <-time.After(time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}
	}()

	proxyAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))
	if err := waitForTCP(ctx, proxyAddress); err != nil {
		detail := trimForLog(stderr.String(), 300)
		if detail != "" {
			return 0, fmt.Errorf("Xray 未就绪: %s", detail)
		}
		return 0, err
	}
	proxyURL, _ := url.Parse("http://" + proxyAddress)
	transport := &http.Transport{
		Proxy:               http.ProxyURL(proxyURL),
		DisableKeepAlives:   true,
		MaxIdleConnsPerHost: -1,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, testURL, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("User-Agent", "cf-betterip-true-connection/1.0")
	startedAt := time.Now()
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	latency := int(time.Since(startedAt).Milliseconds())
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return latency, fmt.Errorf("测试地址返回 HTTP %d", response.StatusCode)
	}
	return latency, nil
}

func reserveTrueConnectionLocalPort() (int, func(), error) {
	for attempt := 0; attempt < 20; attempt++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, nil, err
		}
		port := listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		trueConnectionLocalPortMu.Lock()
		if !trueConnectionLocalPorts[port] {
			trueConnectionLocalPorts[port] = true
			trueConnectionLocalPortMu.Unlock()
			return port, func() {
				trueConnectionLocalPortMu.Lock()
				delete(trueConnectionLocalPorts, port)
				trueConnectionLocalPortMu.Unlock()
			}, nil
		}
		trueConnectionLocalPortMu.Unlock()
	}
	return 0, nil, errors.New("无法为真连接测试分配不重复的本地端口")
}

func waitForTCP(ctx context.Context, address string) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func buildXrayTrueConnectionConfig(node trueConnectionNode, candidateIP string, port, localPort int) (map[string]interface{}, error) {
	user := map[string]interface{}{"id": node.ID}
	switch node.Protocol {
	case "vless":
		user["encryption"] = firstNonEmpty(node.Encryption, "none")
		if node.Flow != "" {
			user["flow"] = node.Flow
		}
	case "vmess":
		user["alterId"] = node.AlterID
		user["security"] = firstNonEmpty(node.Cipher, "auto")
	default:
		return nil, errors.New("不支持的节点协议")
	}
	outbound := map[string]interface{}{
		"tag":      "proxy",
		"protocol": node.Protocol,
		"settings": map[string]interface{}{
			"vnext": []interface{}{map[string]interface{}{
				"address": candidateIP,
				"port":    port,
				"users":   []interface{}{user},
			}},
		},
	}
	streamSettings := map[string]interface{}{
		"network":  node.Network,
		"security": "none",
	}
	if node.Network == "ws" {
		wsSettings := map[string]interface{}{"path": firstNonEmpty(node.Path, "/")}
		if node.Host != "" {
			wsSettings["headers"] = map[string]interface{}{"Host": node.Host}
		}
		streamSettings["wsSettings"] = wsSettings
	}
	if node.TLS {
		streamSettings["security"] = "tls"
		serverName := firstNonEmpty(node.SNI, node.Host, node.OriginalAddress)
		tlsSettings := map[string]interface{}{
			"serverName":    serverName,
			"allowInsecure": node.AllowInsecure,
		}
		if node.Fingerprint != "" {
			tlsSettings["fingerprint"] = node.Fingerprint
		}
		if len(node.ALPN) > 0 {
			tlsSettings["alpn"] = node.ALPN
		}
		streamSettings["tlsSettings"] = tlsSettings
	}
	outbound["streamSettings"] = streamSettings
	return map[string]interface{}{
		"log": map[string]interface{}{"loglevel": "warning"},
		"inbounds": []interface{}{map[string]interface{}{
			"listen":   "127.0.0.1",
			"port":     localPort,
			"protocol": "http",
			"settings": map[string]interface{}{},
		}},
		"outbounds": []interface{}{outbound},
	}, nil
}

func findXrayBinary() (string, error) {
	candidates := []string{
		strings.TrimSpace(os.Getenv("XRAY_BIN")),
		"/root/cf-betterip/xray",
		"/root/cf-betterip/bin/xray",
		"./bin/xray",
	}
	if path, err := exec.LookPath("xray"); err == nil {
		candidates = append(candidates, path)
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			if filepath.IsAbs(candidate) {
				return candidate, nil
			}
			return filepath.Abs(candidate)
		}
	}
	return "", errors.New("真连接测试已启用，但找不到 Xray 核心；请安装到 /root/cf-betterip/xray 或设置 XRAY_BIN")
}

func formatTrueConnectionPorts(results []TrueConnectionPortResult) string {
	if len(results) == 0 {
		return "未通过"
	}
	groups := map[string][]string{"HTTP": {}, "HTTPS": {}}
	for _, result := range results {
		groups[result.Scheme] = append(groups[result.Scheme], fmt.Sprintf("%d (%dms)", result.Port, result.LatencyMs))
	}
	parts := make([]string, 0, 2)
	for _, scheme := range []string{"HTTP", "HTTPS"} {
		if len(groups[scheme]) > 0 {
			parts = append(parts, scheme+": "+strings.Join(groups[scheme], ", "))
		}
	}
	return strings.Join(parts, "；")
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
	data.AppVersion = appVersion
	data.RepositoryURL = repositoryURL
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
	mode := normalizeLocationMode(settings.LocationMode)
	selection := locationSelectionText(settings)
	if mode == "any" {
		if selection != "" {
			return "全局随机（已保留地区 " + selection + "，但当前不参与筛选）"
		}
		return "全局随机"
	}
	if selection == "" {
		return "全局随机"
	}
	if mode == "strict" {
		return "严格地区 / " + selection
	}
	return "地区优先 / " + selection
}

func locationSelectionText(settings Settings) string {
	parts := make([]string, 0, 3)
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

func locationFilterSummaryWithStats(settings Settings, stats GeoFilterStats) string {
	base := locationFilterSummary(settings)
	if normalizeLocationMode(settings.LocationMode) == "any" || locationSelectionText(settings) == "" {
		return base
	}
	targets := fmt.Sprintf("目标机房 %d 个", stats.DataCenterCount)
	if stats.Codes != "" {
		targets += "：" + stats.Codes
	}
	if normalizeLocationMode(settings.LocationMode) == "strict" {
		return fmt.Sprintf("%s（%s；只接受 CF-RAY 实测匹配；不回退全局）", base, targets)
	}
	return fmt.Sprintf("%s（%s；先接受 CF-RAY 实测匹配；单次选优连续 10 分钟无结果后回退全局）", base, targets)
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

func buildConfigTestTargets(settings Settings) []ConfigTestTarget {
	var targets []ConfigTestTarget
	for _, target := range settings.DNSTargets {
		if !target.Enabled {
			continue
		}
		credential, _ := credentialByID(settings, target.CredentialID)
		targets = append(targets, ConfigTestTarget{
			Label:      target.Name + " / " + recordFamilyLabel(target.RecordFamily),
			RootDomain: target.RootDomain,
			RecordName: target.RecordName,
			Credential: credential,
			ZoneID:     target.ZoneID,
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
	target.ZoneID = strings.TrimSpace(target.ZoneID)
	if err := validateCredentialForUse(target.Credential); err != nil {
		result.Message = err.Error() + "。"
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
	zoneName, err := getCloudflareZoneName(client, target.ZoneID, target.Credential)
	if err != nil {
		result.Message = "Zone 访问失败：" + err.Error()
		return result
	}
	rootDomain := normalizeDomain(target.RootDomain)
	if rootDomain == "" {
		rootDomain = normalizeDomain(zoneName)
	}
	if normalizeDomain(zoneName) != rootDomain {
		result.Message = fmt.Sprintf("Zone 校验失败：Zone ID 实际属于 %s，不是配置的根域名 %s。", zoneName, target.RootDomain)
		return result
	}
	if target.RecordName != rootDomain && !strings.HasSuffix(target.RecordName, "."+rootDomain) {
		result.Message = fmt.Sprintf("目标域名 %s 不属于 Zone %s。", target.RecordName, rootDomain)
		return result
	}

	recordID, err := createCloudflareTXT(client, target)
	if err != nil {
		result.Message = "临时 TXT 写入失败：" + err.Error()
		return result
	}
	result.CreatedID = recordID
	if err := cloudflareRequest(client, http.MethodDelete, "https://api.cloudflare.com/client/v4/zones/"+target.ZoneID+"/dns_records/"+recordID, target.Credential, nil, nil); err != nil {
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
	err := cloudflareRequest(client, http.MethodPost, "https://api.cloudflare.com/client/v4/zones/"+target.ZoneID+"/dns_records", target.Credential, body, &parsed)
	if err != nil {
		return "", err
	}
	if parsed.Result.ID == "" {
		return "", errors.New("Cloudflare 未返回临时记录 ID")
	}
	return parsed.Result.ID, nil
}

var shareURLPattern = regexp.MustCompile(`(?i)(vmess://[A-Za-z0-9+/_=-]+|vless://[^\s]+)`)

func decodeVmessPayload(payload string) ([]byte, error) {
	payload = strings.TrimSpace(payload)
	encodings := []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding}
	var lastErr error
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(payload)
		if err == nil {
			return decoded, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func parseShareLinkIPList(input string) ([]string, []string, error) {
	matches := shareURLPattern.FindAllString(input, -1)
	if len(matches) == 0 {
		return nil, nil, errors.New("没有找到可解析的 vmess:// 或 vless:// 分享链接")
	}
	if len(matches) > 500 {
		return nil, nil, fmt.Errorf("一次最多导入 500 个 vmess/vless 链接，当前检测到 %d 个", len(matches))
	}
	seen := make(map[string]bool)
	var ipv4, ipv6 []string
	for i, shareLink := range matches {
		address := ""
		protocol := "vmess"
		if strings.HasPrefix(strings.ToLower(shareLink), "vless://") {
			protocol = "vless"
			parsed, err := url.Parse(shareLink)
			if err != nil || parsed.Hostname() == "" {
				return nil, nil, fmt.Errorf("第 %d 个 vless 链接无法解析服务器地址", i+1)
			}
			address = strings.TrimSpace(parsed.Hostname())
		} else {
			payloadText := shareLink[len("vmess://"):]
			decoded, err := decodeVmessPayload(payloadText)
			if err != nil {
				return nil, nil, fmt.Errorf("第 %d 个 vmess 链接的 Base64 内容无法解码", i+1)
			}
			var payload struct {
				Address string `json:"add"`
			}
			if err := json.Unmarshal(decoded, &payload); err != nil {
				return nil, nil, fmt.Errorf("第 %d 个 vmess 链接不是有效 JSON", i+1)
			}
			address = strings.TrimSpace(payload.Address)
		}
		addr, err := netip.ParseAddr(address)
		if err != nil {
			return nil, nil, fmt.Errorf("第 %d 个 %s 链接的服务器地址 %q 不是 IPv4/IPv6 地址", i+1, protocol, address)
		}
		addr = addr.Unmap()
		normalized := addr.String()
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		if addr.Is4() {
			ipv4 = append(ipv4, normalized)
		} else {
			ipv6 = append(ipv6, normalized)
		}
	}
	return ipv4, ipv6, nil
}

func replaceManualDNSRecords(client *http.Client, settings Settings, configured ManualDNSTargetConfig, ipv4, ipv6 []string, logf func(string)) error {
	if err := validateDNSConfig(settings); err != nil {
		return err
	}
	credential, ok := credentialByID(settings, configured.CredentialID)
	if !ok {
		return errors.New("手动 DNS 目标引用的 Cloudflare 凭据不存在")
	}
	zoneName, err := getCloudflareZoneName(client, configured.ZoneID, credential)
	if err != nil {
		return fmt.Errorf("无法访问手动目标的 Cloudflare Zone：%w", err)
	}
	if normalizeDomain(zoneName) != normalizeDomain(configured.RootDomain) {
		return fmt.Errorf("Zone ID 实际属于 %s，不是配置的根域名 %s", zoneName, configured.RootDomain)
	}
	if configured.RecordName == configured.RootDomain || !strings.HasSuffix(configured.RecordName, "."+configured.RootDomain) {
		return fmt.Errorf("目标域名 %s 不属于 Zone %s 的子域名", configured.RecordName, configured.RootDomain)
	}
	ipv4 = uniqueStrings(ipv4)
	ipv6 = uniqueStrings(ipv6)
	for _, ip := range ipv4 {
		addr, err := netip.ParseAddr(ip)
		if err != nil || !addr.Unmap().Is4() {
			return fmt.Errorf("手动 IPv4 列表包含无效地址 %q", ip)
		}
	}
	for _, ip := range ipv6 {
		addr, err := netip.ParseAddr(ip)
		if err != nil || addr.Unmap().Is4() {
			return fmt.Errorf("手动 IPv6 列表包含无效地址 %q", ip)
		}
	}
	targets := []DNSSyncTarget{
		{TargetID: configured.ID, Label: configured.Name + " / 手动 A", RootDomain: configured.RootDomain, RecordName: configured.RecordName, RecordType: "A", Credential: credential, ZoneID: configured.ZoneID, IPs: ipv4},
		{TargetID: configured.ID, Label: configured.Name + " / 手动 AAAA", RootDomain: configured.RootDomain, RecordName: configured.RecordName, RecordType: "AAAA", Credential: credential, ZoneID: configured.ZoneID, IPs: ipv6},
	}
	type manualPlan struct {
		target DNSSyncTarget
		old    []CloudflareDNSRecord
	}
	plans := make([]manualPlan, 0, len(targets))
	for _, target := range targets {
		old, err := listCloudflareDNSRecords(client, target)
		if err != nil {
			return fmt.Errorf("%s 预检查失败，未修改任何 A/AAAA 记录：%w", target.Label, err)
		}
		plans = append(plans, manualPlan{target: target, old: old})
	}
	var updateErrors []string
	for _, plan := range plans {
		if err := reconcileDNSTarget(client, plan.target, plan.old, logf); err != nil {
			updateErrors = append(updateErrors, err.Error())
		}
	}
	if len(updateErrors) > 0 {
		return errors.New(strings.Join(updateErrors, " | "))
	}
	return nil
}

func syncResultsToCloudflare(settings Settings, results []IPTestResult, logf func(string)) (DNSSyncReport, error) {
	report := DNSSyncReport{
		TotalTargets:  plannedDNSTargetCount(settings),
		ConfirmedIPs:  make(map[string]bool),
		ConfirmedByIP: make(map[string]int),
		PlannedByIP:   make(map[string]int),
	}
	if err := validateDNSConfig(settings); err != nil {
		return report, err
	}
	targets, err := buildDNSSyncTargets(settings, results)
	if err != nil {
		return report, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	type syncPlan struct {
		target DNSSyncTarget
		old    []CloudflareDNSRecord
	}
	type targetPlanGroup struct {
		id              string
		name            string
		plans           []syncPlan
		plannedRecords  int
		preflightErrors []string
	}
	groups := make([]*targetPlanGroup, 0, report.TotalTargets)
	groupsByID := make(map[string]*targetPlanGroup)
	plannedBindingsByIP := make(map[string]int)
	confirmedBindingsByIP := make(map[string]int)
	for _, target := range targets {
		if len(target.IPs) == 0 {
			continue
		}
		group := groupsByID[target.TargetID]
		if group == nil {
			group = &targetPlanGroup{id: target.TargetID, name: strings.TrimSuffix(strings.TrimSuffix(target.Label, " / A"), " / AAAA")}
			groupsByID[target.TargetID] = group
			groups = append(groups, group)
		}
		target.IPs = uniqueStrings(target.IPs)
		group.plannedRecords += len(target.IPs)
		for _, ip := range target.IPs {
			plannedBindingsByIP[ip]++
		}
		if target.RecordName == "" {
			group.preflightErrors = append(group.preflightErrors, fmt.Sprintf("%s 缺少目标域名", target.Label))
			continue
		}
		if target.ZoneID == "" {
			group.preflightErrors = append(group.preflightErrors, fmt.Sprintf("%s 缺少 Zone ID", target.Label))
			continue
		}
		if err := validateCredentialForUse(target.Credential); err != nil {
			group.preflightErrors = append(group.preflightErrors, fmt.Sprintf("%s 的 Cloudflare 凭据不可用：%s", target.Label, err))
			continue
		}
		zoneName, err := getCloudflareZoneName(client, target.ZoneID, target.Credential)
		if err != nil {
			group.preflightErrors = append(group.preflightErrors, fmt.Sprintf("%s 无法访问 Zone：%s", target.Label, err))
			continue
		}
		rootDomain := normalizeDomain(target.RootDomain)
		if rootDomain == "" {
			rootDomain = normalizeDomain(zoneName)
		}
		if normalizeDomain(zoneName) != rootDomain {
			group.preflightErrors = append(group.preflightErrors, fmt.Sprintf("%s 的 Zone ID 实际属于 %s，不是配置的根域名 %s", target.Label, zoneName, target.RootDomain))
			continue
		}
		if target.RecordName != rootDomain && !strings.HasSuffix(target.RecordName, "."+rootDomain) {
			group.preflightErrors = append(group.preflightErrors, fmt.Sprintf("%s 的目标域名 %s 不属于 Zone %s", target.Label, target.RecordName, rootDomain))
			continue
		}
		logf(fmt.Sprintf("%s：同步前检查旧 %s 记录。", target.Label, target.RecordType))
		oldRecords, err := listCloudflareDNSRecords(client, target)
		if err != nil {
			group.preflightErrors = append(group.preflightErrors, fmt.Sprintf("%s 预检查失败：%s", target.Label, err))
			continue
		}
		group.plans = append(group.plans, syncPlan{target: target, old: oldRecords})
	}
	var allErrors []string
	for _, group := range groups {
		result := DNSTargetSyncResult{TargetID: group.id, TargetName: group.name, Status: "failed", PlannedRecords: group.plannedRecords}
		if len(group.preflightErrors) > 0 {
			result.Error = strings.Join(group.preflightErrors, " | ")
			report.TargetResults = append(report.TargetResults, result)
			allErrors = append(allErrors, result.Error)
			logf(group.name + "：预检查失败，本目标未修改；继续处理其他独立目标。")
			continue
		}
		logf(fmt.Sprintf("%s：%d 个 DNS 写入单元预检查通过；开始先创建新记录，再清理旧记录。", group.name, len(group.plans)))
		var targetErrors []string
		for _, plan := range group.plans {
			if err := reconcileDNSTarget(client, plan.target, plan.old, logf); err != nil {
				targetErrors = append(targetErrors, err.Error())
				continue
			}
			confirmed := len(plan.target.IPs)
			result.ConfirmedRecords += confirmed
			report.ConfirmedRecords += confirmed
			for _, ip := range plan.target.IPs {
				confirmedBindingsByIP[ip]++
			}
		}
		if len(targetErrors) == 0 && result.ConfirmedRecords == result.PlannedRecords {
			result.Status = "confirmed"
			report.ConfirmedTargets++
		} else if result.ConfirmedRecords > 0 {
			result.Status = "partial"
			result.Error = strings.Join(targetErrors, " | ")
			allErrors = append(allErrors, result.Error)
		} else {
			result.Error = strings.Join(targetErrors, " | ")
			allErrors = append(allErrors, result.Error)
		}
		report.TargetResults = append(report.TargetResults, result)
	}
	for ip, planned := range plannedBindingsByIP {
		report.PlannedByIP[ip] = planned
		report.ConfirmedByIP[ip] = confirmedBindingsByIP[ip]
		if planned > 0 && confirmedBindingsByIP[ip] == planned {
			report.ConfirmedIPs[ip] = true
		}
	}
	if len(allErrors) > 0 {
		return report, errors.New(strings.Join(allErrors, " | "))
	}
	return report, nil
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
	var targets []DNSSyncTarget
	for _, configured := range settings.DNSTargets {
		if !configured.Enabled {
			continue
		}
		credential, ok := credentialByID(settings, configured.CredentialID)
		if !ok {
			return nil, fmt.Errorf("%s 引用的 Cloudflare 凭据不存在", configured.Name)
		}
		for _, recordType := range targetRecordTypes(configured) {
			ips := ipv4
			if recordType == "AAAA" {
				ips = ipv6
			}
			targets = append(targets, DNSSyncTarget{
				TargetID: configured.ID, Label: configured.Name + " / " + recordType, RootDomain: configured.RootDomain,
				RecordName: configured.RecordName, RecordType: recordType,
				Credential: credential, ZoneID: configured.ZoneID, IPs: ips,
			})
		}
	}
	return targets, nil
}

func getCloudflareZoneName(client *http.Client, zoneID string, credential CloudflareCredentialConfig) (string, error) {
	var parsed struct {
		Result struct {
			Name string `json:"name"`
		} `json:"result"`
	}
	if err := cloudflareRequest(client, http.MethodGet, "https://api.cloudflare.com/client/v4/zones/"+zoneID, credential, nil, &parsed); err != nil {
		return "", err
	}
	if normalizeDomain(parsed.Result.Name) == "" {
		return "", errors.New("Cloudflare 未返回 Zone 名称")
	}
	return normalizeDomain(parsed.Result.Name), nil
}

func reconcileDNSTarget(client *http.Client, target DNSSyncTarget, oldRecords []CloudflareDNSRecord, logf func(string)) error {
	expected := make(map[string]bool, len(target.IPs))
	existing := make(map[string]bool, len(oldRecords))
	for _, ip := range target.IPs {
		expected[ip] = true
	}
	for _, record := range oldRecords {
		existing[record.Content] = true
	}
	for _, ip := range target.IPs {
		if existing[ip] {
			continue
		}
		logf(fmt.Sprintf("%s：先创建新记录 %s %s -> %s。", target.Label, target.RecordType, target.RecordName, ip))
		if _, err := createCloudflareAddressRecord(client, target, ip); err != nil {
			return fmt.Errorf("%s 创建新记录失败，旧记录尚未清理：%w", target.Label, err)
		}
	}
	current, err := listCloudflareDNSRecords(client, target)
	if err != nil {
		return fmt.Errorf("%s 创建后反查失败：%w", target.Label, err)
	}
	currentContents := make(map[string]bool)
	for _, record := range current {
		currentContents[record.Content] = true
	}
	for ip := range expected {
		if !currentContents[ip] {
			return fmt.Errorf("%s 新记录未全部出现，保留旧记录并停止同步", target.Label)
		}
	}
	kept := make(map[string]bool)
	for _, record := range current {
		if expected[record.Content] && !kept[record.Content] {
			kept[record.Content] = true
			continue
		}
		logf(fmt.Sprintf("%s：新记录已就绪，删除旧/重复记录 %s -> %s。", target.Label, record.Type, record.Content))
		if err := cloudflareRequest(client, http.MethodDelete, "https://api.cloudflare.com/client/v4/zones/"+target.ZoneID+"/dns_records/"+record.ID, target.Credential, nil, nil); err != nil {
			return fmt.Errorf("%s 清理旧记录失败：%w", target.Label, err)
		}
	}
	verified, err := listCloudflareDNSRecords(client, target)
	if err != nil {
		return fmt.Errorf("%s 最终反查失败：%w", target.Label, err)
	}
	if !sameContents(verified, target.IPs) {
		return fmt.Errorf("%s 最终确认失败：Cloudflare 记录与目标 IP 不一致", target.Label)
	}
	logf(fmt.Sprintf("%s：已确认 %d 条 %s 记录。", target.Label, len(target.IPs), target.RecordType))
	return nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		unique = append(unique, value)
	}
	return unique
}

func listCloudflareDNSRecords(client *http.Client, target DNSSyncTarget) ([]CloudflareDNSRecord, error) {
	var safe []CloudflareDNSRecord
	const perPage = 100
	for page := 1; page <= 1000; page++ {
		query := url.Values{}
		query.Set("name", target.RecordName)
		query.Set("type", target.RecordType)
		query.Set("page", strconv.Itoa(page))
		query.Set("per_page", strconv.Itoa(perPage))
		var parsed struct {
			Result     []CloudflareDNSRecord `json:"result"`
			ResultInfo struct {
				Page       int `json:"page"`
				TotalPages int `json:"total_pages"`
			} `json:"result_info"`
		}
		err := cloudflareRequest(client, http.MethodGet, "https://api.cloudflare.com/client/v4/zones/"+target.ZoneID+"/dns_records?"+query.Encode(), target.Credential, nil, &parsed)
		if err != nil {
			return nil, err
		}
		for _, record := range parsed.Result {
			if strings.EqualFold(record.Name, target.RecordName) && record.Type == target.RecordType {
				safe = append(safe, record)
			}
		}
		if parsed.ResultInfo.TotalPages > 0 {
			if page >= parsed.ResultInfo.TotalPages {
				return safe, nil
			}
		} else if len(parsed.Result) < perPage {
			return safe, nil
		}
	}
	return nil, errors.New("Cloudflare DNS 记录分页超过安全上限")
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
	err := cloudflareRequest(client, http.MethodPost, "https://api.cloudflare.com/client/v4/zones/"+target.ZoneID+"/dns_records", target.Credential, body, &parsed)
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

func cloudflareRequest(client *http.Client, method, url string, credential CloudflareCredentialConfig, body interface{}, out interface{}) error {
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
	if normalizeAuthType(credential.AuthType) == "global_api_key" {
		req.Header.Set("X-Auth-Email", credential.Email)
		req.Header.Set("X-Auth-Key", credential.APIKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+credential.APIToken)
	}
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
	if len(runs) < limit {
		limit = len(runs)
	}
	views := append([]RunRecord(nil), runs[:limit]...)
	for i := range views {
		decorateRunPlan(&views[i])
	}
	return views
}

func currentRun(runs []RunRecord) *RunRecord {
	for i := range runs {
		if runs[i].Status == "running" {
			view := runs[i]
			decorateRunPlan(&view)
			return &view
		}
	}
	return nil
}

func latestRun(runs []RunRecord) *RunRecord {
	if len(runs) == 0 {
		return nil
	}
	view := runs[0]
	decorateRunPlan(&view)
	return &view
}

func decorateRunPlan(run *RunRecord) {
	if run == nil || run.ConfigSnapshot == nil {
		return
	}
	run.Plan = buildRunPlan(*run.ConfigSnapshot)
	run.Plan.SearchFamilies = cloneRunSearchPlan(run.SearchPlanSnapshot)
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
		runSettings := state.Settings
		if run.ConfigSnapshot != nil {
			runSettings = hydrateRunSettings(*run.ConfigSnapshot, state.Settings)
		}
		required := requiredIPCount(runSettings)
		if required <= 0 {
			continue
		}
		seed := seedResultsForRun(state.Results, run.ID, runSettings)
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
		} else if result.ConfirmedDNSTargets > 0 && result.PlannedDNSTargets > 0 {
			syncedText = fmt.Sprintf("部分同步 %d/%d", result.ConfirmedDNSTargets, result.PlannedDNSTargets)
		} else if result.PlannedDNSTargets > 0 {
			syncedText = fmt.Sprintf("未同步 0/%d", result.PlannedDNSTargets)
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
			TrueConnectionText:      trueConnectionResultText(result),
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

func trueConnectionResultText(result IPTestResult) string {
	if !result.TrueConnectionTested {
		return "未启用"
	}
	return formatTrueConnectionPorts(result.TrueConnectionPorts)
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
	expected := requiredDNSRecordCount(settings)
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
		latestExpected := latest.RequiredDNSRecordCount
		if latestExpected == 0 {
			latestExpected = latest.RequiredIPCount
		}
		if latest.Status == "succeeded" && latest.SyncedIPCount >= latestExpected && latestExpected > 0 {
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
	if activeIPv4Count(settings) > 0 && !hasDNSTargetForFamily(settings, 4) {
		return false
	}
	if activeIPv6Count(settings) > 0 && !hasDNSTargetForFamily(settings, 6) {
		return false
	}
	if requiredDNSRecordCount(settings) == 0 || validateDNSConfig(settings) != nil {
		return false
	}
	return true
}

func hasDNSTargetForFamily(settings Settings, family int) bool {
	for _, target := range settings.DNSTargets {
		if targetSupportsFamily(target, family) {
			return true
		}
	}
	return false
}

func configHint(settings Settings) string {
	if requiredIPCount(settings) == 0 {
		return "至少启用 IPv4 或 IPv6"
	}
	if activeIPv4Count(settings) > 0 && !hasDNSTargetForFamily(settings, 4) {
		return "至少添加一个包含 IPv4 A 的 DNS 目标"
	}
	if activeIPv6Count(settings) > 0 && !hasDNSTargetForFamily(settings, 6) {
		return "至少添加一个包含 IPv6 AAAA 的 DNS 目标"
	}
	if err := validateDNSConfig(settings); err != nil {
		return err.Error()
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

func runSummary(trigger string, settings Settings, geoStats GeoFilterStats) string {
	return fmt.Sprintf("%s / 本轮扫描 IPv4:%d IPv6:%d / %s / %s / %d Mbps / RTT并发:%d / 最大RTT:%dms / %s",
		triggerLabel(trigger),
		activeIPv4Count(settings),
		activeIPv6Count(settings),
		runDNSPlanSummary(settings),
		locationFilterSummaryWithStats(settings, geoStats),
		settings.BandwidthMbps,
		settings.RTTConcurrency,
		settings.MaxRTTMs,
		trueConnectionSummary(settings),
	)
}

func trueConnectionSummary(settings Settings) string {
	var families []string
	if settings.TrueConnectionIPv4 {
		families = append(families, "IPv4")
	}
	if settings.TrueConnectionIPv6 {
		families = append(families, "IPv6")
	}
	if len(families) == 0 {
		return "真连接:关闭"
	}
	var schemes []string
	if settings.TrueConnectionHTTP {
		schemes = append(schemes, "HTTP")
	}
	if settings.TrueConnectionHTTPS {
		schemes = append(schemes, "HTTPS")
	}
	return "真连接:" + strings.Join(families, "+") + "(" + strings.Join(schemes, "+") + ")"
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
	if !settings.ScheduleEnabled || hasRunningRun(state.Runs) || !isConfigReady(settings) {
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
    .dashboard-stack { display: block; }
    .workspace-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 12px; }
    .workspace-card { display: flex; min-height: 86px; flex-direction: column; justify-content: center; gap: 7px; border: 1px solid #dbe3ee; border-radius: 10px; padding: 16px; background: #fbfdff; color: #1f2937; text-decoration: none; }
    .workspace-card:hover { border-color: #2563eb; background: #eff6ff; }
    .workspace-card strong { font-size: 17px; }
    .workspace-card span { color: #64748b; font-size: 13px; line-height: 1.45; }
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
    .run-plan { border: 1px solid #bfdbfe; border-radius: 10px; padding: 16px; margin: 12px 0; background: #eff6ff; }
    .run-plan h3 { margin: 0 0 12px; font-size: 17px; }
    .run-plan .compact-list { margin-top: 10px; }
    .run-plan .skipped { color: #6b7280; }
    .subsection { border-top: 1px solid #e5e7eb; padding-top: 16px; }
    .config-card-list { display: grid; gap: 12px; margin-top: 12px; }
    .config-card { border: 1px solid #dbe3ee; border-radius: 10px; padding: 16px; background: #fbfdff; }
    .config-card-head { display: flex; justify-content: space-between; align-items: flex-start; gap: 12px; margin-bottom: 8px; }
    .config-card-head strong { font-size: 17px; }
    .empty-card { border: 1px dashed #cbd5e1; border-radius: 10px; padding: 20px; color: #64748b; text-align: center; background: #f8fafc; }
    .global-key-field[hidden], .api-token-field[hidden] { display: none; }
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
    .tag { display: inline-block; padding: 3px 8px; border-radius: 999px; background: #e0e7ff; color: #3730a3; font-size: 12px; font-weight: 700; }
    footer { max-width: 1060px; margin: 8px auto 28px; padding: 0 18px; color: #64748b; }
    footer .footer-inner { display: flex; justify-content: space-between; align-items: center; gap: 12px; flex-wrap: wrap; border-top: 1px solid #e5e7eb; padding-top: 16px; }
    footer a { color: #2563eb; text-decoration: none; }
    code { background: #f3f4f6; padding: 2px 5px; border-radius: 4px; }
    @media (max-width: 820px) { .dashboard-grid, .kpi-grid, .workspace-grid { grid-template-columns: 1fr; } }
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
	  <a href="/run">执行</a>
	  <a href="/history">历史</a>
	  <a href="/results">结果</a>
	  <a href="/settings">配置</a>
      <form action="/logout" method="post" style="display:inline"><button class="secondary" type="submit">退出</button></form>
    </nav>{{end}}
  </header>
  <main>
    {{if .Flash}}<div class="flash">{{.Flash}}</div>{{end}}
    {{if .Error}}<div class="error">{{.Error}}</div>{{end}}
    {{template "content" .}}
  </main>
  <footer><div class="footer-inner"><span>CF BetterIP DNS Sync · 当前版本 {{.AppVersion}}</span><span><a href="{{.RepositoryURL}}" target="_blank" rel="noopener">github.com/samni728/better-cf</a> · <a href="{{.RepositoryURL}}" target="_blank" rel="noopener" aria-label="在 GitHub 上给项目 Star">★ Star</a></span></div></footer>
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
	<a class="button" href="/run">执行中心</a>
	<a class="button" href="/history">任务历史</a>
	<a class="button" href="/results">IP 结果</a>
    <a class="button" href="/settings">配置</a>
  </div>
</section>

<section class="kpi-grid">
  <div class="kpi"><span>今日更新 IP</span><strong>{{.Stats.TodayUpdatedIPs}}</strong></div>
  <div class="kpi"><span>今日已由 Cloudflare 核验的记录</span><strong>{{.Stats.TodaySyncedIPs}}</strong></div>
  <div class="kpi"><span>今日任务</span><strong>{{.Stats.TodayTaskCount}}</strong></div>
  <div class="kpi"><span>DNS 状态</span><strong>{{.Stats.LastDNSStatus}}</strong></div>
</section>

<div class="dashboard-stack">
  <section class="panel">
    <h1>执行看板</h1>
    {{if .CurrentRun}}
      <p class="muted">当前阶段：{{.CurrentRun.Stage}}</p>
      <div class="progress"><div style="width: {{.CurrentRun.Progress}}%"></div></div>
      <div class="grid">
        <div class="metric"><span>通过全部筛选的 IP</span><strong>{{.CurrentRun.UpdatedIPCount}} / {{.CurrentRun.RequiredIPCount}}</strong></div>
        <div class="metric"><span>Cloudflare 已核验记录（逐条）</span><strong>{{.CurrentRun.SyncedIPCount}} / {{if .CurrentRun.RequiredDNSRecordCount}}{{.CurrentRun.RequiredDNSRecordCount}}{{else}}{{.CurrentRun.RequiredIPCount}}{{end}}</strong></div>
        <div class="metric"><span>触发方式</span><strong>{{if eq .CurrentRun.Trigger "scheduled"}}定时{{else if eq .CurrentRun.Trigger "resume"}}续接{{else}}手动{{end}}</strong></div>
      </div>
      {{template "runPlan" .CurrentRun.Plan}}
    {{else if .LatestRun}}
      <p class="muted">最近一次：{{.LatestRun.Stage}}</p>
      <div class="progress"><div style="width: {{.LatestRun.Progress}}%"></div></div>
      <div class="grid">
        <div class="metric"><span>通过全部筛选的 IP</span><strong>{{.LatestRun.UpdatedIPCount}} / {{.LatestRun.RequiredIPCount}}</strong></div>
        <div class="metric"><span>Cloudflare 已核验记录（逐条）</span><strong>{{.LatestRun.SyncedIPCount}} / {{if .LatestRun.RequiredDNSRecordCount}}{{.LatestRun.RequiredDNSRecordCount}}{{else}}{{.LatestRun.RequiredIPCount}}{{end}}</strong></div>
        <div class="metric"><span>结果</span><strong>{{.LatestRun.Status}}</strong></div>
      </div>
      {{template "runPlan" .LatestRun.Plan}}
    {{else}}
      <p class="muted">还没有执行记录。</p>
    {{end}}
  </section>

  <section class="panel">
    <h2>目标概览</h2>
    <ul class="compact-list">
      <li><span>同步计划</span><strong>{{.DNSTargetSummary}}</strong></li>
      {{range .DNSTargets}}<li><span>{{.Config.Name}}{{if not .Config.Enabled}}（停用）{{end}}</span><strong>{{.Config.RecordName}} · {{.FamilyLabel}}</strong></li>{{end}}
      <li><span>地区筛选</span><strong>{{.LocationSummary}}</strong></li>
      <li><span>计划</span><strong>{{.ScheduleSummary}}</strong></li>
      <li><span>下次</span><strong>{{.NextRunAt}}</strong></li>
      <li><span>配置</span><strong>{{if .Stats.ConfigReady}}可执行{{else}}{{.Stats.ConfigHint}}{{end}}</strong></li>
    </ul>
  </section>
</div>

<section class="panel">
  <h2>工作区</h2>
  <div class="workspace-grid">
    <a class="workspace-card" href="/run"><strong>执行中心</strong><span>启动、停止和观察当前任务</span></a>
    <a class="workspace-card" href="/history"><strong>任务历史</strong><span>按任务阅读摘要和日志</span></a>
    <a class="workspace-card" href="/results"><strong>IP 结果</strong><span>集中查看 IPv4 / IPv6 与可用端口</span></a>
    <a class="workspace-card" href="/settings"><strong>项目配置</strong><span>管理扫描、地区、DNS 和定时任务</span></a>
  </div>
</section>

<section class="panel">
  <h2>搜寻记忆</h2>
  <p class="muted">SQLite 会按当前地区、节点模板和测试参数隔离记忆。3 天内成功优先级最高，3–7 天成功作为次级线索；近期失败 IP 和反复失败网段会自动冷却，避免停止后重试又从原处开始。</p>
  <div class="grid">
    <div class="metric"><span>IPv4 真连接经验</span><strong>{{.SearchMemoryIPv4.HotSuccesses}} 个新鲜成功</strong><p class="muted">累计 {{.SearchMemoryIPv4.Successes}} 成功 / {{.SearchMemoryIPv4.Failures}} 失败；冷却 {{.SearchMemoryIPv4.CoolingIPs}} IP / {{.SearchMemoryIPv4.CoolingPrefixes}} 网段</p></div>
    <div class="metric"><span>IPv6 真连接经验</span><strong>{{.SearchMemoryIPv6.HotSuccesses}} 个新鲜成功</strong><p class="muted">累计 {{.SearchMemoryIPv6.Successes}} 成功 / {{.SearchMemoryIPv6.Failures}} 失败；冷却 {{.SearchMemoryIPv6.CoolingIPs}} IP / {{.SearchMemoryIPv6.CoolingPrefixes}} 网段</p></div>
  </div>
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
        <th>TCP RTT</th>
		<th>真连接可用端口</th>
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
		  <td>{{.TrueConnectionText}}</td>
          <td>{{.DataCenter}}{{if .DataCenterCode}} ({{.DataCenterCode}}){{end}}{{if .DataCenterRegion}}<br><span class="muted">{{.DataCenterRegion}}</span>{{end}}</td>
          <td>{{.DurationSeconds}} 秒</td>
          <td>{{.SyncedText}}</td>
          <td>{{.TestedAt}}</td>
        </tr>
      {{else}}
		<tr><td colspan="11" class="muted">暂无数据</td></tr>
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
	  <p class="muted">按工作内容分组展开。日常只需要打开正在调整的一组，不必在同一屏阅读全部配置。</p>
	  <div class="workspace-grid" style="margin-bottom:20px">
	    <a class="workspace-card" href="#cloudflare-config"><strong>Cloudflare 与域名</strong><span>凭据、自动 DNS 目标</span></a>
	    <a class="workspace-card" href="#location-config"><strong>地区策略</strong><span>国家、机房与回退模式</span></a>
	    <a class="workspace-card" href="#scan-config"><strong>扫描与真连接</strong><span>数量、RTT、节点与端口</span></a>
	    <a class="workspace-card" href="#search-memory"><strong>搜索记忆</strong><span>成功网段、冷却、预算与分析</span></a>
	    <a class="workspace-card" href="#schedule-config"><strong>定时与手动 DNS</strong><span>运行周期和独立导入</span></a>
	  </div>
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
	  <form method="post" id="settings-form">
	  <details id="cloudflare-config" open><summary>Cloudflare 凭据与自动 DNS 目标</summary>
    <div class="row" style="justify-content:space-between; align-items:flex-start">
      <div>
        <h2>Cloudflare 凭据库</h2>
        <p class="muted">一份凭据可以复用于多个域名，也可以为某个根域名单独新增凭据。优先使用最小权限 API Token；Global API Key 权限较大，必须同时填写账号邮箱。</p>
      </div>
      <button type="button" class="ghost" id="add-credential">＋ 添加凭据</button>
    </div>
    <div id="credential-list" class="config-card-list">
      {{range .CloudflareCredentials}}
        <section class="config-card credential-card" data-credential-id="{{.Config.ID}}">
          <input type="hidden" name="credential_id" value="{{.Config.ID}}">
          <div class="config-card-head">
            <div><strong>{{.Config.Name}}</strong><div class="muted">{{.SecretStatus}}</div></div>
            <button type="button" class="danger remove-card" data-kind="credential">删除凭据</button>
          </div>
          <div class="grid">
            <div><label>凭据名称</label><input type="text" name="credential_name_{{.Config.ID}}" value="{{.Config.Name}}" placeholder="主账号 / 123go.eu.org"></div>
            <div><label>认证方式</label><select class="credential-auth-type" name="credential_auth_type_{{.Config.ID}}"><option value="api_token" {{if ne .Config.AuthType "global_api_key"}}selected{{end}}>API Token（推荐）</option><option value="global_api_key" {{if eq .Config.AuthType "global_api_key"}}selected{{end}}>Global API Key</option></select></div>
            <div class="api-token-field"><label>API Token <span class="muted">{{.SecretStatus}}</span></label><input type="password" name="credential_api_token_{{.Config.ID}}" autocomplete="new-password" placeholder="留空保留已保存 Token"></div>
            <div class="global-key-field"><label>Global API Key <span class="muted">{{.SecretStatus}}</span></label><input type="password" name="credential_api_key_{{.Config.ID}}" autocomplete="new-password" placeholder="留空保留已保存 Key"></div>
            <div class="global-key-field"><label>Cloudflare 账号邮箱</label><input type="text" name="credential_email_{{.Config.ID}}" value="{{.Config.Email}}" placeholder="name@example.com"></div>
            <div><label>Account ID（可选）</label><input type="text" name="credential_account_id_{{.Config.ID}}" value="{{.Config.AccountID}}"></div>
          </div>
        </section>
      {{else}}
        <div class="empty-card">还没有凭据。请先点击“＋ 添加凭据”。</div>
      {{end}}
    </div>

    <div class="row" style="justify-content:space-between; align-items:flex-start; margin-top:26px">
      <div>
        <h2>DNS 写入目标</h2>
        <p class="muted">取消固定的“单域名/分离域名”模式。每个目标独立选择写入 IPv4 A、IPv6 AAAA 或两者；删除这里只删除本地配置，不会自动删除 Cloudflare 上的现有记录。</p>
      </div>
      <button type="button" class="ghost" id="add-target">＋ 添加域名目标</button>
    </div>
    <div class="metric" style="margin-bottom:12px"><span>当前同步计划</span><strong>{{.DNSTargetSummary}}</strong></div>
    <div id="target-list" class="config-card-list">
      {{range .DNSTargets}}
        {{$target := .Config}}
        <section class="config-card target-card" data-target-id="{{$target.ID}}">
          <input type="hidden" name="target_id" value="{{$target.ID}}">
          <div class="config-card-head">
            <div><strong>{{if $target.Name}}{{$target.Name}}{{else}}未命名目标{{end}}</strong><div class="muted">{{.FamilyLabel}} · {{.CredentialName}}</div></div>
            <button type="button" class="danger remove-card" data-kind="target">删除目标</button>
          </div>
          <label class="checkbox"><input type="checkbox" name="target_enabled_{{$target.ID}}" {{if $target.Enabled}}checked{{end}}> 启用这个 DNS 目标</label>
          <div class="grid">
            <div><label>目标名称</label><input type="text" name="target_name_{{$target.ID}}" value="{{$target.Name}}" placeholder="主线路 / 日本优选"></div>
            <div><label>记录类型</label><select name="target_record_family_{{$target.ID}}"><option value="both" {{if eq $target.RecordFamily "both"}}selected{{end}}>IPv4 A + IPv6 AAAA</option><option value="ipv4" {{if eq $target.RecordFamily "ipv4"}}selected{{end}}>仅 IPv4 A</option><option value="ipv6" {{if eq $target.RecordFamily "ipv6"}}selected{{end}}>仅 IPv6 AAAA</option></select></div>
            <div><label>根域名 / Zone</label><input type="text" name="target_root_domain_{{$target.ID}}" value="{{$target.RootDomain}}" placeholder="123go.eu.org"></div>
            <div><label>Cloudflare Zone ID</label><input type="text" name="target_zone_id_{{$target.ID}}" value="{{$target.ZoneID}}"></div>
            <div><label>目标完整域名</label><input type="text" name="target_record_name_{{$target.ID}}" value="{{$target.RecordName}}" placeholder="speed.123go.eu.org"></div>
            <div><label>使用凭据</label><select name="target_credential_id_{{$target.ID}}"><option value="">请选择凭据</option>{{range $.CloudflareCredentials}}<option value="{{.Config.ID}}" {{if eq .Config.ID $target.CredentialID}}selected{{end}}>{{.Config.Name}}</option>{{end}}</select></div>
          </div>
        </section>
      {{else}}
        <div class="empty-card">还没有 DNS 目标。添加凭据后，再点击“＋ 添加域名目标”。</div>
      {{end}}
    </div>

    <template id="credential-template"><section class="config-card credential-card" data-credential-id="__ID__"><input type="hidden" name="credential_id" value="__ID__"><div class="config-card-head"><div><strong>新 Cloudflare 凭据</strong><div class="muted">尚未保存密钥</div></div><button type="button" class="danger remove-card" data-kind="credential">删除凭据</button></div><div class="grid"><div><label>凭据名称</label><input type="text" name="credential_name___ID__" placeholder="主账号 / 123go.eu.org"></div><div><label>认证方式</label><select class="credential-auth-type" name="credential_auth_type___ID__"><option value="api_token">API Token（推荐）</option><option value="global_api_key">Global API Key</option></select></div><div class="api-token-field"><label>API Token</label><input type="password" name="credential_api_token___ID__" autocomplete="new-password"></div><div class="global-key-field"><label>Global API Key</label><input type="password" name="credential_api_key___ID__" autocomplete="new-password"></div><div class="global-key-field"><label>Cloudflare 账号邮箱</label><input type="text" name="credential_email___ID__" placeholder="name@example.com"></div><div><label>Account ID（可选）</label><input type="text" name="credential_account_id___ID__"></div></div></section></template>
    <template id="target-template"><section class="config-card target-card" data-target-id="__ID__"><input type="hidden" name="target_id" value="__ID__"><div class="config-card-head"><div><strong>新 DNS 目标</strong><div class="muted">独立选择域名、记录类型和凭据</div></div><button type="button" class="danger remove-card" data-kind="target">删除目标</button></div><label class="checkbox"><input type="checkbox" name="target_enabled___ID__" checked> 启用这个 DNS 目标</label><div class="grid"><div><label>目标名称</label><input type="text" name="target_name___ID__" placeholder="主线路 / 日本优选"></div><div><label>记录类型</label><select name="target_record_family___ID__"><option value="both">IPv4 A + IPv6 AAAA</option><option value="ipv4">仅 IPv4 A</option><option value="ipv6">仅 IPv6 AAAA</option></select></div><div><label>根域名 / Zone</label><input type="text" name="target_root_domain___ID__" placeholder="123go.eu.org"></div><div><label>Cloudflare Zone ID</label><input type="text" name="target_zone_id___ID__"></div><div><label>目标完整域名</label><input type="text" name="target_record_name___ID__" placeholder="speed.123go.eu.org"></div><div><label>使用凭据</label><select name="target_credential_id___ID__"><option value="">请选择凭据</option>{{range .CloudflareCredentials}}<option value="{{.Config.ID}}">{{.Config.Name}}</option>{{end}}</select></div></div></section></template>
	  </details>

	<details id="location-config"><summary>地区与实测机房筛选</summary>
    <h2 style="margin-top:18px">地区筛选</h2>
    <p class="muted">候选 IPv4 / IPv6 始终来自原版 better-cloudflare-ip 的 Cloudflare Anycast 地址池。地区扫描会先复测同地区历史成功 IP，再从其 IPv4 <code>/24</code> 或 IPv6 <code>/48</code> 子网扩展新 IP，同时无重复遍历原版全局池。结果仍按响应头 <code>CF-RAY</code> 的实测机房筛选，不把 GeoFeed 地理标签网段误当成可测 CDN 地址池。</p>
    <div class="row" style="margin-bottom:12px">
      {{if .GeoDatabase.Ready}}
        <span class="muted">数据库已就绪：{{.GeoDatabase.LocationCount}} 个 Cloudflare 响应机房，更新于 {{.GeoDatabase.UpdatedAt}}</span>
      {{else}}
        <span class="muted">Cloudflare 响应机房数据库尚未下载，请先点击更新。</span>
      {{end}}
      <button type="submit" class="ghost" formaction="/settings/geo-refresh" formmethod="post">更新地区 / 机房数据库</button>
    </div>
    <div class="grid">
      <label class="metric checkbox">
        <input type="radio" name="location_mode" value="any" {{if eq .Settings.LocationMode "any"}}checked{{end}}>
        <span><strong>全局随机</strong><br><span class="muted">忽略下方国家、区域和城市，从全球 IP 池随机抽取。</span></span>
      </label>
      <label class="metric checkbox">
        <input type="radio" name="location_mode" value="prefer" {{if eq .Settings.LocationMode "prefer"}}checked{{end}}>
        <span><strong>所选实测机房优先</strong><br><span class="muted">先只接受 <code>CF-RAY</code> 实测落在所选机房的 IP；连续 10 分钟没有结果才回退全球。</span></span>
      </label>
      <label class="metric checkbox">
        <input type="radio" name="location_mode" value="strict" {{if eq .Settings.LocationMode "strict"}}checked{{end}}>
        <span><strong>仅接受所选实测机房</strong><br><span class="muted">候选仍来自原版全局池，但只接受 <code>CF-RAY</code> 落在所选机房的结果，绝不回退；连续 {{.FamilyNoResultLimit}} 无新增结果则失败。</span></span>
      </label>
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
        <label for="location-region">Cloudflare 大区</label>
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
    <div class="metric" id="geo-selection-status" style="margin-top:12px">
      <span id="geo-save-state">当前已保存配置</span>
      <strong id="geo-effective-summary">{{.LocationSummary}}</strong>
      <p id="geo-prefix-counts">当前选择匹配 {{.GeoFilterStats.DataCenterCount}} 个实际响应机房{{if .GeoFilterStats.Codes}}：{{.GeoFilterStats.Codes}}{{end}}。</p>
      <p class="muted">Anycast IP 本身没有固定的“日本 IP”或“广州 IP”；同一 IP 从不同网络出发可能到达不同机房。这里筛选的是当前 VPS 实际请求后 <code>CF-RAY</code> 报告的响应机房。</p>
      <button type="submit">保存地区筛选和全部配置</button>
    </div>
    <div id="geo-location-source" hidden>
      {{range .GeoLocations}}<span data-country="{{.Country}}" data-region="{{.Region}}" data-city="{{.City}}" data-code="{{.IATA}}"></span>{{end}}
    </div>
	</details>

	<details id="scan-config" open><summary>扫描参数与真连接测试</summary>
    <h2 style="margin-top:18px">扫描参数</h2>
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
        <label>最大 TCP RTT (ms)</label>
        <input type="number" name="max_rtt_ms" min="10" max="2000" value="{{.Settings.MaxRTTMs}}">
        <span class="muted">默认 200ms；测速前后各采样 3 次，任意样本超限即淘汰。</span>
      </div>
      <div>
        <label>定时运行时间</label>
        <input type="time" name="schedule_time" value="{{.Settings.ScheduleTime}}">
      </div>
    </div>
    <label class="checkbox"><input type="checkbox" name="use_tls" {{if .Settings.UseTLS}}checked{{end}}> 使用 TLS 测速</label>

    <div class="subsection" style="margin-top:22px">
      <h2>真连接测试（可选）</h2>
      <p class="muted">这是节点协议的端到端可用性检查，不是带宽测速。系统会把扫描到的候选 IP 替换进你提供的节点，启动临时 Xray 核心并访问测试地址；只有至少一个所选端口真正响应的 IP 才会保留。</p>
      <div class="grid">
        <label class="metric checkbox">
          <input type="checkbox" name="true_connection_ipv4" {{if .Settings.TrueConnectionIPv4}}checked{{end}}>
          <span><strong>IPv4 执行真连接</strong><br><span class="muted">对每个 IPv4 候选执行所选 HTTP/HTTPS 端口测试。</span></span>
        </label>
        <label class="metric checkbox">
          <input type="checkbox" name="true_connection_ipv6" {{if .Settings.TrueConnectionIPv6}}checked{{end}}>
          <span><strong>IPv6 执行真连接</strong><br><span class="muted">可独立启用；不勾选时 IPv6 保持原扫描流程。</span></span>
        </label>
      </div>
      <div class="grid">
        <label class="metric checkbox">
          <input type="checkbox" name="true_connection_http" {{if .Settings.TrueConnectionHTTP}}checked{{end}}>
          <span><strong>测试 HTTP / 非 TLS</strong><br><span class="muted">完整测试端口：80、8080、8880、2052、2082、2086、2095。</span></span>
        </label>
        <label class="metric checkbox">
          <input type="checkbox" name="true_connection_https" {{if .Settings.TrueConnectionHTTPS}}checked{{end}}>
          <span><strong>测试 HTTPS / TLS</strong><br><span class="muted">完整测试端口：443、2053、2083、2087、2096、8443。</span></span>
        </label>
      </div>
      <div class="grid">
        <div>
          <label>HTTP 节点模板（vmess:// 或 vless://）</label>
          <textarea name="true_connection_http_node" rows="5" placeholder="粘贴一条 HTTP / 非 TLS 节点；留空保留已保存模板"></textarea>
          <span class="muted">{{if .Settings.TrueConnectionHTTPNode}}HTTP 模板已安全保存；此处不回显 UUID。{{else}}尚未保存 HTTP 模板。{{end}}</span>
        </div>
        <div>
          <label>HTTPS 节点模板（vmess:// 或 vless://）</label>
          <textarea name="true_connection_https_node" rows="5" placeholder="粘贴一条 HTTPS / TLS 节点；留空保留已保存模板"></textarea>
          <span class="muted">{{if .Settings.TrueConnectionHTTPSNode}}HTTPS 模板已安全保存；此处不回显 UUID。{{else}}尚未保存 HTTPS 模板。{{end}}</span>
        </div>
      </div>
      <label>真连接访问地址</label>
      <input type="url" name="true_connection_test_url" value="{{.Settings.TrueConnectionTestURL}}" required>
	  <label>测试出口 / 运营商标签</label>
	  <input type="text" name="search_network_label" value="{{.Settings.SearchNetworkLabel}}" maxlength="80" placeholder="213 VPS / 中国移动 / 广州电信">
	  <p class="muted">这是分析标签，不改变网络路由。以后从不同 VPS 或运营商出口运行时，用不同标签即可分开比较端口成功率。</p>
      <p class="muted">默认访问 Google <code>generate_204</code>。HTTP 和 HTTPS 会分别使用各自节点模板；系统会测完该协议组全部端口，并记录每个能通端口及真实响应延迟。连续 {{.FamilyNoResultLimit}} 没有获得合格 IP 时，沿用现有保护逻辑停止当前协议族并继续下一个协议族。</p>
    </div>
	</details>

	<details id="schedule-config"><summary>定时任务</summary>
    <h2 style="margin-top:18px">定时任务</h2>
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
	</details>
    <p class="row"><button type="submit">保存配置</button><a class="button" href="/dashboard">返回 Dashboard</a></p>
  </form>
</section>
<section class="panel" id="search-memory">
  <h2>搜索记忆与自适应分析</h2>
  <p class="muted">每个配置档案按 IP 版本、地区、真连接节点、测试门槛和出口标签隔离。覆盖率按“近 7 天已测试唯一 IP ÷ 活跃窄网段的 256 个采样槽”计算，用来判断是否一直重复在很小范围内。</p>
  <div class="config-card-list">
    {{range .SearchMemoryProfiles}}
      {{$insight := .Insight}}
      {{$profile := .Insight.Profile}}
      <details class="config-card" {{if .Current}}open{{end}}>
        <summary>{{.Label}} {{if .Current}}· 当前配置{{end}}</summary>
        <div style="margin-top:14px">
        <div class="config-card-head">
          <div>
            <strong>{{.Label}}</strong>
            <div class="muted">档案 <code>{{$insight.ID}}</code> {{if .Current}}<span class="tag">当前配置</span>{{end}}</div>
          </div>
          <form method="post" action="/search-memory/profile/clear" onsubmit="return window.confirm('确认清除这个配置档案的全部搜索记忆、端口统计和手动优先网段？此操作不会删除 DNS 或任务结果。')">
            <input type="hidden" name="profile_id" value="{{$insight.ID}}">
            <button type="submit" class="danger">清除档案记忆</button>
          </form>
        </div>
        <div class="grid">
          <div class="metric"><span>近 7 天覆盖</span><strong>{{$insight.RecentUniqueIPs}} IP / {{$insight.RecentPrefixes}} 网段</strong><p class="muted">采样覆盖率 {{printf "%.2f" $insight.CoveragePercent}}%</p></div>
          <div class="metric"><span>真连接成功 / 失败</span><strong>{{$insight.Summary.Successes}} / {{$insight.Summary.Failures}}</strong><p class="muted">地区命中 {{$insight.Summary.RegionMatches}} · 带宽达标 {{$insight.Summary.BandwidthPasses}} / 未达标 {{$insight.Summary.BandwidthFails}}</p></div>
          <div class="metric"><span>当前自适应预算</span><strong>精确 {{$insight.Budget.Exact}}% · 窄 {{$insight.Budget.Narrow}}%</strong><p class="muted">父网段 {{$insight.Budget.Wide}}% · 全局 {{$insight.Budget.Global}}%</p></div>
          <div class="metric"><span>出口 / 运营商</span><strong>{{if $profile.NetworkLabel}}{{$profile.NetworkLabel}}{{else}}未标记{{end}}</strong><p class="muted">{{.ModeLabel}}</p></div>
        </div>

        <div class="subsection" style="margin-top:16px">
          <div class="row" style="justify-content:space-between; align-items:flex-end">
            <div><strong>手动优先种子与父网段</strong><div class="muted">IPv4 只接受 <code>/16</code>，IPv6 只接受 <code>/32</code>。建议输入已知可用 IP，例如 <code>172.66.130.219/16</code>：系统会保留种子 IP，并推导 <code>172.66.130.0/24</code> 与 <code>172.66.0.0/16</code>。</div></div>
            <form method="post" action="/search-memory/prefix/add" class="row">
              <input type="hidden" name="profile_id" value="{{$insight.ID}}">
              <input type="hidden" name="ip_version" value="{{$profile.IPVersion}}">
              <input type="text" name="prefix" style="width:220px" required placeholder="{{if eq $profile.IPVersion 4}}172.66.0.0/16{{else}}2606:4700::/32{{end}}">
              <button type="submit">加入优先网段</button>
            </form>
          </div>
          <div class="row" style="margin-top:10px">
            {{range $insight.ManualPriorities}}
              <form method="post" action="/search-memory/prefix/delete" class="row">
				<input type="hidden" name="profile_id" value="{{$insight.ID}}"><input type="hidden" name="prefix" value="{{.Prefix}}">
				<code>{{.Prefix}}</code>{{if .SeedIP}}<span class="tag">种子 {{.SeedIP}}</span><span class="tag">深挖 {{.NarrowPrefix}}</span>{{end}}<button type="submit" class="ghost">移除</button>
              </form>
            {{else}}<span class="muted">尚未添加手动优先网段。</span>{{end}}
          </div>
        </div>

        <details>
          <summary>成功网段与冷却网段</summary>
          <h3 class="section-title">近 7 天成功网段</h3>
          <div class="table-wrap"><table style="min-width:620px"><thead><tr><th>网段</th><th>成功</th><th>失败</th><th>成功率</th><th>最近观察</th></tr></thead><tbody>
          {{range $insight.SuccessfulPrefixes}}<tr><td><code>{{.Prefix}}</code></td><td>{{.Successes}}</td><td>{{.Failures}}</td><td>{{printf "%.1f" .SuccessRate}}%</td><td>{{.LastSeenAt}}</td></tr>{{else}}<tr><td colspan="5" class="muted">还没有通过真连接的成功网段。</td></tr>{{end}}
          </tbody></table></div>
          <h3 class="section-title">当前冷却网段</h3>
          <div class="row">{{range $insight.CoolingPrefixes}}<code>{{.}}</code>{{else}}<span class="muted">没有处于冷却期的网段。</span>{{end}}</div>
        </details>

        <details>
          <summary>端口与出口成功率</summary>
          <div class="table-wrap"><table style="min-width:620px"><thead><tr><th>出口标签</th><th>协议</th><th>端口</th><th>成功 / 尝试</th><th>成功率</th><th>平均延迟</th></tr></thead><tbody>
          {{range $insight.Ports}}<tr><td>{{if $profile.NetworkLabel}}{{$profile.NetworkLabel}}{{else}}未标记{{end}}</td><td>{{.Scheme}}</td><td>{{.Port}}</td><td>{{.Successes}} / {{.Attempts}}</td><td>{{printf "%.1f" .SuccessRate}}%</td><td>{{if .AvgLatencyMs}}{{.AvgLatencyMs}} ms{{else}}—{{end}}</td></tr>{{else}}<tr><td colspan="6" class="muted">还没有逐端口真连接样本。</td></tr>{{end}}
          </tbody></table></div>
        </details>

        <details>
          <summary>HTTP 可用但 HTTPS 不可用的网段特征</summary>
          <p class="muted">只有同一配置同时测试 HTTP 和 HTTPS 时才分析；HTTP 至少一个端口成功、HTTPS 已测试但全部失败才会列出。</p>
          <div class="table-wrap"><table style="min-width:620px"><thead><tr><th>网段</th><th>HTTP 成功 IP</th><th>HTTP 尝试</th><th>HTTPS 失败尝试</th><th>最近观察</th></tr></thead><tbody>
          {{range $insight.HTTPOnlyPrefixes}}<tr><td><code>{{.Prefix}}</code></td><td>{{.HTTPIPs}}</td><td>{{.HTTPAttempts}}</td><td>{{.HTTPSAttempts}}</td><td>{{.LastSeenAt}}</td></tr>{{else}}<tr><td colspan="5" class="muted">当前没有足够样本或尚未发现这种网段。</td></tr>{{end}}
          </tbody></table></div>
        </details>
        </div>
      </details>
    {{else}}
      <div class="empty-card">还没有搜索记忆配置档案。打开本页或执行一次任务后会自动建立。</div>
    {{end}}
  </div>
</section>
<section class="panel" id="manual-dns">
  <div class="row" style="justify-content:space-between; align-items:flex-start">
    <div>
      <h2>手动 DNS 目标</h2>
      <p class="muted">手动目标与上面的自动扫描目标完全分离：不参与立即执行、定时任务或 auto update。粘贴 <code>vmess://</code> 或 <code>vless://</code> 分享链接后，系统只提取服务器 IP；不会使用 SNI、Host、备注或其他字段里的地址。</p>
    </div>
  </div>
  <div class="subsection" style="border-top:0; padding-top:0">
    <h3>＋ 添加手动目标</h3>
    <form method="post" action="/manual-dns/targets/add">
      <div class="grid">
        <div><label>目标名称</label><input type="text" name="manual_name" placeholder="newspeedv4" required></div>
        <div><label>根域名 / Zone</label><input type="text" name="manual_root_domain" placeholder="123go.eu.org" required></div>
        <div><label>Cloudflare Zone ID</label><input type="text" name="manual_zone_id" required></div>
        <div><label>目标完整域名</label><input type="text" name="manual_record_name" placeholder="newspeedv4.123go.eu.org" required></div>
        <div><label>使用已保存凭据</label><select name="manual_credential_id" required><option value="">请选择凭据</option>{{range .CloudflareCredentials}}<option value="{{.Config.ID}}">{{.Config.Name}}</option>{{end}}</select></div>
      </div>
      <p class="muted">手动域名不能与任何自动 DNS 目标重复，防止后续被扫描任务覆盖。</p>
      <button type="submit">添加手动目标</button>
    </form>
  </div>
  <div class="config-card-list">
    {{range .ManualDNSTargets}}
      {{$manual := .Config}}
      <section class="config-card manual-target-card" data-credential-id="{{$manual.CredentialID}}">
        <div class="config-card-head">
          <div>
            <strong>{{$manual.Name}}</strong>
            <div class="muted"><code>{{$manual.RecordName}}</code> · {{.CredentialName}} · 绝不参与自动扫描</div>
          </div>
        </div>
        <div class="metric-grid" style="margin-bottom:12px">
          <div class="metric"><span>上次手动写入</span><strong>A {{$manual.LastIPv4Count}} · AAAA {{$manual.LastIPv6Count}}</strong></div>
          <div class="metric"><span>上次操作时间</span><strong>{{if $manual.LastUpdatedAt}}{{$manual.LastUpdatedAt}}{{else}}尚未写入{{end}}</strong></div>
        </div>
        <form method="post" action="/manual-dns/targets/update">
          <input type="hidden" name="target_id" value="{{$manual.ID}}">
          <div class="row" style="justify-content:space-between; align-items:center">
            <label style="margin:0">导入 vmess / vless 分享链接（每行一个，合计最多 500 个）</label>
            <strong class="share-parse-status muted" aria-live="polite">尚未粘贴分享链接</strong>
          </div>
          <textarea class="share-links-input" name="share_links" rows="8" required placeholder="vmess://...&#10;vless://..."></textarea>
          <p class="muted">点击更新后，系统会去重并完整替换 <code>{{$manual.RecordName}}</code> 的 A/AAAA。本次没有 IPv6 时，原有 AAAA 也会被清空；没有 IPv4 时同理。</p>
          <button type="submit">解析 IP 并手动更新 DNS</button>
        </form>
        <div class="row" style="margin-top:14px">
          <form method="post" action="/manual-dns/targets/clear" onsubmit="return window.confirm('确认删除这个完整域名下的全部 A 和 AAAA 记录？手动目标配置会保留。')">
            <input type="hidden" name="target_id" value="{{$manual.ID}}">
            <button type="submit" class="danger">清空全部 A/AAAA</button>
          </form>
          <form method="post" action="/manual-dns/targets/delete" onsubmit="return window.confirm('确认先清空这个域名的全部 A/AAAA，然后删除本地手动目标配置？')">
            <input type="hidden" name="target_id" value="{{$manual.ID}}">
            <button type="submit" class="ghost">清空并删除目标</button>
          </form>
        </div>
      </section>
    {{else}}
      <div class="empty-card">还没有手动 DNS 目标。添加后才会显示 vmess 导入框。</div>
    {{end}}
  </div>
</section>
<script>
  (function () {
    var credentialList = document.getElementById("credential-list");
    var targetList = document.getElementById("target-list");
    var credentialTemplate = document.getElementById("credential-template");
    var targetTemplate = document.getElementById("target-template");
    function newID(prefix) {
      return prefix + "-" + Date.now().toString(36) + "-" + Math.random().toString(36).slice(2, 10);
    }
    function removeEmptyState(list) {
      var empty = list && list.querySelector(".empty-card");
      if (empty) empty.remove();
    }
    function refreshAuthFields(card) {
      var select = card.querySelector(".credential-auth-type");
      if (!select) return;
      var globalKey = select.value === "global_api_key";
      Array.prototype.forEach.call(card.querySelectorAll(".global-key-field"), function (field) { field.hidden = !globalKey; });
      Array.prototype.forEach.call(card.querySelectorAll(".api-token-field"), function (field) { field.hidden = globalKey; });
    }
    function bindCard(card) {
      var auth = card.querySelector(".credential-auth-type");
      if (auth) {
        auth.addEventListener("change", function () { refreshAuthFields(card); });
        refreshAuthFields(card);
      }
      var remove = card.querySelector(".remove-card");
      if (remove) remove.addEventListener("click", function () {
        if (remove.dataset.kind === "credential") {
          var credentialIDInput = card.querySelector('[name="credential_id"]');
          var credentialID = credentialIDInput ? credentialIDInput.value : "";
          var referenced = Array.prototype.some.call(document.querySelectorAll('.target-card select[name^="target_credential_id_"]'), function (select) {
            return select.value === credentialID;
          });
          referenced = referenced || Array.prototype.some.call(document.querySelectorAll('.manual-target-card[data-credential-id]'), function (manualCard) {
            return manualCard.dataset.credentialId === credentialID;
          });
          if (referenced) {
            window.alert("这份凭据仍被 DNS 目标引用（包括已停用目标）。请先重新绑定或删除相关目标。");
            return;
          }
        }
        var warning = remove.dataset.kind === "target"
          ? "只会删除本地目标配置，不会删除 Cloudflare 上现有 DNS。确认移除？"
		  : "确认移除这份未被引用的凭据？";
        if (window.confirm(warning)) {
          card.remove();
          if (remove.dataset.kind === "credential") refreshAllTargetCredentialSelects();
        }
      });
      var credentialName = card.querySelector('input[name^="credential_name_"]');
      if (credentialName) credentialName.addEventListener("input", refreshAllTargetCredentialSelects);
    }
    function refreshTargetCredentialSelect(card) {
      var select = card.querySelector('select[name^="target_credential_id_"]');
      if (!select) return;
      var selected = select.value;
      select.innerHTML = '<option value="">请选择凭据</option>';
      Array.prototype.forEach.call(document.querySelectorAll(".credential-card"), function (credentialCard) {
        var idInput = credentialCard.querySelector('[name="credential_id"]');
        var nameInput = idInput && credentialCard.querySelector('[name="credential_name_' + idInput.value + '"]');
        if (!idInput) return;
        var option = document.createElement("option");
        option.value = idInput.value;
        option.textContent = nameInput && nameInput.value ? nameInput.value : "未命名凭据";
        select.appendChild(option);
      });
      select.value = selected;
    }
    function refreshAllTargetCredentialSelects() {
      Array.prototype.forEach.call(document.querySelectorAll(".target-card"), refreshTargetCredentialSelect);
    }
    Array.prototype.forEach.call(document.querySelectorAll(".config-card"), bindCard);
    var addCredential = document.getElementById("add-credential");
    if (addCredential) addCredential.addEventListener("click", function () {
      var id = newID("credential");
      removeEmptyState(credentialList);
      credentialList.insertAdjacentHTML("beforeend", credentialTemplate.innerHTML.replaceAll("__ID__", id));
      bindCard(credentialList.lastElementChild);
      refreshAllTargetCredentialSelects();
    });
    var addTarget = document.getElementById("add-target");
    if (addTarget) addTarget.addEventListener("click", function () {
      if (!document.querySelector('[name="credential_id"]')) {
        window.alert("请先添加至少一份 Cloudflare 凭据。");
        return;
      }
      var id = newID("target");
      removeEmptyState(targetList);
      targetList.insertAdjacentHTML("beforeend", targetTemplate.innerHTML.replaceAll("__ID__", id));
      refreshTargetCredentialSelect(targetList.lastElementChild);
      bindCard(targetList.lastElementChild);
    });
  })();
  (function () {
    function normalizeIP(value) {
      var raw = String(value || "").trim();
      var parts = raw.split(".");
      if (parts.length === 4 && parts.every(function (part) {
        return /^\d{1,3}$/.test(part) && Number(part) >= 0 && Number(part) <= 255;
      })) return parts.map(function (part) { return String(Number(part)); }).join(".");
      var unwrapped = raw.replace(/^\[|\]$/g, "");
      if (unwrapped.indexOf(":") < 0 || unwrapped.indexOf("%") >= 0) return "";
      try {
        var hostname = new URL("http://[" + unwrapped + "]/" ).hostname;
        return hostname.replace(/^\[|\]$/g, "").toLowerCase();
      } catch (_) {
        return "";
      }
    }
    function decodeVmessAddress(link) {
      try {
        var payload = link.slice(8).replace(/-/g, "+").replace(/_/g, "/");
        while (payload.length % 4) payload += "=";
        var bytes = Uint8Array.from(atob(payload), function (character) { return character.charCodeAt(0); });
        var decoded = typeof TextDecoder === "function" ? new TextDecoder().decode(bytes) : decodeURIComponent(escape(String.fromCharCode.apply(null, bytes)));
        return JSON.parse(decoded).add || "";
      } catch (_) {
        return "";
      }
    }
    function inspectShareLinks(value) {
      var links = String(value || "").match(/(?:vmess:\/\/[A-Za-z0-9+\/_=-]+|vless:\/\/[^\s]+)/gi) || [];
      var ipv4 = {};
      var ipv6 = {};
      var invalid = 0;
      links.forEach(function (link) {
        var address = "";
        if (link.toLowerCase().indexOf("vmess://") === 0) {
          address = decodeVmessAddress(link);
        } else {
          try { address = new URL(link).hostname.replace(/^\[|\]$/g, ""); } catch (_) {}
        }
        var normalized = normalizeIP(address);
        if (!normalized) {
          invalid += 1;
        } else if (normalized.indexOf(":") >= 0) {
          ipv6[normalized] = true;
        } else {
          ipv4[normalized] = true;
        }
      });
      return {links: links.length, ipv4: Object.keys(ipv4).length, ipv6: Object.keys(ipv6).length, invalid: invalid};
    }
    Array.prototype.forEach.call(document.querySelectorAll(".manual-target-card"), function (card) {
      var input = card.querySelector(".share-links-input");
      var status = card.querySelector(".share-parse-status");
      if (!input || !status) return;
      var timer;
      function refresh() {
        var result = inspectShareLinks(input.value);
        if (!result.links) {
          status.textContent = "尚未识别到分享链接";
          status.style.color = "#6b7280";
          return;
        }
        var total = result.ipv4 + result.ipv6;
        status.textContent = "识别到 " + total + " 个唯一 IP（IPv4 " + result.ipv4 + "，IPv6 " + result.ipv6 + "）";
        if (result.invalid) status.textContent += "；无效 " + result.invalid + " 条";
        if (result.links > 500) status.textContent += "；超过 500 条上限";
        status.style.color = result.invalid || result.links > 500 ? "#b45309" : "#047857";
      }
      input.addEventListener("input", function () {
        window.clearTimeout(timer);
        timer = window.setTimeout(refresh, 120);
      });
      refresh();
    });
  })();
  (function () {
    var country = document.getElementById("location-country");
    var region = document.getElementById("location-region");
    var city = document.getElementById("location-city");
    var modeInputs = Array.prototype.slice.call(document.querySelectorAll('input[name="location_mode"]'));
    var saveState = document.getElementById("geo-save-state");
    var effectiveSummary = document.getElementById("geo-effective-summary");
    var prefixCounts = document.getElementById("geo-prefix-counts");
    var source = Array.prototype.map.call(document.querySelectorAll("#geo-location-source span"), function (node) {
      return {
        country: node.dataset.country,
        region: node.dataset.region,
        city: node.dataset.city,
        code: node.dataset.code
      };
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

    function selectedMode() {
      var selected = modeInputs.filter(function (input) { return input.checked; })[0];
      return selected ? selected.value : "any";
    }

    function selectedPath() {
      return [country.value, region.value, city.value].filter(Boolean).join(" / ");
    }

    function switchGlobalSelectionToStrict() {
      if (!selectedPath() || selectedMode() !== "any") return;
      var strict = modeInputs.filter(function (input) { return input.value === "strict"; })[0];
      if (strict) strict.checked = true;
    }

    function updateSelectionStatus(changed) {
      var path = selectedPath();
      var mode = selectedMode();
	  var matches = source.filter(function (item) {
        return (!country.value || item.country === country.value) &&
          (!region.value || item.region === region.value) &&
          (!city.value || item.city === city.value);
      });
      if (prefixCounts) {
		var codes = matches.map(function (item) { return item.code; }).filter(Boolean).sort();
        prefixCounts.textContent = "当前选择匹配 " + matches.length + " 个实际响应机房" + (codes.length ? "：" + codes.join(" / ") : "") + "。";
      }
      if (effectiveSummary) {
        if (mode === "any") {
          effectiveSummary.textContent = path
            ? "全局随机；已选择的 " + path + " 当前不会参与筛选"
            : "全局随机；从全球 IP 池抽取";
        } else if (mode === "prefer") {
		  effectiveSummary.textContent = "所选实测机房优先：" + (path || "尚未选择地区") + "；按 CF-RAY 判定；10 分钟无结果后回退全球";
        } else {
		  effectiveSummary.textContent = "仅接受所选实测机房：" + (path || "尚未选择地区") + "；按 CF-RAY 判定；绝不回退全球";
        }
      }
      if (changed && saveState) {
        saveState.textContent = "页面配置已修改但尚未保存；立即执行和定时任务仍会使用上一次已保存配置";
        saveState.style.color = "#b45309";
      }
    }

    country.addEventListener("change", function () {
      refresh(true, true);
      switchGlobalSelectionToStrict();
      updateSelectionStatus(true);
    });
    region.addEventListener("change", function () {
      refresh(false, true);
      switchGlobalSelectionToStrict();
      updateSelectionStatus(true);
    });
    city.addEventListener("change", function () {
      switchGlobalSelectionToStrict();
      updateSelectionStatus(true);
    });
    modeInputs.forEach(function (input) {
      input.addEventListener("change", function () { updateSelectionStatus(true); });
    });
    updateSelectionStatus(false);
  })();
</script>
{{end}}
`

const runTemplate = `
{{define "content"}}
<section class="panel">
  <h1>执行中心</h1>
  <p class="muted">这里只负责启动、停止和观察当前任务。历史任务与完整日志请到“任务历史”阅读。</p>
  <div class="row" style="margin-bottom:16px">
    <form action="/runs/start" method="post" style="display:inline"><button type="submit">立即执行</button></form>
    {{if .CanResumeRun}}<form action="/runs/resume" method="post" style="display:inline"><button type="submit">继续执行</button></form>{{end}}
    {{if .CurrentRun}}<form action="/runs/stop" method="post" style="display:inline"><input type="hidden" name="id" value="{{.CurrentRun.ID}}"><button class="danger" type="submit">停止任务</button></form>{{end}}
    <a class="button" href="/settings">调整定时设置</a>
  </div>
  {{if .CurrentRun}}
    <p class="muted">当前阶段：{{.CurrentRun.Stage}}</p>
    <div class="progress"><div style="width: {{.CurrentRun.Progress}}%"></div></div>
  {{else}}
    <div class="empty-card">当前没有运行中的任务。可以立即执行，或到任务历史查看以前的记录。</div>
  {{end}}
  <div class="grid">
    <div class="metric"><span>今日更新 IP</span><strong>{{.Stats.TodayUpdatedIPs}}</strong></div>
    <div class="metric"><span>今日已由 Cloudflare 核验的记录</span><strong>{{.Stats.TodaySyncedIPs}}</strong></div>
    <div class="metric"><span>今日任务</span><strong>{{.Stats.TodayTaskCount}}</strong></div>
    <div class="metric"><span>定时策略</span><strong>{{.ScheduleSummary}}</strong></div>
    <div class="metric"><span>地区筛选</span><strong>{{.LocationSummary}}</strong></div>
  </div>
</section>
<section class="panel">
  <h2>当前任务实时日志</h2>
  {{if .CurrentRun}}
    <p class="muted">下面先列出本轮冻结的实际执行计划，再显示进度和日志；配置页尚未保存的修改不会影响当前任务。</p>
  {{end}}
  {{template "runs" .}}
</section>
{{end}}
`

const historyTemplate = `
{{define "content"}}
<section class="panel">
  <div class="row" style="justify-content:space-between; align-items:flex-start">
    <div>
      <h1>任务历史</h1>
      <p class="muted">这里保留紧凑摘要；点击“查看详情”才展开某次任务的配置、端口诊断和完整日志。</p>
    </div>
    <a class="button" href="/run">返回执行中心</a>
  </div>
  {{if .RecentRuns}}
  <div class="table-wrap">
    <table style="min-width:760px">
      <thead><tr><th>时间</th><th>触发</th><th>状态</th><th>阶段</th><th>通过筛选 IP</th><th>Cloudflare 已核验记录</th><th>操作</th></tr></thead>
      <tbody>{{range .RecentRuns}}
        <tr>
          <td>{{.StartedAt}}</td>
          <td>{{if eq .Trigger "scheduled"}}定时{{else if eq .Trigger "resume"}}续接{{else}}手动{{end}}</td>
          <td><strong class="status-{{.Status}}">{{.Status}}</strong></td>
          <td>{{.Stage}}</td>
          <td>{{.UpdatedIPCount}} / {{.RequiredIPCount}}</td>
          <td>{{.SyncedIPCount}} / {{if .RequiredDNSRecordCount}}{{.RequiredDNSRecordCount}}{{else}}{{.RequiredIPCount}}{{end}}</td>
          <td><a class="button" href="/run/detail?id={{.ID}}">查看详情</a></td>
        </tr>
      {{end}}</tbody>
    </table>
  </div>
  {{else}}<div class="empty-card">还没有执行记录。</div>{{end}}
</section>
{{end}}
`

const runDetailTemplate = `
{{define "content"}}
<section class="panel">
  <div class="row" style="justify-content:space-between; align-items:flex-start">
    <div><h1>任务详情</h1><p class="muted">单次任务的冻结配置、同步结果和完整运行日志。</p></div>
    <div class="row"><a class="button" href="/history">返回任务历史</a><a class="button" href="/run">执行中心</a></div>
  </div>
  {{template "runs" .}}
</section>
{{end}}
`

const resultsPageTemplate = `
{{define "content"}}
<section class="panel">
  <div class="row" style="justify-content:space-between; align-items:flex-start">
    <div><h1>IP 结果</h1><p class="muted">结果与执行日志分开显示，集中查看已发现 IP、真实延迟、带宽和可通端口。</p></div>
    <a class="button" href="/run">执行中心</a>
  </div>
</section>
{{template "ipResultPanel" .}}
{{end}}
`

const runsTemplate = `
{{define "runPlan"}}
  {{if .Available}}
    <section class="run-plan">
      <h3>本轮实际执行计划</h3>
      <div class="grid">
        <div class="metric"><span>扫描协议与数量</span><strong>{{.ScanText}}</strong></div>
        <div class="metric"><span>真连接准入条件</span><strong>{{.TrueConnectionText}}</strong></div>
        <div class="metric"><span>本轮 DNS 写入与复核</span><strong>{{.DNSHeadline}}</strong></div>
      </div>
      {{if .SearchFamilies}}
        <strong class="section-title">本轮冻结的候选来源与优先网段</strong>
        <div class="grid">
          {{range .SearchFamilies}}
            <div class="metric">
              <span>IPv{{.IPVersion}} 搜索记忆</span>
              {{if .Available}}
				<strong>{{if .ManualPrefixes}}手动优先父网段：{{range .ManualPrefixes}}<code>{{.}}</code> {{end}}{{else}}未设置手动优先父网段{{end}}</strong>
				{{if .ManualSeedIPs}}<p class="muted">先精确复测种子：{{range .ManualSeedIPs}}<code>{{.}}</code> {{end}}</p>{{end}}
				{{if .ManualHintPrefixes}}<p class="muted">随后深挖范围：{{range .ManualHintPrefixes}}<code>{{.}}</code> {{end}}</p>{{end}}
                <p class="muted">可复测 IP {{.ExactIPCount}}；窄网段 {{.NarrowHintCount}}；父网段 {{.WideHintCount}}；冷却 IP {{.CoolingIPCount}} / 网段 {{.CoolingPrefixCount}}</p>
				<p class="muted">{{if .ManualQuotaPercent}}手动范围每批保底 {{.ManualQuotaPercent}}%；其余按自适应预算分配：{{end}}精确 {{.Budget.Exact}}% · 窄网段 {{.Budget.Narrow}}% · 父网段 {{.Budget.Wide}}% · 全局 {{.Budget.Global}}%</p>
              {{else}}
                <strong>本轮没有可读取的搜索记忆</strong>
              {{end}}
            </div>
          {{end}}
        </div>
		<p class="muted">这里显示的是任务创建时冻结的搜索计划。手动种子会先精确复测，随后从对应窄网段与父网段持续取样；最终仍须通过 CF-RAY 地区、RTT、带宽和已启用的真连接条件。</p>
      {{end}}
      {{if .ActiveTargets}}
        <strong class="section-title">实际参与本轮 DNS 的目标</strong>
        <ul class="compact-list">
          {{range .ActiveTargets}}<li><span>{{.Name}} · {{.RecordName}}</span><strong>{{.RecordsText}}</strong></li>{{end}}
        </ul>
      {{end}}
      {{if .SkippedTargets}}
        <strong class="section-title skipped">本轮跳过的目标</strong>
        <ul class="compact-list skipped">
          {{range .SkippedTargets}}<li><span>{{.Name}} · {{.RecordName}}</span><strong>{{.Reason}}</strong></li>{{end}}
        </ul>
      {{end}}
      <p class="muted">“记录”是每个 IP 在每个域名下的一条 A 或 AAAA；“域名目标完成”表示该目标的整组记录已经写入，并从 Cloudflare 重新查询确认完全一致。</p>
    </section>
  {{end}}
{{end}}
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
        {{template "runPlan" .Plan}}
        <div class="progress"><div style="width: {{.Progress}}%"></div></div>
        <ul class="compact-list">
          <li><span>通过全部筛选的 IP</span><strong>{{.UpdatedIPCount}} / {{.RequiredIPCount}}</strong></li>
          <li><span>Cloudflare 已核验记录（逐条）</span><strong>{{.SyncedIPCount}} / {{if .RequiredDNSRecordCount}}{{.RequiredDNSRecordCount}}{{else}}{{.RequiredIPCount}}{{end}}</strong></li>
          {{if .PlannedDNSTargetCount}}<li><span>已完成同步并复核的域名目标</span><strong>{{.ConfirmedDNSTargetCount}} / {{.PlannedDNSTargetCount}}</strong></li>{{end}}
          {{if not .Plan.Available}}<li><span>旧任务摘要</span><strong>{{.Summary}}</strong></li>{{end}}
        </ul>
        {{if .DNSTargetResults}}
          <div class="metric-grid" style="margin-top:12px">
            {{range .DNSTargetResults}}
              <div class="metric">
                <span>{{.TargetName}} · {{.Status}}</span>
                <strong>{{.ConfirmedRecords}} / {{.PlannedRecords}} 条</strong>
                {{if .Error}}<p class="muted">{{.Error}}</p>{{end}}
              </div>
            {{end}}
          </div>
        {{end}}
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
