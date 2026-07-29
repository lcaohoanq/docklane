package installdirs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installworkflow"
	"golang.org/x/sys/unix"
)

const (
	markerName                        = ".docklane-owner.json"
	directoryMarkerSchema             = 1
	defaultDirectoryMode  fs.FileMode = 0o700
)

type marker struct {
	SchemaVersion  int    `json:"schemaVersion"`
	InstallationID string `json:"installationId"`
	ResourceID     string `json:"resourceId"`
	Target         string `json:"target"`
}

type WorkflowAdapter struct {
	Steps []installworkflow.Step
}

func NewWorkflowAdapter(
	installationID string,
	stateDirectory string,
	resources []domain.InstallationResource,
) (*WorkflowAdapter, error) {
	return newWorkflowAdapter(
		installationID,
		stateDirectory,
		nil,
		resources,
	)
}

func NewManagedWorkflowAdapter(
	installationID string,
	specification domain.InstallationSpecification,
	resources []domain.InstallationResource,
) (*WorkflowAdapter, error) {
	adapter, err := newWorkflowAdapter(
		installationID,
		specification.Paths.StateDirectory,
		map[string]fs.FileMode{
			filepath.Dir(specification.Paths.ResolverConfig): 0o755,
		},
		resources,
	)
	if err != nil {
		return nil, err
	}
	if err := usePersistentRollback(
		adapter,
		installationID,
		resources,
	); err != nil {
		return nil, err
	}
	return adapter, nil
}

func newWorkflowAdapter(
	installationID string,
	stateDirectory string,
	hostDirectories map[string]fs.FileMode,
	resources []domain.InstallationResource,
) (*WorkflowAdapter, error) {
	if installationID == "" {
		return nil, errors.New("installation ID is required")
	}
	if !absoluteCanonical(stateDirectory) {
		return nil, errors.New("state directory must be absolute and canonical")
	}
	adapter := &WorkflowAdapter{Steps: []installworkflow.Step{}}
	for _, resource := range resources {
		if resource.Ownership != domain.ResourceManaged ||
			resource.Kind != domain.ResourceDirectory {
			continue
		}
		if err := resource.Validate(); err != nil {
			return nil, err
		}
		if resource.Rollback != domain.RollbackRemove {
			return nil, fmt.Errorf(
				"managed directory %s must use remove rollback",
				resource.ID,
			)
		}
		mode := defaultDirectoryMode
		if !pathWithin(stateDirectory, resource.Target) {
			hostMode, allowed := hostDirectories[resource.Target]
			if !allowed {
				return nil, fmt.Errorf(
					"managed directory %s must stay below the state directory or match an allowed host directory",
					resource.ID,
				)
			}
			mode = hostMode
		}
		if mode.Perm() == 0 || mode.Perm() != mode {
			return nil, fmt.Errorf(
				"managed directory %s has invalid mode",
				resource.ID,
			)
		}
		content, err := markerContent(
			installationID,
			resource.ID,
			resource.Target,
		)
		if err != nil {
			return nil, err
		}
		capturedResource := resource
		capturedContent := content
		adapter.Steps = append(adapter.Steps, installworkflow.Step{
			ID:                "create-" + resource.ID,
			ResourceID:        resource.ID,
			Target:            resource.Target,
			Stage:             domain.ExecutionDirectories,
			IntentFingerprint: fingerprint(content),
			IntentMode:        uint32(mode),
			Apply: func(
				context.Context,
			) (domain.InstallationObservation, error) {
				return applyDirectory(capturedResource, capturedContent, mode)
			},
			Inspect: func(
				_ context.Context,
				observation *domain.InstallationObservation,
			) (
				installworkflow.Disposition,
				domain.InstallationObservation,
				error,
			) {
				return inspectDirectory(
					capturedResource,
					capturedContent,
					mode,
					observation,
				)
			},
			Rollback: func(
				_ context.Context,
				observation domain.InstallationObservation,
			) error {
				return rollbackDirectory(
					capturedResource,
					capturedContent,
					mode,
					observation,
				)
			},
		})
	}
	return adapter, nil
}

func applyDirectory(
	resource domain.InstallationResource,
	content []byte,
	mode fs.FileMode,
) (domain.InstallationObservation, error) {
	disposition, observation, err := inspectDirectory(
		resource,
		content,
		mode,
		nil,
	)
	if err != nil {
		return domain.InstallationObservation{}, err
	}
	switch disposition {
	case installworkflow.DispositionApplied:
		return observation, nil
	case installworkflow.DispositionConflict:
		return domain.InstallationObservation{}, errors.New(
			"managed directory conflicts with ownership marker",
		)
	case installworkflow.DispositionPending:
	default:
		return domain.InstallationObservation{}, fmt.Errorf(
			"unexpected directory disposition %q",
			disposition,
		)
	}
	parent := filepath.Dir(resource.Target)
	if err := requireOwnedDirectory(parent); err != nil {
		return domain.InstallationObservation{}, fmt.Errorf(
			"managed directory parent: %w",
			err,
		)
	}
	staging := stagingPath(resource)
	if err := cleanupStaging(staging, content, mode); err != nil {
		return domain.InstallationObservation{}, err
	}
	if err := os.Mkdir(staging, mode); err != nil {
		return domain.InstallationObservation{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			cleanupStaging(staging, content, mode)
		}
	}()
	if err := writeMarker(
		filepath.Join(staging, markerName),
		content,
	); err != nil {
		return domain.InstallationObservation{}, err
	}
	if err := syncDirectory(staging); err != nil {
		return domain.InstallationObservation{}, err
	}
	if err := unix.Renameat2(
		unix.AT_FDCWD,
		staging,
		unix.AT_FDCWD,
		resource.Target,
		unix.RENAME_NOREPLACE,
	); err != nil {
		disposition, observation, inspectErr := inspectDirectory(
			resource,
			content,
			mode,
			nil,
		)
		if inspectErr == nil &&
			disposition == installworkflow.DispositionApplied {
			return observation, nil
		}
		return domain.InstallationObservation{}, fmt.Errorf(
			"publish managed directory without replacement: %w",
			err,
		)
	}
	cleanup = false
	if err := syncDirectory(parent); err != nil {
		return domain.InstallationObservation{}, err
	}
	return expectedObservation(resource, content), nil
}

func inspectDirectory(
	resource domain.InstallationResource,
	content []byte,
	mode fs.FileMode,
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
		if recorded == nil {
			return installworkflow.DispositionPending,
				domain.InstallationObservation{},
				nil
		}
		return installworkflow.DispositionRolledBack, expected, nil
	}
	if err != nil ||
		info.Mode()&os.ModeSymlink != 0 ||
		!info.IsDir() ||
		info.Mode().Perm() != mode.Perm() ||
		!ownedByCurrentUser(info) {
		return installworkflow.DispositionConflict,
			domain.InstallationObservation{},
			nil
	}
	entries, err := os.ReadDir(resource.Target)
	if err != nil {
		return "", domain.InstallationObservation{}, err
	}
	if len(entries) == 0 && recorded != nil {
		return installworkflow.DispositionApplied, expected, nil
	}
	if len(entries) != 1 || entries[0].Name() != markerName {
		return installworkflow.DispositionConflict,
			domain.InstallationObservation{},
			nil
	}
	if err := verifyMarker(
		filepath.Join(resource.Target, markerName),
		content,
	); err != nil {
		return installworkflow.DispositionConflict,
			domain.InstallationObservation{},
			nil
	}
	return installworkflow.DispositionApplied, expected, nil
}

func rollbackDirectory(
	resource domain.InstallationResource,
	content []byte,
	mode fs.FileMode,
	observation domain.InstallationObservation,
) error {
	expected := expectedObservation(resource, content)
	if !sameObservation(observation, expected) {
		return errors.New("directory observation does not match ownership marker")
	}
	info, err := os.Lstat(resource.Target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil ||
		info.Mode()&os.ModeSymlink != 0 ||
		!info.IsDir() ||
		info.Mode().Perm() != mode.Perm() ||
		!ownedByCurrentUser(info) {
		return errors.New("managed directory changed before rollback")
	}
	entries, err := os.ReadDir(resource.Target)
	if err != nil {
		return err
	}
	switch {
	case len(entries) == 0:
		// Recovery after the marker was removed but before the directory.
	case len(entries) == 1 && entries[0].Name() == markerName:
		markerPath := filepath.Join(resource.Target, markerName)
		if err := verifyMarker(markerPath, content); err != nil {
			return err
		}
		if err := os.Remove(markerPath); err != nil {
			return err
		}
		if err := syncDirectory(resource.Target); err != nil {
			return err
		}
	default:
		return errors.New("managed directory is not empty during rollback")
	}
	if err := os.Remove(resource.Target); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(resource.Target))
}

func expectedObservation(
	resource domain.InstallationResource,
	content []byte,
) domain.InstallationObservation {
	return domain.InstallationObservation{
		ExternalID:  filepath.Join(resource.Target, markerName),
		Fingerprint: fingerprint(content),
		Created:     true,
	}
}

func sameObservation(
	actual domain.InstallationObservation,
	expected domain.InstallationObservation,
) bool {
	return actual.ExternalID == expected.ExternalID &&
		actual.Fingerprint == expected.Fingerprint &&
		actual.Created == expected.Created &&
		actual.Backup == nil &&
		actual.SnapshotFingerprint == ""
}

func markerContent(
	installationID string,
	resourceID string,
	target string,
) ([]byte, error) {
	encoded, err := json.MarshalIndent(marker{
		SchemaVersion:  directoryMarkerSchema,
		InstallationID: installationID,
		ResourceID:     resourceID,
		Target:         target,
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func writeMarker(path string, content []byte) error {
	file, err := os.OpenFile(
		path,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func verifyMarker(path string, expected []byte) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 ||
		!info.Mode().IsRegular() ||
		info.Mode().Perm() != 0o600 ||
		!ownedByCurrentUser(info) ||
		info.Size() > 4096 {
		return errors.New("directory ownership marker is unsafe")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(content, expected) {
		return errors.New("directory ownership marker changed")
	}
	return nil
}

func cleanupStaging(
	path string,
	markerContent []byte,
	mode fs.FileMode,
) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil ||
		info.Mode()&os.ModeSymlink != 0 ||
		!info.IsDir() ||
		info.Mode().Perm() != mode.Perm() ||
		!ownedByCurrentUser(info) {
		return errors.New("directory staging path is unsafe")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) == 1 && entries[0].Name() == markerName {
		markerPath := filepath.Join(path, markerName)
		if err := verifyMarker(markerPath, markerContent); err != nil {
			return err
		}
		if err := os.Remove(markerPath); err != nil {
			return err
		}
	} else if len(entries) != 0 {
		return errors.New("directory staging path contains unexpected entries")
	}
	return os.Remove(path)
}

func stagingPath(resource domain.InstallationResource) string {
	return filepath.Join(
		filepath.Dir(resource.Target),
		"."+filepath.Base(resource.Target)+"."+resource.ID+".preparing",
	)
}

func requireOwnedDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 ||
		!info.IsDir() ||
		!ownedByCurrentUser(info) {
		return errors.New("path is not an owned real directory")
	}
	return nil
}

func ownedByCurrentUser(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func absoluteCanonical(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

func pathWithin(parent string, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil &&
		relative != "." &&
		relative != ".." &&
		!filepath.IsAbs(relative) &&
		len(relative) > 0 &&
		relative[0] != '.'
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func fingerprint(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
