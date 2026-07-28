package installdirs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installmanifest"
	"docklane.local/docklane/internal/installspec"
	"docklane.local/docklane/internal/installworkflow"
)

var errDirectoryCheckpoint = errors.New(
	"injected directory checkpoint failure",
)

type failingStore struct {
	store      *installmanifest.Store
	saveCalls  int
	failOnCall int
}

func (store *failingStore) Save(
	expected uint64,
	manifest domain.InstallationManifest,
) error {
	store.saveCalls++
	if store.saveCalls == store.failOnCall {
		return errDirectoryCheckpoint
	}
	return store.store.Save(expected, manifest)
}

func TestDirectoryWorkflowRecoversPublishedDirectoryWithoutRecreate(t *testing.T) {
	fixture := newDirectoryFixture(t, []string{"data"})
	adapter, err := NewWorkflowAdapter(
		fixture.manifest.InstallationID,
		fixture.stateDirectory,
		fixture.manifest.Resources,
	)
	if err != nil {
		t.Fatal(err)
	}
	failing := &failingStore{store: fixture.store, failOnCall: 3}
	runner, err := installworkflow.New(failing)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(
		context.Background(),
		fixture.manifest,
		adapter.Steps,
	); !errors.Is(err, errDirectoryCheckpoint) {
		t.Fatalf("first run error = %v", err)
	}
	target := fixture.manifest.Resources[0].Target
	markerPath := filepath.Join(target, markerName)
	checkpointTime := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	if err := os.Chtimes(markerPath, checkpointTime, checkpointTime); err != nil {
		t.Fatal(err)
	}
	durable, err := fixture.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewWorkflowAdapter(
		durable.InstallationID,
		fixture.stateDirectory,
		durable.Resources,
	)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := installworkflow.New(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := recovery.Run(
		context.Background(),
		durable,
		restarted.Steps,
	)
	if err != nil {
		t.Fatal(err)
	}
	if installed.State != domain.InstallationInstalled {
		t.Fatalf("installed state = %s", installed.State)
	}
	info, err := os.Stat(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(checkpointTime) {
		t.Fatalf("recovery recreated ownership marker at %s", info.ModTime())
	}
}

func TestDirectoryWorkflowFailureRemovesNestedDirectoriesInReverse(t *testing.T) {
	fixture := newDirectoryFixture(t, []string{
		"traefik",
		filepath.Join("traefik", "dynamic"),
		"failure",
	})
	adapter, err := NewWorkflowAdapter(
		fixture.manifest.InstallationID,
		fixture.stateDirectory,
		fixture.manifest.Resources,
	)
	if err != nil {
		t.Fatal(err)
	}
	adapter.Steps[2].Apply = func(
		context.Context,
	) (domain.InstallationObservation, error) {
		return domain.InstallationObservation{}, errors.New(
			"injected directory create failure",
		)
	}
	runner, err := installworkflow.New(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(
		context.Background(),
		fixture.manifest,
		adapter.Steps,
	)
	if err == nil || !strings.Contains(err.Error(), "injected directory") {
		t.Fatalf("run error = %v", err)
	}
	if result.State != domain.InstallationRolledBack {
		t.Fatalf("result state = %s, execution = %#v", result.State, result.Execution)
	}
	for _, resource := range fixture.manifest.Resources {
		if _, err := os.Lstat(resource.Target); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("directory %s remains: %v", resource.Target, err)
		}
	}
}

func TestDirectoryRollbackRefusesNonEmptyOrChangedOwnership(t *testing.T) {
	fixture := newDirectoryFixture(t, []string{"data"})
	resource := fixture.manifest.Resources[0]
	content, err := markerContent(
		fixture.manifest.InstallationID,
		resource.ID,
		resource.Target,
	)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := applyDirectory(resource, content)
	if err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(resource.Target, "user-data")
	if err := os.WriteFile(userFile, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rollbackDirectory(
		resource,
		content,
		observation,
	); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("non-empty rollback error = %v", err)
	}
	if err := os.Remove(userFile); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(resource.Target, markerName)
	if err := os.WriteFile(markerPath, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rollbackDirectory(
		resource,
		content,
		observation,
	); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed marker rollback error = %v", err)
	}
	if err := os.WriteFile(markerPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rollbackDirectory(resource, content, observation); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryRecoveryCompletesRollbackAfterMarkerRemoval(t *testing.T) {
	fixture := newDirectoryFixture(t, []string{"secrets"})
	resource := fixture.manifest.Resources[0]
	content, err := markerContent(
		fixture.manifest.InstallationID,
		resource.ID,
		resource.Target,
	)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := applyDirectory(resource, content)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(resource.Target, markerName)); err != nil {
		t.Fatal(err)
	}
	disposition, _, err := inspectDirectory(
		resource,
		content,
		&observation,
	)
	if err != nil || disposition != installworkflow.DispositionApplied {
		t.Fatalf("partial rollback disposition = %q, error = %v", disposition, err)
	}
	if err := rollbackDirectory(resource, content, observation); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(resource.Target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partially rolled back directory remains: %v", err)
	}
}

func TestDirectoryApplyRefusesUnmarkedTargetAndRecoversPrivateStaging(t *testing.T) {
	fixture := newDirectoryFixture(t, []string{"pki"})
	resource := fixture.manifest.Resources[0]
	content, err := markerContent(
		fixture.manifest.InstallationID,
		resource.ID,
		resource.Target,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(resource.Target, defaultDirectoryMode); err != nil {
		t.Fatal(err)
	}
	if _, err := applyDirectory(
		resource,
		content,
	); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("unmarked target error = %v", err)
	}
	if err := os.Remove(resource.Target); err != nil {
		t.Fatal(err)
	}
	staging := stagingPath(resource)
	if err := os.Mkdir(staging, defaultDirectoryMode); err != nil {
		t.Fatal(err)
	}
	observation, err := applyDirectory(resource, content)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Fingerprint != fingerprint(content) {
		t.Fatalf("observation = %#v", observation)
	}
	if _, err := os.Lstat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging directory remains: %v", err)
	}
}

type directoryFixture struct {
	stateDirectory string
	manifest       domain.InstallationManifest
	store          *installmanifest.Store
}

func newDirectoryFixture(
	t *testing.T,
	relativeTargets []string,
) directoryFixture {
	t.Helper()
	root := t.TempDir()
	stateDirectory := filepath.Join(root, "state")
	specification, err := installspec.Build(installspec.Config{
		BaseDomain:      "docker.home.arpa",
		ProxyNetwork:    "proxy",
		DockerSocket:    "/var/run/docker.sock",
		StateDirectory:  stateDirectory,
		DataDirectory:   filepath.Join(stateDirectory, "data"),
		DnsmasqConfig:   filepath.Join(root, "dnsmasq", "docklane.conf"),
		ResolverConfig:  filepath.Join(root, "resolved", "docklane.conf"),
		TrustAnchorPath: filepath.Join(root, "trust", "root.crt"),
		TraefikImage:    "traefik:v3.7",
		DocklaneImage:   "docklane:local",
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := installmanifest.New(
		"dev",
		"docker.home.arpa",
		"proxy",
		time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManagedSpecification = &specification
	for index, relative := range relativeTargets {
		manifest.Resources = append(
			manifest.Resources,
			domain.InstallationResource{
				ID:        "directory-" + strings.ReplaceAll(relative, string(filepath.Separator), "-"),
				Kind:      domain.ResourceDirectory,
				Target:    filepath.Join(stateDirectory, relative),
				Ownership: domain.ResourceManaged,
				State:     domain.ResourcePlanned,
				Rollback:  domain.RollbackRemove,
			},
		)
		_ = index
	}
	store, err := installmanifest.NewStore(
		filepath.Join(stateDirectory, "install-manifest.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(manifest); err != nil {
		t.Fatal(err)
	}
	return directoryFixture{
		stateDirectory: stateDirectory,
		manifest:       manifest,
		store:          store,
	}
}
