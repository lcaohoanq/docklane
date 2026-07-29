package installlinks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installworkflow"
	"golang.org/x/sys/unix"
)

type WorkflowAdapter struct {
	Steps []installworkflow.Step
}

func NewWorkflowAdapter(
	resources []domain.InstallationResource,
) (*WorkflowAdapter, error) {
	adapter := &WorkflowAdapter{Steps: []installworkflow.Step{}}
	for _, resource := range resources {
		if resource.Ownership != domain.ResourceManaged ||
			resource.Kind != domain.ResourceSymlink {
			continue
		}
		if err := resource.Validate(); err != nil {
			return nil, err
		}
		if resource.Rollback != domain.RollbackRestore {
			return nil, fmt.Errorf(
				"managed symlink %s must use restore rollback",
				resource.ID,
			)
		}
		captured := resource
		adapter.Steps = append(adapter.Steps, installworkflow.Step{
			ID:                "switch-" + resource.ID,
			ResourceID:        resource.ID,
			Target:            resource.Target,
			Stage:             domain.ExecutionHost,
			IntentFingerprint: fingerprint(resource.LinkTarget),
			IntentMode:        0o777,
			Apply: func(
				context.Context,
			) (domain.InstallationObservation, error) {
				return apply(captured)
			},
			Inspect: func(
				_ context.Context,
				recorded *domain.InstallationObservation,
			) (
				installworkflow.Disposition,
				domain.InstallationObservation,
				error,
			) {
				return inspect(captured, recorded)
			},
			Rollback: func(
				_ context.Context,
				observation domain.InstallationObservation,
			) error {
				return rollback(captured, observation)
			},
		})
	}
	return adapter, nil
}

func apply(
	resource domain.InstallationResource,
) (domain.InstallationObservation, error) {
	disposition, observation, err := inspect(resource, nil)
	if err != nil {
		return domain.InstallationObservation{}, err
	}
	switch disposition {
	case installworkflow.DispositionApplied:
		return observation, nil
	case installworkflow.DispositionPending:
	case installworkflow.DispositionConflict:
		return domain.InstallationObservation{}, errors.New(
			"managed symlink changed from its reviewed target",
		)
	default:
		return domain.InstallationObservation{}, fmt.Errorf(
			"unexpected symlink disposition %q",
			disposition,
		)
	}
	if err := swap(
		resource,
		resource.PriorTarget,
		resource.LinkTarget,
	); err != nil {
		return domain.InstallationObservation{}, err
	}
	return expectedObservation(resource), nil
}

func inspect(
	resource domain.InstallationResource,
	recorded *domain.InstallationObservation,
) (
	installworkflow.Disposition,
	domain.InstallationObservation,
	error,
) {
	expected := expectedObservation(resource)
	if recorded != nil && !sameObservation(*recorded, expected) {
		return installworkflow.DispositionConflict,
			domain.InstallationObservation{},
			nil
	}
	target, err := normalizedTarget(resource.Target)
	if err != nil {
		return installworkflow.DispositionConflict,
			domain.InstallationObservation{},
			nil
	}
	switch target {
	case resource.LinkTarget:
		return installworkflow.DispositionApplied, expected, nil
	case resource.PriorTarget:
		if recorded == nil {
			return installworkflow.DispositionPending,
				domain.InstallationObservation{},
				nil
		}
		return installworkflow.DispositionRolledBack, expected, nil
	default:
		return installworkflow.DispositionConflict,
			domain.InstallationObservation{},
			nil
	}
}

func rollback(
	resource domain.InstallationResource,
	observation domain.InstallationObservation,
) error {
	if !sameObservation(observation, expectedObservation(resource)) {
		return errors.New("symlink observation changed before rollback")
	}
	disposition, _, err := inspect(resource, &observation)
	if err != nil {
		return err
	}
	switch disposition {
	case installworkflow.DispositionRolledBack:
		return nil
	case installworkflow.DispositionApplied:
		return swap(resource, resource.LinkTarget, resource.PriorTarget)
	case installworkflow.DispositionConflict:
		return errors.New("managed symlink changed before rollback")
	default:
		return fmt.Errorf("unexpected symlink disposition %q", disposition)
	}
}

func swap(
	resource domain.InstallationResource,
	from string,
	to string,
) error {
	current, err := normalizedTarget(resource.Target)
	if err != nil {
		return err
	}
	if current == to {
		return nil
	}
	if current != from {
		return errors.New("managed symlink no longer matches reviewed target")
	}
	staging := stagingPath(resource)
	if err := cleanupStaging(staging, from); err != nil {
		return err
	}
	if err := os.Symlink(to, staging); err != nil {
		return err
	}
	if err := unix.Renameat2(
		unix.AT_FDCWD,
		staging,
		unix.AT_FDCWD,
		resource.Target,
		unix.RENAME_EXCHANGE,
	); err != nil {
		os.Remove(staging)
		return fmt.Errorf("atomically exchange managed symlink: %w", err)
	}
	previous, err := normalizedTarget(staging)
	if err != nil || previous != from {
		_ = unix.Renameat2(
			unix.AT_FDCWD,
			staging,
			unix.AT_FDCWD,
			resource.Target,
			unix.RENAME_EXCHANGE,
		)
		return errors.New("managed symlink changed during atomic exchange")
	}
	if err := os.Remove(staging); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(resource.Target))
}

func cleanupStaging(path string, expected string) error {
	target, err := normalizedTarget(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || target != expected {
		return errors.New("managed symlink staging path is unsafe")
	}
	return os.Remove(path)
}

func normalizedTarget(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", errors.New("path is not a symlink")
	}
	target, err := os.Readlink(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return filepath.Clean(target), nil
}

func expectedObservation(
	resource domain.InstallationResource,
) domain.InstallationObservation {
	return domain.InstallationObservation{
		ExternalID:          resource.Target,
		Fingerprint:         fingerprint(resource.LinkTarget),
		SnapshotFingerprint: fingerprint(resource.PriorTarget),
	}
}

func sameObservation(
	actual domain.InstallationObservation,
	expected domain.InstallationObservation,
) bool {
	return actual.ExternalID == expected.ExternalID &&
		actual.Fingerprint == expected.Fingerprint &&
		actual.SnapshotFingerprint == expected.SnapshotFingerprint &&
		actual.Created == expected.Created &&
		actual.Backup == nil
}

func fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func stagingPath(resource domain.InstallationResource) string {
	return filepath.Join(
		filepath.Dir(resource.Target),
		"."+filepath.Base(resource.Target)+"."+resource.ID+".preparing",
	)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
