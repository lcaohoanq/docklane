package installmanifest

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"docklane.local/docklane/internal/domain"
)

func testManifest(t *testing.T) domain.InstallationManifest {
	t.Helper()
	manifest, err := New(
		"dev",
		"docker.home.arpa",
		"proxy",
		time.Date(2026, time.July, 28, 4, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestStoreCreateLoadAndSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "install-manifest.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest := testManifest(t)
	if err := store.Create(manifest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.InstallationID != manifest.InstallationID ||
		loaded.Generation != 1 ||
		loaded.Resources == nil {
		t.Fatalf("loaded manifest = %#v", loaded)
	}
	loaded.Generation++
	loaded.State = domain.InstallationApplying
	loaded.UpdatedAt = loaded.UpdatedAt.Add(time.Second)
	if err := store.Save(1, loaded); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Generation != 2 ||
		reloaded.State != domain.InstallationApplying {
		t.Fatalf("reloaded manifest = %#v", reloaded)
	}
}

func TestStoreProtectsExistingAndConcurrentGenerations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install-manifest.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest := testManifest(t)
	if err := store.Create(manifest); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(manifest); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate create error = %v", err)
	}
	replacement := manifest
	replacement.Generation = 2
	replacement.UpdatedAt = replacement.UpdatedAt.Add(time.Second)
	if err := store.Save(7, replacement); err == nil ||
		!strings.Contains(err.Error(), "generation must be 8") {
		t.Fatalf("invalid replacement error = %v", err)
	}
	if err := store.Save(0, manifest); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("stale save error = %v", err)
	}
}

func TestStoreRejectsUnknownFieldsWithoutReplacingValidState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install-manifest.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest := testManifest(t)
	if err := store.Create(manifest); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	encoded = []byte(strings.Replace(
		string(encoded),
		`"schemaVersion":1`,
		`"schemaVersion":1,"unexpected":true`,
		1,
	))
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil ||
		!strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("load error = %v", err)
	}
}

func TestStoreRejectsManifestSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "install-manifest.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil ||
		!strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("load error = %v", err)
	}
}

func TestStoreRejectsOversizedManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install-manifest.json")
	if err := os.WriteFile(path, make([]byte, (4<<20)+1), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil ||
		!strings.Contains(err.Error(), "4 MiB") {
		t.Fatalf("load error = %v", err)
	}
}

func TestStoreRejectsBroadManifestPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install-manifest.json")
	encoded, err := json.Marshal(testManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil ||
		!strings.Contains(err.Error(), "permissions") {
		t.Fatalf("load error = %v", err)
	}
}

func TestStoreRequiresAbsolutePath(t *testing.T) {
	if _, err := NewStore("data/install-manifest.json"); err == nil {
		t.Fatal("relative manifest path accepted")
	}
}
