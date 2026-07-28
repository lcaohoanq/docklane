package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
)

const InstallationMaterialCacheSchemaVersion = 1

type InstallationMaterialCacheState string

const (
	MaterialCacheReady    InstallationMaterialCacheState = "ready"
	MaterialCacheClearing InstallationMaterialCacheState = "clearing"
	MaterialCacheCleared  InstallationMaterialCacheState = "cleared"
)

type InstallationMaterialCacheEntry struct {
	ArtifactID  string `json:"artifactId"`
	Target      string `json:"target"`
	CachePath   string `json:"cachePath"`
	Mode        uint32 `json:"mode"`
	Fingerprint string `json:"fingerprint"`
	Sensitive   bool   `json:"sensitive"`
}

type InstallationMaterialCache struct {
	SchemaVersion        int                              `json:"schemaVersion"`
	State                InstallationMaterialCacheState   `json:"state"`
	Directory            string                           `json:"directory"`
	InventoryFingerprint string                           `json:"inventoryFingerprint"`
	Entries              []InstallationMaterialCacheEntry `json:"entries"`
}

func (cache InstallationMaterialCache) Validate(
	installationID string,
	stateDirectory string,
) error {
	if cache.SchemaVersion != InstallationMaterialCacheSchemaVersion {
		return fmt.Errorf(
			"unsupported schema version %d",
			cache.SchemaVersion,
		)
	}
	if cache.State != MaterialCacheReady &&
		cache.State != MaterialCacheClearing &&
		cache.State != MaterialCacheCleared {
		return fmt.Errorf("invalid state %q", cache.State)
	}
	expectedDirectory := filepath.Join(
		stateDirectory,
		".material-cache",
		installationID,
	)
	if cache.Directory != expectedDirectory {
		return fmt.Errorf(
			"directory must be the installation-bound path %s",
			expectedDirectory,
		)
	}
	if len(cache.Entries) == 0 {
		return fmt.Errorf("entries must not be empty")
	}
	seenIDs := map[string]bool{}
	seenTargets := map[string]bool{}
	seenPaths := map[string]bool{}
	for index, entry := range cache.Entries {
		if !resourceIDPattern.MatchString(entry.ArtifactID) {
			return fmt.Errorf(
				"entry %d has invalid artifact ID %q",
				index,
				entry.ArtifactID,
			)
		}
		if seenIDs[entry.ArtifactID] {
			return fmt.Errorf("duplicate artifact ID %q", entry.ArtifactID)
		}
		seenIDs[entry.ArtifactID] = true
		if !filepath.IsAbs(entry.Target) ||
			filepath.Clean(entry.Target) != entry.Target {
			return fmt.Errorf("entry %s target is not absolute and canonical", entry.ArtifactID)
		}
		if seenTargets[entry.Target] {
			return fmt.Errorf("duplicate target %q", entry.Target)
		}
		seenTargets[entry.Target] = true
		if !filepath.IsAbs(entry.CachePath) ||
			filepath.Clean(entry.CachePath) != entry.CachePath ||
			!pathWithin(cache.Directory, entry.CachePath) ||
			filepath.Dir(entry.CachePath) != cache.Directory {
			return fmt.Errorf("entry %s cache path escapes its directory", entry.ArtifactID)
		}
		if seenPaths[entry.CachePath] {
			return fmt.Errorf("duplicate cache path %q", entry.CachePath)
		}
		seenPaths[entry.CachePath] = true
		if entry.Mode == 0 || entry.Mode > 0o777 {
			return fmt.Errorf("entry %s has invalid target mode", entry.ArtifactID)
		}
		if entry.Sensitive && entry.Mode&0o077 != 0 {
			return fmt.Errorf("entry %s has broad sensitive mode", entry.ArtifactID)
		}
		if !fingerprintPattern.MatchString(entry.Fingerprint) {
			return fmt.Errorf("entry %s fingerprint must be lowercase SHA-256", entry.ArtifactID)
		}
	}
	expectedFingerprint, err := MaterialCacheInventoryFingerprint(cache.Entries)
	if err != nil {
		return err
	}
	if cache.InventoryFingerprint != expectedFingerprint {
		return fmt.Errorf("inventory fingerprint does not match entries")
	}
	return nil
}

func (cache InstallationMaterialCache) ValidateArtifacts(
	artifacts []InstallationArtifact,
) error {
	files := make([]InstallationArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Kind != ArtifactContainerSpec {
			files = append(files, artifact)
		}
	}
	if len(cache.Entries) != len(files) {
		return fmt.Errorf(
			"entries cover %d of %d managed file artifacts",
			len(cache.Entries),
			len(files),
		)
	}
	for index, artifact := range files {
		entry := cache.Entries[index]
		if entry.ArtifactID != artifact.ID ||
			entry.Target != artifact.Target ||
			entry.Mode != artifact.Mode ||
			entry.Sensitive != artifact.Sensitive {
			return fmt.Errorf(
				"entry %d does not match managed artifact %s",
				index,
				artifact.ID,
			)
		}
		if artifact.Fingerprint != "" &&
			entry.Fingerprint != artifact.Fingerprint {
			return fmt.Errorf(
				"entry %s fingerprint does not match rendered artifact",
				entry.ArtifactID,
			)
		}
	}
	return nil
}

func (cache InstallationMaterialCache) ValidateExecution(
	execution InstallationExecution,
) error {
	fileOperations := map[string]InstallationExecutionOperation{}
	for _, operation := range execution.Operations {
		if operation.Stage == ExecutionFiles {
			fileOperations[operation.ResourceID] = operation
		}
	}
	if len(fileOperations) != len(cache.Entries) {
		return fmt.Errorf(
			"execution covers %d of %d cached file entries",
			len(fileOperations),
			len(cache.Entries),
		)
	}
	for _, entry := range cache.Entries {
		operation, exists := fileOperations[entry.ArtifactID]
		if !exists ||
			operation.Target != entry.Target ||
			operation.IntentFingerprint != entry.Fingerprint ||
			operation.IntentMode != entry.Mode {
			return fmt.Errorf(
				"entry %s does not match execution intent",
				entry.ArtifactID,
			)
		}
	}
	return nil
}

func MaterialCacheInventoryFingerprint(
	entries []InstallationMaterialCacheEntry,
) (string, error) {
	encoded, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("encode material cache inventory: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
