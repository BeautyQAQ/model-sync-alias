package syncer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type Status struct {
	Configured      bool      `json:"configured"`
	Running         bool      `json:"running"`
	LastAttempt     time.Time `json:"last_attempt,omitempty"`
	LastSuccess     time.Time `json:"last_success,omitempty"`
	NextRun         time.Time `json:"next_run,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	UpstreamCount   int       `json:"upstream_count"`
	ConfiguredCount int       `json:"configured_count"`
	AddedCount      int       `json:"added_count"`
	RemovedCount    int       `json:"removed_count"`
	AliasChanges    int       `json:"alias_changes"`
	AliasPoolCount  int       `json:"alias_pool_count"`
	Changed         bool      `json:"changed"`
	Applied         bool      `json:"applied"`
}

type Result struct {
	Status Status                               `json:"status"`
	Diff   Diff                                 `json:"diff"`
	Models []sdkconfig.OpenAICompatibilityModel `json:"models"`
}

type Manager struct {
	mu        sync.RWMutex
	settings  settings
	hasConfig bool
	status    Status
	cancel    context.CancelFunc
	done      chan struct{}
	runMu     sync.Mutex
	log       func(level, message string, fields map[string]any)
}

const startupSyncDelay = time.Second

func NewManager(logger func(level, message string, fields map[string]any)) *Manager {
	return &Manager{log: logger}
}

func (m *Manager) Configure(raw []byte) error {
	cfg, errParse := parseSettings(raw)
	if errParse != nil {
		m.stopWorker()
		m.mu.Lock()
		m.hasConfig = false
		m.status.Configured = false
		m.status.LastError = errParse.Error()
		m.mu.Unlock()
		return errParse
	}
	m.mu.RLock()
	same := m.hasConfig && settingsEqual(m.settings, cfg)
	m.mu.RUnlock()
	if same {
		return nil
	}
	m.stopWorker()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.mu.Lock()
	m.settings = cfg
	m.hasConfig = true
	m.status.Configured = true
	m.status.LastError = ""
	m.cancel = cancel
	m.done = done
	m.mu.Unlock()
	go m.worker(ctx, done, cfg)
	return nil
}

func (m *Manager) stopWorker() {
	m.mu.Lock()
	cancel := m.cancel
	done := m.done
	m.cancel = nil
	m.done = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

func (m *Manager) Shutdown() {
	m.stopWorker()
}

func (m *Manager) worker(ctx context.Context, done chan struct{}, cfg settings) {
	defer close(done)
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	m.setNextRun(time.Now().Add(cfg.Interval))
	if cfg.SyncOnStart {
		startupTimer := time.NewTimer(startupSyncDelay)
		select {
		case <-ctx.Done():
			if !startupTimer.Stop() {
				<-startupTimer.C
			}
			return
		case <-startupTimer.C:
			_, _ = m.Run(ctx, true, false)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			m.setNextRun(now.Add(cfg.Interval))
			_, _ = m.Run(ctx, true, false)
		}
	}
}

func (m *Manager) setNextRun(next time.Time) {
	m.mu.Lock()
	m.status.NextRun = next
	m.mu.Unlock()
}

func (m *Manager) currentSettings() (settings, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.hasConfig {
		return settings{}, fmt.Errorf("plugin is not configured")
	}
	return m.settings, nil
}

func (m *Manager) Run(ctx context.Context, apply, returnModels bool) (Result, error) {
	if !m.runMu.TryLock() {
		return Result{}, fmt.Errorf("a model synchronization is already running")
	}
	defer m.runMu.Unlock()
	cfg, errSettings := m.currentSettings()
	if errSettings != nil {
		return Result{}, errSettings
	}
	m.mu.Lock()
	m.status.Running = true
	m.status.LastAttempt = time.Now()
	m.status.LastError = ""
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.status.Running = false
		m.mu.Unlock()
	}()

	snapshot, errSnapshot := loadSourceSnapshot(cfg.ConfigPath, cfg.Provider)
	if errSnapshot != nil {
		m.recordError(errSnapshot)
		return Result{}, errSnapshot
	}
	ids, errFetch := fetchUpstreamModels(ctx, snapshot, cfg.RequestTimeout)
	if errFetch != nil {
		m.recordError(errFetch)
		return Result{}, errFetch
	}
	desired := buildDesiredModels(ids, snapshot.Provider.Models, cfg)
	diff := compareModels(snapshot.Provider.Models, desired)
	applied := false
	if apply && diff.Changed {
		result, errApply := applyModels(snapshot, ids, cfg)
		if errApply != nil {
			m.recordError(errApply)
			return Result{}, errApply
		}
		diff = result.Diff
		applied = result.Applied
	}
	now := time.Now()
	m.mu.Lock()
	m.status.LastSuccess = now
	m.status.LastError = ""
	m.status.UpstreamCount = diff.UpstreamCount
	m.status.ConfiguredCount = diff.ConfiguredCount
	if applied {
		m.status.ConfiguredCount = diff.UpstreamCount
	}
	m.status.AddedCount = len(diff.Added)
	m.status.RemovedCount = len(diff.Removed)
	m.status.AliasChanges = diff.AliasChanges
	m.status.AliasPoolCount = diff.AliasPoolCount
	m.status.Changed = diff.Changed
	m.status.Applied = applied
	status := m.status
	m.mu.Unlock()
	if m.log != nil {
		m.log("info", "AxonHub model synchronization completed", map[string]any{
			"upstream_models": diff.UpstreamCount,
			"added":           len(diff.Added),
			"removed":         len(diff.Removed),
			"alias_changes":   diff.AliasChanges,
			"changed":         diff.Changed,
			"applied":         applied,
		})
	}
	result := Result{Status: status, Diff: diff}
	if returnModels {
		result.Models = desired
	}
	return result, nil
}

func (m *Manager) recordError(err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	m.status.LastError = err.Error()
	m.mu.Unlock()
	if m.log != nil {
		level := "error"
		if errors.Is(err, errConcurrentConfigEdit) {
			level = "warn"
		}
		m.log(level, "AxonHub model synchronization failed", map[string]any{"error": err.Error()})
	}
}

func (m *Manager) StatusSnapshot() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}
