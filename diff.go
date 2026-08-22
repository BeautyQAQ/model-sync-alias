package main

import (
	"reflect"
	"sort"
	"strings"

	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type modelDiff struct {
	UpstreamCount   int      `json:"upstream_count"`
	ConfiguredCount int      `json:"configured_count"`
	Added           []string `json:"added"`
	Removed         []string `json:"removed"`
	AliasChanges    int      `json:"alias_changes"`
	AliasPoolCount  int      `json:"alias_pool_count"`
	Changed         bool     `json:"changed"`
}

func compareModels(existing, desired []sdkconfig.OpenAICompatibilityModel) modelDiff {
	diff := modelDiff{
		UpstreamCount:   len(desired),
		ConfiguredCount: len(existing),
		Changed:         !reflect.DeepEqual(existing, desired),
	}
	oldByName := make(map[string]sdkconfig.OpenAICompatibilityModel, len(existing))
	newByName := make(map[string]sdkconfig.OpenAICompatibilityModel, len(desired))
	for _, model := range existing {
		oldByName[strings.ToLower(strings.TrimSpace(model.Name))] = model
	}
	for _, model := range desired {
		newByName[strings.ToLower(strings.TrimSpace(model.Name))] = model
	}
	for key, model := range newByName {
		old, exists := oldByName[key]
		if !exists {
			diff.Added = append(diff.Added, model.Name)
			continue
		}
		if old.Name != model.Name || old.Alias != model.Alias {
			diff.AliasChanges++
		}
	}
	for key, model := range oldByName {
		if _, exists := newByName[key]; !exists {
			diff.Removed = append(diff.Removed, model.Name)
		}
	}
	sort.Strings(diff.Added)
	sort.Strings(diff.Removed)
	pools := make(map[string]int)
	for _, model := range desired {
		if model.Alias != "" {
			pools[model.Alias]++
		}
	}
	for _, size := range pools {
		if size > 1 {
			diff.AliasPoolCount++
		}
	}
	return diff
}
