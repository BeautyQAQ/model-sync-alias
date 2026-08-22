package main

import (
	"regexp"
	"sort"
	"strings"

	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

var (
	deepSeekV4Pattern = regexp.MustCompile(`(?i)^deepseek[-_]?v4[-_](flash|pro)(?:[-_\[].*)?$`)
	glm5Pattern       = regexp.MustCompile(`(?i)^glm[-_]?5(?:[._-]?([12]))?(?:[-_].*)?$`)
	kimiK2Pattern     = regexp.MustCompile(`(?i)^kimi[-_]?k2[._-]([567])(?:[-_](?:code|think))*$`)
	qwenNextPattern   = regexp.MustCompile(`(?i)^qwen3[-_.]?next(?:[-_].*)?$`)
	qwenSeriesPattern = regexp.MustCompile(`(?i)^qwen3(?:[._-]?([5678]))?[-_](?:\d.*)$`)
	miniMaxPattern    = regexp.MustCompile(`(?i)^minimax[-_]?m(2[._-][57]|3)(?:[-_](?:highspeed|think))*$`)
	gptOSSPattern     = regexp.MustCompile(`(?i)^gpt[-_]oss[-_](20b|120b)$`)
	gptImage2Pattern  = regexp.MustCompile(`(?i)^gpt-image-2-\d+x\d+$`)
	fluxKleinPattern  = regexp.MustCompile(`(?i)^flux\.2-klein-(4|9)b$`)
	contextBracket    = regexp.MustCompile(`(?i)\[(?:\d+(?:\.\d+)?[km])\]$`)
	genericSuffix     = regexp.MustCompile(`(?i)(?:-(?:free|think|highspeed|long|\d+(?:\.\d+)?[km]))+$`)
)

var knownNamespaces = map[string]struct{}{
	"openai":            {},
	"deepseek-ai":       {},
	"z-ai":              {},
	"qwen":              {},
	"moonshotai":        {},
	"minimaxai":         {},
	"google":            {},
	"black-forest-labs": {},
}

func canonicalModelName(raw string, cfg settings) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if value, ok := exactOverride(raw, cfg.ExactOverrides); ok {
		if value == "" {
			return raw
		}
		return value
	}
	for _, override := range cfg.RegexOverrides {
		if override.Regexp.MatchString(raw) {
			value := strings.TrimSpace(override.Regexp.ReplaceAllString(raw, override.Replacement))
			if value == "" {
				return raw
			}
			return value
		}
	}
	if value, ok := builtInCanonicalName(raw); ok {
		return value
	}
	return conservativeCanonicalName(raw)
}

func exactOverride(raw string, overrides map[string]string) (string, bool) {
	if value, ok := overrides[raw]; ok {
		return strings.TrimSpace(value), true
	}
	for name, value := range overrides {
		if strings.EqualFold(name, raw) {
			return strings.TrimSpace(value), true
		}
	}
	return "", false
}

func builtInCanonicalName(raw string) (string, bool) {
	name, _ := stripKnownNamespace(strings.TrimSpace(raw))
	if strings.HasPrefix(strings.ToLower(name), "zai-glm-") {
		name = name[len("zai-"):]
	}
	if match := deepSeekV4Pattern.FindStringSubmatch(name); match != nil {
		return "deepseek-v4-" + strings.ToLower(match[1]), true
	}
	if match := glm5Pattern.FindStringSubmatch(name); match != nil {
		if match[1] == "" {
			return "glm-5", true
		}
		return "glm-5." + match[1], true
	}
	if match := kimiK2Pattern.FindStringSubmatch(name); match != nil {
		return "kimi-k2." + match[1], true
	}
	if qwenNextPattern.MatchString(name) {
		return "qwen3-next", true
	}
	if match := qwenSeriesPattern.FindStringSubmatch(name); match != nil {
		if match[1] == "" {
			return "qwen3", true
		}
		return "qwen3." + match[1], true
	}
	if match := miniMaxPattern.FindStringSubmatch(name); match != nil {
		series := strings.ReplaceAll(match[1], "_", ".")
		series = strings.ReplaceAll(series, "-", ".")
		return "MiniMax-M" + strings.ToUpper(series), true
	}
	if match := gptOSSPattern.FindStringSubmatch(name); match != nil {
		return "gpt-oss-" + strings.ToLower(match[1]), true
	}
	if gptImage2Pattern.MatchString(name) {
		return "gpt-image-2", true
	}
	if match := fluxKleinPattern.FindStringSubmatch(name); match != nil {
		return "FLUX.2-klein-" + match[1] + "B", true
	}
	return "", false
}

func conservativeCanonicalName(raw string) string {
	name, namespaceRemoved := stripKnownNamespace(strings.TrimSpace(raw))
	cleaned := contextBracket.ReplaceAllString(name, "")
	cleaned = genericSuffix.ReplaceAllString(cleaned, "")
	cleaned = strings.TrimSuffix(cleaned, "-")
	if cleaned == "" {
		return raw
	}
	if namespaceRemoved || cleaned != name || looksLikeKnownModelID(cleaned) {
		return officialCase(cleaned)
	}
	return raw
}

func stripKnownNamespace(raw string) (string, bool) {
	parts := strings.SplitN(raw, "/", 2)
	if len(parts) != 2 {
		return raw, false
	}
	if _, ok := knownNamespaces[strings.ToLower(strings.TrimSpace(parts[0]))]; !ok {
		return raw, false
	}
	name := strings.TrimSpace(parts[1])
	if name == "" {
		return raw, false
	}
	return name, true
}

func looksLikeKnownModelID(name string) bool {
	lower := strings.ToLower(name)
	for _, prefix := range []string{
		"abab", "claude-", "codex-", "deepseek-", "gemini-", "gemma-", "glm-", "gpt-",
		"grok-", "hy3", "kimi-", "laguna-", "llama", "mimo-", "minimax-", "nemotron-", "qwen",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func officialCase(name string) string {
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "minimax-m") {
		rest := lower[len("minimax-m"):]
		return "MiniMax-M" + strings.ToUpper(rest)
	}
	if match := fluxKleinPattern.FindStringSubmatch(lower); match != nil {
		return "FLUX.2-klein-" + match[1] + "B"
	}
	return lower
}

func buildDesiredModels(ids []string, existing []sdkconfig.OpenAICompatibilityModel, cfg settings) []sdkconfig.OpenAICompatibilityModel {
	oldByName := make(map[string]sdkconfig.OpenAICompatibilityModel, len(existing))
	for _, model := range existing {
		key := strings.ToLower(strings.TrimSpace(model.Name))
		if key != "" {
			oldByName[key] = model
		}
	}
	seen := make(map[string]struct{}, len(ids))
	desired := make([]sdkconfig.OpenAICompatibilityModel, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		key := strings.ToLower(id)
		if id == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		model := oldByName[key]
		model.Name = id
		canonical := canonicalModelName(id, cfg)
		if canonical != id {
			model.Alias = canonical
		} else {
			model.Alias = ""
		}
		desired = append(desired, model)
	}
	sort.SliceStable(desired, func(i, j int) bool {
		left := desired[i].Alias
		if left == "" {
			left = desired[i].Name
		}
		right := desired[j].Alias
		if right == "" {
			right = desired[j].Name
		}
		if !strings.EqualFold(left, right) {
			return strings.ToLower(left) < strings.ToLower(right)
		}
		if !strings.EqualFold(desired[i].Name, desired[j].Name) {
			return strings.ToLower(desired[i].Name) < strings.ToLower(desired[j].Name)
		}
		return desired[i].Name < desired[j].Name
	})
	return desired
}
