package installfiles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installworkflow"
)

// WorkflowAdapter owns cloned file material until Clear is called. Callers
// must clear it after the workflow reaches a durable terminal state.
type WorkflowAdapter struct {
	Steps []installworkflow.Step
	files []File
}

func NewWorkflowAdapter(
	files []File,
	resources []domain.InstallationResource,
	backupRoot string,
) (*WorkflowAdapter, error) {
	if !absoluteCanonical(backupRoot) ||
		backupRoot == string(filepath.Separator) {
		return nil, errors.New(
			"workflow backup root must be absolute, canonical, and not root",
		)
	}
	resourceByTarget := map[string]domain.InstallationResource{}
	for _, resource := range resources {
		if resource.Ownership != domain.ResourceManaged ||
			(resource.Kind != domain.ResourceFile &&
				resource.Kind != domain.ResourceTrustAnchor) {
			continue
		}
		if resourceByTarget[resource.Target].ID != "" {
			return nil, fmt.Errorf(
				"multiple managed file resources target %s",
				resource.Target,
			)
		}
		resourceByTarget[resource.Target] = resource
	}
	if len(files) != len(resourceByTarget) {
		return nil, fmt.Errorf(
			"materialized files cover %d of %d managed file resources",
			len(files),
			len(resourceByTarget),
		)
	}
	adapter := &WorkflowAdapter{
		Steps: make([]installworkflow.Step, 0, len(files)),
		files: make([]File, 0, len(files)),
	}
	seenTargets := map[string]bool{}
	for index, input := range files {
		resource, exists := resourceByTarget[input.Target]
		if !exists {
			adapter.Clear()
			return nil, fmt.Errorf(
				"materialized file %s has no managed resource",
				input.ID,
			)
		}
		if input.ID != resource.ID {
			adapter.Clear()
			return nil, fmt.Errorf(
				"materialized file %s does not match resource %s",
				input.ID,
				resource.ID,
			)
		}
		if seenTargets[input.Target] {
			adapter.Clear()
			return nil, fmt.Errorf(
				"duplicate materialized target %s",
				input.Target,
			)
		}
		seenTargets[input.Target] = true
		file := input
		file.Content = bytes.Clone(input.Content)
		if err := validateWorkflowFile(file, resource); err != nil {
			adapter.Clear()
			return nil, fmt.Errorf("materialized file %d: %w", index, err)
		}
		backupDirectory := filepath.Join(backupRoot, resource.ID)
		if err := validateInput([]File{file}, backupDirectory); err != nil {
			adapter.Clear()
			return nil, fmt.Errorf("materialized file %d: %w", index, err)
		}
		adapter.files = append(adapter.files, file)
		capturedFile := file
		capturedResource := resource
		capturedBackupDirectory := backupDirectory
		adapter.Steps = append(adapter.Steps, installworkflow.Step{
			ID:                "write-" + resource.ID,
			ResourceID:        resource.ID,
			Target:            resource.Target,
			Stage:             domain.ExecutionFiles,
			IntentFingerprint: fingerprint(file.Content),
			IntentMode:        uint32(file.Mode.Perm()),
			Apply: func(
				context.Context,
			) (domain.InstallationObservation, error) {
				return applyWorkflowFile(
					capturedFile,
					capturedResource,
					capturedBackupDirectory,
				)
			},
			Inspect: func(
				_ context.Context,
				observation *domain.InstallationObservation,
			) (
				installworkflow.Disposition,
				domain.InstallationObservation,
				error,
			) {
				return inspectWorkflowFile(
					capturedFile,
					capturedResource,
					capturedBackupDirectory,
					observation,
				)
			},
			Rollback: func(
				_ context.Context,
				observation domain.InstallationObservation,
			) error {
				return rollbackWorkflowFile(
					capturedFile,
					capturedResource,
					capturedBackupDirectory,
					observation,
				)
			},
		})
	}
	return adapter, nil
}

func (adapter *WorkflowAdapter) Clear() {
	if adapter == nil {
		return
	}
	for index := range adapter.files {
		if adapter.files[index].Sensitive {
			clear(adapter.files[index].Content)
		}
	}
	adapter.files = nil
	adapter.Steps = nil
}

func validateWorkflowFile(
	file File,
	resource domain.InstallationResource,
) error {
	if err := resource.Validate(); err != nil {
		return err
	}
	if file.Content == nil || len(file.Content) > maxManagedFileSize {
		return errors.New("file content is invalid")
	}
	if !absoluteCanonical(file.Target) ||
		file.Target == string(filepath.Separator) {
		return errors.New("file target must be absolute and canonical")
	}
	if file.Mode.Perm() == 0 || file.Mode&^fs.FileMode(0o777) != 0 {
		return errors.New("file mode is invalid")
	}
	if file.Sensitive && file.Mode.Perm()&0o077 != 0 {
		return errors.New("sensitive file mode is too broad")
	}
	if resource.Rollback != domain.RollbackRemove &&
		resource.Rollback != domain.RollbackRestore {
		return errors.New("managed file rollback must remove or restore")
	}
	return nil
}

func applyWorkflowFile(
	file File,
	resource domain.InstallationResource,
	backupDirectory string,
) (domain.InstallationObservation, error) {
	if err := requireRealDirectory(filepath.Dir(file.Target)); err != nil {
		return domain.InstallationObservation{}, fmt.Errorf(
			"managed target parent: %w",
			err,
		)
	}
	if err := requireRealDirectory(filepath.Dir(backupDirectory)); err != nil {
		return domain.InstallationObservation{}, fmt.Errorf(
			"backup root: %w",
			err,
		)
	}
	disposition, _, err := inspectWorkflowFile(
		file,
		resource,
		backupDirectory,
		nil,
	)
	if err != nil {
		return domain.InstallationObservation{}, err
	}
	switch disposition {
	case installworkflow.DispositionApplied:
		return domain.InstallationObservation{}, errors.New(
			"managed file is already applied without a durable observation",
		)
	case installworkflow.DispositionConflict:
		return domain.InstallationObservation{}, errors.New(
			"managed file precondition conflicts with rollback contract",
		)
	case installworkflow.DispositionPending:
		if err := cleanupPendingBackup(
			file,
			resource,
			backupDirectory,
		); err != nil {
			return domain.InstallationObservation{}, err
		}
	default:
		return domain.InstallationObservation{}, fmt.Errorf(
			"unexpected pre-apply disposition %q",
			disposition,
		)
	}
	if err := verifyRollbackPrecondition(file.Target, resource.Rollback); err != nil {
		return domain.InstallationObservation{}, err
	}
	transaction, err := NewStager().Stage(
		[]File{file},
		backupDirectory,
	)
	if err != nil {
		return domain.InstallationObservation{}, err
	}
	observation, err := observationFromResult(transaction.Results[0])
	if err != nil {
		rollbackErr := transaction.Rollback()
		return domain.InstallationObservation{}, errors.Join(err, rollbackErr)
	}
	if err := validateObservationContract(resource, observation); err != nil {
		rollbackErr := transaction.Rollback()
		return domain.InstallationObservation{}, errors.Join(err, rollbackErr)
	}
	return observation, nil
}

func inspectWorkflowFile(
	file File,
	resource domain.InstallationResource,
	backupDirectory string,
	recorded *domain.InstallationObservation,
) (
	installworkflow.Disposition,
	domain.InstallationObservation,
	error,
) {
	target, err := inspectFile(file.Target)
	if err != nil {
		return installworkflow.DispositionConflict,
			domain.InstallationObservation{},
			nil
	}
	evidence, err := inspectBackupEvidence(file, backupDirectory)
	if err != nil {
		return installworkflow.DispositionConflict,
			domain.InstallationObservation{},
			nil
	}
	if recorded == nil {
		if !evidence.exists {
			switch resource.Rollback {
			case domain.RollbackRemove:
				if !target.exists {
					return installworkflow.DispositionPending,
						domain.InstallationObservation{},
						nil
				}
			case domain.RollbackRestore:
				if target.exists {
					return installworkflow.DispositionPending,
						domain.InstallationObservation{},
						nil
				}
			}
			return installworkflow.DispositionConflict,
				domain.InstallationObservation{},
				nil
		}
		observation, valid := evidence.observation(file)
		if !valid || validateObservationContract(resource, observation) != nil {
			return installworkflow.DispositionConflict,
				domain.InstallationObservation{},
				nil
		}
		if target.matches(file) {
			return installworkflow.DispositionApplied, observation, nil
		}
		if evidence.matchesOriginal(target) {
			return installworkflow.DispositionPending,
				domain.InstallationObservation{},
				nil
		}
		return installworkflow.DispositionConflict,
			domain.InstallationObservation{},
			nil
	}
	if err := validateRecordedObservation(
		file,
		resource,
		evidence,
		*recorded,
	); err != nil {
		return installworkflow.DispositionConflict,
			domain.InstallationObservation{},
			nil
	}
	if target.matches(file) && evidence.exists {
		return installworkflow.DispositionApplied, *recorded, nil
	}
	if recorded.Created {
		if !target.exists {
			return installworkflow.DispositionRolledBack, *recorded, nil
		}
	} else if target.exists &&
		target.snapshotFingerprint == recorded.SnapshotFingerprint {
		return installworkflow.DispositionRolledBack, *recorded, nil
	}
	return installworkflow.DispositionConflict,
		domain.InstallationObservation{},
		nil
}

func rollbackWorkflowFile(
	file File,
	resource domain.InstallationResource,
	backupDirectory string,
	observation domain.InstallationObservation,
) error {
	evidence, err := inspectBackupEvidence(file, backupDirectory)
	if err != nil {
		return err
	}
	if err := validateRecordedObservation(
		file,
		resource,
		evidence,
		observation,
	); err != nil {
		return err
	}
	result := Result{
		ID:          file.ID,
		Target:      file.Target,
		Mode:        file.Mode.Perm(),
		Fingerprint: observation.Fingerprint,
		Created:     observation.Created,
		Applied:     true,
		Backup:      copyResourceBackup(observation.Backup),
	}
	transaction := &Transaction{
		Results:         []Result{result},
		BackupDirectory: backupDirectory,
		backupCreated:   true,
	}
	return transaction.Rollback()
}

type inspectedFile struct {
	exists              bool
	mode                fs.FileMode
	fingerprint         string
	snapshotFingerprint string
}

func inspectFile(path string) (inspectedFile, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return inspectedFile{}, nil
	}
	if err != nil {
		return inspectedFile{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		info.Size() > maxManagedFileSize {
		return inspectedFile{}, errors.New("path is not a safe managed file")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return inspectedFile{}, err
	}
	return inspectedFile{
		exists:              true,
		mode:                info.Mode().Perm(),
		fingerprint:         fingerprint(content),
		snapshotFingerprint: fileSnapshotFingerprint(info.Mode().Perm(), content),
	}, nil
}

func (inspected inspectedFile) matches(file File) bool {
	return inspected.exists &&
		inspected.mode == file.Mode.Perm() &&
		inspected.fingerprint == fingerprint(file.Content)
}

type backupEvidence struct {
	exists              bool
	created             bool
	backup              *domain.ResourceBackup
	snapshotFingerprint string
}

func inspectBackupEvidence(
	file File,
	backupDirectory string,
) (backupEvidence, error) {
	info, err := os.Lstat(backupDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return backupEvidence{}, nil
	}
	if err != nil {
		return backupEvidence{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() ||
		info.Mode().Perm()&0o077 != 0 {
		return backupEvidence{}, errors.New("backup directory is unsafe")
	}
	entries, err := os.ReadDir(backupDirectory)
	if err != nil {
		return backupEvidence{}, err
	}
	if len(entries) == 0 {
		return backupEvidence{exists: true, created: true}, nil
	}
	expectedPath := workflowBackupPath(file, backupDirectory)
	if len(entries) != 1 ||
		filepath.Join(backupDirectory, entries[0].Name()) != expectedPath {
		return backupEvidence{}, errors.New("backup directory contents are unexpected")
	}
	backup, err := inspectFile(expectedPath)
	if err != nil || !backup.exists {
		return backupEvidence{}, errors.New("backup file is unsafe")
	}
	return backupEvidence{
		exists:  true,
		created: false,
		backup: &domain.ResourceBackup{
			Path:        expectedPath,
			Fingerprint: backup.fingerprint,
		},
		snapshotFingerprint: backup.snapshotFingerprint,
	}, nil
}

func (evidence backupEvidence) observation(
	file File,
) (domain.InstallationObservation, bool) {
	if !evidence.exists {
		return domain.InstallationObservation{}, false
	}
	return domain.InstallationObservation{
		Fingerprint:         fingerprint(file.Content),
		Created:             evidence.created,
		Backup:              copyResourceBackup(evidence.backup),
		SnapshotFingerprint: evidence.snapshotFingerprint,
	}, true
}

func (evidence backupEvidence) matchesOriginal(target inspectedFile) bool {
	if evidence.created {
		return !target.exists
	}
	return target.exists &&
		target.snapshotFingerprint == evidence.snapshotFingerprint
}

func observationFromResult(
	result Result,
) (domain.InstallationObservation, error) {
	observation := domain.InstallationObservation{
		Fingerprint: result.Fingerprint,
		Created:     result.Created,
		Backup:      copyResourceBackup(result.Backup),
	}
	if result.Backup != nil {
		backup, err := inspectFile(result.Backup.Path)
		if err != nil || !backup.exists {
			return domain.InstallationObservation{}, errors.New(
				"inspect staged backup",
			)
		}
		observation.SnapshotFingerprint = backup.snapshotFingerprint
	}
	return observation, nil
}

func validateObservationContract(
	resource domain.InstallationResource,
	observation domain.InstallationObservation,
) error {
	switch resource.Rollback {
	case domain.RollbackRemove:
		if !observation.Created || observation.Backup != nil ||
			observation.SnapshotFingerprint != "" {
			return errors.New("remove rollback requires a newly created file")
		}
	case domain.RollbackRestore:
		if observation.Created || observation.Backup == nil ||
			observation.SnapshotFingerprint == "" {
			return errors.New("restore rollback requires a prior file snapshot")
		}
	default:
		return errors.New("unsupported file rollback contract")
	}
	return nil
}

func validateRecordedObservation(
	file File,
	resource domain.InstallationResource,
	evidence backupEvidence,
	observation domain.InstallationObservation,
) error {
	if observation.Fingerprint != fingerprint(file.Content) {
		return errors.New("managed content fingerprint changed")
	}
	if err := validateObservationContract(resource, observation); err != nil {
		return err
	}
	if evidence.exists {
		reconstructed, valid := evidence.observation(file)
		if !valid ||
			reconstructed.Created != observation.Created ||
			reconstructed.SnapshotFingerprint != observation.SnapshotFingerprint ||
			!sameBackup(reconstructed.Backup, observation.Backup) {
			return errors.New("backup evidence changed")
		}
	}
	return nil
}

func verifyRollbackPrecondition(
	target string,
	strategy domain.RollbackStrategy,
) error {
	inspected, err := inspectFile(target)
	if err != nil {
		return err
	}
	switch strategy {
	case domain.RollbackRemove:
		if inspected.exists {
			return errors.New("remove rollback target already exists")
		}
	case domain.RollbackRestore:
		if !inspected.exists {
			return errors.New("restore rollback target does not exist")
		}
	}
	return nil
}

func cleanupPendingBackup(
	file File,
	resource domain.InstallationResource,
	backupDirectory string,
) error {
	evidence, err := inspectBackupEvidence(file, backupDirectory)
	if err != nil || !evidence.exists {
		return err
	}
	target, err := inspectFile(file.Target)
	if err != nil || !evidence.matchesOriginal(target) {
		return errors.New("cannot clean backup without proving original target")
	}
	if err := validateObservationContract(
		resource,
		domain.InstallationObservation{
			Fingerprint:         fingerprint(file.Content),
			Created:             evidence.created,
			Backup:              evidence.backup,
			SnapshotFingerprint: evidence.snapshotFingerprint,
		},
	); err != nil {
		return err
	}
	if evidence.backup != nil {
		if err := removeFile(evidence.backup.Path); err != nil {
			return err
		}
	}
	if err := os.Remove(backupDirectory); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(backupDirectory))
}

func requireRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("path is not a real directory")
	}
	return nil
}

func workflowBackupPath(file File, backupDirectory string) string {
	return filepath.Join(
		backupDirectory,
		fmt.Sprintf(
			"%03d-%s-%s",
			0,
			fingerprint([]byte(file.Target))[:16],
			filepath.Base(file.Target),
		),
	)
}

func fileSnapshotFingerprint(mode fs.FileMode, content []byte) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%04o\x00", mode.Perm())
	hash.Write(content)
	return hex.EncodeToString(hash.Sum(nil))
}

func copyResourceBackup(
	backup *domain.ResourceBackup,
) *domain.ResourceBackup {
	if backup == nil {
		return nil
	}
	copied := *backup
	return &copied
}

func sameBackup(left *domain.ResourceBackup, right *domain.ResourceBackup) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
