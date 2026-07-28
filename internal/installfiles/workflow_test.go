package installfiles

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
	"docklane.local/docklane/internal/installmanifest"
	"docklane.local/docklane/internal/installspec"
	"docklane.local/docklane/internal/installworkflow"
)

var errFileCheckpoint = errors.New("injected file checkpoint failure")

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
		return errFileCheckpoint
	}
	return store.store.Save(expected, manifest)
}

func TestFileWorkflowRecoversAppliedCreateWithoutRewriting(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "targets", "config.yml")
	backupRoot := filepath.Join(root, "backups")
	mkdir(t, filepath.Dir(target))
	mkdir(t, backupRoot)
	file := File{
		ID:      "managed-config",
		Target:  target,
		Mode:    0o640,
		Content: []byte("managed\n"),
	}
	resource := fileResource(file, domain.RollbackRemove)
	manifest, store := createFileManifest(t, root, []domain.InstallationResource{
		resource,
	})
	adapter, err := NewWorkflowAdapter(
		[]File{file},
		manifest.Resources,
		backupRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Clear()
	failing := &failingStore{store: store, failOnCall: 3}
	runner, err := installworkflow.New(failing)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(
		context.Background(),
		manifest,
		adapter.Steps,
	); !errors.Is(err, errFileCheckpoint) {
		t.Fatalf("first run error = %v", err)
	}
	assertFile(t, target, "managed\n", 0o640)
	checkpointTime := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	if err := os.Chtimes(target, checkpointTime, checkpointTime); err != nil {
		t.Fatal(err)
	}
	durable, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if durable.Execution.Operations[0].State != domain.OperationApplying {
		t.Fatalf("durable operation = %#v", durable.Execution.Operations[0])
	}
	changedFile := file
	changedFile.Content = []byte("different generation\n")
	changed, err := NewWorkflowAdapter(
		[]File{changedFile},
		durable.Resources,
		backupRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	changedRunner, err := installworkflow.New(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := changedRunner.Run(
		context.Background(),
		durable,
		changed.Steps,
	); err == nil || !strings.Contains(err.Error(), "does not match workflow") {
		t.Fatalf("changed material recovery error = %v", err)
	}
	changed.Clear()
	assertFile(t, target, "managed\n", 0o640)
	restarted, err := NewWorkflowAdapter(
		[]File{file},
		durable.Resources,
		backupRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Clear()
	recoveryRunner, err := installworkflow.New(store)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := recoveryRunner.Run(
		context.Background(),
		durable,
		restarted.Steps,
	)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != domain.InstallationInstalled ||
		recovered.Resources[0].Fingerprint != fingerprint(file.Content) {
		t.Fatalf("recovered manifest = %#v", recovered)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(checkpointTime) {
		t.Fatalf("recovery rewrote target; modtime = %s", info.ModTime())
	}
}

func TestFileWorkflowFailureRestoresEarlierFiles(t *testing.T) {
	root := t.TempDir()
	backupRoot := filepath.Join(root, "backups")
	mkdir(t, backupRoot)
	files := []File{
		{
			ID:      "first-config",
			Target:  filepath.Join(root, "first.conf"),
			Mode:    0o644,
			Content: []byte("first managed\n"),
		},
		{
			ID:      "second-config",
			Target:  filepath.Join(root, "second.conf"),
			Mode:    0o600,
			Content: []byte("second managed\n"),
		},
	}
	if err := os.WriteFile(files[0].Target, []byte("first original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(files[1].Target, []byte("second original\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	resources := []domain.InstallationResource{
		fileResource(files[0], domain.RollbackRestore),
		fileResource(files[1], domain.RollbackRestore),
	}
	manifest, store := createFileManifest(t, root, resources)
	adapter, err := NewWorkflowAdapter(files, manifest.Resources, backupRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Clear()
	adapter.Steps[1].Apply = func(
		context.Context,
	) (domain.InstallationObservation, error) {
		return domain.InstallationObservation{}, errors.New("second write failed")
	}
	runner, err := installworkflow.New(store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(
		context.Background(),
		manifest,
		adapter.Steps,
	)
	if err == nil || !strings.Contains(err.Error(), "second write failed") {
		t.Fatalf("run error = %v", err)
	}
	if result.State != domain.InstallationRolledBack {
		t.Fatalf(
			"result state = %s, error = %v, execution = %#v",
			result.State,
			err,
			result.Execution,
		)
	}
	assertFile(t, files[0].Target, "first original\n", 0o600)
	assertFile(t, files[1].Target, "second original\n", 0o640)
	if entries, err := os.ReadDir(backupRoot); err != nil || len(entries) != 0 {
		t.Fatalf("backup root entries = %v, error = %v", entries, err)
	}
}

func TestFileWorkflowRefusesTargetAndBackupDrift(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "dynamic.yml")
	backupRoot := filepath.Join(root, "backups")
	mkdir(t, backupRoot)
	if err := os.WriteFile(target, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := File{
		ID:      "dynamic-config",
		Target:  target,
		Mode:    0o644,
		Content: []byte("managed\n"),
	}
	resource := fileResource(file, domain.RollbackRestore)
	adapter, err := NewWorkflowAdapter(
		[]File{file},
		[]domain.InstallationResource{resource},
		backupRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Clear()
	observation, err := adapter.Steps[0].Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("external\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	disposition, _, err := adapter.Steps[0].Inspect(
		context.Background(),
		&observation,
	)
	if err != nil || disposition != installworkflow.DispositionConflict {
		t.Fatalf("target drift disposition = %q, error = %v", disposition, err)
	}
	if err := adapter.Steps[0].Rollback(
		context.Background(),
		observation,
	); err == nil {
		t.Fatal("rollback accepted target drift")
	}
	if err := os.WriteFile(target, file.Content, file.Mode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		observation.Backup.Path,
		[]byte("tampered\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	disposition, _, err = adapter.Steps[0].Inspect(
		context.Background(),
		&observation,
	)
	if err != nil || disposition != installworkflow.DispositionConflict {
		t.Fatalf("backup drift disposition = %q, error = %v", disposition, err)
	}
	if err := adapter.Steps[0].Rollback(
		context.Background(),
		observation,
	); err == nil {
		t.Fatal("rollback accepted backup drift")
	}
}

func TestFileWorkflowRetriesOnlyAfterOriginalStateIsProven(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "dnsmasq.conf")
	backupRoot := filepath.Join(root, "backups")
	mkdir(t, backupRoot)
	if err := os.WriteFile(target, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := File{
		ID:      "dnsmasq-domain",
		Target:  target,
		Mode:    0o644,
		Content: []byte("managed\n"),
	}
	resource := fileResource(file, domain.RollbackRestore)
	adapter, err := NewWorkflowAdapter(
		[]File{file},
		[]domain.InstallationResource{resource},
		backupRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Clear()
	observation, err := adapter.Steps[0].Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := restoreBackup(target, *observation.Backup); err != nil {
		t.Fatal(err)
	}
	disposition, _, err := adapter.Steps[0].Inspect(
		context.Background(),
		nil,
	)
	if err != nil || disposition != installworkflow.DispositionPending {
		t.Fatalf("interrupted state = %q, error = %v", disposition, err)
	}
	retried, err := adapter.Steps[0].Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if retried.Backup == nil ||
		retried.Backup.Fingerprint != fingerprint([]byte("original\n")) {
		t.Fatalf("retried observation = %#v", retried)
	}
	assertFile(t, target, "managed\n", 0o644)
}

func TestFileWorkflowClearErasesClonedSensitiveMaterial(t *testing.T) {
	root := t.TempDir()
	backupRoot := filepath.Join(root, "backups")
	mkdir(t, backupRoot)
	content := []byte("secret material")
	file := File{
		ID:        "private-key",
		Target:    filepath.Join(root, "private.key"),
		Mode:      0o600,
		Content:   content,
		Sensitive: true,
	}
	adapter, err := NewWorkflowAdapter(
		[]File{file},
		[]domain.InstallationResource{
			fileResource(file, domain.RollbackRemove),
		},
		backupRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	cloned := adapter.files[0].Content
	if &cloned[0] == &content[0] {
		t.Fatal("adapter retained caller-owned sensitive buffer")
	}
	adapter.Clear()
	if !bytes.Equal(cloned, make([]byte, len(cloned))) {
		t.Fatalf("sensitive clone was not cleared: %x", cloned)
	}
	if string(content) != "secret material" {
		t.Fatal("clearing adapter changed caller buffer")
	}
}

func fileResource(
	file File,
	rollback domain.RollbackStrategy,
) domain.InstallationResource {
	return domain.InstallationResource{
		ID:        file.ID,
		Kind:      domain.ResourceFile,
		Target:    file.Target,
		Ownership: domain.ResourceManaged,
		State:     domain.ResourcePlanned,
		Rollback:  rollback,
	}
}

func createFileManifest(
	t *testing.T,
	root string,
	resources []domain.InstallationResource,
) (domain.InstallationManifest, *installmanifest.Store) {
	t.Helper()
	manifest, err := installmanifest.New(
		"dev",
		"docker.home.arpa",
		"proxy",
		time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	specification, err := installspec.Build(installspec.Config{
		BaseDomain:      "docker.home.arpa",
		ProxyNetwork:    "proxy",
		DockerSocket:    "/var/run/docker.sock",
		StateDirectory:  "/var/lib/docklane",
		DataDirectory:   "/var/lib/docklane/data",
		DnsmasqConfig:   "/etc/dnsmasq.d/docklane.conf",
		TrustAnchorPath: "/etc/ca-certificates/trust-source/anchors/docklane-local-root-ca.crt",
		TraefikImage:    "traefik:v3.7",
		DocklaneImage:   "docklane:local",
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManagedSpecification = &specification
	manifest.Resources = resources
	path := filepath.Join(root, "install-manifest.json")
	store, err := installmanifest.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(manifest); err != nil {
		t.Fatal(err)
	}
	return manifest, store
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}
