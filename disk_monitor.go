package main

// Disk Monitor: periodic free-space checks over SSH (df -PB1) on hosts from the
// shared "hosts" map, with threshold alerts and a daily summary by email.
//
// This file holds the config model (the top-level "disk_monitor" block),
// normalisation/validation, shared helpers and the admin REST handlers.
// Companion files:
//   disk_monitor_check.go      df runner and parser
//   disk_monitor_state.go      runtime state + history persistence
//   disk_monitor_scheduler.go  tick loop and per-host batches
//   disk_monitor_alerts.go     alert engine and daily summary
//   disk_monitor_email.go      text/HTML email bodies

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	diskCheckJobType = "disk_check"

	diskStatusOK      = "OK"
	diskStatusTripped = "TRIPPED"
	diskStatusUnknown = "UNKNOWN"

	diskUnitMB      = "MB"
	diskUnitGB      = "GB"
	diskUnitPercent = "%"

	diskBarModeFree = "free"
	diskBarModeUsed = "used"

	diskMonitorMinInterval   = time.Minute
	diskMonitorMaxInterval   = 7 * 24 * time.Hour
	diskMonitorMinAlertEvery = time.Minute
	diskMonitorMaxAlertEvery = 30 * 24 * time.Hour
	diskMonitorMaxConcurrent = 100
	diskMonitorHistorySize   = 24

	diskMonitorDefaultAlertEvery    = 4 * time.Hour
	diskMonitorDefaultInterval      = time.Hour
	diskMonitorDefaultThresholdVal  = 5
	diskMonitorDefaultThresholdUnit = diskUnitGB
	diskMonitorDefaultConcurrency   = 10
	diskMonitorDefaultSummaryTime   = "08:00"
	diskMonitorDefaultEmailSubject  = "[Disk Monitor] {{summary}}"
	diskMonitorMaxEmailSubjectLen   = 200
)

var (
	diskVolumeIDPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	diskDailySummaryTimeRegex = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
)

// DiskVolume is one monitored mount point on a host from the shared hosts map.
type DiskVolume struct {
	VolumeID       string  `json:"volume_id"`
	HostRef        string  `json:"host_ref"`
	MountPath      string  `json:"mount_path"`
	Description    string  `json:"description,omitempty"`
	CheckInterval  string  `json:"check_interval,omitempty"` // "30m", "1h", "2d"; empty = default_check_interval
	ThresholdValue float64 `json:"threshold_value"`
	ThresholdUnit  string  `json:"threshold_unit"` // MB | GB | %
	UseSudo        bool    `json:"use_sudo,omitempty"`
	Enabled        *bool   `json:"enabled,omitempty"` // nil = true
}

func (v DiskVolume) isEnabled() bool { return v.Enabled == nil || *v.Enabled }

// DiskMonitorRecipients lists the alert / summary email recipients.
type DiskMonitorRecipients struct {
	To []string `json:"to"`
	CC []string `json:"cc,omitempty"`
}

// DiskMonitorConfig is the optional top-level "disk_monitor" block in config.json.
type DiskMonitorConfig struct {
	Enabled                 *bool                 `json:"enabled,omitempty"` // nil = true
	SendAlertOnceEvery      string                `json:"send_alert_once_every,omitempty"`
	DailySummaryEnabled     *bool                 `json:"daily_summary_enabled,omitempty"` // nil = true
	DailySummaryTime        string                `json:"daily_summary_time,omitempty"`    // HH:MM local time
	DefaultCheckInterval    string                `json:"default_check_interval,omitempty"`
	DefaultThresholdValue   float64               `json:"default_threshold_value,omitempty"`
	DefaultThresholdUnit    string                `json:"default_threshold_unit,omitempty"`
	MaxConcurrentHostChecks int                   `json:"max_concurrent_host_checks,omitempty"`
	EmailBarMode            string                `json:"email_bar_mode,omitempty"` // free | used
	EmailSubject            string                `json:"email_subject,omitempty"`  // template, see diskEmailSubjectPlaceholders
	Recipients              DiskMonitorRecipients `json:"recipients"`
	Volumes                 []DiskVolume          `json:"volumes"`
}

func (c DiskMonitorConfig) isEnabled() bool { return c.Enabled == nil || *c.Enabled }
func (c DiskMonitorConfig) summaryEnabled() bool {
	return c.DailySummaryEnabled == nil || *c.DailySummaryEnabled
}

func (c DiskMonitorConfig) alertEvery() time.Duration {
	if d, err := parseDiskDuration(c.SendAlertOnceEvery); err == nil && d > 0 {
		return d
	}
	return diskMonitorDefaultAlertEvery
}

func (c DiskMonitorConfig) defaultInterval() time.Duration {
	if d, err := parseDiskDuration(c.DefaultCheckInterval); err == nil && d > 0 {
		return d
	}
	return diskMonitorDefaultInterval
}

func (c DiskMonitorConfig) maxConcurrent() int {
	if c.MaxConcurrentHostChecks > 0 {
		return c.MaxConcurrentHostChecks
	}
	return diskMonitorDefaultConcurrency
}

func (c DiskMonitorConfig) findVolume(id string) (DiskVolume, bool) {
	for _, v := range c.Volumes {
		if v.VolumeID == id {
			return v, true
		}
	}
	return DiskVolume{}, false
}

// diskVolumeInterval returns the effective check interval of a volume.
func diskVolumeInterval(v DiskVolume, settings DiskMonitorConfig) time.Duration {
	if d, err := parseDiskDuration(v.CheckInterval); err == nil && d > 0 {
		return d
	}
	return settings.defaultInterval()
}

// ---------------------------------------------------------------------------
// Helpers: durations, thresholds, byte formatting
// ---------------------------------------------------------------------------

// parseDiskDuration accepts Go durations ("30m", "1h30m") plus a day suffix
// ("2d", "1.5d"). An empty string is an error so callers can apply defaults.
func parseDiskDuration(value string) (time.Duration, error) {
	s := strings.TrimSpace(strings.ToLower(value))
	if s == "" {
		return 0, errors.New("duration is empty")
	}
	if strings.HasSuffix(s, "d") {
		var days float64
		if _, err := fmt.Sscanf(strings.TrimSuffix(s, "d"), "%g", &days); err == nil && days > 0 {
			return time.Duration(days * float64(24*time.Hour)), nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (use e.g. 30m, 1h, 1h30m or 2d)", value)
	}
	if d <= 0 {
		return 0, fmt.Errorf("duration %q must be positive", value)
	}
	return d, nil
}

// formatDiskDuration renders a duration compactly ("2d", "1h", "1h30m", "45s").
func formatDiskDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
	var sb strings.Builder
	if h := int(d / time.Hour); h > 0 {
		fmt.Fprintf(&sb, "%dh", h)
		d -= time.Duration(h) * time.Hour
	}
	if m := int(d / time.Minute); m > 0 {
		fmt.Fprintf(&sb, "%dm", m)
		d -= time.Duration(m) * time.Minute
	}
	if s := int(d / time.Second); s > 0 {
		fmt.Fprintf(&sb, "%ds", s)
	}
	return sb.String()
}

// diskThresholdBytes converts a threshold to the trip line in bytes.
func diskThresholdBytes(value float64, unit string, total uint64) uint64 {
	switch strings.ToUpper(strings.TrimSpace(unit)) {
	case diskUnitMB:
		return uint64(value * 1024 * 1024)
	case diskUnitGB:
		return uint64(value * 1024 * 1024 * 1024)
	case diskUnitPercent:
		return uint64(float64(total) * (value / 100.0))
	}
	return 0
}

func formatDiskThreshold(value float64, unit string) string {
	switch strings.ToUpper(strings.TrimSpace(unit)) {
	case diskUnitMB:
		return fmt.Sprintf("%g MB", value)
	case diskUnitGB:
		return fmt.Sprintf("%g GB", value)
	case diskUnitPercent:
		return fmt.Sprintf("%g%%", value)
	}
	return ""
}

// humanBytes1024 formats a byte count with base-1024 units and one decimal.
func humanBytes1024(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	suffixes := []string{"KB", "MB", "GB", "TB", "PB", "EB"}
	div, exp := uint64(unit), 0
	for n2 := n / unit; n2 >= unit && exp < len(suffixes)-1; n2 /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %s", float64(n)/float64(div), suffixes[exp])
}

func diskPercentFree(free, total uint64) float64 {
	if total == 0 {
		return 0
	}
	pct := float64(free) / float64(total) * 100
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}

func normalizeDiskMountPath(path string) string {
	path = strings.TrimSpace(path)
	for len(path) > 1 && strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
	}
	return path
}

func normalizeDiskRecipientList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool)
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			addr := strings.TrimSpace(part)
			if addr == "" {
				continue
			}
			key := strings.ToLower(addr)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, addr)
		}
	}
	return out
}

// newDiskVolumeID derives a stable ID from host and mount path, so a volume
// written by hand without volume_id keeps the same ID (and its runtime state)
// across reloads. A numeric suffix resolves the unlikely clash.
func newDiskVolumeID(existing map[string]bool, hostRef, mountPath string) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '-'
	}, strings.TrimSpace(hostRef))
	if len(slug) > 24 {
		slug = slug[:24]
	}
	if slug == "" {
		slug = "host"
	}
	sum := sha1.Sum([]byte(hostRef + "\x00" + mountPath))
	base := fmt.Sprintf("dv-%s-%s", slug, hex.EncodeToString(sum[:])[:8])
	id := base
	for n := 2; existing[id]; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	existing[id] = true
	return id
}

// ---------------------------------------------------------------------------
// Normalise / validate
// ---------------------------------------------------------------------------

func diskMonitorDefaults() DiskMonitorConfig {
	enabled := true
	summary := true
	return DiskMonitorConfig{
		Enabled:                 &enabled,
		SendAlertOnceEvery:      formatDiskDuration(diskMonitorDefaultAlertEvery),
		DailySummaryEnabled:     &summary,
		DailySummaryTime:        diskMonitorDefaultSummaryTime,
		DefaultCheckInterval:    formatDiskDuration(diskMonitorDefaultInterval),
		DefaultThresholdValue:   diskMonitorDefaultThresholdVal,
		DefaultThresholdUnit:    diskMonitorDefaultThresholdUnit,
		MaxConcurrentHostChecks: diskMonitorDefaultConcurrency,
		EmailBarMode:            diskBarModeFree,
		EmailSubject:            diskMonitorDefaultEmailSubject,
		Recipients:              DiskMonitorRecipients{To: []string{}, CC: []string{}},
		Volumes:                 []DiskVolume{},
	}
}

// normalizeDiskMonitorConfig fills defaults, trims strings and assigns missing
// volume IDs. It never fails; validateDiskMonitorConfig reports problems.
func normalizeDiskMonitorConfig(cfg *DiskMonitorConfig) DiskMonitorConfig {
	out := diskMonitorDefaults()
	if cfg == nil {
		return out
	}
	if cfg.Enabled != nil {
		enabled := *cfg.Enabled
		out.Enabled = &enabled
	}
	if cfg.DailySummaryEnabled != nil {
		summary := *cfg.DailySummaryEnabled
		out.DailySummaryEnabled = &summary
	}
	if s := strings.TrimSpace(cfg.SendAlertOnceEvery); s != "" {
		out.SendAlertOnceEvery = s
	}
	if s := strings.TrimSpace(cfg.DailySummaryTime); s != "" {
		out.DailySummaryTime = s
	}
	if s := strings.TrimSpace(cfg.DefaultCheckInterval); s != "" {
		out.DefaultCheckInterval = s
	}
	if cfg.DefaultThresholdValue != 0 {
		out.DefaultThresholdValue = cfg.DefaultThresholdValue
	}
	if s := strings.ToUpper(strings.TrimSpace(cfg.DefaultThresholdUnit)); s != "" {
		out.DefaultThresholdUnit = s
	}
	if cfg.MaxConcurrentHostChecks != 0 {
		out.MaxConcurrentHostChecks = cfg.MaxConcurrentHostChecks
	}
	if s := strings.ToLower(strings.TrimSpace(cfg.EmailBarMode)); s != "" {
		out.EmailBarMode = s
	}
	if s := strings.TrimSpace(cfg.EmailSubject); s != "" {
		out.EmailSubject = s
	}
	out.Recipients = DiskMonitorRecipients{
		To: normalizeDiskRecipientList(cfg.Recipients.To),
		CC: normalizeDiskRecipientList(cfg.Recipients.CC),
	}

	existing := make(map[string]bool)
	for _, v := range cfg.Volumes {
		if id := strings.TrimSpace(v.VolumeID); id != "" {
			existing[id] = true
		}
	}
	out.Volumes = make([]DiskVolume, 0, len(cfg.Volumes))
	for _, v := range cfg.Volumes {
		nv := v
		nv.VolumeID = strings.TrimSpace(v.VolumeID)
		nv.HostRef = strings.TrimSpace(v.HostRef)
		nv.MountPath = normalizeDiskMountPath(v.MountPath)
		if nv.VolumeID == "" {
			nv.VolumeID = newDiskVolumeID(existing, nv.HostRef, nv.MountPath)
		}
		nv.Description = strings.TrimSpace(v.Description)
		nv.CheckInterval = strings.TrimSpace(v.CheckInterval)
		if nv.CheckInterval == "" {
			nv.CheckInterval = out.DefaultCheckInterval
		}
		nv.ThresholdUnit = strings.ToUpper(strings.TrimSpace(v.ThresholdUnit))
		if nv.ThresholdUnit == "" {
			nv.ThresholdUnit = out.DefaultThresholdUnit
		}
		if nv.ThresholdValue == 0 {
			nv.ThresholdValue = out.DefaultThresholdValue
		}
		enabled := v.isEnabled()
		nv.Enabled = &enabled
		out.Volumes = append(out.Volumes, nv)
	}
	return out
}

func validateDiskThreshold(value float64, unit string) error {
	switch unit {
	case diskUnitMB, diskUnitGB:
		if value <= 0 {
			return fmt.Errorf("threshold must be greater than 0 %s", unit)
		}
	case diskUnitPercent:
		if value <= 0 || value >= 100 {
			return errors.New("percent threshold must be between 0 and 100 (exclusive)")
		}
	default:
		return fmt.Errorf("threshold unit must be MB, GB or %% (got %q)", unit)
	}
	return nil
}

func validateDiskDurationRange(label, value string, min, max time.Duration) error {
	d, err := parseDiskDuration(value)
	if err != nil {
		return fmt.Errorf("%s: %v", label, err)
	}
	if d < min || d > max {
		return fmt.Errorf("%s must be between %s and %s (got %s)", label, formatDiskDuration(min), formatDiskDuration(max), value)
	}
	return nil
}

// validateDiskMonitorConfig checks a normalised block against the hosts map.
// SMTP is deliberately not required here; readiness is reported at runtime.
func validateDiskMonitorConfig(cfg DiskMonitorConfig, hosts map[string]Host) error {
	if err := validateDiskDurationRange("send_alert_once_every", cfg.SendAlertOnceEvery, diskMonitorMinAlertEvery, diskMonitorMaxAlertEvery); err != nil {
		return err
	}
	if !diskDailySummaryTimeRegex.MatchString(cfg.DailySummaryTime) {
		return fmt.Errorf("daily_summary_time must be HH:MM in 24-hour local time (got %q)", cfg.DailySummaryTime)
	}
	if err := validateDiskDurationRange("default_check_interval", cfg.DefaultCheckInterval, diskMonitorMinInterval, diskMonitorMaxInterval); err != nil {
		return err
	}
	if err := validateDiskThreshold(cfg.DefaultThresholdValue, cfg.DefaultThresholdUnit); err != nil {
		return fmt.Errorf("default threshold: %v", err)
	}
	if cfg.MaxConcurrentHostChecks < 1 || cfg.MaxConcurrentHostChecks > diskMonitorMaxConcurrent {
		return fmt.Errorf("max_concurrent_host_checks must be between 1 and %d", diskMonitorMaxConcurrent)
	}
	if cfg.EmailBarMode != diskBarModeFree && cfg.EmailBarMode != diskBarModeUsed {
		return fmt.Errorf("email_bar_mode must be \"free\" or \"used\" (got %q)", cfg.EmailBarMode)
	}
	if err := validateDiskEmailSubjectText(cfg.EmailSubject); err != nil {
		return err
	}
	for _, list := range [][]string{cfg.Recipients.To, cfg.Recipients.CC} {
		for _, addr := range list {
			if _, err := mail.ParseAddress(addr); err != nil {
				return fmt.Errorf("invalid recipient email address %q", addr)
			}
		}
	}

	seenIDs := make(map[string]bool, len(cfg.Volumes))
	seenMounts := make(map[string]bool, len(cfg.Volumes))
	for _, v := range cfg.Volumes {
		if err := validateDiskVolume(v, hosts); err != nil {
			return err
		}
		if seenIDs[v.VolumeID] {
			return fmt.Errorf("duplicate disk monitor volume_id '%s'", v.VolumeID)
		}
		seenIDs[v.VolumeID] = true
		key := v.HostRef + "\x00" + v.MountPath
		if seenMounts[key] {
			return fmt.Errorf("mount path '%s' on host '%s' is monitored more than once", v.MountPath, v.HostRef)
		}
		seenMounts[key] = true
	}
	return nil
}

func validateDiskVolume(v DiskVolume, hosts map[string]Host) error {
	if !diskVolumeIDPattern.MatchString(v.VolumeID) {
		return fmt.Errorf("invalid disk monitor volume_id '%s' (letters, digits, '-' and '_', max 64 characters)", v.VolumeID)
	}
	label := fmt.Sprintf("disk monitor volume '%s'", v.VolumeID)
	if v.HostRef == "" {
		return fmt.Errorf("%s has no host_ref", label)
	}
	host, exists := hosts[v.HostRef]
	if !exists {
		return fmt.Errorf("%s references non-existent host '%s'", label, v.HostRef)
	}
	if !isSSHHost(host) {
		return fmt.Errorf("%s references host '%s' which is not an SSH host (disk monitoring needs SSH)", label, v.HostRef)
	}
	if v.MountPath == "" {
		return fmt.Errorf("%s has an empty mount_path", label)
	}
	if !strings.HasPrefix(v.MountPath, "/") {
		return fmt.Errorf("%s: mount_path '%s' must be an absolute path starting with /", label, v.MountPath)
	}
	if strings.ContainsAny(v.MountPath, "\r\n") {
		return fmt.Errorf("%s: mount_path must not contain line breaks", label)
	}
	if strings.Contains(v.MountPath, "{{") || strings.Contains(v.MountPath, "}}") || strings.Contains(v.MountPath, "${{") {
		return fmt.Errorf("%s: mount_path must not contain {{ }} or ${{ (reserved for config variables)", label)
	}
	if err := validateDiskDurationRange("check_interval", v.CheckInterval, diskMonitorMinInterval, diskMonitorMaxInterval); err != nil {
		return fmt.Errorf("%s: %v", label, err)
	}
	if err := validateDiskThreshold(v.ThresholdValue, v.ThresholdUnit); err != nil {
		return fmt.Errorf("%s: %v", label, err)
	}
	return nil
}

// currentDiskMonitorSettings returns the normalised block from the live config
// and whether the block is present at all.
func currentDiskMonitorSettings() (DiskMonitorConfig, bool) {
	cfg := cache.GetConfig()
	if cfg == nil || cfg.DiskMonitor == nil {
		return diskMonitorDefaults(), false
	}
	return normalizeDiskMonitorConfig(cfg.DiskMonitor), true
}

// diskMonitorHostUsage counts volumes in a raw config map that reference a host.
func diskMonitorHostUsage(config map[string]interface{}, hostName string) int {
	block, err := decodeDiskMonitorConfig(config["disk_monitor"])
	if err != nil {
		return 0
	}
	count := 0
	for _, v := range block.Volumes {
		if strings.TrimSpace(v.HostRef) == hostName {
			count++
		}
	}
	return count
}

func decodeDiskMonitorConfig(value interface{}) (DiskMonitorConfig, error) {
	if value == nil {
		return DiskMonitorConfig{}, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return DiskMonitorConfig{}, err
	}
	var cfg DiskMonitorConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return DiskMonitorConfig{}, err
	}
	return cfg, nil
}

func decodeHostsMap(value interface{}) map[string]Host {
	hosts := make(map[string]Host)
	if value == nil {
		return hosts
	}
	data, err := json.Marshal(value)
	if err != nil {
		return hosts
	}
	_ = json.Unmarshal(data, &hosts)
	for id, host := range hosts {
		normalizeHost(&host)
		hosts[id] = host
	}
	return hosts
}

// ---------------------------------------------------------------------------
// Admin API: /api/config/disk-monitor (settings + volumes, persisted)
// ---------------------------------------------------------------------------

type diskMonitorHostOption struct {
	HostID   string `json:"host_id"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
}

func diskMonitorSSHHostOptions(hosts map[string]Host) []diskMonitorHostOption {
	options := make([]diskMonitorHostOption, 0, len(hosts))
	for id, host := range hosts {
		if !isSSHHost(host) {
			continue
		}
		options = append(options, diskMonitorHostOption{HostID: id, Host: host.Host, Port: host.Port, Username: host.Username})
	}
	sort.Slice(options, func(i, j int) bool { return strings.ToLower(options[i].HostID) < strings.ToLower(options[j].HostID) })
	return options
}

func diskMonitorSettingsView(settings DiskMonitorConfig, hosts map[string]Host) map[string]interface{} {
	cfg := cache.GetConfig()
	smtpReady := cfg != nil && diskMonitorSMTPReady(cfg, settings)
	return map[string]interface{}{
		"enabled":                    settings.isEnabled(),
		"send_alert_once_every":      settings.SendAlertOnceEvery,
		"daily_summary_enabled":      settings.summaryEnabled(),
		"daily_summary_time":         settings.DailySummaryTime,
		"default_check_interval":     settings.DefaultCheckInterval,
		"default_threshold_value":    settings.DefaultThresholdValue,
		"default_threshold_unit":     settings.DefaultThresholdUnit,
		"max_concurrent_host_checks": settings.MaxConcurrentHostChecks,
		"email_bar_mode":             settings.EmailBarMode,
		"email_subject":              settings.EmailSubject,
		"recipients":                 settings.Recipients,
		"volume_count":               len(settings.Volumes),
		"smtp_ready":                 smtpReady,
		"hosts":                      diskMonitorSSHHostOptions(hosts),
	}
}

type diskMonitorConfigContext struct {
	config   map[string]interface{}
	settings DiskMonitorConfig
	hosts    map[string]Host
}

func (ca *ConfigAPI) loadDiskMonitorContext(w http.ResponseWriter) (*diskMonitorConfigContext, bool) {
	config, err := loadConfigMap(ca.configPath)
	if err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Failed to read configuration")
		return nil, false
	}
	raw, err := decodeDiskMonitorConfig(config["disk_monitor"])
	if err != nil {
		sendConfigJSONError(w, http.StatusInternalServerError, "Invalid disk monitor configuration")
		return nil, false
	}
	return &diskMonitorConfigContext{config: config, settings: normalizeDiskMonitorConfig(&raw), hosts: decodeHostsMap(config["hosts"])}, true
}

// saveDiskMonitor validates and persists the block, returning false after
// writing an error response.
func (ca *ConfigAPI) saveDiskMonitor(w http.ResponseWriter, ctx *diskMonitorConfigContext, candidate DiskMonitorConfig, failure string) (DiskMonitorConfig, bool) {
	normalized := normalizeDiskMonitorConfig(&candidate)
	if err := validateDiskMonitorConfig(normalized, ctx.hosts); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, err.Error())
		return normalized, false
	}
	// The raw file still holds {{config_variable}} references (they are only
	// substituted at load), so unknown names are checked against them here.
	variables, _ := ctx.config["variables"].(map[string]interface{})
	if err := validateDiskEmailSubjectPlaceholders(normalized.EmailSubject, variables); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, err.Error())
		return normalized, false
	}
	// The candidate was built from the block loaded in ctx. Save under the config
	// lock, and refuse (409) if another request changed the block in between, so
	// neither edit silently overwrites the other.
	before, _ := json.Marshal(ctx.config["disk_monitor"])
	err := ca.mutateConfig(func(config map[string]interface{}) error {
		current, _ := json.Marshal(config["disk_monitor"])
		if !bytes.Equal(before, current) {
			return httpErr(http.StatusConflict, "Disk monitor configuration was changed by another request; reload the page and try again")
		}
		config["disk_monitor"] = normalized
		return nil
	})
	if err != nil {
		sendConfigMutationError(w, err, failure)
		return normalized, false
	}
	return normalized, true
}

func diskVolumeAuditDetails(v DiskVolume) map[string]interface{} {
	return map[string]interface{}{
		"host_ref":        v.HostRef,
		"mount_path":      v.MountPath,
		"threshold_value": v.ThresholdValue,
		"threshold_unit":  v.ThresholdUnit,
		"check_interval":  v.CheckInterval,
		"enabled":         v.isEnabled(),
	}
}

// HandleDiskMonitorConfig serves /api/config/disk-monitor and
// /api/config/disk-monitor/volumes[/bulk|/{id}].
func (ca *ConfigAPI) HandleDiskMonitorConfig(w http.ResponseWriter, r *http.Request) {
	ctx, ok := ca.loadDiskMonitorContext(w)
	if !ok {
		return
	}
	actor, role := currentActor(r)
	path := strings.TrimSuffix(r.URL.Path, "/")

	switch {
	case path == "/api/config/disk-monitor":
		switch r.Method {
		case http.MethodGet:
			sendConfigJSONSuccess(w, http.StatusOK, diskMonitorSettingsView(ctx.settings, ctx.hosts), "Disk monitor settings retrieved successfully")
		case http.MethodPut:
			var requested DiskMonitorConfig
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&requested); err != nil {
				sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
				return
			}
			requested.Volumes = ctx.settings.Volumes // volumes are managed through their own endpoints
			saved, ok := ca.saveDiskMonitor(w, ctx, requested, "Failed to save disk monitor settings")
			if !ok {
				return
			}
			writeSecurityAudit(r, actor, role, "disk_monitor_settings_update", "disk_monitor", "success", map[string]interface{}{
				"enabled":               saved.isEnabled(),
				"send_alert_once_every": saved.SendAlertOnceEvery,
				"daily_summary_time":    saved.DailySummaryTime,
				"email_subject":         saved.EmailSubject,
				"recipients_to":         len(saved.Recipients.To),
			})
			sendConfigJSONSuccess(w, http.StatusOK, diskMonitorSettingsView(saved, ctx.hosts), "Disk monitor settings updated successfully")
		default:
			sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return

	case path == "/api/config/disk-monitor/volumes":
		switch r.Method {
		case http.MethodGet:
			sendConfigJSONSuccess(w, http.StatusOK, ctx.settings.Volumes, "Disk monitor volumes retrieved successfully")
		case http.MethodPost:
			var requested DiskVolume
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&requested); err != nil {
				sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
				return
			}
			requested.VolumeID = strings.TrimSpace(requested.VolumeID)
			requested.HostRef = strings.TrimSpace(requested.HostRef)
			requested.MountPath = normalizeDiskMountPath(requested.MountPath)
			for _, existing := range ctx.settings.Volumes {
				if requested.VolumeID != "" && existing.VolumeID == requested.VolumeID {
					sendConfigJSONError(w, http.StatusConflict, fmt.Sprintf("Disk monitor volume '%s' already exists", requested.VolumeID))
					return
				}
				if existing.HostRef == requested.HostRef && existing.MountPath == requested.MountPath {
					sendConfigJSONError(w, http.StatusConflict, fmt.Sprintf("Mount path '%s' on host '%s' is already monitored (%s)", requested.MountPath, requested.HostRef, existing.VolumeID))
					return
				}
			}
			candidate := ctx.settings
			candidate.Volumes = append(append([]DiskVolume{}, ctx.settings.Volumes...), requested)
			saved, ok := ca.saveDiskMonitor(w, ctx, candidate, "Failed to save disk monitor volume")
			if !ok {
				return
			}
			created := saved.Volumes[len(saved.Volumes)-1]
			diskMonitor.MarkDue(created.VolumeID)
			writeSecurityAudit(r, actor, role, "disk_volume_create", created.VolumeID, "success", diskVolumeAuditDetails(created))
			sendConfigJSONSuccess(w, http.StatusCreated, created, "Disk monitor volume created successfully")
		default:
			sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return

	case path == "/api/config/disk-monitor/volumes/bulk":
		if r.Method != http.MethodPost {
			sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		ca.handleDiskVolumeBulkCreate(w, r, ctx, actor, role)
		return
	}

	if !strings.HasPrefix(path, "/api/config/disk-monitor/volumes/") {
		sendConfigJSONError(w, http.StatusNotFound, "Endpoint not found")
		return
	}
	volumeID := strings.TrimPrefix(path, "/api/config/disk-monitor/volumes/")
	if volumeID == "" || strings.Contains(volumeID, "/") {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid disk monitor volume ID")
		return
	}
	index := -1
	for i := range ctx.settings.Volumes {
		if ctx.settings.Volumes[i].VolumeID == volumeID {
			index = i
			break
		}
	}
	if index < 0 {
		sendConfigJSONError(w, http.StatusNotFound, "Disk monitor volume not found")
		return
	}

	switch r.Method {
	case http.MethodGet:
		sendConfigJSONSuccess(w, http.StatusOK, ctx.settings.Volumes[index], "Disk monitor volume retrieved successfully")
	case http.MethodPut:
		var requested DiskVolume
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&requested); err != nil {
			sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
			return
		}
		requested.VolumeID = volumeID
		requested.HostRef = strings.TrimSpace(requested.HostRef)
		requested.MountPath = normalizeDiskMountPath(requested.MountPath)
		for i, existing := range ctx.settings.Volumes {
			if i != index && existing.HostRef == requested.HostRef && existing.MountPath == requested.MountPath {
				sendConfigJSONError(w, http.StatusConflict, fmt.Sprintf("Mount path '%s' on host '%s' is already monitored (%s)", requested.MountPath, requested.HostRef, existing.VolumeID))
				return
			}
		}
		candidate := ctx.settings
		candidate.Volumes = append([]DiskVolume{}, ctx.settings.Volumes...)
		candidate.Volumes[index] = requested
		saved, ok := ca.saveDiskMonitor(w, ctx, candidate, "Failed to save disk monitor volume")
		if !ok {
			return
		}
		updated := saved.Volumes[index]
		if updated.isEnabled() {
			diskMonitor.MarkDue(updated.VolumeID)
		}
		writeSecurityAudit(r, actor, role, "disk_volume_update", updated.VolumeID, "success", diskVolumeAuditDetails(updated))
		sendConfigJSONSuccess(w, http.StatusOK, updated, "Disk monitor volume updated successfully")
	case http.MethodDelete:
		removed := ctx.settings.Volumes[index]
		candidate := ctx.settings
		candidate.Volumes = append(append([]DiskVolume{}, ctx.settings.Volumes[:index]...), ctx.settings.Volumes[index+1:]...)
		if _, ok := ca.saveDiskMonitor(w, ctx, candidate, "Failed to delete disk monitor volume"); !ok {
			return
		}
		diskMonitor.RemoveVolume(removed.VolumeID)
		writeSecurityAudit(r, actor, role, "disk_volume_delete", removed.VolumeID, "success", diskVolumeAuditDetails(removed))
		sendConfigJSONSuccess(w, http.StatusOK, nil, "Disk monitor volume deleted successfully")
	default:
		sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

type diskVolumeBulkRequest struct {
	HostRef string `json:"host_ref"`
	UseSudo bool   `json:"use_sudo"`
	Volumes []struct {
		MountPath      string  `json:"mount_path"`
		ThresholdValue float64 `json:"threshold_value"`
		ThresholdUnit  string  `json:"threshold_unit"`
		CheckInterval  string  `json:"check_interval"`
		Description    string  `json:"description"`
	} `json:"volumes"`
}

func (ca *ConfigAPI) handleDiskVolumeBulkCreate(w http.ResponseWriter, r *http.Request, ctx *diskMonitorConfigContext, actor, role string) {
	var requested diskVolumeBulkRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&requested); err != nil {
		sendConfigJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	hostRef := strings.TrimSpace(requested.HostRef)
	host, exists := ctx.hosts[hostRef]
	if !exists {
		sendConfigJSONError(w, http.StatusNotFound, "Host not found")
		return
	}
	if !isSSHHost(host) {
		sendConfigJSONError(w, http.StatusBadRequest, fmt.Sprintf("Host '%s' is not an SSH host", hostRef))
		return
	}
	if len(requested.Volumes) == 0 {
		sendConfigJSONError(w, http.StatusBadRequest, "No mount paths supplied")
		return
	}

	type skipped struct {
		MountPath string `json:"mount_path"`
		Reason    string `json:"reason"`
	}
	created := make([]DiskVolume, 0, len(requested.Volumes))
	skippedRows := make([]skipped, 0)
	candidate := ctx.settings
	candidate.Volumes = append([]DiskVolume{}, ctx.settings.Volumes...)
	existingIDs := make(map[string]bool)
	monitored := make(map[string]bool)
	for _, v := range candidate.Volumes {
		existingIDs[v.VolumeID] = true
		monitored[v.HostRef+"\x00"+v.MountPath] = true
	}
	for _, row := range requested.Volumes {
		mount := normalizeDiskMountPath(row.MountPath)
		if monitored[hostRef+"\x00"+mount] {
			skippedRows = append(skippedRows, skipped{MountPath: mount, Reason: "already monitored"})
			continue
		}
		enabled := true
		volume := DiskVolume{
			VolumeID:       newDiskVolumeID(existingIDs, hostRef, mount),
			HostRef:        hostRef,
			MountPath:      mount,
			Description:    strings.TrimSpace(row.Description),
			CheckInterval:  strings.TrimSpace(row.CheckInterval),
			ThresholdValue: row.ThresholdValue,
			ThresholdUnit:  strings.ToUpper(strings.TrimSpace(row.ThresholdUnit)),
			UseSudo:        requested.UseSudo,
			Enabled:        &enabled,
		}
		probe := normalizeDiskMonitorConfig(&DiskMonitorConfig{
			DefaultCheckInterval:  ctx.settings.DefaultCheckInterval,
			DefaultThresholdValue: ctx.settings.DefaultThresholdValue,
			DefaultThresholdUnit:  ctx.settings.DefaultThresholdUnit,
			Volumes:               []DiskVolume{volume},
		})
		if err := validateDiskVolume(probe.Volumes[0], ctx.hosts); err != nil {
			skippedRows = append(skippedRows, skipped{MountPath: mount, Reason: err.Error()})
			continue
		}
		monitored[hostRef+"\x00"+mount] = true
		candidate.Volumes = append(candidate.Volumes, probe.Volumes[0])
		created = append(created, probe.Volumes[0])
	}
	if len(created) == 0 {
		sendConfigJSONSuccess(w, http.StatusOK, map[string]interface{}{"created": []DiskVolume{}, "skipped": skippedRows}, "No new volumes were added")
		return
	}
	if _, ok := ca.saveDiskMonitor(w, ctx, candidate, "Failed to save disk monitor volumes"); !ok {
		return
	}
	ids := make([]string, 0, len(created))
	for _, v := range created {
		ids = append(ids, v.VolumeID)
		diskMonitor.MarkDue(v.VolumeID)
	}
	writeSecurityAudit(r, actor, role, "disk_volume_bulk_create", hostRef, "success", map[string]interface{}{"created": ids, "skipped": len(skippedRows)})
	sendConfigJSONSuccess(w, http.StatusCreated, map[string]interface{}{"created": created, "skipped": skippedRows},
		fmt.Sprintf("%d volume(s) added on host '%s'", len(created), hostRef))
}

// ---------------------------------------------------------------------------
// Admin API: /api/disk-monitor/... (runtime: state, check now, detect, test)
// ---------------------------------------------------------------------------

func handleDiskMonitorOps(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	rest := strings.TrimPrefix(path, "/api/disk-monitor")
	rest = strings.TrimPrefix(rest, "/")
	parts := strings.Split(rest, "/")
	actor, role := currentActor(r)

	switch {
	case rest == "state":
		if r.Method != http.MethodGet {
			sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		if err := cache.loadIfChanged(); err != nil {
			sendConfigJSONError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to load configuration: %v", err))
			return
		}
		sendConfigJSONSuccess(w, http.StatusOK, buildDiskMonitorStateView(time.Now()), "Disk monitor state retrieved successfully")

	case rest == "test-email":
		if r.Method != http.MethodPost {
			sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		if err := sendDiskMonitorTestEmail(); err != nil {
			writeSecurityAudit(r, actor, role, "disk_monitor_test_email", "disk_monitor", "failure", map[string]interface{}{"error": err.Error()})
			sendConfigJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeSecurityAudit(r, actor, role, "disk_monitor_test_email", "disk_monitor", "success", nil)
		sendConfigJSONSuccess(w, http.StatusOK, nil, "Test email sent to the configured recipients")

	case len(parts) == 3 && parts[0] == "volumes" && parts[2] == "history":
		if r.Method != http.MethodGet {
			sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		state, ok := diskStateStore.volume(parts[1])
		if !ok {
			sendConfigJSONSuccess(w, http.StatusOK, []diskReadingView{}, "No readings yet")
			return
		}
		sendConfigJSONSuccess(w, http.StatusOK, diskReadingViews(state.History), "Volume history retrieved successfully")

	case len(parts) == 3 && parts[0] == "volumes" && parts[2] == "check":
		if r.Method != http.MethodPost {
			sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		settings, _ := currentDiskMonitorSettings()
		volume, ok := settings.findVolume(parts[1])
		if !ok {
			sendConfigJSONError(w, http.StatusNotFound, "Disk monitor volume not found")
			return
		}
		if !settings.isEnabled() {
			sendConfigJSONError(w, http.StatusConflict, "Disk monitor is disabled in settings")
			return
		}
		if !volume.isEnabled() {
			sendConfigJSONError(w, http.StatusConflict, "This volume is disabled; enable it to run a check")
			return
		}
		diskMonitor.MarkDue(volume.VolumeID)
		diskMonitor.Kick()
		writeSecurityAudit(r, actor, role, "disk_check_now", volume.VolumeID, "success", map[string]interface{}{"host_ref": volume.HostRef, "mount_path": volume.MountPath})
		sendConfigJSONSuccess(w, http.StatusAccepted, map[string]interface{}{"queued": true}, "Check queued; it runs within a few seconds")

	case len(parts) == 3 && parts[0] == "hosts":
		hostRef := parts[1]
		cfg := cache.GetConfig()
		if cfg == nil {
			sendConfigJSONError(w, http.StatusInternalServerError, "Configuration is not loaded")
			return
		}
		host, exists := cfg.Hosts[hostRef]
		if !exists {
			sendConfigJSONError(w, http.StatusNotFound, "Host not found")
			return
		}
		if !isSSHHost(host) {
			sendConfigJSONError(w, http.StatusBadRequest, fmt.Sprintf("Host '%s' is not an SSH host", hostRef))
			return
		}
		if r.Method != http.MethodPost {
			sendConfigJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		switch parts[2] {
		case "check":
			settings, _ := currentDiskMonitorSettings()
			queued := 0
			for _, v := range settings.Volumes {
				if v.HostRef == hostRef && v.isEnabled() {
					diskMonitor.MarkDue(v.VolumeID)
					queued++
				}
			}
			if queued == 0 {
				sendConfigJSONError(w, http.StatusNotFound, "No enabled volumes are configured on this host")
				return
			}
			diskMonitor.Kick()
			writeSecurityAudit(r, actor, role, "disk_check_now", hostRef, "success", map[string]interface{}{"volumes": queued})
			sendConfigJSONSuccess(w, http.StatusAccepted, map[string]interface{}{"queued": queued}, fmt.Sprintf("%d check(s) queued", queued))
		case "test":
			handleDiskHostTest(w, r, hostRef, host, actor, role)
		case "detect":
			handleDiskHostDetect(w, r, hostRef, host, actor, role)
		default:
			sendConfigJSONError(w, http.StatusNotFound, "Endpoint not found")
		}

	default:
		sendConfigJSONError(w, http.StatusNotFound, "Endpoint not found")
	}
}

type diskHostTestResult struct {
	MountPath  string `json:"mount_path"`
	Status     string `json:"status"`
	FreeBytes  uint64 `json:"free_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
	Error      string `json:"error,omitempty"`
}

func handleDiskHostTest(w http.ResponseWriter, r *http.Request, hostRef string, host Host, actor, role string) {
	var body struct {
		UseSudo *bool `json:"use_sudo"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body)

	settings, _ := currentDiskMonitorSettings()
	volumes := make([]DiskVolume, 0)
	useSudo := false
	for _, v := range settings.Volumes {
		if v.HostRef == hostRef && v.isEnabled() {
			volumes = append(volumes, v)
			useSudo = useSudo || v.UseSudo
		}
	}
	if body.UseSudo != nil {
		useSudo = *body.UseSudo
	}
	if len(volumes) == 0 {
		// No volumes yet: run a plain df so the connection itself is exercised.
		ctx, cancel := diskCheckContext(r.Context())
		defer cancel()
		run, mounts, err := listDiskMounts(ctx, host, useSudo)
		if err != nil {
			writeSecurityAudit(r, actor, role, "disk_host_test", hostRef, "failure", map[string]interface{}{"error": err.Error()})
			sendConfigJSONError(w, http.StatusBadGateway, fmt.Sprintf("Connection test failed: %v", err))
			return
		}
		results := make([]diskHostTestResult, 0, len(mounts))
		for _, m := range mounts {
			results = append(results, diskHostTestResult{MountPath: m.MountPath, Status: diskStatusOK, FreeBytes: m.FreeBytes, TotalBytes: m.TotalBytes, UsedBytes: m.UsedBytes})
		}
		writeSecurityAudit(r, actor, role, "disk_host_test", hostRef, "success", map[string]interface{}{"mounts": len(results)})
		sendConfigJSONSuccess(w, http.StatusOK, map[string]interface{}{
			"ok": true, "duration_ms": run.Duration.Milliseconds(), "results": results, "logged": false,
		}, "Connection OK (no volumes configured on this host yet)")
		return
	}

	outcome, err := diskMonitor.RunHostNow(hostRef, host, useSudo, volumes, "test", actor)
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, errDiskHostBusy) {
			code = http.StatusConflict
		}
		writeSecurityAudit(r, actor, role, "disk_host_test", hostRef, "failure", map[string]interface{}{"error": err.Error()})
		sendConfigJSONError(w, code, err.Error())
		return
	}
	results := make([]diskHostTestResult, 0, len(outcome.Results))
	for _, res := range outcome.Results {
		row := diskHostTestResult{MountPath: res.MountPath, Status: res.Status}
		if res.Err != nil {
			row.Error = res.Err.Error()
		} else if res.Result != nil {
			row.FreeBytes, row.TotalBytes, row.UsedBytes = res.Result.FreeBytes, res.Result.TotalBytes, res.Result.UsedBytes
		}
		results = append(results, row)
	}
	writeSecurityAudit(r, actor, role, "disk_host_test", hostRef, "success", map[string]interface{}{"trace_id": outcome.TraceID, "ok": outcome.OK})
	message := "Connection OK; all mounts reported"
	if !outcome.OK {
		message = "Check finished with errors; see results"
	}
	sendConfigJSONSuccess(w, http.StatusOK, map[string]interface{}{
		"ok": outcome.OK, "trace_id": outcome.TraceID, "duration_ms": outcome.Duration.Milliseconds(), "results": results, "logged": true,
	}, message)
}

type diskDetectRow struct {
	MountPath        string  `json:"mount_path"`
	Filesystem       string  `json:"filesystem"`
	TotalBytes       uint64  `json:"total_bytes"`
	UsedBytes        uint64  `json:"used_bytes"`
	FreeBytes        uint64  `json:"free_bytes"`
	PctUsed          float64 `json:"pct_used"`
	AlreadyMonitored bool    `json:"already_monitored"`
	Precheck         bool    `json:"precheck"`
	RealDisk         bool    `json:"real_disk"`
}

func handleDiskHostDetect(w http.ResponseWriter, r *http.Request, hostRef string, host Host, actor, role string) {
	var body struct {
		UseSudo bool `json:"use_sudo"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body)

	ctx, cancel := diskCheckContext(r.Context())
	defer cancel()
	_, mounts, err := listDiskMounts(ctx, host, body.UseSudo)
	if err != nil {
		writeSecurityAudit(r, actor, role, "disk_detect_volumes", hostRef, "failure", map[string]interface{}{"error": err.Error()})
		sendConfigJSONError(w, http.StatusBadGateway, fmt.Sprintf("Could not list mounts on '%s': %v", hostRef, err))
		return
	}
	settings, _ := currentDiskMonitorSettings()
	monitored := make(map[string]bool)
	for _, v := range settings.Volumes {
		if v.HostRef == hostRef {
			monitored[v.MountPath] = true
		}
	}
	rows := make([]diskDetectRow, 0, len(mounts))
	for _, m := range mounts {
		real := isRealDisk(m.Filesystem, m.TotalBytes)
		row := diskDetectRow{
			MountPath:        m.MountPath,
			Filesystem:       m.Filesystem,
			TotalBytes:       m.TotalBytes,
			UsedBytes:        m.UsedBytes,
			FreeBytes:        m.FreeBytes,
			AlreadyMonitored: monitored[m.MountPath],
			RealDisk:         real,
		}
		if m.TotalBytes > 0 {
			row.PctUsed = float64(m.UsedBytes) / float64(m.TotalBytes) * 100
		}
		row.Precheck = real && !row.AlreadyMonitored
		rows = append(rows, row)
	}
	writeSecurityAudit(r, actor, role, "disk_detect_volumes", hostRef, "success", map[string]interface{}{"mounts": len(rows)})
	sendConfigJSONSuccess(w, http.StatusOK, map[string]interface{}{
		"host_ref":                hostRef,
		"rows":                    rows,
		"default_threshold_value": settings.DefaultThresholdValue,
		"default_threshold_unit":  settings.DefaultThresholdUnit,
		"default_check_interval":  settings.DefaultCheckInterval,
	}, fmt.Sprintf("%d mount(s) found on '%s'", len(rows), hostRef))
}

// ---------------------------------------------------------------------------
// Dashboard state view
// ---------------------------------------------------------------------------

type diskReadingView struct {
	At         string  `json:"at"`
	FreeBytes  uint64  `json:"free_bytes"`
	TotalBytes uint64  `json:"total_bytes"`
	PctFree    float64 `json:"pct_free"`
}

func diskReadingViews(history []diskReading) []diskReadingView {
	out := make([]diskReadingView, 0, len(history))
	for _, r := range history {
		out = append(out, diskReadingView{
			At:         r.At.Format("2006-01-02 15:04:05"),
			FreeBytes:  r.FreeBytes,
			TotalBytes: r.TotalBytes,
			PctFree:    diskPercentFree(r.FreeBytes, r.TotalBytes),
		})
	}
	return out
}

type diskVolumeView struct {
	VolumeID        string            `json:"volume_id"`
	HostRef         string            `json:"host_ref"`
	HostAddress     string            `json:"host_address"`
	HostMissing     bool              `json:"host_missing"`
	MountPath       string            `json:"mount_path"`
	Description     string            `json:"description,omitempty"`
	CheckInterval   string            `json:"check_interval"`
	IntervalSeconds int               `json:"interval_seconds"`
	ThresholdValue  float64           `json:"threshold_value"`
	ThresholdUnit   string            `json:"threshold_unit"`
	ThresholdLabel  string            `json:"threshold_label"`
	ThresholdBytes  uint64            `json:"threshold_bytes"`
	UseSudo         bool              `json:"use_sudo"`
	Enabled         bool              `json:"enabled"`
	Status          string            `json:"status"` // OK | TRIPPED | UNKNOWN | never | disabled
	LastCheckedAt   string            `json:"last_checked_at,omitempty"`
	LastFreeBytes   uint64            `json:"last_free_bytes"`
	LastTotalBytes  uint64            `json:"last_total_bytes"`
	LastUsedBytes   uint64            `json:"last_used_bytes"`
	PercentFree     float64           `json:"percent_free"`
	HasReading      bool              `json:"has_reading"`
	LastError       string            `json:"last_error,omitempty"`
	TrippedSince    string            `json:"tripped_since,omitempty"`
	LastAlertSent   string            `json:"last_alert_sent,omitempty"`
	PendingRecovery bool              `json:"pending_recovery"`
	NextDue         string            `json:"next_due,omitempty"`
	LastTraceID     string            `json:"last_trace_id,omitempty"`
	InFlight        bool              `json:"in_flight"`
	History         []diskReadingView `json:"history"`
}

func formatDiskTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}

func buildDiskMonitorStateView(now time.Time) map[string]interface{} {
	cfg := cache.GetConfig()
	settings, present := currentDiskMonitorSettings()
	state := diskStateStore.snapshot()
	inFlight := diskMonitor.inFlightHosts()

	summary := map[string]int{"ok": 0, "tripped": 0, "unknown": 0, "disabled": 0, "never": 0}
	views := make([]diskVolumeView, 0, len(settings.Volumes))
	var nextDue time.Time
	var lastCheck time.Time
	for _, v := range settings.Volumes {
		view := diskVolumeView{
			VolumeID:       v.VolumeID,
			HostRef:        v.HostRef,
			MountPath:      v.MountPath,
			Description:    v.Description,
			CheckInterval:  v.CheckInterval,
			ThresholdValue: v.ThresholdValue,
			ThresholdUnit:  v.ThresholdUnit,
			ThresholdLabel: formatDiskThreshold(v.ThresholdValue, v.ThresholdUnit),
			UseSudo:        v.UseSudo,
			Enabled:        v.isEnabled(),
			History:        []diskReadingView{},
		}
		view.IntervalSeconds = int(diskVolumeInterval(v, settings).Seconds())
		if cfg != nil {
			if host, ok := cfg.Hosts[v.HostRef]; ok {
				view.HostAddress = host.Host
				if host.Port > 0 && host.Port != 22 {
					view.HostAddress = fmt.Sprintf("%s:%d", host.Host, host.Port)
				}
				view.InFlight = inFlight[diskHostKey(v.HostRef, v.UseSudo)]
			} else {
				view.HostMissing = true
			}
		}
		if st, ok := state.Volumes[v.VolumeID]; ok {
			view.LastCheckedAt = formatDiskTime(st.LastCheckedAt)
			view.LastFreeBytes = st.LastFreeBytes
			view.LastTotalBytes = st.LastTotalBytes
			view.LastUsedBytes = st.LastUsedBytes
			view.HasReading = st.LastTotalBytes > 0
			view.PercentFree = diskPercentFree(st.LastFreeBytes, st.LastTotalBytes)
			view.ThresholdBytes = diskThresholdBytes(v.ThresholdValue, v.ThresholdUnit, st.LastTotalBytes)
			view.LastError = st.LastError
			view.TrippedSince = formatDiskTime(st.TrippedSince)
			view.LastAlertSent = formatDiskTime(st.LastAlertSent)
			view.PendingRecovery = st.PendingRecovery
			view.NextDue = formatDiskTime(st.NextDue)
			view.LastTraceID = st.LastTraceID
			view.History = diskReadingViews(st.History)
			view.Status = st.Status
			if st.LastCheckedAt.After(lastCheck) {
				lastCheck = st.LastCheckedAt
			}
		}
		switch {
		case !v.isEnabled():
			view.Status = "disabled"
			summary["disabled"]++
		case view.Status == "":
			view.Status = "never"
			summary["never"]++
		case view.Status == diskStatusOK:
			summary["ok"]++
		case view.Status == diskStatusTripped:
			summary["tripped"]++
		default:
			summary["unknown"]++
		}
		if v.isEnabled() && view.NextDue != "" {
			if st := state.Volumes[v.VolumeID]; st != nil && (nextDue.IsZero() || st.NextDue.Before(nextDue)) {
				nextDue = st.NextDue
			}
		}
		views = append(views, view)
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].HostRef != views[j].HostRef {
			return strings.ToLower(views[i].HostRef) < strings.ToLower(views[j].HostRef)
		}
		return views[i].MountPath < views[j].MountPath
	})

	hosts := make([]string, 0, len(inFlight))
	for key := range inFlight {
		hosts = append(hosts, strings.SplitN(key, "|", 2)[0])
	}
	sort.Strings(hosts)

	var smtpReady bool
	if cfg != nil {
		smtpReady = diskMonitorSMTPReady(cfg, settings)
	}
	return map[string]interface{}{
		"configured":          present,
		"enabled":             settings.isEnabled(),
		"smtp_ready":          smtpReady,
		"recipients":          settings.Recipients,
		"last_email_error":    state.LastEmailError,
		"last_email_error_at": formatDiskTime(state.LastEmailErrorAt),
		"last_email_sent_at":  formatDiskTime(state.LastEmailSentAt),
		"last_daily_summary":  state.LastDailySummaryDate,
		"in_flight_hosts":     hosts,
		"summary":             summary,
		"next_due":            formatDiskTime(nextDue),
		"last_check_at":       formatDiskTime(lastCheck),
		"server_time":         now.Format("2006-01-02 15:04:05"),
		"volumes":             views,
	}
}

// DiskMonitorStatus is the /api/status summary.
type DiskMonitorStatus struct {
	Configured     bool     `json:"configured"`
	Enabled        bool     `json:"enabled"`
	Volumes        int      `json:"volumes"`
	OK             int      `json:"ok"`
	Tripped        int      `json:"tripped"`
	Unknown        int      `json:"unknown"`
	Disabled       int      `json:"disabled"`
	NeverChecked   int      `json:"never_checked"`
	InFlightHosts  []string `json:"in_flight_hosts"`
	NextDue        string   `json:"next_due"`
	LastCheckAt    string   `json:"last_check_at"`
	SMTPReady      bool     `json:"smtp_ready"`
	LastEmailError string   `json:"last_email_error,omitempty"`
}

func diskMonitorStatusSummary() *DiskMonitorStatus {
	view := buildDiskMonitorStateView(time.Now())
	summary, _ := view["summary"].(map[string]int)
	hosts, _ := view["in_flight_hosts"].([]string)
	volumes, _ := view["volumes"].([]diskVolumeView)
	status := &DiskMonitorStatus{
		Configured:     view["configured"].(bool),
		Enabled:        view["enabled"].(bool),
		Volumes:        len(volumes),
		OK:             summary["ok"],
		Tripped:        summary["tripped"],
		Unknown:        summary["unknown"],
		Disabled:       summary["disabled"],
		NeverChecked:   summary["never"],
		InFlightHosts:  hosts,
		NextDue:        view["next_due"].(string),
		LastCheckAt:    view["last_check_at"].(string),
		SMTPReady:      view["smtp_ready"].(bool),
		LastEmailError: view["last_email_error"].(string),
	}
	if status.InFlightHosts == nil {
		status.InFlightHosts = []string{}
	}
	return status
}
