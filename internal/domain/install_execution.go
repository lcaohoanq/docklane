package domain

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

const InstallationExecutionSchemaVersion = 1

type InstallationExecutionPhase string

const (
	ExecutionForward    InstallationExecutionPhase = "forward"
	ExecutionRollback   InstallationExecutionPhase = "rollback"
	ExecutionComplete   InstallationExecutionPhase = "complete"
	ExecutionRolledBack InstallationExecutionPhase = "rolled_back"
	ExecutionFailed     InstallationExecutionPhase = "failed"
)

type InstallationExecutionStage string

const (
	ExecutionDirectories InstallationExecutionStage = "directories"
	ExecutionFiles       InstallationExecutionStage = "files"
	ExecutionHost        InstallationExecutionStage = "host"
	ExecutionDocker      InstallationExecutionStage = "docker"
	ExecutionVerify      InstallationExecutionStage = "verify"
)

type InstallationOperationState string

const (
	OperationPending     InstallationOperationState = "pending"
	OperationApplying    InstallationOperationState = "applying"
	OperationApplied     InstallationOperationState = "applied"
	OperationRollingBack InstallationOperationState = "rolling_back"
	OperationRolledBack  InstallationOperationState = "rolled_back"
	OperationFailed      InstallationOperationState = "failed"
)

type InstallationObservation struct {
	ExternalID          string          `json:"externalId,omitempty"`
	Fingerprint         string          `json:"fingerprint,omitempty"`
	Created             bool            `json:"created"`
	Backup              *ResourceBackup `json:"backup,omitempty"`
	SnapshotFingerprint string          `json:"snapshotFingerprint,omitempty"`
}

type InstallationExecutionOperation struct {
	ID                string                     `json:"id"`
	ResourceID        string                     `json:"resourceId"`
	Target            string                     `json:"target"`
	Stage             InstallationExecutionStage `json:"stage"`
	IntentFingerprint string                     `json:"intentFingerprint,omitempty"`
	IntentMode        uint32                     `json:"intentMode,omitempty"`
	State             InstallationOperationState `json:"state"`
	Attempt           uint64                     `json:"attempt"`
	Observation       *InstallationObservation   `json:"observation,omitempty"`
	ErrorMessage      string                     `json:"error,omitempty"`
}

type InstallationExecution struct {
	SchemaVersion int                              `json:"schemaVersion"`
	Phase         InstallationExecutionPhase       `json:"phase"`
	Operations    []InstallationExecutionOperation `json:"operations"`
}

func (execution InstallationExecution) Validate(
	resources []InstallationResource,
) error {
	if execution.SchemaVersion != InstallationExecutionSchemaVersion {
		return fmt.Errorf(
			"unsupported schema version %d",
			execution.SchemaVersion,
		)
	}
	if !validExecutionPhase(execution.Phase) {
		return fmt.Errorf("invalid phase %q", execution.Phase)
	}
	if len(execution.Operations) == 0 {
		return fmt.Errorf("operations must not be empty")
	}
	managed := map[string]InstallationResource{}
	for _, resource := range resources {
		if resource.Ownership == ResourceManaged {
			managed[resource.ID] = resource
		}
	}
	if len(managed) == 0 {
		return fmt.Errorf("journal requires managed resources")
	}
	seenOperations := map[string]bool{}
	seenResources := map[string]bool{}
	transient := 0
	failed := 0
	for index, operation := range execution.Operations {
		if err := operation.validate(); err != nil {
			return fmt.Errorf("operation %d: %w", index, err)
		}
		if seenOperations[operation.ID] {
			return fmt.Errorf("duplicate operation ID %q", operation.ID)
		}
		seenOperations[operation.ID] = true
		resource, exists := managed[operation.ResourceID]
		if !exists {
			return fmt.Errorf(
				"operation %q references unknown managed resource %q",
				operation.ID,
				operation.ResourceID,
			)
		}
		if seenResources[operation.ResourceID] {
			return fmt.Errorf(
				"managed resource %q has multiple operations",
				operation.ResourceID,
			)
		}
		seenResources[operation.ResourceID] = true
		if operation.Target != resource.Target {
			return fmt.Errorf(
				"operation %q target does not match resource %q",
				operation.ID,
				operation.ResourceID,
			)
		}
		if operation.State == OperationApplying ||
			operation.State == OperationRollingBack {
			transient++
		}
		if operation.State == OperationFailed {
			failed++
		}
	}
	for resourceID := range managed {
		if !seenResources[resourceID] {
			return fmt.Errorf(
				"managed resource %q is missing an operation",
				resourceID,
			)
		}
	}
	if transient > 1 {
		return fmt.Errorf("at most one operation may be in progress")
	}
	switch execution.Phase {
	case ExecutionForward:
		if err := validateOperationOrder(
			execution.Operations,
			map[InstallationOperationState]int{
				OperationApplied:  0,
				OperationApplying: 1,
				OperationPending:  2,
			},
		); err != nil {
			return fmt.Errorf("forward execution: %w", err)
		}
	case ExecutionRollback:
		if err := validateOperationOrder(
			execution.Operations,
			map[InstallationOperationState]int{
				OperationApplied:     0,
				OperationApplying:    1,
				OperationRollingBack: 2,
				OperationRolledBack:  2,
				OperationPending:     3,
			},
		); err != nil {
			return fmt.Errorf("rollback execution: %w", err)
		}
	case ExecutionComplete:
		for _, operation := range execution.Operations {
			if operation.State != OperationApplied {
				return fmt.Errorf("complete execution requires applied operations")
			}
		}
	case ExecutionRolledBack:
		for _, operation := range execution.Operations {
			if operation.State != OperationPending &&
				operation.State != OperationRolledBack {
				return fmt.Errorf(
					"rolled-back execution contains %s operation",
					operation.State,
				)
			}
		}
	case ExecutionFailed:
		if failed == 0 {
			return fmt.Errorf("failed execution requires a failed operation")
		}
	}
	return nil
}

func validateOperationOrder(
	operations []InstallationExecutionOperation,
	ranks map[InstallationOperationState]int,
) error {
	previous := -1
	for _, operation := range operations {
		rank, allowed := ranks[operation.State]
		if !allowed {
			return fmt.Errorf(
				"operation %q cannot be %s",
				operation.ID,
				operation.State,
			)
		}
		if rank < previous {
			return fmt.Errorf("operation order is inconsistent")
		}
		previous = rank
	}
	return nil
}

func (operation InstallationExecutionOperation) validate() error {
	if !resourceIDPattern.MatchString(operation.ID) {
		return fmt.Errorf("invalid operation ID %q", operation.ID)
	}
	if !resourceIDPattern.MatchString(operation.ResourceID) {
		return fmt.Errorf("invalid resource ID %q", operation.ResourceID)
	}
	if strings.TrimSpace(operation.Target) == "" ||
		strings.TrimSpace(operation.Target) != operation.Target {
		return fmt.Errorf("target is required")
	}
	if !validExecutionStage(operation.Stage) {
		return fmt.Errorf("invalid stage %q", operation.Stage)
	}
	if operation.IntentFingerprint != "" &&
		!fingerprintPattern.MatchString(operation.IntentFingerprint) {
		return fmt.Errorf("intent fingerprint must be lowercase SHA-256")
	}
	if operation.IntentMode > 0o777 {
		return fmt.Errorf("intent mode must not exceed 0777")
	}
	if (operation.Stage == ExecutionDirectories ||
		operation.Stage == ExecutionFiles) &&
		(operation.IntentFingerprint == "" || operation.IntentMode == 0) {
		return fmt.Errorf(
			"%s operation requires intent fingerprint and mode",
			operation.Stage,
		)
	}
	if !validOperationState(operation.State) {
		return fmt.Errorf("invalid state %q", operation.State)
	}
	if len(operation.ErrorMessage) > 4096 {
		return fmt.Errorf("error exceeds 4096 bytes")
	}
	switch operation.State {
	case OperationPending:
		if operation.Observation != nil ||
			operation.ErrorMessage != "" {
			return fmt.Errorf("pending operation contains execution results")
		}
	case OperationApplying:
		if operation.Attempt == 0 ||
			operation.Observation != nil ||
			operation.ErrorMessage != "" {
			return fmt.Errorf("applying operation has invalid checkpoint data")
		}
	case OperationApplied, OperationRollingBack, OperationRolledBack:
		if operation.Attempt == 0 || operation.Observation == nil ||
			operation.ErrorMessage != "" {
			return fmt.Errorf("%s operation has invalid checkpoint data", operation.State)
		}
	case OperationFailed:
		if operation.Attempt == 0 || operation.ErrorMessage == "" {
			return fmt.Errorf("failed operation requires an attempt and error")
		}
	}
	if operation.Observation != nil {
		if err := operation.Observation.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (observation InstallationObservation) validate() error {
	for label, value := range map[string]string{
		"fingerprint":          observation.Fingerprint,
		"snapshot fingerprint": observation.SnapshotFingerprint,
	} {
		if value != "" && !fingerprintPattern.MatchString(value) {
			return fmt.Errorf("%s must be lowercase SHA-256", label)
		}
	}
	if len(observation.ExternalID) > 1024 ||
		strings.TrimSpace(observation.ExternalID) != observation.ExternalID ||
		strings.IndexFunc(observation.ExternalID, unicode.IsControl) >= 0 {
		return fmt.Errorf("invalid external ID")
	}
	if observation.Backup != nil {
		if !filepath.IsAbs(observation.Backup.Path) ||
			filepath.Clean(observation.Backup.Path) != observation.Backup.Path {
			return fmt.Errorf("observation backup path must be absolute")
		}
		if !fingerprintPattern.MatchString(observation.Backup.Fingerprint) {
			return fmt.Errorf("observation backup fingerprint must be lowercase SHA-256")
		}
	}
	return nil
}

func validExecutionPhase(phase InstallationExecutionPhase) bool {
	switch phase {
	case ExecutionForward,
		ExecutionRollback,
		ExecutionComplete,
		ExecutionRolledBack,
		ExecutionFailed:
		return true
	default:
		return false
	}
}

func validExecutionStage(stage InstallationExecutionStage) bool {
	switch stage {
	case ExecutionDirectories,
		ExecutionFiles,
		ExecutionHost,
		ExecutionDocker,
		ExecutionVerify:
		return true
	default:
		return false
	}
}

func validOperationState(state InstallationOperationState) bool {
	switch state {
	case OperationPending,
		OperationApplying,
		OperationApplied,
		OperationRollingBack,
		OperationRolledBack,
		OperationFailed:
		return true
	default:
		return false
	}
}
