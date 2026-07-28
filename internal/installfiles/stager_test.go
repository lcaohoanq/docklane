package installfiles

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStageWritesAtomicallyAndRollbackRestoresPriorState(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing.conf")
	if err := os.WriteFile(existing, []byte("before\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(root, "state", "secrets", "password")
	backupDirectory := filepath.Join(root, "backups", "transaction-1")
	transaction, err := NewStager().Stage([]File{
		{
			ID:      "existing",
			Target:  existing,
			Mode:    0o644,
			Content: []byte("after\n"),
		},
		{
			ID:        "password",
			Target:    created,
			Mode:      0o600,
			Content:   []byte("secret\n"),
			Sensitive: true,
		},
	}, backupDirectory)
	if err != nil {
		t.Fatal(err)
	}
	assertFile(t, existing, "after\n", 0o644)
	assertFile(t, created, "secret\n", 0o600)
	if len(transaction.Results) != 2 ||
		transaction.Results[0].Created ||
		!transaction.Results[0].Applied ||
		transaction.Results[0].Backup == nil ||
		!transaction.Results[1].Created ||
		!transaction.Results[1].Applied ||
		transaction.Results[1].Backup != nil {
		t.Fatalf("results = %#v", transaction.Results)
	}
	assertFile(t, transaction.Results[0].Backup.Path, "before\n", 0o640)

	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertFile(t, existing, "before\n", 0o640)
	if _, err := os.Lstat(created); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created file still exists: %v", err)
	}
	if _, err := os.Lstat(backupDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup directory still exists: %v", err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatalf("repeated rollback: %v", err)
	}
}

func TestStageFailureRollsBackEarlierWrites(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first.conf")
	second := filepath.Join(root, "second.conf")
	if err := os.WriteFile(first, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stager := NewStager()
	stager.beforeWrite = func(index int, _ File) error {
		if index == 1 {
			return errors.New("injected write failure")
		}
		return nil
	}
	backupDirectory := filepath.Join(root, "backups", "transaction-2")
	_, err := stager.Stage([]File{
		{ID: "first", Target: first, Mode: 0o644, Content: []byte("changed\n")},
		{ID: "second", Target: second, Mode: 0o644, Content: []byte("new\n")},
	}, backupDirectory)
	if err == nil || !strings.Contains(err.Error(), "injected write failure") {
		t.Fatalf("error = %v", err)
	}
	assertFile(t, first, "original\n", 0o600)
	if _, err := os.Lstat(second); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second file exists after failed transaction: %v", err)
	}
	if _, err := os.Lstat(backupDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup directory exists after failed transaction: %v", err)
	}
}

func TestRollbackRefusesModifiedBackup(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "dnsmasq.conf")
	if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	transaction, err := NewStager().Stage(
		[]File{{
			ID:      "dnsmasq",
			Target:  target,
			Mode:    0o644,
			Content: []byte("managed\n"),
		}},
		filepath.Join(root, "backups", "transaction-3"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		transaction.Results[0].Backup.Path,
		[]byte("tampered\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	err = transaction.Rollback()
	if err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("rollback error = %v", err)
	}
	assertFile(t, target, "managed\n", 0o644)
	if _, err := os.Stat(transaction.Results[0].Backup.Path); err != nil {
		t.Fatalf("tampered backup was removed: %v", err)
	}
	if err := os.WriteFile(
		transaction.Results[0].Backup.Path,
		[]byte("original\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatalf("retry rollback after repairing backup: %v", err)
	}
	assertFile(t, target, "original\n", 0o644)
}

func TestRollbackRefusesTargetChangedAfterStaging(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "dynamic.yml")
	if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	transaction, err := NewStager().Stage(
		[]File{{
			ID:      "dynamic",
			Target:  target,
			Mode:    0o644,
			Content: []byte("managed\n"),
		}},
		filepath.Join(root, "backups", "transaction-4"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("external change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = transaction.Rollback()
	if err == nil || !strings.Contains(err.Error(), "changed content") {
		t.Fatalf("rollback error = %v", err)
	}
	assertFile(t, target, "external change\n", 0o644)
	if _, err := os.Stat(transaction.Results[0].Backup.Path); err != nil {
		t.Fatalf("backup was removed after target conflict: %v", err)
	}
}

func TestStageRejectsSymbolicLinkTargetAndParent(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	targetLink := filepath.Join(root, "target-link")
	if err := os.Symlink(outside, targetLink); err != nil {
		t.Fatal(err)
	}
	_, err := NewStager().Stage(
		[]File{{ID: "link", Target: targetLink, Mode: 0o644, Content: []byte("bad\n")}},
		filepath.Join(root, "backup-target-link"),
	)
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("target symlink error = %v", err)
	}
	assertFile(t, outside, "outside\n", 0o644)

	parentLink := filepath.Join(root, "parent-link")
	if err := os.Symlink(root, parentLink); err != nil {
		t.Fatal(err)
	}
	_, err = NewStager().Stage(
		[]File{{
			ID:      "parent-link",
			Target:  filepath.Join(parentLink, "escaped"),
			Mode:    0o644,
			Content: []byte("bad\n"),
		}},
		filepath.Join(root, "backup-parent-link"),
	)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("parent symlink error = %v", err)
	}
}

func TestStageRejectsBroadSensitivePermissionsBeforeMutation(t *testing.T) {
	root := t.TempDir()
	backupDirectory := filepath.Join(root, "backups")
	_, err := NewStager().Stage(
		[]File{{
			ID:        "password",
			Target:    filepath.Join(root, "password"),
			Mode:      0o640,
			Content:   []byte("secret"),
			Sensitive: true,
		}},
		backupDirectory,
	)
	if err == nil || !strings.Contains(err.Error(), "group or other") {
		t.Fatalf("permission error = %v", err)
	}
	if _, err := os.Lstat(backupDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("validation mutated backup path: %v", err)
	}
}

func TestStageDoesNotRemovePreexistingBackupDirectory(t *testing.T) {
	root := t.TempDir()
	backupDirectory := filepath.Join(root, "existing-backups")
	if err := os.Mkdir(backupDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(backupDirectory, "keep")
	if err := os.WriteFile(marker, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewStager().Stage(
		[]File{{
			ID:      "config",
			Target:  filepath.Join(root, "config"),
			Mode:    0o644,
			Content: []byte("managed\n"),
		}},
		backupDirectory,
	)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("backup collision error = %v", err)
	}
	assertFile(t, marker, "keep\n", 0o600)
}

func assertFile(
	t *testing.T,
	path string,
	content string,
	mode os.FileMode,
) {
	t.Helper()
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != content {
		t.Fatalf("%s content = %q, want %q", path, actual, content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), mode)
	}
}
