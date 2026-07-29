package installdirs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installworkflow"
)

const persistentDataResourceID = "docklane-data"

func NewUninstallWorkflowAdapter(
	manifest domain.InstallationManifest,
) (*WorkflowAdapter, error) {
	if manifest.ManagedSpecification == nil {
		return nil, errors.New(
			"managed specification is required for directory uninstall",
		)
	}
	adapter, err := NewWorkflowAdapter(
		manifest.InstallationID,
		manifest.ManagedSpecification.Paths.StateDirectory,
		manifest.Resources,
	)
	if err != nil {
		return nil, err
	}
	var data *domain.InstallationResource
	for index := range manifest.Resources {
		resource := &manifest.Resources[index]
		if resource.ID == persistentDataResourceID &&
			resource.Ownership == domain.ResourceManaged {
			data = resource
			break
		}
	}
	if data == nil {
		return adapter, nil
	}
	content, err := markerContent(
		manifest.InstallationID,
		data.ID,
		data.Target,
	)
	if err != nil {
		return nil, err
	}
	replaced := false
	for index := range adapter.Steps {
		if adapter.Steps[index].ResourceID != data.ID {
			continue
		}
		resource := *data
		adapter.Steps[index].Inspect = func(
			_ context.Context,
			recorded *domain.InstallationObservation,
		) (
			installworkflow.Disposition,
			domain.InstallationObservation,
			error,
		) {
			return inspectPersistentDirectory(
				resource,
				content,
				recorded,
			)
		}
		adapter.Steps[index].Rollback = func(
			_ context.Context,
			observation domain.InstallationObservation,
		) error {
			return releasePersistentDirectory(
				resource,
				content,
				observation,
			)
		}
		replaced = true
	}
	if !replaced {
		return nil, errors.New(
			"managed persistent data directory has no workflow step",
		)
	}
	return adapter, nil
}

func inspectPersistentDirectory(
	resource domain.InstallationResource,
	content []byte,
	recorded *domain.InstallationObservation,
) (
	installworkflow.Disposition,
	domain.InstallationObservation,
	error,
) {
	expected := expectedObservation(resource, content)
	if recorded != nil && !sameObservation(*recorded, expected) {
		return installworkflow.DispositionConflict,
			domain.InstallationObservation{},
			nil
	}
	info, err := os.Lstat(resource.Target)
	if errors.Is(err, os.ErrNotExist) {
		return installworkflow.DispositionRolledBack, expected, nil
	}
	if err != nil ||
		info.Mode()&os.ModeSymlink != 0 ||
		!info.IsDir() ||
		info.Mode().Perm() != defaultDirectoryMode ||
		!ownedByCurrentUser(info) {
		return installworkflow.DispositionConflict,
			domain.InstallationObservation{},
			nil
	}
	markerPath := filepath.Join(resource.Target, markerName)
	if _, err := os.Lstat(markerPath); errors.Is(err, os.ErrNotExist) {
		return installworkflow.DispositionRolledBack, expected, nil
	} else if err != nil {
		return "", domain.InstallationObservation{}, err
	}
	if err := verifyMarker(markerPath, content); err != nil {
		return installworkflow.DispositionConflict,
			domain.InstallationObservation{},
			nil
	}
	return installworkflow.DispositionApplied, expected, nil
}

func releasePersistentDirectory(
	resource domain.InstallationResource,
	content []byte,
	observation domain.InstallationObservation,
) error {
	expected := expectedObservation(resource, content)
	if !sameObservation(observation, expected) {
		return errors.New(
			"persistent directory observation does not match ownership marker",
		)
	}
	disposition, _, err := inspectPersistentDirectory(
		resource,
		content,
		&observation,
	)
	if err != nil {
		return err
	}
	switch disposition {
	case installworkflow.DispositionRolledBack:
		return nil
	case installworkflow.DispositionApplied:
	case installworkflow.DispositionConflict:
		return errors.New(
			"persistent data directory changed before uninstall",
		)
	default:
		return fmt.Errorf(
			"persistent data directory has disposition %s",
			disposition,
		)
	}
	markerPath := filepath.Join(resource.Target, markerName)
	if err := os.Remove(markerPath); err != nil {
		return err
	}
	if err := syncDirectory(resource.Target); err != nil {
		return err
	}
	entries, err := os.ReadDir(resource.Target)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return nil
	}
	if err := os.Remove(resource.Target); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(resource.Target))
}
