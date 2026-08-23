package syncer

import (
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
