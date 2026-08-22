package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseSettingsDefaults(t *testing.T) {
	cfg, errParse := parseSettings([]byte("config_path: ./config.yaml\n"))
	if errParse != nil {
		t.Fatal(errParse)
	}
	if cfg.Provider != defaultProvider || cfg.Interval != 3*time.Hour || cfg.RequestTimeout != 30*time.Second || cfg.BackupRetention != 30 || !cfg.SyncOnStart {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestParseSettingsRejectsBadRegex(t *testing.T) {
	_, errParse := parseSettings([]byte(`
config_path: /tmp/config.yaml
regex_overrides:
  - pattern: '('
    replacement: x
`))
	if errParse == nil || !strings.Contains(errParse.Error(), "compile regex_overrides[0]") {
		t.Fatalf("unexpected error: %v", errParse)
	}
}
