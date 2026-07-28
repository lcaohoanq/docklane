package installcompose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installartifacts"
	"docklane.local/docklane/internal/installdirs"
	"docklane.local/docklane/internal/installfiles"
	"docklane.local/docklane/internal/installmanifest"
	"docklane.local/docklane/internal/installmaterial"
	"docklane.local/docklane/internal/installspec"
	"docklane.local/docklane/internal/installworkflow"
)

func TestComposedDirectoryAndCachedFileRollbackRemovesChildrenFirst(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	specification, err := installspec.Build(installspec.Config{
		BaseDomain:      "docker.home.arpa",
		ProxyNetwork:    "proxy",
		DockerSocket:    "/var/run/docker.sock",
		StateDirectory:  state,
		DataDirectory:   filepath.Join(state, "data"),
		DnsmasqConfig:   filepath.Join(root, "dnsmasq", "docklane.conf"),
		ResolverConfig:  filepath.Join(root, "resolved", "docklane.conf"),
		TrustAnchorPath: filepath.Join(root, "trust", "root.crt"),
		TraefikImage:    "traefik:v3.7",
		DocklaneImage:   "docklane:local",
	})
	if err != nil {
		t.Fatal(err)
	}
	allArtifacts, err := installartifacts.Build(specification)
	if err != nil {
		t.Fatal(err)
	}
	var artifact domain.InstallationArtifact
	for _, candidate := range allArtifacts {
		if candidate.ID == "traefik-dynamic-config" {
			artifact = candidate
		}
	}
	resources := []domain.InstallationResource{
		managedResource(
			"traefik-directory",
			domain.ResourceDirectory,
			specification.Paths.TraefikDirectory,
		),
		managedResource(
			"dynamic-directory",
			domain.ResourceDirectory,
			filepath.Dir(specification.Paths.TraefikDynamicConfig),
		),
		managedResource(
			"backup-directory",
			domain.ResourceDirectory,
			specification.Paths.BackupDirectory,
		),
		managedResource(
			artifact.ID,
			domain.ResourceFile,
			artifact.Target,
		),
		{
			ID:        "final-verification",
			Kind:      domain.ResourceResolverRule,
			Target:    specification.BaseDomain,
			Ownership: domain.ResourceManaged,
			State:     domain.ResourcePlanned,
			Rollback:  domain.RollbackRestore,
		},
	}
	manifest, err := installmanifest.New(
		"dev",
		specification.BaseDomain,
		specification.ProxyNetwork,
		time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManagedSpecification = &specification
	manifest.ManagedArtifacts = []domain.InstallationArtifact{artifact}
	manifest.Resources = resources
	store, err := installmanifest.NewStore(
		filepath.Join(state, "install-manifest.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(manifest); err != nil {
		t.Fatal(err)
	}
	materials, err := installmaterial.New(store)
	if err != nil {
		t.Fatal(err)
	}
	prepared, files, err := materials.PrepareArtifacts(
		context.Background(),
		manifest,
		time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer installmaterial.ClearFiles(files)
	directories, err := installdirs.NewWorkflowAdapter(
		prepared.InstallationID,
		specification.Paths.StateDirectory,
		prepared.Resources,
	)
	if err != nil {
		t.Fatal(err)
	}
	fileAdapter, err := installfiles.NewWorkflowAdapter(
		files,
		prepared.Resources,
		specification.Paths.BackupDirectory,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer fileAdapter.Clear()
	verify := step(
		"final-verification",
		specification.BaseDomain,
		domain.ExecutionVerify,
	)
	verify.Apply = func(
		context.Context,
	) (domain.InstallationObservation, error) {
		return domain.InstallationObservation{}, errors.New(
			"injected final verification failure",
		)
	}
	steps, err := Build(prepared.Resources, Groups{
		Directories: directories.Steps,
		Files:       fileAdapter.Steps,
		Verify:      []installworkflow.Step{verify},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := installworkflow.New(store)
	if err != nil {
		t.Fatal(err)
	}
	rolledBack, err := runner.Run(
		context.Background(),
		prepared,
		steps,
	)
	if err == nil || !strings.Contains(err.Error(), "final verification") {
		t.Fatalf("run error = %v", err)
	}
	if rolledBack.State != domain.InstallationRolledBack {
		t.Fatalf("rolled-back manifest = %#v", rolledBack)
	}
	if _, err := os.Lstat(artifact.Target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed file remains: %v", err)
	}
	for _, resource := range resources[:3] {
		if _, err := os.Lstat(resource.Target); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("managed directory %s remains: %v", resource.Target, err)
		}
	}
	cleared, err := materials.Clear(context.Background(), rolledBack)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.MaterialCache.State != domain.MaterialCacheCleared {
		t.Fatalf("material cache state = %s", cleared.MaterialCache.State)
	}
}

func managedResource(
	id string,
	kind domain.ResourceKind,
	target string,
) domain.InstallationResource {
	return domain.InstallationResource{
		ID:        id,
		Kind:      kind,
		Target:    target,
		Ownership: domain.ResourceManaged,
		State:     domain.ResourcePlanned,
		Rollback:  domain.RollbackRemove,
	}
}
