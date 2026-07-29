package installmanifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
		fmt.Sprintf(`"schemaVersion":%d`, domain.InstallationManifestSchemaVersion),
		fmt.Sprintf(
			`"schemaVersion":%d,"unexpected":true`,
			domain.InstallationManifestSchemaVersion,
		),
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

func TestStoreLoadsLegacyManifestOnlyForUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install-manifest.json")
	legacy, raw := writeLegacyManifestV1(t, path)
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrUpgradeRequired) {
		t.Fatalf("load error = %v", err)
	}
	source, err := store.LoadForUpgrade()
	if err != nil {
		t.Fatal(err)
	}
	if source.Manifest.InstallationID != legacy.InstallationID ||
		source.Manifest.SchemaVersion != 1 ||
		source.Fingerprint == "" {
		t.Fatalf("source = %#v", source)
	}
	sum := sha256.Sum256(raw)
	if source.Fingerprint != hex.EncodeToString(sum[:]) {
		t.Fatalf("fingerprint = %s", source.Fingerprint)
	}
}

func TestStoreAppliesUpgradeWithExactPrivateBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install-manifest.json")
	legacy, raw := writeLegacyManifestV1(t, path)
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.LoadForUpgrade()
	if err != nil {
		t.Fatal(err)
	}
	appliedAt := legacy.UpdatedAt.Add(time.Minute)
	backupPath := store.UpgradeBackupPath(
		legacy.SchemaVersion,
		legacy.Generation,
	)
	upgraded := upgradeCandidate(
		store,
		source,
		appliedAt,
	)
	if err := store.ApplyUpgrade(
		legacy.Generation,
		legacy.SchemaVersion,
		source.Fingerprint,
		upgraded,
	); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != domain.InstallationManifestSchemaVersion ||
		loaded.Generation != legacy.Generation+1 ||
		len(loaded.UpgradeHistory) != 1 {
		t.Fatalf("loaded = %#v", loaded)
	}
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(backup, raw) {
		t.Fatal("upgrade backup does not contain the exact legacy manifest")
	}
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %o", info.Mode().Perm())
	}
}

func TestStoreUpgradeCannotChangeInstallationContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install-manifest.json")
	legacy, _ := writeLegacyManifestV1(t, path)
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.LoadForUpgrade()
	if err != nil {
		t.Fatal(err)
	}
	upgraded := upgradeCandidate(
		store,
		source,
		legacy.UpdatedAt.Add(time.Minute),
	)
	upgraded.Settings.ProxyNetwork = "different"
	err = store.ApplyUpgrade(
		legacy.Generation,
		legacy.SchemaVersion,
		source.Fingerprint,
		upgraded,
	)
	if err == nil || !strings.Contains(err.Error(), "may change only") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(
		store.UpgradeBackupPath(legacy.SchemaVersion, legacy.Generation),
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected backup after rejected migration: %v", err)
	}
}

func TestStoreUpgradeResumesAfterExactBackupWasAlreadyPublished(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install-manifest.json")
	legacy, raw := writeLegacyManifestV1(t, path)
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.LoadForUpgrade()
	if err != nil {
		t.Fatal(err)
	}
	backupPath := store.UpgradeBackupPath(
		legacy.SchemaVersion,
		legacy.Generation,
	)
	if err := os.WriteFile(backupPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	upgraded := upgradeCandidate(
		store,
		source,
		legacy.UpdatedAt.Add(time.Minute),
	)
	if err := store.ApplyUpgrade(
		legacy.Generation,
		legacy.SchemaVersion,
		source.Fingerprint,
		upgraded,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
}

func writeLegacyManifestV1(
	t *testing.T,
	path string,
) (domain.InstallationManifest, []byte) {
	t.Helper()
	manifest := testManifest(t)
	manifest.SchemaVersion = 1
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return manifest, raw
}

func upgradeCandidate(
	store *Store,
	source UpgradeSource,
	appliedAt time.Time,
) domain.InstallationManifest {
	upgraded := source.Manifest
	upgraded.SchemaVersion = domain.InstallationManifestSchemaVersion
	upgraded.Generation++
	upgraded.UpdatedAt = appliedAt
	upgraded.UpgradeHistory = append(
		append(
			[]domain.InstallationUpgradeRecord(nil),
			source.Manifest.UpgradeHistory...,
		),
		domain.InstallationUpgradeRecord{
			FromSchemaVersion: source.Manifest.SchemaVersion,
			ToSchemaVersion:   domain.InstallationManifestSchemaVersion,
			AppliedAt:         appliedAt,
			SourceBackup: domain.ResourceBackup{
				Path: store.UpgradeBackupPath(
					source.Manifest.SchemaVersion,
					source.Manifest.Generation,
				),
				Fingerprint: source.Fingerprint,
			},
		},
	)
	return upgraded
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
