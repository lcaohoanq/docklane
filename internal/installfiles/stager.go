package installfiles

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"docklane.local/docklane/internal/domain"
)

const maxManagedFileSize = 16 << 20

type File struct {
	ID        string
	Target    string
	Mode      fs.FileMode
	Content   []byte
	Sensitive bool
}

type Result struct {
	ID          string
	Target      string
	Mode        fs.FileMode
	Fingerprint string
	Created     bool
	Applied     bool
	Reverted    bool
	Backup      *domain.ResourceBackup
}

type Transaction struct {
	Results            []Result
	BackupDirectory    string
	createdDirectories []string
	backupCreated      bool
	rolledBack         bool
}

type Stager struct {
	beforeWrite func(int, File) error
}

func NewStager() *Stager {
	return &Stager{}
}

func (stager *Stager) Stage(
	files []File,
	backupDirectory string,
) (*Transaction, error) {
	if stager == nil {
		return nil, errors.New("file stager is required")
	}
	if err := validateInput(files, backupDirectory); err != nil {
		return nil, err
	}
	transaction := &Transaction{
		Results:         []Result{},
		BackupDirectory: backupDirectory,
	}
	if err := transaction.prepareBackupDirectory(); err != nil {
		return nil, transaction.abort(err)
	}
	for index, file := range files {
		result, err := transaction.prepare(file, index)
		if err != nil {
			return nil, transaction.abort(err)
		}
		transaction.Results = append(transaction.Results, result)
		if stager.beforeWrite != nil {
			if err := stager.beforeWrite(index, file); err != nil {
				return nil, transaction.abort(err)
			}
		}
		if err := ensureDirectory(
			filepath.Dir(file.Target),
			0o700,
			&transaction.createdDirectories,
		); err != nil {
			return nil, transaction.abort(err)
		}
		if err := writeAtomic(file.Target, file.Mode, file.Content); err != nil {
			if targetMatches(
				file.Target,
				file.Mode.Perm(),
				fingerprint(file.Content),
			) {
				transaction.Results[len(transaction.Results)-1].Applied = true
			}
			return nil, transaction.abort(fmt.Errorf(
				"write managed file %s: %w",
				file.Target,
				err,
			))
		}
		transaction.Results[len(transaction.Results)-1].Applied = true
	}
	return transaction, nil
}

func (transaction *Transaction) Rollback() error {
	if transaction == nil {
		return errors.New("file transaction is required")
	}
	if transaction.rolledBack {
		return nil
	}
	var rollbackErrors []error
	for index := len(transaction.Results) - 1; index >= 0; index-- {
		result := &transaction.Results[index]
		if !result.Applied || result.Reverted {
			continue
		}
		if err := verifyAppliedTarget(*result); err != nil {
			rollbackErrors = append(rollbackErrors, err)
			continue
		}
		if result.Created {
			if err := removeFile(result.Target); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf(
					"remove created file %s: %w",
					result.Target,
					err,
				))
			} else {
				result.Reverted = true
			}
			continue
		}
		if result.Backup == nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf(
				"restore %s: backup is missing",
				result.Target,
			))
			continue
		}
		if err := restoreBackup(result.Target, *result.Backup); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		} else {
			result.Reverted = true
		}
	}
	if len(rollbackErrors) != 0 {
		return errors.Join(rollbackErrors...)
	}
	transaction.rolledBack = true
	var cleanupErrors []error
	for _, result := range transaction.Results {
		if result.Backup != nil {
			if err := os.Remove(result.Backup.Path); err != nil &&
				!errors.Is(err, os.ErrNotExist) {
				cleanupErrors = append(cleanupErrors, fmt.Errorf(
					"remove consumed backup: %w",
					err,
				))
			}
		}
	}
	if transaction.backupCreated {
		if err := os.Remove(transaction.BackupDirectory); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf(
				"remove backup directory: %w",
				err,
			))
		}
	}
	for index := len(transaction.createdDirectories) - 1; index >= 0; index-- {
		err := os.Remove(transaction.createdDirectories[index])
		if err != nil &&
			!errors.Is(err, os.ErrNotExist) &&
			!errors.Is(err, syscall.ENOTEMPTY) &&
			!errors.Is(err, syscall.EEXIST) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf(
				"remove created directory: %w",
				err,
			))
		}
	}
	return errors.Join(cleanupErrors...)
}

func (transaction *Transaction) abort(cause error) error {
	rollbackError := transaction.Rollback()
	if rollbackError != nil {
		return errors.Join(cause, fmt.Errorf(
			"rollback staged files: %w",
			rollbackError,
		))
	}
	return cause
}

func (transaction *Transaction) prepareBackupDirectory() error {
	if _, err := os.Lstat(transaction.BackupDirectory); err == nil {
		return fmt.Errorf(
			"backup directory already exists: %s",
			transaction.BackupDirectory,
		)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ensureDirectory(
		filepath.Dir(transaction.BackupDirectory),
		0o700,
		&transaction.createdDirectories,
	); err != nil {
		return err
	}
	if err := os.Mkdir(transaction.BackupDirectory, 0o700); err != nil {
		return err
	}
	transaction.backupCreated = true
	return syncDirectory(filepath.Dir(transaction.BackupDirectory))
}

func (transaction *Transaction) prepare(file File, index int) (Result, error) {
	result := Result{
		ID:          file.ID,
		Target:      file.Target,
		Mode:        file.Mode.Perm(),
		Fingerprint: fingerprint(file.Content),
	}
	info, err := os.Lstat(file.Target)
	if errors.Is(err, os.ErrNotExist) {
		result.Created = true
		return result, nil
	}
	if err != nil {
		return Result{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Result{}, fmt.Errorf(
			"managed target must be a regular file, not %s",
			info.Mode(),
		)
	}
	if info.Size() > maxManagedFileSize {
		return Result{}, fmt.Errorf(
			"existing managed target exceeds %d bytes",
			maxManagedFileSize,
		)
	}
	content, err := os.ReadFile(file.Target)
	if err != nil {
		return Result{}, err
	}
	backupName := fmt.Sprintf(
		"%03d-%s-%s",
		index,
		fingerprint([]byte(file.Target))[:16],
		filepath.Base(file.Target),
	)
	backupPath := filepath.Join(transaction.BackupDirectory, backupName)
	if err := writeAtomic(backupPath, info.Mode().Perm(), content); err != nil {
		return Result{}, fmt.Errorf("write backup for %s: %w", file.Target, err)
	}
	result.Backup = &domain.ResourceBackup{
		Path:        backupPath,
		Fingerprint: fingerprint(content),
	}
	return result, nil
}

func validateInput(files []File, backupDirectory string) error {
	if len(files) == 0 {
		return errors.New("at least one managed file is required")
	}
	if !absoluteCanonical(backupDirectory) ||
		backupDirectory == string(filepath.Separator) {
		return errors.New("backup directory must be absolute, canonical, and not root")
	}
	targets := map[string]bool{}
	ids := map[string]bool{}
	for index, file := range files {
		if strings.TrimSpace(file.ID) == "" {
			return fmt.Errorf("managed file %d has no ID", index)
		}
		if ids[file.ID] {
			return fmt.Errorf("duplicate managed file ID %q", file.ID)
		}
		ids[file.ID] = true
		if !absoluteCanonical(file.Target) ||
			file.Target == string(filepath.Separator) {
			return fmt.Errorf(
				"managed file %s target must be absolute, canonical, and not root",
				file.ID,
			)
		}
		if targets[file.Target] {
			return fmt.Errorf("duplicate managed file target %q", file.Target)
		}
		targets[file.Target] = true
		if file.Mode.Perm() == 0 || file.Mode&^fs.FileMode(0o777) != 0 {
			return fmt.Errorf("managed file %s has invalid mode", file.ID)
		}
		if file.Sensitive && file.Mode.Perm()&0o077 != 0 {
			return fmt.Errorf(
				"sensitive managed file %s permits group or other access",
				file.ID,
			)
		}
		if file.Content == nil || len(file.Content) > maxManagedFileSize {
			return fmt.Errorf("managed file %s has invalid content size", file.ID)
		}
		if pathWithin(backupDirectory, file.Target) {
			return fmt.Errorf(
				"managed target %s cannot be inside its backup directory",
				file.Target,
			)
		}
		if file.Target == backupDirectory ||
			pathWithin(file.Target, backupDirectory) {
			return fmt.Errorf(
				"backup directory cannot be at or below managed target %s",
				file.Target,
			)
		}
		if err := validateExistingPath(filepath.Dir(file.Target), true); err != nil {
			return fmt.Errorf("managed target %s: %w", file.Target, err)
		}
	}
	return validateExistingPath(filepath.Dir(backupDirectory), true)
}

func ensureDirectory(
	path string,
	mode fs.FileMode,
	created *[]string,
) error {
	current := string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(path, current), current)
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, mode); err != nil {
				return err
			}
			*created = append(*created, current)
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("path component is not a real directory: %s", current)
		}
	}
	return nil
}

func validateExistingPath(path string, directory bool) error {
	current := string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(path, current), current)
	for index, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic link path component is not allowed: %s", current)
		}
		if index < len(parts)-1 || directory {
			if !info.IsDir() {
				return fmt.Errorf("path component is not a directory: %s", current)
			}
		}
	}
	return nil
}

func writeAtomic(path string, mode fs.FileMode, content []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".docklane-stage-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := io.Copy(temporary, bytes.NewReader(content)); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode.Perm()); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func restoreBackup(target string, backup domain.ResourceBackup) error {
	info, err := os.Lstat(backup.Path)
	if err != nil {
		return fmt.Errorf("inspect backup for %s: %w", target, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("backup for %s is not a regular file", target)
	}
	if info.Size() > maxManagedFileSize {
		return fmt.Errorf("backup for %s exceeds size limit", target)
	}
	content, err := os.ReadFile(backup.Path)
	if err != nil {
		return err
	}
	if fingerprint(content) != backup.Fingerprint {
		return fmt.Errorf("backup fingerprint for %s changed", target)
	}
	if err := writeAtomic(target, info.Mode().Perm(), content); err != nil {
		return fmt.Errorf("restore backup for %s: %w", target, err)
	}
	return nil
}

func verifyAppliedTarget(result Result) error {
	info, err := os.Lstat(result.Target)
	if err != nil {
		return fmt.Errorf(
			"verify staged target %s before rollback: %w",
			result.Target,
			err,
		)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf(
			"staged target %s changed type before rollback",
			result.Target,
		)
	}
	if info.Mode().Perm() != result.Mode.Perm() {
		return fmt.Errorf(
			"staged target %s changed mode before rollback",
			result.Target,
		)
	}
	if info.Size() > maxManagedFileSize {
		return fmt.Errorf(
			"staged target %s exceeds size limit before rollback",
			result.Target,
		)
	}
	content, err := os.ReadFile(result.Target)
	if err != nil {
		return err
	}
	if fingerprint(content) != result.Fingerprint {
		return fmt.Errorf(
			"staged target %s changed content before rollback",
			result.Target,
		)
	}
	return nil
}

func targetMatches(
	target string,
	mode fs.FileMode,
	expectedFingerprint string,
) bool {
	info, err := os.Lstat(target)
	if err != nil ||
		info.Mode()&os.ModeSymlink != 0 ||
		!info.Mode().IsRegular() ||
		info.Mode().Perm() != mode.Perm() ||
		info.Size() > maxManagedFileSize {
		return false
	}
	content, err := os.ReadFile(target)
	return err == nil && fingerprint(content) == expectedFingerprint
}

func removeFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("rollback target is not a regular file")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func absoluteCanonical(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

func pathWithin(parent string, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil &&
		relative != "." &&
		relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func fingerprint(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
