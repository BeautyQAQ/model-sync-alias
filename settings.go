package main

import (
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultProvider        = "AxonHub"
	defaultInterval        = 3 * time.Hour
	defaultRequestTimeout  = 30 * time.Second
	defaultBackupRetention = 30
)

type durationValue struct {
	time.Duration
}

func (d *durationValue) UnmarshalYAML(node *yaml.Node) error {
	if node == nil || node.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration must be a string")
	}
	parsed, errParse := time.ParseDuration(strings.TrimSpace(node.Value))
	if errParse != nil {
		return errParse
	}
	d.Duration = parsed
	return nil
}

type regexOverrideConfig struct {
	Pattern     string `yaml:"pattern" json:"pattern"`
	Replacement string `yaml:"replacement" json:"replacement"`
}

type compiledRegexOverride struct {
	Pattern     string
	Replacement string
	Regexp      *regexp.Regexp
}

type pluginSettingsYAML struct {
	Enabled         bool                  `yaml:"enabled"`
	Provider        string                `yaml:"provider"`
	ConfigPath      string                `yaml:"config_path"`
	Interval        durationValue         `yaml:"interval"`
	SyncOnStart     *bool                 `yaml:"sync_on_start"`
	RequestTimeout  durationValue         `yaml:"request_timeout"`
	BackupRetention int                   `yaml:"backup_retention"`
	ExactOverrides  map[string]string     `yaml:"exact_overrides"`
	RegexOverrides  []regexOverrideConfig `yaml:"regex_overrides"`
}

type settings struct {
	Provider        string
	ConfigPath      string
	Interval        time.Duration
	SyncOnStart     bool
	RequestTimeout  time.Duration
	BackupRetention int
	ExactOverrides  map[string]string
	RegexOverrides  []compiledRegexOverride
}

func parseSettings(raw []byte) (settings, error) {
	var input pluginSettingsYAML
	if errUnmarshal := yaml.Unmarshal(raw, &input); errUnmarshal != nil {
		return settings{}, fmt.Errorf("decode plugin configuration: %w", errUnmarshal)
	}

	provider := strings.TrimSpace(input.Provider)
	if provider == "" {
		provider = defaultProvider
	}
	configPath := strings.TrimSpace(input.ConfigPath)
	if configPath == "" {
		return settings{}, fmt.Errorf("config_path is required")
	}
	absPath, errAbs := filepath.Abs(configPath)
	if errAbs != nil {
		return settings{}, fmt.Errorf("resolve config_path: %w", errAbs)
	}

	interval := input.Interval.Duration
	if interval == 0 {
		interval = defaultInterval
	}
	if interval < time.Second {
		return settings{}, fmt.Errorf("interval must be at least 1s")
	}
	requestTimeout := input.RequestTimeout.Duration
	if requestTimeout == 0 {
		requestTimeout = defaultRequestTimeout
	}
	if requestTimeout < time.Second {
		return settings{}, fmt.Errorf("request_timeout must be at least 1s")
	}
	retention := input.BackupRetention
	if retention == 0 {
		retention = defaultBackupRetention
	}
	if retention < 1 {
		return settings{}, fmt.Errorf("backup_retention must be at least 1")
	}
	syncOnStart := true
	if input.SyncOnStart != nil {
		syncOnStart = *input.SyncOnStart
	}

	exact := make(map[string]string, len(input.ExactOverrides))
	for name, alias := range input.ExactOverrides {
		name = strings.TrimSpace(name)
		if name == "" {
			return settings{}, fmt.Errorf("exact_overrides contains an empty model name")
		}
		exact[name] = strings.TrimSpace(alias)
	}
	compiled := make([]compiledRegexOverride, 0, len(input.RegexOverrides))
	for index, override := range input.RegexOverrides {
		pattern := strings.TrimSpace(override.Pattern)
		if pattern == "" {
			return settings{}, fmt.Errorf("regex_overrides[%d].pattern is required", index)
		}
		re, errCompile := regexp.Compile(pattern)
		if errCompile != nil {
			return settings{}, fmt.Errorf("compile regex_overrides[%d]: %w", index, errCompile)
		}
		compiled = append(compiled, compiledRegexOverride{
			Pattern:     pattern,
			Replacement: strings.TrimSpace(override.Replacement),
			Regexp:      re,
		})
	}

	return settings{
		Provider:        provider,
		ConfigPath:      filepath.Clean(absPath),
		Interval:        interval,
		SyncOnStart:     syncOnStart,
		RequestTimeout:  requestTimeout,
		BackupRetention: retention,
		ExactOverrides:  exact,
		RegexOverrides:  compiled,
	}, nil
}

func settingsEqual(left, right settings) bool {
	return reflect.DeepEqual(left, right)
}
