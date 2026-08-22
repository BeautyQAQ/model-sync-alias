package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/BeautyQAQ/model-sync-alias/internal/syncer"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	ID      = "axonhub-model-sync"
	Version = "1.0.3"
)

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	ManagementAPI bool `json:"management_api"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type managementRegistration struct {
	Routes []managementRoute `json:"routes"`
}

type managementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Description string `json:"Description"`
}

type managementRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Query   url.Values
	Body    []byte
}

type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

type Service struct {
	manager *syncer.Manager
	log     func(level, message string, fields map[string]any)
}

func NewService(logger func(level, message string, fields map[string]any)) *Service {
	return &Service{
		manager: syncer.NewManager(logger),
		log:     logger,
	}
}

func (s *Service) HandleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var lifecycle lifecycleRequest
		if errDecode := json.Unmarshal(request, &lifecycle); errDecode != nil {
			return nil, fmt.Errorf("decode lifecycle request: %w", errDecode)
		}
		if errConfigure := s.manager.Configure(lifecycle.ConfigYAML); errConfigure != nil && s.log != nil {
			s.log("error", "AxonHub model sync configuration is invalid", map[string]any{"error": errConfigure.Error()})
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistration{Routes: []managementRoute{
			{Method: http.MethodGet, Path: "plugins/" + ID + "/status", Description: "Current AxonHub model synchronization status."},
			{Method: http.MethodPost, Path: "plugins/" + ID + "/preview", Description: "Fetch and normalize AxonHub models without writing the CPA configuration."},
			{Method: http.MethodPost, Path: "plugins/" + ID + "/sync", Description: "Run an AxonHub model synchronization immediately."},
		}})
	case pluginabi.MethodManagementHandle:
		return s.handleManagement(request)
	case pluginabi.MethodPluginShutdown:
		s.Shutdown()
		return okEnvelope(struct{}{})
	default:
		return ErrorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func (s *Service) Shutdown() {
	s.manager.Shutdown()
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             ID,
			Version:          Version,
			Author:           "BeautyQAQ",
			GitHubRepository: "https://github.com/BeautyQAQ/model-sync-alias",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "provider", Type: pluginapi.ConfigFieldTypeString, Description: "Exact OpenAI-compatible provider name to synchronize."},
				{Name: "config_path", Type: pluginapi.ConfigFieldTypeString, Description: "Absolute path to the CPA YAML configuration."},
				{Name: "interval", Type: pluginapi.ConfigFieldTypeString, Description: "Synchronization interval as a Go duration, for example 3h."},
				{Name: "sync_on_start", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Synchronize immediately when the plugin starts."},
				{Name: "request_timeout", Type: pluginapi.ConfigFieldTypeString, Description: "Timeout for fetching the upstream model catalog."},
				{Name: "backup_retention", Type: pluginapi.ConfigFieldTypeInteger, Description: "Number of pre-change configuration backups to retain."},
				{Name: "exact_overrides", Type: pluginapi.ConfigFieldTypeObject, Description: "Exact upstream model ID to canonical alias overrides. An empty value keeps the raw ID."},
				{Name: "regex_overrides", Type: pluginapi.ConfigFieldTypeArray, Description: "Ordered regular-expression pattern and replacement overrides; the first match wins and unmatched IDs use built-in normalization."},
			},
		},
		Capabilities: registrationCapabilities{ManagementAPI: true},
	}
}

func (s *Service) handleManagement(raw []byte) ([]byte, error) {
	var req managementRequest
	if errDecode := json.Unmarshal(raw, &req); errDecode != nil {
		return nil, fmt.Errorf("decode management request: %w", errDecode)
	}
	path := strings.TrimRight(req.Path, "/")
	switch {
	case req.Method == http.MethodGet && strings.HasSuffix(path, "/status"):
		return jsonManagementResponse(http.StatusOK, s.manager.StatusSnapshot())
	case req.Method == http.MethodPost && strings.HasSuffix(path, "/preview"):
		result, errRun := s.manager.Run(context.Background(), false, true)
		if errRun != nil {
			return jsonManagementResponse(http.StatusBadGateway, map[string]any{"error": errRun.Error()})
		}
		return jsonManagementResponse(http.StatusOK, result)
	case req.Method == http.MethodPost && strings.HasSuffix(path, "/sync"):
		result, errRun := s.manager.Run(context.Background(), true, true)
		if errRun != nil {
			return jsonManagementResponse(http.StatusBadGateway, map[string]any{"error": errRun.Error()})
		}
		return jsonManagementResponse(http.StatusOK, result)
	default:
		return jsonManagementResponse(http.StatusNotFound, map[string]any{"error": "unknown management route"})
	}
}

func jsonManagementResponse(status int, value any) ([]byte, error) {
	body, errMarshal := json.MarshalIndent(value, "", "  ")
	if errMarshal != nil {
		return nil, errMarshal
	}
	return okEnvelope(managementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	})
}

func okEnvelope(value any) ([]byte, error) {
	result, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(pluginabi.Envelope{OK: true, Result: result})
}

func ErrorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(pluginabi.Envelope{OK: false, Error: &pluginabi.Error{Code: code, Message: message}})
	return raw
}
