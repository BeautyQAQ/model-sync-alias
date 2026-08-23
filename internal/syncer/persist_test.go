package syncer

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestApplyModelsStrictMirrorIncludingEmpty(t *testing.T) {
	configPath := writeTestConfig(t, []sdkconfig.OpenAICompatibilityModel{
		{Name: "stale", Alias: "old"},
		{Name: "GLM-5.2-think", Alias: "wrong", DisplayName: "Keep metadata"},
	})
	cfg := testSettings(configPath)
	snapshot, errSnapshot := loadSourceSnapshot(configPath, cfg.Provider)
	if errSnapshot != nil {
		t.Fatal(errSnapshot)
	}
	result, errApply := applyModels(snapshot, []string{"GLM-5.2-think", "new-model", "NEW-MODEL"}, cfg)
	if errApply != nil {
		t.Fatal(errApply)
	}
	if !result.Applied || !reflect.DeepEqual(result.Diff.Added, []string{"new-model"}) || !reflect.DeepEqual(result.Diff.Removed, []string{"stale"}) {
		t.Fatalf("unexpected apply result: %+v", result)
	}
	loaded, errLoad := sdkconfig.LoadConfig(configPath)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	provider, errProvider := findProvider(loaded, defaultProvider)
	if errProvider != nil {
		t.Fatal(errProvider)
	}
	if len(provider.Models) != 2 {
		t.Fatalf("models = %+v", provider.Models)
	}
	for _, model := range provider.Models {
		if model.Name == "GLM-5.2-think" && (model.Alias != "glm-5.2" || model.DisplayName != "Keep metadata") {
			t.Fatalf("surviving model was not recomputed while preserving metadata: %+v", model)
		}
		if model.Name == "new-model" && model.Alias != "new-model" {
			t.Fatalf("new standalone model did not receive a self-alias: %+v", model)
		}
	}
	if result.BackupPath == "" {
		t.Fatal("backup path is empty")
	}
	if _, errStat := os.Stat(result.BackupPath); errStat != nil {
		t.Fatalf("backup missing: %v", errStat)
	}

	secondSnapshot, errSecondSnapshot := loadSourceSnapshot(configPath, cfg.Provider)
	if errSecondSnapshot != nil {
		t.Fatal(errSecondSnapshot)
	}
	emptyResult, errEmpty := applyModels(secondSnapshot, []string{}, cfg)
	if errEmpty != nil {
		t.Fatal(errEmpty)
	}
	if !emptyResult.Applied || emptyResult.Diff.UpstreamCount != 0 {
		t.Fatalf("valid empty mirror result: %+v", emptyResult)
	}
	loaded, errLoad = sdkconfig.LoadConfig(configPath)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	provider, _ = findProvider(loaded, defaultProvider)
	if len(provider.Models) != 0 {
		t.Fatalf("valid empty upstream did not clear models: %+v", provider.Models)
	}
}

func TestApplyModelsNoOpDoesNotRewriteOrBackup(t *testing.T) {
	models := []sdkconfig.OpenAICompatibilityModel{{Name: "model-a", Alias: "model-a"}}
	configPath := writeTestConfig(t, models)
	cfg := testSettings(configPath)
	before, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatal(errRead)
	}
	snapshot, errSnapshot := loadSourceSnapshot(configPath, cfg.Provider)
	if errSnapshot != nil {
		t.Fatal(errSnapshot)
	}
	result, errApply := applyModels(snapshot, []string{"model-a"}, cfg)
	if errApply != nil {
		t.Fatal(errApply)
	}
	if result.Applied || result.Diff.Changed {
		t.Fatalf("no-op was applied: %+v", result)
	}
	after, _ := os.ReadFile(configPath)
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("no-op rewrote configuration")
	}
	if _, errStat := os.Stat(filepath.Join(filepath.Dir(configPath), "config_backup")); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("no-op created backup directory: %v", errStat)
	}
}

func TestApplyModelsAbortsAfterConcurrentEdit(t *testing.T) {
	configPath := writeTestConfig(t, []sdkconfig.OpenAICompatibilityModel{{Name: "old"}})
	cfg := testSettings(configPath)
	snapshot, errSnapshot := loadSourceSnapshot(configPath, cfg.Provider)
	if errSnapshot != nil {
		t.Fatal(errSnapshot)
	}
	raw, _ := os.ReadFile(configPath)
	raw = append(raw, []byte("\n# concurrent user edit\n")...)
	if errWrite := os.WriteFile(configPath, raw, 0o640); errWrite != nil {
		t.Fatal(errWrite)
	}
	_, errApply := applyModels(snapshot, []string{"new"}, cfg)
	if !errors.Is(errApply, errConcurrentConfigEdit) {
		t.Fatalf("error = %v, want concurrent edit", errApply)
	}
	if _, errStat := os.Stat(filepath.Join(filepath.Dir(configPath), "config_backup")); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("concurrent edit created backup directory: %v", errStat)
	}
}

func TestPruneBackupsKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	for index := 0; index < 5; index++ {
		name := filepath.Join(dir, fmt.Sprintf("axonhub_sync_20260817_12000%d.yaml", index))
		if errWrite := os.WriteFile(name, []byte("x"), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	if errPrune := pruneBackups(dir, 2); errPrune != nil {
		t.Fatal(errPrune)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 || !strings.Contains(entries[0].Name(), "120003") || !strings.Contains(entries[1].Name(), "120004") {
		t.Fatalf("retained entries = %+v", entries)
	}
}

func writeTestConfig(t *testing.T, models []sdkconfig.OpenAICompatibilityModel) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	var rows strings.Builder
	for _, model := range models {
		rows.WriteString("      - name: ")
		rows.WriteString(model.Name)
		rows.WriteString("\n        alias: ")
		rows.WriteString(model.Alias)
		rows.WriteString("\n")
		if model.DisplayName != "" {
			rows.WriteString("        display-name: ")
			rows.WriteString(model.DisplayName)
			rows.WriteString("\n")
		}
	}
	if len(models) == 0 {
		rows.WriteString("      []\n")
	}
	raw := fmt.Sprintf(`# retained test comment
host: 127.0.0.1
port: 18317
proxy-url: ""
api-keys:
  - client-key
openai-compatibility:
  - name: Other
    base-url: https://other.invalid/v1
    api-key-entries:
      - api-key: other-secret
    models:
      - name: other-model
        alias: other-alias
  - name: AxonHub
    base-url: https://axon.invalid/v1
    headers:
      X-Test: retained
    api-key-entries:
      - api-key: axon-secret
    models:
%s`, rows.String())
	if errWrite := os.WriteFile(path, []byte(raw), 0o640); errWrite != nil {
		t.Fatal(errWrite)
	}
	return path
}

func testSettings(configPath string) settings {
	return settings{
		Provider:        defaultProvider,
		ConfigPath:      configPath,
		Interval:        time.Hour,
		SyncOnStart:     true,
		RequestTimeout:  time.Second,
		BackupRetention: 30,
	}
}
