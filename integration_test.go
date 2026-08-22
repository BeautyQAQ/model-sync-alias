package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestCPAPluginEndToEnd(t *testing.T) {
	if os.Getenv("CPA_INTEGRATION") != "1" {
		t.Skip("set CPA_INTEGRATION=1 to run the CPA process integration test")
	}
	cpaBinary := os.Getenv("CPA_BINARY")
	pluginSO := os.Getenv("CPA_PLUGIN_SO")
	if cpaBinary == "" || pluginSO == "" {
		t.Fatal("CPA_BINARY and CPA_PLUGIN_SO are required")
	}

	var upstreamMu sync.RWMutex
	upstreamIDs := []string{
		"GLM-5.2-think",
		"z-ai/glm-5.2",
		"openai/gpt-oss-20b",
		"openai/gpt-oss-120b",
	}
	receivedModels := make(chan string, 10)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
			upstreamMu.RLock()
			ids := append([]string(nil), upstreamIDs...)
			upstreamMu.RUnlock()
			rows := make([]map[string]any, 0, len(ids))
			for _, id := range ids {
				rows = append(rows, map[string]any{"id": id, "object": "model", "owned_by": "test"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": rows})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
			var body struct {
				Model string `json:"model"`
			}
			if errDecode := json.NewDecoder(r.Body).Decode(&body); errDecode != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			receivedModels <- body.Model
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "chatcmpl-test", "object": "chat.completion", "created": time.Now().Unix(), "model": body.Model,
				"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
				"usage":   map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	tempDir := t.TempDir()
	pluginsDir := filepath.Join(tempDir, "plugins")
	if errMkdir := os.MkdirAll(pluginsDir, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	installedSO := filepath.Join(pluginsDir, pluginID+".so")
	copyIntegrationFile(t, pluginSO, installedSO, 0o755)
	port := availablePort(t)
	configPath := filepath.Join(tempDir, "config.yaml")
	configYAML := fmt.Sprintf(`host: 127.0.0.1
port: %d
remote-management:
  allow-remote: false
  secret-key: integration-management
  disable-control-panel: true
auth-dir: %q
api-keys:
  - integration-client
plugins:
  enabled: true
  dir: %q
  configs:
    axonhub-model-sync:
      enabled: true
      provider: AxonHub
      config_path: %q
      interval: 1h
      sync_on_start: true
      request_timeout: 3s
      backup_retention: 5
      exact_overrides: {}
      regex_overrides: []
openai-compatibility:
  - name: AxonHub
    base-url: %q
    api-key-entries:
      - api-key: upstream-key
    models:
      - name: stale-model
        alias: stale
      - name: openai/gpt-oss-20b
        alias: gpt-oss-120b
`, port, filepath.Join(tempDir, "auths"), pluginsDir, configPath, upstream.URL+"/v1")
	if errWrite := os.WriteFile(configPath, []byte(configYAML), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	logPath := filepath.Join(tempDir, "cpa.log")
	logFile, errLog := os.Create(logPath)
	if errLog != nil {
		t.Fatal(errLog)
	}
	command := exec.Command(cpaBinary, "--config", configPath, "--local-model")
	command.Dir = tempDir
	command.Stdout = logFile
	command.Stderr = logFile
	if errStart := command.Start(); errStart != nil {
		_ = logFile.Close()
		t.Fatal(errStart)
	}
	defer stopIntegrationProcess(command, logFile)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	waitForIntegration(t, 15*time.Second, func() error {
		models, errModels := configuredModels(configPath)
		if errModels != nil {
			return errModels
		}
		if len(models) != 4 {
			return fmt.Errorf("configured model count = %d", len(models))
		}
		return nil
	}, logPath)

	statusCode, _ := integrationRequest(t, http.MethodGet, baseURL+"/v0/management/plugins/"+pluginID+"/status", "", nil)
	if statusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated management status = %d, want 401", statusCode)
	}
	statusCode, body := integrationRequest(t, http.MethodGet, baseURL+"/v0/management/plugins/"+pluginID+"/status", "integration-management", nil)
	if statusCode != http.StatusOK || !bytes.Contains(body, []byte(`"upstream_count":4`)) {
		t.Fatalf("authenticated status = %d, body = %s", statusCode, body)
	}

	listed := waitForModelCatalog(t, baseURL, logPath, []string{"glm-5.2", "gpt-oss-20b", "gpt-oss-120b"}, []string{"GLM-5.2-think", "stale"})
	_ = listed
	postChat(t, baseURL, "gpt-oss-20b")
	if raw := receiveRawModel(t, receivedModels); raw != "openai/gpt-oss-20b" {
		t.Fatalf("gpt-oss-20b routed to %q", raw)
	}
	postChat(t, baseURL, "glm-5.2")
	if raw := receiveRawModel(t, receivedModels); raw != "GLM-5.2-think" && raw != "z-ai/glm-5.2" {
		t.Fatalf("glm-5.2 routed to %q", raw)
	}

	upstreamMu.Lock()
	upstreamIDs = []string{"DeepSeek-V4-Flash-0731-think"}
	upstreamMu.Unlock()
	statusCode, body = integrationRequest(t, http.MethodPost, baseURL+"/v0/management/plugins/"+pluginID+"/sync", "integration-management", []byte(`{}`))
	if statusCode != http.StatusOK {
		t.Fatalf("manual sync = %d, body = %s", statusCode, body)
	}
	waitForIntegration(t, 10*time.Second, func() error {
		models, errModels := configuredModels(configPath)
		if errModels != nil {
			return errModels
		}
		if len(models) != 1 || models[0].Alias != "deepseek-v4-flash" {
			return fmt.Errorf("hot-reloaded models = %+v", models)
		}
		return nil
	}, logPath)
	waitForModelCatalog(t, baseURL, logPath, []string{"deepseek-v4-flash"}, []string{"glm-5.2", "gpt-oss-20b"})
	postChat(t, baseURL, "deepseek-v4-flash")
	if raw := receiveRawModel(t, receivedModels); raw != "DeepSeek-V4-Flash-0731-think" {
		t.Fatalf("deepseek-v4-flash routed to %q", raw)
	}
}

func copyIntegrationFile(t *testing.T, source, destination string, mode os.FileMode) {
	t.Helper()
	in, errOpen := os.Open(source)
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	defer func() { _ = in.Close() }()
	out, errCreate := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	if _, errCopy := io.Copy(out, in); errCopy != nil {
		_ = out.Close()
		t.Fatal(errCopy)
	}
	if errClose := out.Close(); errClose != nil {
		t.Fatal(errClose)
	}
}

func availablePort(t *testing.T) int {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func stopIntegrationProcess(command *exec.Cmd, logFile *os.File) {
	if command != nil && command.Process != nil {
		_ = command.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() {
			_ = command.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
	}
	if logFile != nil {
		_ = logFile.Close()
	}
}

func waitForIntegration(t *testing.T, timeout time.Duration, check func() error, logPath string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastError error
	for time.Now().Before(deadline) {
		if errCheck := check(); errCheck == nil {
			return
		} else {
			lastError = errCheck
		}
		time.Sleep(250 * time.Millisecond)
	}
	logs, _ := os.ReadFile(logPath)
	t.Fatalf("integration wait timed out: %v\nCPA logs:\n%s", lastError, logs)
}

func configuredModels(configPath string) ([]sdkconfig.OpenAICompatibilityModel, error) {
	cfg, errLoad := sdkconfig.LoadConfig(configPath)
	if errLoad != nil {
		return nil, errLoad
	}
	provider, errProvider := findProvider(cfg, defaultProvider)
	if errProvider != nil {
		return nil, errProvider
	}
	return provider.Models, nil
}

func integrationRequest(t *testing.T, method, url, managementKey string, body []byte) (int, []byte) {
	t.Helper()
	req, errRequest := http.NewRequestWithContext(context.Background(), method, url, bytes.NewReader(body))
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	if managementKey != "" {
		req.Header.Set("X-Management-Key", managementKey)
	} else {
		req.Header.Set("Authorization", "Bearer integration-client")
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, errDo := client.Do(req)
	if errDo != nil {
		t.Fatal(errDo)
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		t.Fatal(errRead)
	}
	return resp.StatusCode, responseBody
}

func waitForModelCatalog(t *testing.T, baseURL, logPath string, wanted, unwanted []string) map[string]bool {
	t.Helper()
	var listed map[string]bool
	waitForIntegration(t, 10*time.Second, func() error {
		status, body := integrationRequest(t, http.MethodGet, baseURL+"/v1/models", "", nil)
		if status != http.StatusOK {
			return fmt.Errorf("model catalog status = %d", status)
		}
		var response struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if errDecode := json.Unmarshal(body, &response); errDecode != nil {
			return errDecode
		}
		listed = make(map[string]bool, len(response.Data))
		for _, model := range response.Data {
			listed[model.ID] = true
		}
		for _, id := range wanted {
			if !listed[id] {
				return fmt.Errorf("model %q is missing from %v", id, listed)
			}
		}
		for _, id := range unwanted {
			if listed[id] {
				return fmt.Errorf("model %q is still listed", id)
			}
		}
		return nil
	}, logPath)
	return listed
}

func postChat(t *testing.T, baseURL, model string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": "ping"}},
		"stream":   false,
	})
	status, response := integrationRequest(t, http.MethodPost, baseURL+"/v1/chat/completions", "", body)
	if status != http.StatusOK {
		t.Fatalf("chat for %s = %d, body = %s", model, status, response)
	}
}

func receiveRawModel(t *testing.T, received <-chan string) string {
	t.Helper()
	select {
	case model := <-received:
		return model
	case <-time.After(5 * time.Second):
		t.Fatal("upstream did not receive chat request")
		return ""
	}
}
