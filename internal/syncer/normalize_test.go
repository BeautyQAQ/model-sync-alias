package syncer

import (
	"bufio"
	"os"
	"strings"
	"testing"

	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestCurrentModelGolden(t *testing.T) {
	file, errOpen := os.Open("testdata/current_models.golden")
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	line := 0
	for scanner.Scan() {
		line++
		parts := strings.SplitN(scanner.Text(), "|", 2)
		if len(parts) != 2 {
			t.Fatalf("line %d is invalid", line)
		}
		if got := canonicalModelName(parts[0], settings{}); got != parts[1] {
			t.Errorf("line %d canonicalModelName(%q) = %q, want %q", line, parts[0], got, parts[1])
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		t.Fatal(errScan)
	}
	if line != 121 {
		t.Fatalf("golden model count = %d, want 121", line)
	}
}

func TestOverridePrecedenceAndEmptyOverride(t *testing.T) {
	cfg, errParse := parseSettings([]byte(`
config_path: /tmp/config.yaml
exact_overrides:
  DeepSeek-V4-Flash-think: exact-name
  raw-model: ""
regex_overrides:
  - pattern: '(?i)^deepseek.*$'
    replacement: regex-name
  - pattern: '^vendor/(.*)$'
    replacement: '$1'
`))
	if errParse != nil {
		t.Fatal(errParse)
	}
	cases := map[string]string{
		"DeepSeek-V4-Flash-think": "exact-name",
		"deepseek-v4-pro":         "regex-name",
		"GLM-5.2-think":           "glm-5.2",
		"vendor/Model-X":          "Model-X",
		"raw-model":               "raw-model",
	}
	for raw, want := range cases {
		if got := canonicalModelName(raw, cfg); got != want {
			t.Errorf("canonicalModelName(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestQwen38CanonicalName(t *testing.T) {
	if got := canonicalModelName("Qwen/Qwen3.8-27B-FP8", settings{}); got != "qwen3.8" {
		t.Fatalf("canonicalModelName(Qwen/Qwen3.8-27B-FP8) = %q, want qwen3.8", got)
	}
}

func TestBuildDesiredModelsDeduplicatesAndPreservesMetadata(t *testing.T) {
	existing := []sdkconfig.OpenAICompatibilityModel{{
		Name:             "openai/gpt-oss-20b",
		Alias:            "wrong",
		DisplayName:      "Twenty",
		MaxContextLength: 123,
	}}
	desired := buildDesiredModels([]string{
		"openai/gpt-oss-20b",
		"OPENAI/GPT-OSS-20B",
		"openai/gpt-oss-120b",
	}, existing, settings{})
	if len(desired) != 2 {
		t.Fatalf("desired count = %d, want 2", len(desired))
	}
	byAlias := make(map[string]sdkconfig.OpenAICompatibilityModel)
	for _, model := range desired {
		byAlias[model.Alias] = model
	}
	if byAlias["gpt-oss-20b"].DisplayName != "Twenty" || byAlias["gpt-oss-20b"].MaxContextLength != 123 {
		t.Fatalf("surviving model metadata was not preserved: %+v", byAlias["gpt-oss-20b"])
	}
	if _, exists := byAlias["gpt-oss-120b"]; !exists {
		t.Fatal("20b and 120b must remain separate aliases")
	}
}

func TestAliasPoolsAndDeterministicOrder(t *testing.T) {
	desired := buildDesiredModels([]string{
		"z-ai/glm-5.2",
		"GLM-5.2-think",
		"openai/gpt-oss-20b",
		"gpt-oss-120b",
	}, nil, settings{})
	diff := compareModels(nil, desired)
	if diff.AliasPoolCount != 1 {
		t.Fatalf("alias pool count = %d, want 1", diff.AliasPoolCount)
	}
	if desired[0].Alias != "glm-5.2" || desired[1].Alias != "glm-5.2" {
		t.Fatalf("GLM pool was not ordered together: %+v", desired)
	}
}

func TestBuildDesiredModelsAlwaysSetsExplicitAliases(t *testing.T) {
	cfg := settings{ExactOverrides: map[string]string{
		"deepseek/deepseek-v4-pro-0813": "deepseek-v4-pro",
		"raw-model":                     "",
	}}
	desired := buildDesiredModels([]string{
		"deepseek-v4-pro",
		"deepseek/deepseek-v4-pro-0813",
		"diffusiongemma-26b-a4b-it",
		"claude-fable-5",
		"raw-model",
	}, nil, cfg)

	byName := make(map[string]sdkconfig.OpenAICompatibilityModel, len(desired))
	for _, model := range desired {
		byName[model.Name] = model
	}
	for _, name := range []string{"deepseek-v4-pro", "deepseek/deepseek-v4-pro-0813"} {
		if got := byName[name].Alias; got != "deepseek-v4-pro" {
			t.Errorf("model %q alias = %q, want deepseek-v4-pro", name, got)
		}
	}
	for _, name := range []string{"diffusiongemma-26b-a4b-it", "claude-fable-5", "raw-model"} {
		if got := byName[name].Alias; got != name {
			t.Errorf("standalone model %q alias = %q, want self-alias", name, got)
		}
	}
	for _, model := range desired {
		if model.Alias == "" {
			t.Errorf("model %q has an empty alias", model.Name)
		}
	}

	diff := compareModels(nil, desired)
	if diff.AliasPoolCount != 1 {
		t.Fatalf("alias pool count = %d, want 1", diff.AliasPoolCount)
	}
}
