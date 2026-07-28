package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
)

const InstallationPlanSchemaVersion = 1

type InstallationAction string

const (
	InstallationAdopt          InstallationAction = "adopt"
	InstallationCreate         InstallationAction = "create"
	InstallationConfigure      InstallationAction = "configure"
	InstallationCreateManifest InstallationAction = "create_manifest"
	InstallationPreserve       InstallationAction = "preserve"
	InstallationRemove         InstallationAction = "remove"
	InstallationRestore        InstallationAction = "restore"
)

type InstallationOperation struct {
	ID         string             `json:"id"`
	Action     InstallationAction `json:"action"`
	ResourceID string             `json:"resourceId,omitempty"`
	Kind       ResourceKind       `json:"kind"`
	Target     string             `json:"target"`
	Reason     string             `json:"reason"`
	Mutating   bool               `json:"mutating"`
	Backup     *ResourceBackup    `json:"backup,omitempty"`
}

type InstallationArtifactKind string

const (
	ArtifactConfig          InstallationArtifactKind = "config"
	ArtifactGeneratedPKI    InstallationArtifactKind = "generated_pki"
	ArtifactGeneratedSecret InstallationArtifactKind = "generated_secret"
	ArtifactContainerSpec   InstallationArtifactKind = "container_spec"
)

type InstallationArtifact struct {
	ID               string                   `json:"id"`
	Kind             InstallationArtifactKind `json:"kind"`
	Target           string                   `json:"target"`
	Mode             uint32                   `json:"mode,omitempty"`
	Fingerprint      string                   `json:"fingerprint,omitempty"`
	Content          string                   `json:"content,omitempty"`
	Sensitive        bool                     `json:"sensitive"`
	GeneratedAtApply bool                     `json:"generatedAtApply"`
}

func (artifact InstallationArtifact) Validate() error {
	if !resourceIDPattern.MatchString(artifact.ID) {
		return fmt.Errorf("invalid artifact ID %q", artifact.ID)
	}
	switch artifact.Kind {
	case ArtifactConfig, ArtifactGeneratedPKI, ArtifactGeneratedSecret:
		if !filepath.IsAbs(artifact.Target) ||
			filepath.Clean(artifact.Target) != artifact.Target {
			return fmt.Errorf("artifact target must be absolute and canonical")
		}
		if artifact.Mode == 0 || artifact.Mode > 0o777 {
			return fmt.Errorf("file artifact mode must be between 0001 and 0777")
		}
	case ArtifactContainerSpec:
		if !dockerNamePattern.MatchString(artifact.Target) {
			return fmt.Errorf("invalid container artifact target %q", artifact.Target)
		}
		if artifact.Mode != 0 {
			return fmt.Errorf("container artifact cannot have a file mode")
		}
	default:
		return fmt.Errorf("invalid installation artifact kind %q", artifact.Kind)
	}
	if artifact.Sensitive && artifact.Mode&0o077 != 0 {
		return fmt.Errorf("sensitive artifact permissions must exclude group and other access")
	}
	if artifact.GeneratedAtApply {
		if artifact.Kind != ArtifactGeneratedPKI &&
			artifact.Kind != ArtifactGeneratedSecret {
			return fmt.Errorf("only generated PKI or secret artifacts may be deferred")
		}
		if artifact.Content != "" || artifact.Fingerprint != "" {
			return fmt.Errorf("deferred artifact cannot expose content or fingerprint")
		}
		return nil
	}
	if artifact.Kind == ArtifactGeneratedPKI ||
		artifact.Kind == ArtifactGeneratedSecret {
		return fmt.Errorf("generated PKI and secret artifacts must be deferred")
	}
	if artifact.Content == "" {
		return fmt.Errorf("rendered artifact content is required")
	}
	if !fingerprintPattern.MatchString(artifact.Fingerprint) {
		return fmt.Errorf("rendered artifact fingerprint must be lowercase SHA-256")
	}
	sum := sha256.Sum256([]byte(artifact.Content))
	if artifact.Fingerprint != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("rendered artifact fingerprint does not match content")
	}
	return nil
}

func ValidateInstallationArtifacts(artifacts []InstallationArtifact) error {
	ids := map[string]bool{}
	targets := map[string]bool{}
	for index, artifact := range artifacts {
		if err := artifact.Validate(); err != nil {
			return fmt.Errorf("managed artifact %d: %w", index, err)
		}
		if ids[artifact.ID] {
			return fmt.Errorf("duplicate managed artifact ID %q", artifact.ID)
		}
		ids[artifact.ID] = true
		targetClass := "file"
		if artifact.Kind == ArtifactContainerSpec {
			targetClass = "container"
		}
		targetKey := targetClass + "\x00" + artifact.Target
		if targets[targetKey] {
			return fmt.Errorf(
				"duplicate managed artifact target %q",
				artifact.Target,
			)
		}
		targets[targetKey] = true
	}
	return nil
}

type UninstallationPlan struct {
	SchemaVersion  int                     `json:"schemaVersion"`
	Token          string                  `json:"token"`
	Ready          bool                    `json:"ready"`
	Status         DiagnosticStatus        `json:"status"`
	ManifestPath   string                  `json:"manifestPath"`
	InstallationID string                  `json:"installationId"`
	Generation     uint64                  `json:"generation"`
	Operations     []InstallationOperation `json:"operations"`
	Blockers       []string                `json:"blockers"`
}

type InstallationPlan struct {
	SchemaVersion        int                        `json:"schemaVersion"`
	Token                string                     `json:"token"`
	Ready                bool                       `json:"ready"`
	Complete             bool                       `json:"complete"`
	Status               DiagnosticStatus           `json:"status"`
	Target               PreflightTarget            `json:"target"`
	Inventory            PreflightInventory         `json:"inventory"`
	ManagedSpecification *InstallationSpecification `json:"managedSpecification,omitempty"`
	ManagedArtifacts     []InstallationArtifact     `json:"managedArtifacts,omitempty"`
	Resources            []InstallationResource     `json:"resources"`
	Operations           []InstallationOperation    `json:"operations"`
	Blockers             []string                   `json:"blockers"`
	Pending              []string                   `json:"pending"`
}
