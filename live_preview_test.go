package main

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLivePreview(t *testing.T) {
	configPath := os.Getenv("AXONHUB_LIVE_CONFIG")
	if configPath == "" {
		t.Skip("set AXONHUB_LIVE_CONFIG to perform a read-only live preview")
	}
	cfg := settings{
		Provider:        defaultProvider,
		ConfigPath:      configPath,
		RequestTimeout:  30 * time.Second,
		BackupRetention: 30,
	}
	snapshot, errSnapshot := loadSourceSnapshot(configPath, cfg.Provider)
	if errSnapshot != nil {
		t.Fatal(errSnapshot)
	}
	ids, errFetch := fetchUpstreamModels(context.Background(), snapshot, cfg.RequestTimeout)
	if errFetch != nil {
		t.Fatal(errFetch)
	}
	desired := buildDesiredModels(ids, snapshot.Provider.Models, cfg)
	diff := compareModels(snapshot.Provider.Models, desired)
	t.Logf("upstream=%d configured=%d added=%v removed=%v alias_changes=%d alias_pools=%d changed=%t",
		diff.UpstreamCount, diff.ConfiguredCount, diff.Added, diff.Removed, diff.AliasChanges, diff.AliasPoolCount, diff.Changed)
}
