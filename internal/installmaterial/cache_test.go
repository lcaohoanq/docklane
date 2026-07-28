package installmaterial

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installartifacts"
	"docklane.local/docklane/internal/installfiles"
	"docklane.local/docklane/internal/installmanifest"
	"docklane.local/docklane/internal/installspec"
	"docklane.local/docklane/internal/installworkflow"
)

var errMaterialCheckpoint = errors.New("injected material checkpoint failure")

type failingStore struct {
	store      *installmanifest.Store
	saveCalls  int
	failOnCall int
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("randomness must not be consumed during reload")
}

func (store *failingStore) Save(
	expected uint64,
	manifest domain.InstallationManifest,
) error {
	store.saveCalls++
	if store.saveCalls == store.failOnCall {
		return errMaterialCheckpoint
	}
	return store.store.Save(expected, manifest)
}

func TestPrepareReloadWorkflowAndTerminalClear(t *testing.T) {
	fixture := newFixture(t)
	coordinator, err := New(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	prepared, files, err := coordinator.Prepare(
		context.Background(),
		fixture.manifest,
		fixture.materialize,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ClearFiles(files)
	if prepared.MaterialCache == nil ||
		prepared.MaterialCache.State != domain.MaterialCacheReady ||
		prepared.Generation != 2 {
		t.Fatalf("prepared manifest = %#v", prepared)
	}
	for _, entry := range prepared.MaterialCache.Entries {
		info, err := os.Stat(entry.CachePath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o", entry.ArtifactID, info.Mode().Perm())
		}
	}
	descriptor, err := os.ReadFile(filepath.Join(
		prepared.MaterialCache.Directory,
		cacheDescriptorName,
	))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(descriptor, []byte("private cached value")) {
		t.Fatal("cache descriptor exposed private material")
	}
	materializeCalls := 0
	reloadedManifest, reloaded, err := coordinator.Prepare(
		context.Background(),
		prepared,
		func() ([]installfiles.File, error) {
			materializeCalls++
			return nil, errors.New("materializer should not run")
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ClearFiles(reloaded)
	if materializeCalls != 0 ||
		reloadedManifest.Generation != prepared.Generation ||
		!sameFiles(files, reloaded) {
		t.Fatalf(
			"reload calls = %d, manifest = %#v, files = %#v",
			materializeCalls,
			reloadedManifest,
			reloaded,
		)
	}
	backupRoot := filepath.Join(fixture.root, "workflow-backups")
	mkdir(t, backupRoot)
	for _, file := range reloaded {
		mkdir(t, filepath.Dir(file.Target))
	}
	changedFiles := cloneFiles(reloaded)
	defer ClearFiles(changedFiles)
	changedFiles[0].Content = []byte("changed cached intent\n")
	changedAdapter, err := installfiles.NewWorkflowAdapter(
		changedFiles,
		reloadedManifest.Resources,
		backupRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	changedRunner, err := installworkflow.New(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := changedRunner.Run(
		context.Background(),
		reloadedManifest,
		changedAdapter.Steps,
	); err == nil || !strings.Contains(err.Error(), "cached material") {
		t.Fatalf("changed cached intent error = %v", err)
	}
	changedAdapter.Clear()
	adapter, err := installfiles.NewWorkflowAdapter(
		reloaded,
		reloadedManifest.Resources,
		backupRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Clear()
	runner, err := installworkflow.New(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := runner.Run(
		context.Background(),
		reloadedManifest,
		adapter.Steps,
	)
	if err != nil {
		t.Fatal(err)
	}
	if installed.State != domain.InstallationInstalled {
		t.Fatalf("installed state = %s", installed.State)
	}
	cleared, err := coordinator.Clear(context.Background(), installed)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.MaterialCache.State != domain.MaterialCacheCleared {
		t.Fatalf("cleared cache = %#v", cleared.MaterialCache)
	}
	if _, err := os.Lstat(
		cleared.MaterialCache.Directory,
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cache remains after clear: %v", err)
	}
}

func TestPrepareArtifactsRehydratesWithoutRegeneratingSecrets(t *testing.T) {
	fixture := newFixture(t)
	coordinator, err := New(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	prepared, first, err := coordinator.PrepareArtifacts(
		context.Background(),
		fixture.manifest,
		time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC),
		bytes.NewReader(make([]byte, 64)),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ClearFiles(first)
	reloadedManifest, second, err := coordinator.PrepareArtifacts(
		context.Background(),
		prepared,
		time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
		errorReader{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ClearFiles(second)
	if reloadedManifest.Generation != prepared.Generation ||
		!sameFiles(first, second) {
		t.Fatalf(
			"rehydrated manifest = %#v, first = %#v, second = %#v",
			reloadedManifest,
			first,
			second,
		)
	}
}

func TestPrepareRecoversPublishedCacheAfterManifestSaveFailure(t *testing.T) {
	fixture := newFixture(t)
	failing := &failingStore{store: fixture.store, failOnCall: 1}
	coordinator, err := New(failing)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := coordinator.Prepare(
		context.Background(),
		fixture.manifest,
		fixture.materialize,
	); !errors.Is(err, errMaterialCheckpoint) {
		t.Fatalf("prepare error = %v", err)
	}
	if _, err := os.Stat(cacheDirectory(fixture.manifest)); err != nil {
		t.Fatalf("published cache missing: %v", err)
	}
	durable, err := fixture.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if durable.MaterialCache != nil {
		t.Fatalf("failed checkpoint became durable: %#v", durable.MaterialCache)
	}
	recovery, err := New(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	prepared, files, err := recovery.Prepare(
		context.Background(),
		durable,
		func() ([]installfiles.File, error) {
			calls++
			return nil, errors.New("must reuse published cache")
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ClearFiles(files)
	if calls != 0 || prepared.MaterialCache == nil {
		t.Fatalf("materializer calls = %d, cache = %#v", calls, prepared.MaterialCache)
	}
}

func TestPrepareReplacesInterruptedPrivateStagingDirectory(t *testing.T) {
	fixture := newFixture(t)
	cacheRoot := filepath.Join(
		fixture.manifest.ManagedSpecification.Paths.StateDirectory,
		".material-cache",
	)
	mkdir(t, cacheRoot)
	staging := filepath.Join(
		cacheRoot,
		"."+fixture.manifest.InstallationID+".preparing",
	)
	mkdir(t, staging)
	if err := os.WriteFile(
		filepath.Join(staging, "partial"),
		[]byte("partial private material"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	coordinator, err := New(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	prepared, files, err := coordinator.Prepare(
		context.Background(),
		fixture.manifest,
		fixture.materialize,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ClearFiles(files)
	if prepared.MaterialCache == nil {
		t.Fatal("cache was not prepared")
	}
	if _, err := os.Lstat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted staging remains: %v", err)
	}
}

func TestLoadRefusesCorruptionPermissionsAndSymlinks(t *testing.T) {
	tests := []struct {
		name   string
		change func(t *testing.T, entry domain.InstallationMaterialCacheEntry)
	}{
		{
			name: "content corruption",
			change: func(t *testing.T, entry domain.InstallationMaterialCacheEntry) {
				t.Helper()
				if err := os.WriteFile(
					entry.CachePath,
					[]byte("tampered"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "broad permissions",
			change: func(t *testing.T, entry domain.InstallationMaterialCacheEntry) {
				t.Helper()
				if err := os.Chmod(entry.CachePath, 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symbolic link",
			change: func(t *testing.T, entry domain.InstallationMaterialCacheEntry) {
				t.Helper()
				outside := filepath.Join(
					filepath.Dir(filepath.Dir(entry.CachePath)),
					"outside",
				)
				if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(entry.CachePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, entry.CachePath); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t)
			coordinator, err := New(fixture.store)
			if err != nil {
				t.Fatal(err)
			}
			prepared, files, err := coordinator.Prepare(
				context.Background(),
				fixture.manifest,
				fixture.materialize,
			)
			if err != nil {
				t.Fatal(err)
			}
			ClearFiles(files)
			entry := prepared.MaterialCache.Entries[len(
				prepared.MaterialCache.Entries,
			)-1]
			test.change(t, entry)
			if _, err := coordinator.Load(prepared); err == nil {
				t.Fatal("changed cache was accepted")
			}
		})
	}
}

func TestClearRecoversAfterDirectoryRemovalBeforeFinalCheckpoint(t *testing.T) {
	fixture := newFixture(t)
	coordinator, err := New(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	prepared, files, err := coordinator.Prepare(
		context.Background(),
		fixture.manifest,
		fixture.materialize,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ClearFiles(files)
	terminal := terminalManifest(t, fixture.store, prepared, files)
	failing := &failingStore{store: fixture.store, failOnCall: 2}
	cleanup, err := New(failing)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cleanup.Clear(
		context.Background(),
		terminal,
	); !errors.Is(err, errMaterialCheckpoint) {
		t.Fatalf("clear error = %v", err)
	}
	durable, err := fixture.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if durable.MaterialCache.State != domain.MaterialCacheClearing {
		t.Fatalf("durable cache state = %s", durable.MaterialCache.State)
	}
	if _, err := os.Lstat(
		durable.MaterialCache.Directory,
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed cache reappeared: %v", err)
	}
	recovery, err := New(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := recovery.Clear(context.Background(), durable)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.MaterialCache.State != domain.MaterialCacheCleared {
		t.Fatalf("recovered cache state = %s", cleared.MaterialCache.State)
	}
}

func TestClearRefusesUnexpectedCacheEntry(t *testing.T) {
	fixture := newFixture(t)
	coordinator, err := New(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	prepared, files, err := coordinator.Prepare(
		context.Background(),
		fixture.manifest,
		fixture.materialize,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ClearFiles(files)
	terminal := terminalManifest(t, fixture.store, prepared, files)
	extra := filepath.Join(terminal.MaterialCache.Directory, "unexpected")
	if err := os.WriteFile(extra, []byte("do not delete"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Clear(
		context.Background(),
		terminal,
	); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("clear error = %v", err)
	}
	if content, err := os.ReadFile(extra); err != nil ||
		string(content) != "do not delete" {
		t.Fatalf("unexpected entry changed: %q, %v", content, err)
	}
}

type fixture struct {
	root        string
	manifest    domain.InstallationManifest
	store       *installmanifest.Store
	files       []installfiles.File
	materialize Materialize
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	stateDirectory := filepath.Join(root, "state")
	specification, err := installspec.Build(installspec.Config{
		BaseDomain:      "docker.home.arpa",
		ProxyNetwork:    "proxy",
		DockerSocket:    "/var/run/docker.sock",
		StateDirectory:  stateDirectory,
		DataDirectory:   filepath.Join(stateDirectory, "data"),
		DnsmasqConfig:   filepath.Join(root, "targets", "dnsmasq.conf"),
		ResolverConfig:  filepath.Join(root, "targets", "resolver.conf"),
		TrustAnchorPath: filepath.Join(root, "targets", "root.crt"),
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
	selected := []domain.InstallationArtifact{}
	for _, artifact := range allArtifacts {
		if artifact.ID == "dnsmasq-domain" ||
			artifact.ID == "traefik-dashboard-password" {
			selected = append(selected, artifact)
		}
	}
	files := make([]installfiles.File, 0, len(selected))
	resources := make([]domain.InstallationResource, 0, len(selected))
	for _, artifact := range selected {
		content := []byte(artifact.Content)
		if artifact.ID == "traefik-dashboard-password" {
			content = []byte("private cached value\n")
		}
		files = append(files, installfiles.File{
			ID:        artifact.ID,
			Target:    artifact.Target,
			Mode:      os.FileMode(artifact.Mode),
			Content:   content,
			Sensitive: artifact.Sensitive,
		})
		resources = append(resources, domain.InstallationResource{
			ID:        artifact.ID,
			Kind:      domain.ResourceFile,
			Target:    artifact.Target,
			Ownership: domain.ResourceManaged,
			State:     domain.ResourcePlanned,
			Rollback:  domain.RollbackRemove,
		})
	}
	manifest, err := installmanifest.New(
		"dev",
		"docker.home.arpa",
		"proxy",
		time.Date(2026, 7, 28, 11, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManagedSpecification = &specification
	manifest.ManagedArtifacts = selected
	manifest.Resources = resources
	store, err := installmanifest.NewStore(
		filepath.Join(stateDirectory, "install-manifest.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(manifest); err != nil {
		t.Fatal(err)
	}
	materialize := func() ([]installfiles.File, error) {
		cloned := make([]installfiles.File, len(files))
		for index, file := range files {
			cloned[index] = file
			cloned[index].Content = bytes.Clone(file.Content)
		}
		return cloned, nil
	}
	return fixture{
		root:        root,
		manifest:    manifest,
		store:       store,
		files:       files,
		materialize: materialize,
	}
}

func terminalManifest(
	t *testing.T,
	store *installmanifest.Store,
	prepared domain.InstallationManifest,
	files []installfiles.File,
) domain.InstallationManifest {
	t.Helper()
	backupRoot := filepath.Join(
		filepath.Dir(prepared.MaterialCache.Directory),
		"workflow-backups",
	)
	mkdir(t, backupRoot)
	for _, file := range files {
		mkdir(t, filepath.Dir(file.Target))
	}
	adapter, err := installfiles.NewWorkflowAdapter(
		files,
		prepared.Resources,
		backupRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Clear()
	runner, err := installworkflow.New(store)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := runner.Run(
		context.Background(),
		prepared,
		adapter.Steps,
	)
	if err != nil {
		t.Fatal(err)
	}
	return terminal
}

func sameFiles(left []installfiles.File, right []installfiles.File) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID ||
			left[index].Target != right[index].Target ||
			left[index].Mode != right[index].Mode ||
			left[index].Sensitive != right[index].Sensitive ||
			!bytes.Equal(left[index].Content, right[index].Content) {
			return false
		}
	}
	return true
}

func cloneFiles(files []installfiles.File) []installfiles.File {
	cloned := make([]installfiles.File, len(files))
	for index, file := range files {
		cloned[index] = file
		cloned[index].Content = bytes.Clone(file.Content)
	}
	return cloned
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}
