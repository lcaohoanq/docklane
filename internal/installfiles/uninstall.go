package installfiles

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installworkflow"
)

func NewRollbackWorkflowAdapter(
	manifest domain.InstallationManifest,
) (*WorkflowAdapter, error) {
	if manifest.ManagedSpecification == nil {
		return nil, errors.New(
			"managed specification is required for file rollback",
		)
	}
	if manifest.Execution == nil {
		return nil, errors.New(
			"execution journal is required for file rollback",
		)
	}
	resources := map[string]domain.InstallationResource{}
	for _, resource := range manifest.Resources {
		if resource.Ownership == domain.ResourceManaged &&
			(resource.Kind == domain.ResourceFile ||
				resource.Kind == domain.ResourceTrustAnchor) {
			resources[resource.ID] = resource
		}
	}
	adapter := &WorkflowAdapter{Steps: []installworkflow.Step{}}
	seen := map[string]bool{}
	for _, operation := range manifest.Execution.Operations {
		if operation.Stage != domain.ExecutionFiles {
			continue
		}
		resource, exists := resources[operation.ResourceID]
		if !exists {
			return nil, fmt.Errorf(
				"file operation %s has no managed resource",
				operation.ID,
			)
		}
		if operation.Observation == nil {
			return nil, fmt.Errorf(
				"file operation %s has no installed observation",
				operation.ID,
			)
		}
		observation := *operation.Observation
		if err := validateRollbackContract(
			resource,
			operation,
			observation,
		); err != nil {
			return nil, fmt.Errorf(
				"file operation %s: %w",
				operation.ID,
				err,
			)
		}
		backupDirectory := filepath.Join(
			manifest.ManagedSpecification.Paths.BackupDirectory,
			resource.ID,
		)
		capturedResource := resource
		capturedOperation := operation
		capturedObservation := observation
		capturedBackupDirectory := backupDirectory
		adapter.Steps = append(adapter.Steps, installworkflow.Step{
			ID:                operation.ID,
			ResourceID:        operation.ResourceID,
			Target:            operation.Target,
			Stage:             operation.Stage,
			IntentFingerprint: operation.IntentFingerprint,
			IntentMode:        operation.IntentMode,
			Apply: func(context.Context) (
				domain.InstallationObservation,
				error,
			) {
				return domain.InstallationObservation{},
					errors.New("rollback-only file step cannot apply")
			},
			Inspect: func(
				context.Context,
				*domain.InstallationObservation,
			) (
				installworkflow.Disposition,
				domain.InstallationObservation,
				error,
			) {
				return inspectInstalledFile(
					capturedResource,
					capturedOperation,
					capturedObservation,
				)
			},
			Rollback: func(
				context.Context,
				domain.InstallationObservation,
			) error {
				return rollbackInstalledFile(
					capturedResource,
					capturedOperation,
					capturedObservation,
					capturedBackupDirectory,
				)
			},
		})
		seen[resource.ID] = true
	}
	if len(seen) != len(resources) {
		return nil, fmt.Errorf(
			"file execution covers %d of %d managed file resources",
			len(seen),
			len(resources),
		)
	}
	return adapter, nil
}

func validateRollbackContract(
	resource domain.InstallationResource,
	operation domain.InstallationExecutionOperation,
	observation domain.InstallationObservation,
) error {
	if operation.IntentFingerprint == "" || operation.IntentMode == 0 {
		return errors.New("file intent fingerprint and mode are required")
	}
	if observation.Fingerprint != operation.IntentFingerprint {
		return errors.New("installed fingerprint does not match file intent")
	}
	switch resource.Rollback {
	case domain.RollbackRemove:
		if !observation.Created || observation.Backup != nil {
			return errors.New("remove rollback has invalid creation evidence")
		}
	case domain.RollbackRestore:
		if observation.Created ||
			observation.Backup == nil ||
			observation.SnapshotFingerprint == "" {
			return errors.New("restore rollback has invalid backup evidence")
		}
		if resource.State != domain.ResourceRolledBack &&
			(resource.Backup == nil ||
				*observation.Backup != *resource.Backup) {
			return errors.New(
				"restore rollback resource backup changed",
			)
		}
	default:
		return fmt.Errorf(
			"unsupported file rollback strategy %s",
			resource.Rollback,
		)
	}
	return nil
}

func inspectInstalledFile(
	resource domain.InstallationResource,
	operation domain.InstallationExecutionOperation,
	observation domain.InstallationObservation,
) (
	installworkflow.Disposition,
	domain.InstallationObservation,
	error,
) {
	target, err := inspectFile(resource.Target)
	if err != nil {
		return "", domain.InstallationObservation{}, err
	}
	applied := target.exists &&
		target.mode.Perm() == os.FileMode(operation.IntentMode).Perm() &&
		target.fingerprint == operation.IntentFingerprint
	switch resource.Rollback {
	case domain.RollbackRemove:
		if applied {
			return installworkflow.DispositionApplied, observation, nil
		}
		if !target.exists {
			return installworkflow.DispositionRolledBack, observation, nil
		}
	case domain.RollbackRestore:
		if applied {
			return installworkflow.DispositionApplied, observation, nil
		}
		if target.exists &&
			target.snapshotFingerprint == observation.SnapshotFingerprint {
			return installworkflow.DispositionRolledBack, observation, nil
		}
	}
	return installworkflow.DispositionConflict,
		domain.InstallationObservation{},
		nil
}

func rollbackInstalledFile(
	resource domain.InstallationResource,
	operation domain.InstallationExecutionOperation,
	observation domain.InstallationObservation,
	backupDirectory string,
) error {
	disposition, _, err := inspectInstalledFile(
		resource,
		operation,
		observation,
	)
	if err != nil {
		return err
	}
	switch disposition {
	case installworkflow.DispositionApplied:
		switch resource.Rollback {
		case domain.RollbackRemove:
			if err := removeFile(resource.Target); err != nil {
				return err
			}
		case domain.RollbackRestore:
			if err := restoreBackup(
				resource.Target,
				*observation.Backup,
			); err != nil {
				return err
			}
		}
	case installworkflow.DispositionRolledBack:
	case installworkflow.DispositionConflict:
		return fmt.Errorf(
			"managed file %s changed before uninstall",
			resource.Target,
		)
	default:
		return fmt.Errorf(
			"managed file %s has disposition %s",
			resource.Target,
			disposition,
		)
	}
	return cleanupInstalledBackup(backupDirectory, observation.Backup)
}

func cleanupInstalledBackup(
	backupDirectory string,
	backup *domain.ResourceBackup,
) error {
	if backup != nil {
		info, err := os.Lstat(backup.Path)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 ||
				!info.Mode().IsRegular() ||
				info.Size() > maxManagedFileSize {
				return errors.New("installed backup is unsafe")
			}
			content, err := os.ReadFile(backup.Path)
			if err != nil {
				return err
			}
			if fingerprint(content) != backup.Fingerprint {
				return errors.New("installed backup fingerprint changed")
			}
			if err := os.Remove(backup.Path); err != nil {
				return err
			}
			if err := syncDirectory(backupDirectory); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	info, err := os.Lstat(backupDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil ||
		info.Mode()&os.ModeSymlink != 0 ||
		!info.IsDir() {
		return errors.New("installed backup directory is unsafe")
	}
	entries, err := os.ReadDir(backupDirectory)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("installed backup directory is not empty")
	}
	if err := os.Remove(backupDirectory); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(backupDirectory))
}
