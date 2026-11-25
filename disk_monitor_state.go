package main

// Runtime state of the Disk Monitor: per-volume status, last readings, alert
// cooldown markers and the sparkline history. It lives in memory and is
// persisted (debounced, atomically) to logs/disk_monitor/state.json so a
// restart keeps the dashboard, the cooldowns and the daily-summary marker.
// It is deliberately kept out of config.json so checks never rewrite the
// config file (which would trigger hot reloads and git noise).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	diskMonitorStateVersion = 1
	diskMonitorFlushDelay   = 2 * time.Second
)

var diskMonitorStatePath = filepath.Join("logs", "disk_monitor", "state.json")

type diskReading struct {
	At         time.Time `json:"at"`
	FreeBytes  uint64    `json:"free_bytes"`
	TotalBytes uint64    `json:"total_bytes"`
}

type diskVolumeState struct {
	Status          string        `json:"status"` // OK | TRIPPED | UNKNOWN | "" (never checked)
	LastCheckedAt   time.Time     `json:"last_checked_at"`
	LastFreeBytes   uint64        `json:"last_free_bytes"`
	LastTotalBytes  uint64        `json:"last_total_bytes"`
	LastUsedBytes   uint64        `json:"last_used_bytes"`
	LastError       string        `json:"last_error,omitempty"`
	TrippedSince    time.Time     `json:"tripped_since"`
	LastAlertSent   time.Time     `json:"last_alert_sent"`
	PendingRecovery bool          `json:"pending_recovery,omitempty"`
	NextDue         time.Time     `json:"next_due"`
	LastTraceID     string        `json:"last_trace_id,omitempty"`
	History         []diskReading `json:"history"`
}

type diskMonitorState struct {
	Version              int                         `json:"version"`
	UpdatedAt            time.Time                   `json:"updated_at"`
	LastDailySummaryDate string                      `json:"last_daily_summary_date,omitempty"`
	LastEmailError       string                      `json:"last_email_error,omitempty"`
	LastEmailErrorAt     time.Time                   `json:"last_email_error_at"`
	LastEmailSentAt      time.Time                   `json:"last_email_sent_at"`
	Volumes              map[string]*diskVolumeState `json:"volumes"`
}

type diskStateStoreType struct {
	mu    sync.Mutex
	state diskMonitorState
	dirty bool
	timer *time.Timer
}

var diskStateStore = &diskStateStoreType{state: diskMonitorState{Version: diskMonitorStateVersion, Volumes: map[string]*diskVolumeState{}}}

// loadDiskMonitorState reads the persisted state at startup (missing = empty).
func loadDiskMonitorState() {
	diskStateStore.mu.Lock()
	defer diskStateStore.mu.Unlock()
	diskStateStore.state = diskMonitorState{Version: diskMonitorStateVersion, Volumes: map[string]*diskVolumeState{}}
	data, err := os.ReadFile(diskMonitorStatePath)
	if err != nil {
		return
	}
	var loaded diskMonitorState
	if err := json.Unmarshal(data, &loaded); err != nil {
		fmt.Printf("[DISK] Ignoring unreadable state file %s: %v\n", diskMonitorStatePath, err)
		return
	}
	if loaded.Volumes == nil {
		loaded.Volumes = map[string]*diskVolumeState{}
	}
	for _, st := range loaded.Volumes {
		if st.History == nil {
			st.History = []diskReading{}
		}
	}
	loaded.Version = diskMonitorStateVersion
	diskStateStore.state = loaded
}

// update mutates the state under lock and schedules a flush.
func (s *diskStateStoreType) update(fn func(*diskMonitorState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.state)
	s.state.UpdatedAt = time.Now()
	s.dirty = true
	if s.timer == nil {
		s.timer = time.AfterFunc(diskMonitorFlushDelay, s.flush)
	}
}

// volumeState returns (creating if needed) the entry for a volume. Callers must
// hold the lock, i.e. use it inside update().
func (st *diskMonitorState) volumeState(id string) *diskVolumeState {
	v, ok := st.Volumes[id]
	if !ok {
		v = &diskVolumeState{History: []diskReading{}}
		st.Volumes[id] = v
	}
	return v
}

func (s *diskStateStoreType) volume(id string) (diskVolumeState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.state.Volumes[id]
	if !ok {
		return diskVolumeState{}, false
	}
	return cloneDiskVolumeState(st), true
}

func (s *diskStateStoreType) removeVolume(id string) {
	s.update(func(st *diskMonitorState) { delete(st.Volumes, id) })
}

// snapshot returns a deep copy for read-only consumers (API, alert engine).
func (s *diskStateStoreType) snapshot() diskMonitorState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.state
	out.Volumes = make(map[string]*diskVolumeState, len(s.state.Volumes))
	for id, st := range s.state.Volumes {
		clone := cloneDiskVolumeState(st)
		out.Volumes[id] = &clone
	}
	return out
}

func cloneDiskVolumeState(st *diskVolumeState) diskVolumeState {
	clone := *st
	clone.History = append([]diskReading{}, st.History...)
	return clone
}

// flush writes the state file if it changed. Safe to call from the timer and
// synchronously at shutdown.
func (s *diskStateStoreType) flush() {
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if !s.dirty {
		s.mu.Unlock()
		return
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "\t")
	err := encoder.Encode(s.state)
	s.dirty = false
	s.mu.Unlock()
	if err != nil {
		fmt.Printf("[DISK] Failed to encode state: %v\n", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(diskMonitorStatePath), privateDirMode); err != nil {
		fmt.Printf("[DISK] Failed to create state directory: %v\n", err)
		return
	}
	tmp := diskMonitorStatePath + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), privateFileMode); err != nil {
		fmt.Printf("[DISK] Failed to write state file: %v\n", err)
		return
	}
	if err := os.Rename(tmp, diskMonitorStatePath); err != nil {
		fmt.Printf("[DISK] Failed to replace state file: %v\n", err)
	}
}

// pruneDiskMonitorState drops state for volumes that no longer exist in config.
func pruneDiskMonitorState(settings DiskMonitorConfig) {
	live := make(map[string]bool, len(settings.Volumes))
	for _, v := range settings.Volumes {
		live[v.VolumeID] = true
	}
	stale := make([]string, 0)
	diskStateStore.mu.Lock()
	for id := range diskStateStore.state.Volumes {
		if !live[id] {
			stale = append(stale, id)
		}
	}
	diskStateStore.mu.Unlock()
	if len(stale) == 0 {
		return
	}
	diskStateStore.update(func(st *diskMonitorState) {
		for _, id := range stale {
			delete(st.Volumes, id)
		}
	})
}
