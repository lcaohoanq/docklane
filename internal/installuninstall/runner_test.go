package installuninstall

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installmanifest"
	"docklane.local/docklane/internal/uninstallplan"
)

func TestAdoptionOnlyUninstallPreservesResourcesAndResumes(t *testing.T) {
	store, manifest := adoptionFixture(t)
	plan, err := uninstallplan.Build(manifest, store.Path())
	if err != nil {
		t.Fatal(err)
	}
	runner, err := New(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rolledBack, err := runner.Apply(
		context.Background(),
		manifest,
		plan,
		plan.Token,
	)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.State != domain.InstallationRolledBack ||
		rolledBack.RollbackToken != plan.Token {
		t.Fatalf("rolled-back adoption = %#v", rolledBack)
	}
	for _, resource := range rolledBack.Resources {
		if resource.Ownership != domain.ResourceAdopted ||
			resource.State != domain.ResourceVerified ||
			resource.Rollback != domain.RollbackPreserve {
			t.Fatalf("adopted resource changed = %#v", resource)
		}
	}
	resumed, err := runner.Resume(context.Background(), plan.Token)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Generation != rolledBack.Generation {
		t.Fatal("terminal adoption resume changed generation")
	}
}

func TestUninstallTokenMismatchDoesNotCheckpoint(t *testing.T) {
	store, manifest := adoptionFixture(t)
	plan, err := uninstallplan.Build(manifest, store.Path())
	if err != nil {
		t.Fatal(err)
	}
	runner, err := New(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Apply(
		context.Background(),
		manifest,
		plan,
		strings.Repeat("0", 64),
	)
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("token error = %v", err)
	}
	current, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if current.Generation != manifest.Generation ||
		current.State != domain.InstallationInstalled {
		t.Fatalf("manifest changed after token rejection = %#v", current)
	}
}

func TestResumeRequiresOriginalRollbackToken(t *testing.T) {
	store, manifest := adoptionFixture(t)
	rollingBack := manifest
	rollingBack.Generation++
	rollingBack.State = domain.InstallationRollingBack
	rollingBack.RollbackToken = strings.Repeat("a", 64)
	rollingBack.UpdatedAt = manifest.UpdatedAt.Add(time.Second)
	if err := store.Save(manifest.Generation, rollingBack); err != nil {
		t.Fatal(err)
	}
	runner, err := New(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Resume(
		context.Background(),
		strings.Repeat("b", 64),
	)
	if err == nil || !strings.Contains(err.Error(), "resume token") {
		t.Fatalf("resume token error = %v", err)
	}
	current, loadErr := store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if current.Generation != rollingBack.Generation {
		t.Fatal("resume token rejection changed manifest")
	}
}

func adoptionFixture(
	t *testing.T,
) (*installmanifest.Store, domain.InstallationManifest) {
	t.Helper()
	store, err := installmanifest.NewStore(
		filepath.Join(t.TempDir(), "install-manifest.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := installmanifest.New(
		"dev",
		"docker.home.arpa",
		"proxy",
		time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest.State = domain.InstallationInstalled
	manifest.ReviewedToken = strings.Repeat("c", 64)
	manifest.Resources = []domain.InstallationResource{
		{
			ID:        "global-traefik",
			Kind:      domain.ResourceDockerContainer,
			Target:    "traefik",
			Ownership: domain.ResourceAdopted,
			State:     domain.ResourceVerified,
			Rollback:  domain.RollbackPreserve,
		},
		{
			ID:        "proxy-network",
			Kind:      domain.ResourceDockerNetwork,
			Target:    "proxy",
			Ownership: domain.ResourceAdopted,
			State:     domain.ResourceVerified,
			Rollback:  domain.RollbackPreserve,
		},
	}
	if err := store.Create(manifest); err != nil {
		t.Fatal(err)
	}
	return store, manifest
}
