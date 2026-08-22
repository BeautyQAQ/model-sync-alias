package plugin

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func TestManagementRegistration(t *testing.T) {
	service := NewService(nil)
	defer service.Shutdown()

	raw, errHandle := service.HandleMethod(pluginabi.MethodManagementRegister, nil)
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	envelope := decodeEnvelope(t, raw)
	if !envelope.OK {
		t.Fatalf("management registration failed: %+v", envelope.Error)
	}
	var registration managementRegistration
	if errDecode := json.Unmarshal(envelope.Result, &registration); errDecode != nil {
		t.Fatal(errDecode)
	}
	wantPaths := []string{
		"plugins/" + ID + "/status",
		"plugins/" + ID + "/preview",
		"plugins/" + ID + "/sync",
	}
	if len(registration.Routes) != len(wantPaths) {
		t.Fatalf("route count = %d, want %d", len(registration.Routes), len(wantPaths))
	}
	for index, want := range wantPaths {
		if got := registration.Routes[index].Path; got != want {
			t.Errorf("route %d path = %q, want %q", index, got, want)
		}
	}
}

func TestInvalidConfigurationIsLoggedAndStillRegisters(t *testing.T) {
	var loggedError string
	service := NewService(func(level, _ string, fields map[string]any) {
		if level == "error" {
			loggedError, _ = fields["error"].(string)
		}
	})
	defer service.Shutdown()

	request, errMarshal := json.Marshal(lifecycleRequest{ConfigYAML: []byte("provider: AxonHub\n")})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	raw, errHandle := service.HandleMethod(pluginabi.MethodPluginRegister, request)
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	envelope := decodeEnvelope(t, raw)
	if !envelope.OK {
		t.Fatalf("plugin registration failed: %+v", envelope.Error)
	}
	var got registration
	if errDecode := json.Unmarshal(envelope.Result, &got); errDecode != nil {
		t.Fatal(errDecode)
	}
	if got.Metadata.Name != ID || got.Metadata.Version != Version || !got.Capabilities.ManagementAPI {
		t.Fatalf("unexpected registration: %+v", got)
	}
	if loggedError == "" {
		t.Fatal("invalid configuration was not logged")
	}
}

func TestUnknownMethodReturnsErrorEnvelope(t *testing.T) {
	service := NewService(nil)
	defer service.Shutdown()

	raw, errHandle := service.HandleMethod("unknown", nil)
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	envelope := decodeEnvelope(t, raw)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "unknown_method" {
		t.Fatalf("unexpected envelope: %+v", envelope)
	}
}

func decodeEnvelope(t *testing.T, raw []byte) pluginabi.Envelope {
	t.Helper()
	var envelope pluginabi.Envelope
	if errDecode := json.Unmarshal(raw, &envelope); errDecode != nil {
		t.Fatal(errDecode)
	}
	return envelope
}
