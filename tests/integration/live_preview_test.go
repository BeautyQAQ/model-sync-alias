package integration

import (
	"context"
	"os"
	"testing"

	"github.com/BeautyQAQ/model-sync-alias/internal/syncer"
	"gopkg.in/yaml.v3"
)

func TestLivePreview(t *testing.T) {
	configPath := os.Getenv("AXONHUB_LIVE_CONFIG")
	if configPath == "" {
		t.Skip("set AXONHUB_LIVE_CONFIG to perform a read-only live preview")
	}
	rawConfig, errMarshal := yaml.Marshal(map[string]any{
		"provider":         defaultProvider,
		"config_path":      configPath,
		"interval":         "1h",
		"sync_on_start":    false,
		"request_timeout":  "30s",
		"backup_retention": 30,
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	manager := syncer.NewManager(nil)
	if errConfigure := manager.Configure(rawConfig); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	defer manager.Shutdown()
	result, errRun := manager.Run(context.Background(), false, true)
	if errRun != nil {
		t.Fatal(errRun)
	}
	diff := result.Diff
	t.Logf("upstream=%d configured=%d added=%v removed=%v alias_changes=%d alias_pools=%d changed=%t",
		diff.UpstreamCount, diff.ConfiguredCount, diff.Added, diff.Removed, diff.AliasChanges, diff.AliasPoolCount, diff.Changed)
}
