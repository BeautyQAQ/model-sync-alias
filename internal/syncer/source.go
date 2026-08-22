package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

const maxModelsResponseBytes = 16 << 20

type sourceSnapshot struct {
	Hash        [sha256.Size]byte
	Provider    sdkconfig.OpenAICompatibility
	GlobalProxy string
}

type modelsResponse struct {
	Data json.RawMessage `json:"data"`
}

type upstreamModel struct {
	ID string `json:"id"`
}

func loadSourceSnapshot(configPath, providerName string) (sourceSnapshot, error) {
	raw, errRead := os.ReadFile(configPath)
	if errRead != nil {
		return sourceSnapshot{}, fmt.Errorf("read CPA configuration: %w", errRead)
	}
	cfg, errParse := sdkconfig.ParseConfigBytes(raw)
	if errParse != nil {
		return sourceSnapshot{}, fmt.Errorf("parse CPA configuration: %w", errParse)
	}
	provider, errProvider := findProvider(cfg, providerName)
	if errProvider != nil {
		return sourceSnapshot{}, errProvider
	}
	return sourceSnapshot{
		Hash:        sha256.Sum256(raw),
		Provider:    *provider,
		GlobalProxy: cfg.ProxyURL,
	}, nil
}

func findProvider(cfg *sdkconfig.Config, providerName string) (*sdkconfig.OpenAICompatibility, error) {
	if cfg == nil {
		return nil, fmt.Errorf("CPA configuration is empty")
	}
	for index := range cfg.OpenAICompatibility {
		if cfg.OpenAICompatibility[index].Name == providerName {
			return &cfg.OpenAICompatibility[index], nil
		}
	}
	return nil, fmt.Errorf("OpenAI-compatible provider %q was not found", providerName)
}

func modelsEndpoint(baseURL string) (string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return "", fmt.Errorf("provider base-url is empty")
	}
	parsed, errParse := url.Parse(baseURL)
	if errParse != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("provider base-url is invalid")
	}
	if strings.HasSuffix(strings.ToLower(parsed.Path), "/models") {
		return baseURL, nil
	}
	return baseURL + "/models", nil
}

func fetchUpstreamModels(ctx context.Context, snapshot sourceSnapshot, timeout time.Duration) ([]string, error) {
	endpoint, errEndpoint := modelsEndpoint(snapshot.Provider.BaseURL)
	if errEndpoint != nil {
		return nil, errEndpoint
	}
	keys := activeKeys(snapshot.Provider.APIKeyEntries)
	if len(keys) == 0 {
		return nil, fmt.Errorf("provider has no active API keys")
	}
	var errorsSeen []string
	for _, key := range keys {
		ids, errFetch := fetchWithKey(ctx, endpoint, snapshot.Provider.Headers, key, snapshot.GlobalProxy, timeout)
		if errFetch == nil {
			return ids, nil
		}
		errorsSeen = append(errorsSeen, errFetch.Error())
	}
	return nil, fmt.Errorf("all %d API keys failed: %s", len(keys), strings.Join(errorsSeen, "; "))
}

func activeKeys(entries []sdkconfig.OpenAICompatibilityAPIKey) []sdkconfig.OpenAICompatibilityAPIKey {
	out := make([]sdkconfig.OpenAICompatibilityAPIKey, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.APIKey) == "" {
			continue
		}
		if entry.Weight != nil && *entry.Weight <= 0 {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func fetchWithKey(
	ctx context.Context,
	endpoint string,
	headers map[string]string,
	key sdkconfig.OpenAICompatibilityAPIKey,
	globalProxy string,
	timeout time.Duration,
) ([]string, error) {
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if errRequest != nil {
		return nil, fmt.Errorf("create models request: %w", errRequest)
	}
	for name, value := range headers {
		if strings.TrimSpace(name) != "" {
			req.Header.Set(name, value)
		}
	}
	if strings.TrimSpace(req.Header.Get("Authorization")) == "" {
		req.Header.Set("Authorization", "Bearer "+key.APIKey)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "CPA-AxonHub-Model-Sync/1.0")

	transport := http.DefaultTransport.(*http.Transport).Clone()
	proxyURL := strings.TrimSpace(key.ProxyURL)
	if proxyURL == "" {
		proxyURL = strings.TrimSpace(globalProxy)
	}
	if proxyURL != "" {
		parsedProxy, errProxy := url.Parse(proxyURL)
		if errProxy != nil || parsedProxy.Scheme == "" || parsedProxy.Host == "" {
			return nil, fmt.Errorf("invalid configured proxy URL")
		}
		transport.Proxy = http.ProxyURL(parsedProxy)
	}
	client := &http.Client{Transport: transport}
	if timeout > 0 {
		client.Timeout = timeout
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		if errors.Is(errDo, context.Canceled) {
			return nil, context.Canceled
		}
		if errors.Is(errDo, context.DeadlineExceeded) {
			return nil, fmt.Errorf("models request timed out")
		}
		return nil, fmt.Errorf("models request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxModelsResponseBytes+1))
	if errRead != nil {
		return nil, fmt.Errorf("read models response: %w", errRead)
	}
	if len(body) > maxModelsResponseBytes {
		return nil, fmt.Errorf("models response exceeds %d bytes", maxModelsResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("models endpoint returned HTTP %d", resp.StatusCode)
	}
	return decodeModelIDs(body)
}

func decodeModelIDs(body []byte) ([]string, error) {
	var envelope modelsResponse
	if errDecode := json.Unmarshal(body, &envelope); errDecode != nil {
		return nil, fmt.Errorf("decode models response: %w", errDecode)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil, fmt.Errorf("models response data must be an array")
	}
	var rows []upstreamModel
	if errDecode := json.Unmarshal(envelope.Data, &rows); errDecode != nil {
		return nil, fmt.Errorf("models response data must be an array: %w", errDecode)
	}
	seen := make(map[string]struct{}, len(rows))
	ids := make([]string, 0, len(rows))
	for index, row := range rows {
		id := strings.TrimSpace(row.ID)
		if id == "" {
			return nil, fmt.Errorf("models response row %d has an empty id", index)
		}
		key := strings.ToLower(id)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}
