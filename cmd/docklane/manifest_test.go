package main

import (
	"errors"
	"path/filepath"
	"testing"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installmanifest"
)

func TestManifestInitCreatesOnlyPlannedOwnershipState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install-manifest.json")
	if err := manifestInit([]string{
		"--path", path,
		"--base-domain", "docker.home.arpa",
		"--proxy-network", "proxy",
	}); err != nil {
		t.Fatal(err)
	}
	store, err := installmanifest.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	installation, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if installation.SchemaVersion != domain.InstallationManifestSchemaVersion ||
		installation.State != domain.InstallationPlanned ||
		len(installation.Resources) != 0 {
		t.Fatalf("installation = %#v", installation)
	}
	if err := manifestValidate([]string{"--path", path}); err != nil {
		t.Fatal(err)
	}
	if err := manifestInit([]string{"--path", path}); !errors.Is(
		err,
		installmanifest.ErrAlreadyExists,
	) {
		t.Fatalf("duplicate init error = %v", err)
	}
}

func TestDefaultManifestPathUsesEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	t.Setenv("DOCKLANE_MANIFEST", path)
	if defaultManifestPath() != path {
		t.Fatalf("default path = %q", defaultManifestPath())
	}
}
