package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestConfigureReconcilesUnchangedSettingsAfterHostReload(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "deepseek-v4-pro"},
				{"id": "deepseek/deepseek-v4-pro-0813"},
				{"id": "claude-fable-5"},
			},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := fmt.Sprintf(`openai-compatibility:
  - name: AxonHub
    base-url: %q
    api-key-entries:
      - api-key: test-key
    models:
      - name: deepseek-v4-pro
        alias: ""
      - name: deepseek/deepseek-v4-pro-0813
        alias: deepseek-v4-pro
      - name: claude-fable-5
        alias: ""
`, upstream.URL+"/v1")
	if errWrite := os.WriteFile(configPath, []byte(configYAML), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	pluginYAML := []byte(fmt.Sprintf(`provider: AxonHub
config_path: %q
interval: 1h
sync_on_start: false
request_timeout: 3s
backup_retention: 2
exact_overrides:
  deepseek/deepseek-v4-pro-0813: deepseek-v4-pro
regex_overrides: []
`, configPath))

	manager := NewManager(nil)
	defer manager.Shutdown()
	if errConfigure := manager.Configure(pluginYAML); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("initial configure made %d upstream requests with sync_on_start disabled", got)
	}

	// CPA reconfigures every loaded plugin after any live config save, even if
	// that plugin's own settings did not change.
	if errConfigure := manager.Configure(pluginYAML); errConfigure != nil {
		t.Fatal(errConfigure)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		cfg, errLoad := sdkconfig.LoadConfig(configPath)
		if errLoad == nil && requests.Load() > 0 && len(cfg.OpenAICompatibility) == 1 {
			aliases := make(map[string]string, len(cfg.OpenAICompatibility[0].Models))
			for _, model := range cfg.OpenAICompatibility[0].Models {
				aliases[model.Name] = model.Alias
			}
			if aliases["deepseek-v4-pro"] == "deepseek-v4-pro" && aliases["claude-fable-5"] == "claude-fable-5" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("same-settings reconfigure did not restore the canonical alias; upstream requests = %d", requests.Load())
}

func TestRunReloadsExternalOverridesAndDoesNotApplyInvalidRules(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "vendor/model"}},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := fmt.Sprintf(`openai-compatibility:
  - name: AxonHub
    base-url: %q
    api-key-entries:
      - api-key: test-key
    models:
      - name: vendor/model
        alias: old-alias
`, upstream.URL+"/v1")
	if errWrite := os.WriteFile(configPath, []byte(configYAML), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	overridesPath := filepath.Join(dir, "overrides.yaml")
	writeOverrides := func(raw string) {
		t.Helper()
		if errWrite := os.WriteFile(overridesPath, []byte(raw), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	writeOverrides("exact_overrides:\n  vendor/model: first-alias\nregex_overrides: []\n")

	manager := NewManager(nil)
	defer manager.Shutdown()
	pluginYAML := []byte(fmt.Sprintf(`provider: AxonHub
config_path: %q
overrides_path: %q
interval: 1h
sync_on_start: false
request_timeout: 3s
`, configPath, overridesPath))
	if errConfigure := manager.Configure(pluginYAML); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	first, errFirst := manager.Run(context.Background(), false, true)
	if errFirst != nil {
		t.Fatal(errFirst)
	}
	if len(first.Models) != 1 || first.Models[0].Alias != "first-alias" {
		t.Fatalf("unexpected first preview: %+v", first.Models)
	}

	writeOverrides("exact_overrides:\n  vendor/model: second-alias\nregex_overrides: []\n")
	second, errSecond := manager.Run(context.Background(), false, true)
	if errSecond != nil {
		t.Fatal(errSecond)
	}
	if len(second.Models) != 1 || second.Models[0].Alias != "second-alias" {
		t.Fatalf("unexpected reloaded preview: %+v", second.Models)
	}

	before, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatal(errRead)
	}
	writeOverrides("regex_overrides:\n  - pattern: '('\n")
	if _, errRun := manager.Run(context.Background(), true, true); errRun == nil {
		t.Fatal("invalid external overrides unexpectedly succeeded")
	}
	after, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("CPA configuration changed after invalid external overrides")
	}
}
