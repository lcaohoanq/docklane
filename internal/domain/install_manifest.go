package domain

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const InstallationManifestSchemaVersion = 1

type InstallationState string

const (
	InstallationPlanned     InstallationState = "planned"
	InstallationApplying    InstallationState = "applying"
	InstallationInstalled   InstallationState = "installed"
	InstallationRollingBack InstallationState = "rolling_back"
	InstallationRolledBack  InstallationState = "rolled_back"
	InstallationFailed      InstallationState = "failed"
)

type ResourceKind string

const (
	ResourceFile            ResourceKind = "file"
	ResourceDirectory       ResourceKind = "directory"
	ResourceTrustAnchor     ResourceKind = "trust_anchor"
	ResourceDockerNetwork   ResourceKind = "docker_network"
	ResourceDockerVolume    ResourceKind = "docker_volume"
	ResourceDockerContainer ResourceKind = "docker_container"
	ResourceSystemService   ResourceKind = "system_service"
	ResourceResolverRule    ResourceKind = "resolver_rule"
)

type ResourceOwnership string

const (
	ResourceManaged ResourceOwnership = "managed"
	ResourceAdopted ResourceOwnership = "adopted"
)

type ResourceState string

const (
	ResourcePlanned    ResourceState = "planned"
	ResourceApplied    ResourceState = "applied"
	ResourceVerified   ResourceState = "verified"
	ResourceRolledBack ResourceState = "rolled_back"
)

type RollbackStrategy string

const (
	RollbackRemove   RollbackStrategy = "remove"
	RollbackRestore  RollbackStrategy = "restore"
	RollbackPreserve RollbackStrategy = "preserve"
)

type InstallationSettings struct {
	BaseDomain   string `json:"baseDomain"`
	ProxyNetwork string `json:"proxyNetwork"`
}

func (settings InstallationSettings) Validate() error {
	if !domainPattern.MatchString(settings.BaseDomain) {
		return fmt.Errorf(
			"invalid installation base domain %q",
			settings.BaseDomain,
		)
	}
	if !dockerNamePattern.MatchString(settings.ProxyNetwork) {
		return fmt.Errorf(
			"invalid installation proxy network %q",
			settings.ProxyNetwork,
		)
	}
	return nil
}

type ResourceBackup struct {
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
}

type InstallationResource struct {
	ID          string            `json:"id"`
	Kind        ResourceKind      `json:"kind"`
	Target      string            `json:"target"`
	Ownership   ResourceOwnership `json:"ownership"`
	State       ResourceState     `json:"state"`
	Rollback    RollbackStrategy  `json:"rollback"`
	Fingerprint string            `json:"fingerprint,omitempty"`
	Backup      *ResourceBackup   `json:"backup,omitempty"`
}

type InstallationManifest struct {
	SchemaVersion        int                        `json:"schemaVersion"`
	InstallationID       string                     `json:"installationId"`
	Generation           uint64                     `json:"generation"`
	ProductVersion       string                     `json:"productVersion"`
	State                InstallationState          `json:"state"`
	CreatedAt            time.Time                  `json:"createdAt"`
	UpdatedAt            time.Time                  `json:"updatedAt"`
	Settings             InstallationSettings       `json:"settings"`
	ManagedSpecification *InstallationSpecification `json:"managedSpecification,omitempty"`
	ManagedArtifacts     []InstallationArtifact     `json:"managedArtifacts,omitempty"`
	Execution            *InstallationExecution     `json:"execution,omitempty"`
	Resources            []InstallationResource     `json:"resources"`
}

var (
	installationIDPattern = regexp.MustCompile(
		`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
	)
	resourceIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	domainPattern     = regexp.MustCompile(
		`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`,
	)
	fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	dockerNamePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,254}$`)
)

func (manifest InstallationManifest) Validate() error {
	if manifest.SchemaVersion != InstallationManifestSchemaVersion {
		return fmt.Errorf(
			"unsupported installation manifest schema version %d",
			manifest.SchemaVersion,
		)
	}
	if !installationIDPattern.MatchString(manifest.InstallationID) {
		return fmt.Errorf("installation ID must be a lowercase UUID v4")
	}
	if manifest.Generation == 0 {
		return fmt.Errorf("manifest generation must be greater than zero")
	}
	if strings.TrimSpace(manifest.ProductVersion) == "" ||
		strings.TrimSpace(manifest.ProductVersion) != manifest.ProductVersion {
		return fmt.Errorf("product version is required")
	}
	if !validInstallationState(manifest.State) {
		return fmt.Errorf("invalid installation state %q", manifest.State)
	}
	if manifest.CreatedAt.IsZero() || manifest.UpdatedAt.IsZero() {
		return fmt.Errorf("manifest timestamps are required")
	}
	if manifest.UpdatedAt.Before(manifest.CreatedAt) {
		return fmt.Errorf("manifest updatedAt cannot precede createdAt")
	}
	if err := manifest.Settings.Validate(); err != nil {
		return err
	}
	if manifest.ManagedSpecification != nil {
		if err := manifest.ManagedSpecification.Validate(); err != nil {
			return fmt.Errorf("managed specification: %w", err)
		}
	}
	if err := ValidateInstallationArtifacts(manifest.ManagedArtifacts); err != nil {
		return err
	}
	if manifest.ManagedSpecification == nil && len(manifest.ManagedArtifacts) != 0 {
		return fmt.Errorf("managed artifacts require a managed specification")
	}
	if manifest.Resources == nil {
		return fmt.Errorf("installation resources must be an array")
	}
	seenIDs := map[string]bool{}
	seenTargets := map[string]bool{}
	for index, resource := range manifest.Resources {
		if err := resource.Validate(); err != nil {
			return fmt.Errorf("resource %d: %w", index, err)
		}
		if seenIDs[resource.ID] {
			return fmt.Errorf("duplicate resource ID %q", resource.ID)
		}
		seenIDs[resource.ID] = true
		targetKey := string(resource.Kind) + "\x00" + resource.Target
		if seenTargets[targetKey] {
			return fmt.Errorf(
				"duplicate %s target %q",
				resource.Kind,
				resource.Target,
			)
		}
		seenTargets[targetKey] = true
	}
	if manifest.Execution != nil {
		if manifest.ManagedSpecification == nil {
			return fmt.Errorf("execution journal requires a managed specification")
		}
		if err := manifest.Execution.Validate(manifest.Resources); err != nil {
			return fmt.Errorf("execution journal: %w", err)
		}
		expectedState := map[InstallationExecutionPhase]InstallationState{
			ExecutionForward:    InstallationApplying,
			ExecutionRollback:   InstallationRollingBack,
			ExecutionComplete:   InstallationInstalled,
			ExecutionRolledBack: InstallationRolledBack,
			ExecutionFailed:     InstallationFailed,
		}[manifest.Execution.Phase]
		if manifest.State != expectedState {
			return fmt.Errorf(
				"execution phase %s requires installation state %s",
				manifest.Execution.Phase,
				expectedState,
			)
		}
	}
	if manifest.State == InstallationInstalled {
		if len(manifest.Resources) == 0 {
			return fmt.Errorf("installed manifest must contain resources")
		}
		for _, resource := range manifest.Resources {
			if resource.State != ResourceVerified {
				return fmt.Errorf(
					"installed resource %q must be verified",
					resource.ID,
				)
			}
		}
		if manifest.Execution != nil &&
			manifest.Execution.Phase != ExecutionComplete {
			return fmt.Errorf(
				"installed manifest execution phase must be complete",
			)
		}
	}
	if manifest.State == InstallationRolledBack {
		for _, resource := range manifest.Resources {
			if resource.Ownership == ResourceManaged &&
				resource.State != ResourceRolledBack {
				return fmt.Errorf(
					"rolled-back managed resource %q must be rolled_back",
					resource.ID,
				)
			}
		}
		if manifest.Execution != nil &&
			manifest.Execution.Phase != ExecutionRolledBack {
			return fmt.Errorf(
				"rolled-back manifest execution phase must be rolled_back",
			)
		}
	}
	return nil
}

func (resource InstallationResource) Validate() error {
	if !resourceIDPattern.MatchString(resource.ID) {
		return fmt.Errorf("invalid resource ID %q", resource.ID)
	}
	if !validResourceKind(resource.Kind) {
		return fmt.Errorf("invalid resource kind %q", resource.Kind)
	}
	if strings.TrimSpace(resource.Target) == "" ||
		strings.TrimSpace(resource.Target) != resource.Target {
		return fmt.Errorf("resource target is required")
	}
	if resource.Kind == ResourceFile || resource.Kind == ResourceDirectory {
		if !filepath.IsAbs(resource.Target) {
			return fmt.Errorf("file target %q must be absolute", resource.Target)
		}
		if filepath.Clean(resource.Target) != resource.Target {
			return fmt.Errorf("file target %q must be canonical", resource.Target)
		}
	}
	if !validResourceState(resource.State) {
		return fmt.Errorf("invalid resource state %q", resource.State)
	}
	switch resource.Ownership {
	case ResourceManaged:
		if resource.Rollback != RollbackRemove &&
			resource.Rollback != RollbackRestore {
			return fmt.Errorf(
				"managed resource rollback must be remove or restore",
			)
		}
	case ResourceAdopted:
		if resource.Rollback != RollbackPreserve {
			return fmt.Errorf("adopted resource rollback must be preserve")
		}
	default:
		return fmt.Errorf("invalid resource ownership %q", resource.Ownership)
	}
	if resource.Fingerprint != "" &&
		!fingerprintPattern.MatchString(resource.Fingerprint) {
		return fmt.Errorf("resource fingerprint must be lowercase SHA-256")
	}
	if resource.Backup != nil {
		if resource.Rollback != RollbackRestore {
			return fmt.Errorf("resource backup requires restore rollback")
		}
		if !filepath.IsAbs(resource.Backup.Path) {
			return fmt.Errorf("backup path %q must be absolute", resource.Backup.Path)
		}
		if filepath.Clean(resource.Backup.Path) != resource.Backup.Path {
			return fmt.Errorf("backup path %q must be canonical", resource.Backup.Path)
		}
		if !fingerprintPattern.MatchString(resource.Backup.Fingerprint) {
			return fmt.Errorf("backup fingerprint must be lowercase SHA-256")
		}
	}
	if resource.Rollback == RollbackRestore &&
		(resource.State == ResourceApplied ||
			resource.State == ResourceVerified) &&
		resource.Backup == nil {
		return fmt.Errorf("applied restore resource requires a backup")
	}
	return nil
}

func validInstallationState(state InstallationState) bool {
	switch state {
	case InstallationPlanned,
		InstallationApplying,
		InstallationInstalled,
		InstallationRollingBack,
		InstallationRolledBack,
		InstallationFailed:
		return true
	default:
		return false
	}
}

func validResourceKind(kind ResourceKind) bool {
	switch kind {
	case ResourceFile,
		ResourceDirectory,
		ResourceTrustAnchor,
		ResourceDockerNetwork,
		ResourceDockerVolume,
		ResourceDockerContainer,
		ResourceSystemService,
		ResourceResolverRule:
		return true
	default:
		return false
	}
}

func validResourceState(state ResourceState) bool {
	switch state {
	case ResourcePlanned,
		ResourceApplied,
		ResourceVerified,
		ResourceRolledBack:
		return true
	default:
		return false
	}
}
