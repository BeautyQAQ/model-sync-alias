package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestLiveCPACatalog(t *testing.T) {
	configPath := os.Getenv("AXONHUB_LIVE_CONFIG")
	if configPath == "" {
		t.Skip("set AXONHUB_LIVE_CONFIG to audit the running CPA model catalog")
	}
	cfg, errLoad := sdkconfig.LoadConfig(configPath)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if len(cfg.APIKeys) == 0 {
		t.Fatal("CPA has no frontend API key")
	}
	host := cfg.Host
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	endpoint := fmt.Sprintf("http://%s:%d/v1/models", host, cfg.Port)
	req, errRequest := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKeys[0])
	client := &http.Client{Timeout: 5 * time.Second}
	resp, errDo := client.Do(req)
	if errDo != nil {
		t.Fatal(errDo)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("model catalog returned HTTP %d", resp.StatusCode)
	}
	var catalog struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if errDecode := json.NewDecoder(resp.Body).Decode(&catalog); errDecode != nil {
		t.Fatal(errDecode)
	}
	ids := make(map[string]bool, len(catalog.Data))
	for _, model := range catalog.Data {
		ids[model.ID] = true
	}
	for _, wanted := range []string{
		"deepseek-v4-flash", "deepseek-v4-pro", "glm-5.2", "gpt-oss-20b", "gpt-oss-120b",
		"qwen3.6", "MiniMax-M3", "FLUX.2-klein-4B",
	} {
		if !ids[wanted] {
			t.Errorf("canonical model %q is missing", wanted)
		}
	}
	for _, unwanted := range []string{
		"DeepSeek-V4-Flash-0731-262K", "DeepSeek-V4-Flash-0731-262K-think",
		"GLM-5.2-Long-think", "openai/gpt-oss-20b", "Qwen/Qwen3.6-27B-FP8",
	} {
		if ids[unwanted] {
			t.Errorf("raw or stale model %q is still published", unwanted)
		}
	}
	if t.Failed() {
		return
	}
	t.Logf("running CPA catalog contains %d public model IDs and all audited AxonHub aliases are canonical", len(ids))
}
