package syncer

import (
	"fmt"
	"os"
	"path/filepath"
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

func TestParseSettingsRejectsRelativeOverridesPath(t *testing.T) {
	_, errParse := parseSettings([]byte("config_path: /tmp/config.yaml\noverrides_path: ./overrides.yaml\n"))
	if errParse == nil || !strings.Contains(errParse.Error(), "overrides_path must be an absolute path") {
		t.Fatalf("unexpected error: %v", errParse)
	}
}

func TestParseSettingsRejectsExternalAndInlineOverrides(t *testing.T) {
	_, errParse := parseSettings([]byte(`
config_path: /tmp/config.yaml
overrides_path: /tmp/overrides.yaml
exact_overrides:
  model-a: alias-a
`))
	if errParse == nil || !strings.Contains(errParse.Error(), "cannot be combined") {
		t.Fatalf("unexpected error: %v", errParse)
	}
}

func TestLoadOverridesReloadsExternalFile(t *testing.T) {
	dir := t.TempDir()
	overridesPath := filepath.Join(dir, "overrides.yaml")
	if errWrite := os.WriteFile(overridesPath, []byte(`
exact_overrides:
  vendor/model: first-alias
regex_overrides: []
`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errParse := parseSettings([]byte(fmt.Sprintf("config_path: /tmp/config.yaml\noverrides_path: %q\n", overridesPath)))
	if errParse != nil {
		t.Fatal(errParse)
	}
	first, errLoad := loadOverrides(cfg)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if got := first.ExactOverrides["vendor/model"]; got != "first-alias" {
		t.Fatalf("first alias = %q, want first-alias", got)
	}

	if errWrite := os.WriteFile(overridesPath, []byte(`
exact_overrides: {}
regex_overrides:
  - pattern: '^vendor/(.*)$'
    replacement: '$1'
`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	second, errReload := loadOverrides(cfg)
	if errReload != nil {
		t.Fatal(errReload)
	}
	if len(second.ExactOverrides) != 0 || len(second.RegexOverrides) != 1 {
		t.Fatalf("unexpected reloaded overrides: %+v", second)
	}
	if got := second.RegexOverrides[0].Regexp.ReplaceAllString("vendor/model", second.RegexOverrides[0].Replacement); got != "model" {
		t.Fatalf("reloaded regex alias = %q, want model", got)
	}
}

func TestLoadOverridesRejectsInvalidFile(t *testing.T) {
	dir := t.TempDir()
	overridesPath := filepath.Join(dir, "overrides.yaml")
	if errWrite := os.WriteFile(overridesPath, []byte("regex_overrides:\n  - pattern: '('\n"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errParse := parseSettings([]byte(fmt.Sprintf("config_path: /tmp/config.yaml\noverrides_path: %q\n", overridesPath)))
	if errParse != nil {
		t.Fatal(errParse)
	}
	_, errLoad := loadOverrides(cfg)
	if errLoad == nil || !strings.Contains(errLoad.Error(), "validate overrides_path") {
		t.Fatalf("unexpected error: %v", errLoad)
	}
}

func TestRepositoryOverridesFileIsValid(t *testing.T) {
	overridesPath, errAbs := filepath.Abs(filepath.Join("..", "..", "overrides.yaml"))
	if errAbs != nil {
		t.Fatal(errAbs)
	}
	loaded, errLoad := loadOverrides(settings{OverridesPath: overridesPath})
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if len(loaded.ExactOverrides) == 0 || len(loaded.RegexOverrides) == 0 {
		t.Fatalf("repository overrides are unexpectedly empty: %+v", loaded)
	}
}
