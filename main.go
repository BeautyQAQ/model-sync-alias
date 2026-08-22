package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	pluginID      = "axonhub-model-sync"
	pluginVersion = "1.0.2"
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

var manager = newSyncManager(hostLog)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = length
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	manager.shutdown()
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var lifecycle lifecycleRequest
		if errDecode := json.Unmarshal(request, &lifecycle); errDecode != nil {
			return nil, fmt.Errorf("decode lifecycle request: %w", errDecode)
		}
		if errConfigure := manager.configure(lifecycle.ConfigYAML); errConfigure != nil {
			hostLog("error", "AxonHub model sync configuration is invalid", map[string]any{"error": errConfigure.Error()})
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistration{Routes: []managementRoute{
			{Method: http.MethodGet, Path: "plugins/" + pluginID + "/status", Description: "Current AxonHub model synchronization status."},
			{Method: http.MethodPost, Path: "plugins/" + pluginID + "/preview", Description: "Fetch and normalize AxonHub models without writing the CPA configuration."},
			{Method: http.MethodPost, Path: "plugins/" + pluginID + "/sync", Description: "Run an AxonHub model synchronization immediately."},
		}})
	case pluginabi.MethodManagementHandle:
		return handleManagement(request)
	case pluginabi.MethodPluginShutdown:
		manager.shutdown()
		return okEnvelope(struct{}{})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginID,
			Version:          pluginVersion,
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
				{Name: "regex_overrides", Type: pluginapi.ConfigFieldTypeArray, Description: "Ordered regular-expression pattern and replacement overrides."},
			},
		},
		Capabilities: registrationCapabilities{ManagementAPI: true},
	}
}

func handleManagement(raw []byte) ([]byte, error) {
	var req managementRequest
	if errDecode := json.Unmarshal(raw, &req); errDecode != nil {
		return nil, fmt.Errorf("decode management request: %w", errDecode)
	}
	path := strings.TrimRight(req.Path, "/")
	switch {
	case req.Method == http.MethodGet && strings.HasSuffix(path, "/status"):
		return jsonManagementResponse(http.StatusOK, manager.statusSnapshot())
	case req.Method == http.MethodPost && strings.HasSuffix(path, "/preview"):
		result, errRun := manager.run(contextFromManagementRequest(), false, true)
		if errRun != nil {
			return jsonManagementResponse(http.StatusBadGateway, map[string]any{"error": errRun.Error()})
		}
		return jsonManagementResponse(http.StatusOK, result)
	case req.Method == http.MethodPost && strings.HasSuffix(path, "/sync"):
		result, errRun := manager.run(contextFromManagementRequest(), true, true)
		if errRun != nil {
			return jsonManagementResponse(http.StatusBadGateway, map[string]any{"error": errRun.Error()})
		}
		return jsonManagementResponse(http.StatusOK, result)
	default:
		return jsonManagementResponse(http.StatusNotFound, map[string]any{"error": "unknown management route"})
	}
}

func contextFromManagementRequest() context.Context {
	return context.Background()
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

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(pluginabi.Envelope{OK: false, Error: &pluginabi.Error{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

func hostLog(level, message string, fields map[string]any) {
	payload, errMarshal := json.Marshal(map[string]any{
		"level":   level,
		"message": message,
		"fields":  fields,
	})
	if errMarshal != nil {
		return
	}
	callHost(pluginabi.MethodHostLog, payload)
}

func callHost(method string, payload []byte) {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var response C.cliproxy_buffer
	var request *C.uint8_t
	if len(payload) > 0 {
		request = (*C.uint8_t)(C.CBytes(payload))
		defer C.free(unsafe.Pointer(request))
	}
	if C.call_host_api(cMethod, request, C.size_t(len(payload)), &response) == 0 && response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}
}
