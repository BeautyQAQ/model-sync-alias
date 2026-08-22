package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"time"

	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"gopkg.in/yaml.v3"
)

var errConcurrentConfigEdit = errors.New("CPA configuration changed while models were being fetched")

type applyResult struct {
	Diff       modelDiff
	Applied    bool
	BackupPath string
}

func applyModels(snapshot sourceSnapshot, ids []string, cfg settings) (applyResult, error) {
	currentRaw, errRead := os.ReadFile(cfg.ConfigPath)
	if errRead != nil {
		return applyResult{}, fmt.Errorf("re-read CPA configuration: %w", errRead)
	}
	if sha256.Sum256(currentRaw) != snapshot.Hash {
		return applyResult{}, errConcurrentConfigEdit
	}
	currentCfg, errParse := sdkconfig.ParseConfigBytes(currentRaw)
	if errParse != nil {
		return applyResult{}, fmt.Errorf("parse current CPA configuration: %w", errParse)
	}
	provider, errProvider := findProvider(currentCfg, cfg.Provider)
	if errProvider != nil {
		return applyResult{}, errProvider
	}
	desired := buildDesiredModels(ids, provider.Models, cfg)
	diff := compareModels(provider.Models, desired)
	result := applyResult{Diff: diff}
	if !diff.Changed {
		return result, nil
	}

	info, errStat := os.Stat(cfg.ConfigPath)
	if errStat != nil {
		return applyResult{}, fmt.Errorf("stat CPA configuration: %w", errStat)
	}
	dir := filepath.Dir(cfg.ConfigPath)
	temp, errTemp := os.CreateTemp(dir, ".axonhub-sync-*.yaml")
	if errTemp != nil {
		return applyResult{}, fmt.Errorf("create temporary configuration: %w", errTemp)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if _, errWrite := temp.Write(currentRaw); errWrite != nil {
		_ = temp.Close()
		return applyResult{}, fmt.Errorf("seed temporary configuration: %w", errWrite)
	}
	if errSync := temp.Sync(); errSync != nil {
		_ = temp.Close()
		return applyResult{}, fmt.Errorf("sync temporary configuration: %w", errSync)
	}
	if errClose := temp.Close(); errClose != nil {
		return applyResult{}, fmt.Errorf("close temporary configuration: %w", errClose)
	}
	if errMode := os.Chmod(tempPath, info.Mode().Perm()); errMode != nil {
		return applyResult{}, fmt.Errorf("preserve configuration mode: %w", errMode)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		if errOwner := os.Chown(tempPath, int(stat.Uid), int(stat.Gid)); errOwner != nil && !errors.Is(errOwner, syscall.EPERM) {
			return applyResult{}, fmt.Errorf("preserve configuration owner: %w", errOwner)
		}
	}

	if errSave := writeModelsPreserveYAML(tempPath, cfg.Provider, desired); errSave != nil {
		return applyResult{}, fmt.Errorf("render temporary configuration: %w", errSave)
	}
	validated, errValidate := sdkconfig.LoadConfigOptional(tempPath, false)
	if errValidate != nil {
		return applyResult{}, fmt.Errorf("validate temporary configuration: %w", errValidate)
	}
	validatedProvider, errValidatedProvider := findProvider(validated, cfg.Provider)
	if errValidatedProvider != nil {
		return applyResult{}, fmt.Errorf("validate target provider: %w", errValidatedProvider)
	}
	if !reflect.DeepEqual(validatedProvider.Models, desired) {
		return applyResult{}, fmt.Errorf("validated model list differs from generated list")
	}
	if errSemantic := verifyOnlyTargetModelsChanged(currentRaw, tempPath, cfg.Provider); errSemantic != nil {
		return applyResult{}, errSemantic
	}
	if errSync := syncFile(tempPath); errSync != nil {
		return applyResult{}, errSync
	}

	latestRaw, errLatest := os.ReadFile(cfg.ConfigPath)
	if errLatest != nil {
		return applyResult{}, fmt.Errorf("final re-read of CPA configuration: %w", errLatest)
	}
	if sha256.Sum256(latestRaw) != snapshot.Hash {
		return applyResult{}, errConcurrentConfigEdit
	}
	backupPath, errBackup := createBackup(cfg.ConfigPath, latestRaw, info, time.Now())
	if errBackup != nil {
		return applyResult{}, errBackup
	}
	generatedRaw, errGenerated := os.ReadFile(tempPath)
	if errGenerated != nil {
		return applyResult{}, fmt.Errorf("read generated configuration for commit: %w", errGenerated)
	}
	if errCommit := writeLiveConfig(cfg.ConfigPath, generatedRaw); errCommit != nil {
		if errRestore := writeLiveConfig(cfg.ConfigPath, latestRaw); errRestore != nil {
			return applyResult{}, fmt.Errorf("commit CPA configuration: %v; restore backup content: %w", errCommit, errRestore)
		}
		return applyResult{}, fmt.Errorf("commit CPA configuration: %w", errCommit)
	}
	if errDirSync := syncDirectory(dir); errDirSync != nil {
		return applyResult{}, errDirSync
	}
	result.Applied = true
	result.BackupPath = backupPath
	_ = pruneBackups(filepath.Dir(backupPath), cfg.BackupRetention)
	return result, nil
}

func writeLiveConfig(path string, raw []byte) error {
	file, errOpen := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if errOpen != nil {
		return errOpen
	}
	written := 0
	for written < len(raw) {
		count, errWrite := file.Write(raw[written:])
		if errWrite != nil {
			_ = file.Close()
			return errWrite
		}
		if count == 0 {
			_ = file.Close()
			return io.ErrShortWrite
		}
		written += count
	}
	if errSync := file.Sync(); errSync != nil {
		_ = file.Close()
		return errSync
	}
	return file.Close()
}

func writeModelsPreserveYAML(path, providerName string, models []sdkconfig.OpenAICompatibilityModel) error {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return errRead
	}
	var document yaml.Node
	if errParse := yaml.Unmarshal(raw, &document); errParse != nil {
		return errParse
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("expected a YAML root mapping")
	}
	providers := mappingValue(document.Content[0], "openai-compatibility")
	if providers == nil || providers.Kind != yaml.SequenceNode {
		return fmt.Errorf("openai-compatibility must be a YAML sequence")
	}
	var target *yaml.Node
	for _, item := range providers.Content {
		if item == nil || item.Kind != yaml.MappingNode {
			continue
		}
		name := mappingValue(item, "name")
		if name != nil && name.Value == providerName {
			target = item
			break
		}
	}
	if target == nil {
		return fmt.Errorf("OpenAI-compatible provider %q was not found", providerName)
	}
	renderedModels, errMarshal := yaml.Marshal(models)
	if errMarshal != nil {
		return errMarshal
	}
	var modelDocument yaml.Node
	if errParse := yaml.Unmarshal(renderedModels, &modelDocument); errParse != nil {
		return errParse
	}
	if len(modelDocument.Content) == 0 {
		return fmt.Errorf("generated model YAML is empty")
	}
	modelsNode := modelDocument.Content[0]
	if modelsNode.Kind == yaml.SequenceNode && len(modelsNode.Content) == 0 {
		modelsNode.Style = yaml.FlowStyle
	}
	setMappingValue(target, "models", modelsNode)

	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if errEncode := encoder.Encode(&document); errEncode != nil {
		_ = encoder.Close()
		return errEncode
	}
	if errClose := encoder.Close(); errClose != nil {
		return errClose
	}
	return os.WriteFile(path, sdkconfig.NormalizeCommentIndentation(buffer.Bytes()), 0)
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index] != nil && mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func setMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index] != nil && mapping.Content[index].Value == key {
			mapping.Content[index+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}

func verifyOnlyTargetModelsChanged(original []byte, generatedPath, providerName string) error {
	generated, errRead := os.ReadFile(generatedPath)
	if errRead != nil {
		return fmt.Errorf("read generated configuration for semantic verification: %w", errRead)
	}
	var before map[string]any
	if errBefore := yaml.Unmarshal(original, &before); errBefore != nil {
		return fmt.Errorf("parse original configuration for semantic verification: %w", errBefore)
	}
	var after map[string]any
	if errAfter := yaml.Unmarshal(generated, &after); errAfter != nil {
		return fmt.Errorf("parse generated configuration for semantic verification: %w", errAfter)
	}
	if !clearProviderModels(before, providerName) || !clearProviderModels(after, providerName) {
		return fmt.Errorf("target provider %q disappeared during semantic verification", providerName)
	}
	if !reflect.DeepEqual(before, after) {
		return fmt.Errorf("generated configuration changes fields outside %s models", providerName)
	}
	return nil
}

func clearProviderModels(root map[string]any, providerName string) bool {
	providers, ok := root["openai-compatibility"].([]any)
	if !ok {
		return false
	}
	for _, rawProvider := range providers {
		provider, okProvider := rawProvider.(map[string]any)
		if !okProvider || provider["name"] != providerName {
			continue
		}
		provider["models"] = nil
		return true
	}
	return false
}

func syncFile(path string) error {
	file, errOpen := os.OpenFile(path, os.O_RDWR, 0)
	if errOpen != nil {
		return fmt.Errorf("open generated configuration for sync: %w", errOpen)
	}
	defer func() { _ = file.Close() }()
	if errSync := file.Sync(); errSync != nil {
		return fmt.Errorf("sync generated configuration: %w", errSync)
	}
	return nil
}

func syncDirectory(path string) error {
	dir, errOpen := os.Open(path)
	if errOpen != nil {
		return fmt.Errorf("open configuration directory for sync: %w", errOpen)
	}
	defer func() { _ = dir.Close() }()
	if errSync := dir.Sync(); errSync != nil {
		return fmt.Errorf("sync configuration directory: %w", errSync)
	}
	return nil
}

func createBackup(configPath string, raw []byte, info os.FileInfo, now time.Time) (string, error) {
	backupDir := filepath.Join(filepath.Dir(configPath), "config_backup")
	if errMkdir := os.MkdirAll(backupDir, 0o700); errMkdir != nil {
		return "", fmt.Errorf("create backup directory: %w", errMkdir)
	}
	base := "axonhub_sync_" + now.Format("20060102_150405")
	var backupPath string
	var file *os.File
	for suffix := 0; suffix < 1000; suffix++ {
		name := base + ".yaml"
		if suffix > 0 {
			name = fmt.Sprintf("%s_%03d.yaml", base, suffix)
		}
		backupPath = filepath.Join(backupDir, name)
		created, errCreate := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
		if errors.Is(errCreate, os.ErrExist) {
			continue
		}
		if errCreate != nil {
			return "", fmt.Errorf("create configuration backup: %w", errCreate)
		}
		file = created
		break
	}
	if file == nil {
		return "", fmt.Errorf("create configuration backup: too many files share one timestamp")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		if errOwner := file.Chown(int(stat.Uid), int(stat.Gid)); errOwner != nil && !errors.Is(errOwner, syscall.EPERM) {
			_ = file.Close()
			return "", fmt.Errorf("preserve backup owner: %w", errOwner)
		}
	}
	if _, errWrite := file.Write(raw); errWrite != nil {
		_ = file.Close()
		return "", fmt.Errorf("write configuration backup: %w", errWrite)
	}
	if errSync := file.Sync(); errSync != nil {
		_ = file.Close()
		return "", fmt.Errorf("sync configuration backup: %w", errSync)
	}
	if errClose := file.Close(); errClose != nil {
		return "", fmt.Errorf("close configuration backup: %w", errClose)
	}
	if errDirSync := syncDirectory(backupDir); errDirSync != nil {
		return "", errDirSync
	}
	return backupPath, nil
}

func pruneBackups(backupDir string, retention int) error {
	entries, errRead := os.ReadDir(backupDir)
	if errRead != nil {
		return errRead
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "axonhub_sync_") || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if len(names) <= retention {
		return nil
	}
	for _, name := range names[:len(names)-retention] {
		path := filepath.Join(backupDir, name)
		if errRemove := os.Remove(path); errRemove != nil {
			return errRemove
		}
	}
	return syncDirectory(backupDir)
}

func copyFile(source, destination string, mode os.FileMode) error {
	in, errOpen := os.Open(source)
	if errOpen != nil {
		return errOpen
	}
	defer func() { _ = in.Close() }()
	out, errCreate := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if errCreate != nil {
		return errCreate
	}
	defer func() { _ = out.Close() }()
	if _, errCopy := io.Copy(out, in); errCopy != nil {
		return errCopy
	}
	return out.Sync()
}
