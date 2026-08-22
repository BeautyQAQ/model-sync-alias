package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestDecodeModelIDsValidEmptyAndDuplicate(t *testing.T) {
	ids, errDecode := decodeModelIDs([]byte(`{"data":[]}`))
	if errDecode != nil || len(ids) != 0 {
		t.Fatalf("valid empty array = %v, %v", ids, errDecode)
	}
	ids, errDecode = decodeModelIDs([]byte(`{"object":"list","data":[{"id":"A"},{"id":"a"},{"id":"B"}]}`))
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if !reflect.DeepEqual(ids, []string{"A", "B"}) {
		t.Fatalf("deduplicated IDs = %#v", ids)
	}
}

func TestDecodeModelIDsRejectsMalformedStructures(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"data":null}`,
		`{"data":{}}`,
		`{"data":[{"owned_by":"x"}]}`,
		`not-json`,
	} {
		if _, errDecode := decodeModelIDs([]byte(body)); errDecode == nil {
			t.Errorf("decodeModelIDs(%q) succeeded", body)
		}
	}
}

func TestFetchUsesModelsEndpointAndFallsBackAcrossKeys(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
		}
		attempts.Add(1)
		if r.Header.Get("Authorization") == "Bearer first" {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer second" {
			t.Errorf("authorization header = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"one"},{"id":"ONE"},{"id":"two"}]}`))
	}))
	defer server.Close()
	snapshot := sourceSnapshot{Provider: sdkconfig.OpenAICompatibility{
		BaseURL: server.URL + "/v1/",
		APIKeyEntries: []sdkconfig.OpenAICompatibilityAPIKey{
			{APIKey: "first"},
			{APIKey: "disabled", Weight: intPointer(0)},
			{APIKey: "second"},
		},
	}}
	ids, errFetch := fetchUpstreamModels(context.Background(), snapshot, time.Second)
	if errFetch != nil {
		t.Fatal(errFetch)
	}
	if attempts.Load() != 2 || !reflect.DeepEqual(ids, []string{"one", "two"}) {
		t.Fatalf("attempts = %d, IDs = %#v", attempts.Load(), ids)
	}
}

func TestCustomAuthorizationHeaderWins(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Custom credential" {
			t.Errorf("authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	snapshot := sourceSnapshot{Provider: sdkconfig.OpenAICompatibility{
		BaseURL:       server.URL + "/models",
		Headers:       map[string]string{"authorization": "Custom credential"},
		APIKeyEntries: []sdkconfig.OpenAICompatibilityAPIKey{{APIKey: "secret"}},
	}}
	if _, errFetch := fetchUpstreamModels(context.Background(), snapshot, time.Second); errFetch != nil {
		t.Fatal(errFetch)
	}
}

func TestFetchFailureDoesNotTreatMalformed200AsEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":null}`))
	}))
	defer server.Close()
	snapshot := sourceSnapshot{Provider: sdkconfig.OpenAICompatibility{
		BaseURL:       server.URL,
		APIKeyEntries: []sdkconfig.OpenAICompatibilityAPIKey{{APIKey: "secret"}},
	}}
	if _, errFetch := fetchUpstreamModels(context.Background(), snapshot, time.Second); errFetch == nil {
		t.Fatal("malformed HTTP 200 response succeeded")
	}
}

func intPointer(value int) *int {
	return &value
}
