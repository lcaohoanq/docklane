package installapply

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"docklane.local/docklane/internal/domain"
)

type recordingStore struct {
	states      []domain.InstallationManifest
	failOnState domain.InstallationState
	failedOnce  bool
}

func (store *recordingStore) Path() string {
	return "/var/lib/docklane/install-manifest.json"
}

func (store *recordingStore) Create(
	manifest domain.InstallationManifest,
) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	store.states = append(store.states, manifest)
	return nil
}

func (store *recordingStore) Save(
	expectedGeneration uint64,
	manifest domain.InstallationManifest,
) error {
	if manifest.Generation != expectedGeneration+1 {
		return errors.New("generation mismatch")
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	if manifest.State == store.failOnState && !store.failedOnce {
		store.failedOnce = true
		return errors.New("injected save failure")
	}
	store.states = append(store.states, manifest)
	return nil
}

func adoptionPlan() domain.InstallationPlan {
	token := strings.Repeat("a", 64)
	resource := domain.InstallationResource{
		ID:          "global-traefik",
		Kind:        domain.ResourceDockerContainer,
		Target:      "traefik",
		Ownership:   domain.ResourceAdopted,
		State:       domain.ResourceVerified,
		Rollback:    domain.RollbackPreserve,
		Fingerprint: strings.Repeat("b", 64),
	}
	return domain.InstallationPlan{
		SchemaVersion: domain.InstallationPlanSchemaVersion,
		Token:         token,
		Ready:         true,
		Complete:      true,
		Status:        domain.DiagnosticPass,
		Target: domain.PreflightTarget{
			BaseDomain:   "docker.home.arpa",
			ProxyNetwork: "proxy",
			ManifestPath: "/var/lib/docklane/install-manifest.json",
		},
		Resources: []domain.InstallationResource{resource},
		Operations: []domain.InstallationOperation{
			{
				ID:       "create-install-manifest",
				Action:   domain.InstallationCreateManifest,
				Kind:     domain.ResourceFile,
				Target:   "/var/lib/docklane/install-manifest.json",
				Mutating: true,
			},
			{
				ID:         "adopt-global-traefik",
				Action:     domain.InstallationAdopt,
				ResourceID: resource.ID,
				Kind:       resource.Kind,
				Target:     resource.Target,
			},
		},
		Blockers: []string{},
		Pending:  []string{},
	}
}

func TestApplyRecordsAdoptionThroughInstalledState(t *testing.T) {
	store := &recordingStore{}
	runner, err := New(store, "dev")
	if err != nil {
		t.Fatal(err)
	}
	runner.now = func() time.Time {
		return time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)
	}
	plan := adoptionPlan()
	installed, err := runner.Apply(context.Background(), plan, plan.Token)
	if err != nil {
		t.Fatal(err)
	}
	if installed.State != domain.InstallationInstalled ||
		installed.Generation != 3 ||
		len(installed.Resources) != 1 {
		t.Fatalf("installed manifest = %#v", installed)
	}
	wantStates := []domain.InstallationState{
		domain.InstallationPlanned,
		domain.InstallationApplying,
		domain.InstallationInstalled,
	}
	if len(store.states) != len(wantStates) {
		t.Fatalf("states = %#v", store.states)
	}
	for index, want := range wantStates {
		if store.states[index].State != want {
			t.Fatalf("state %d = %q, want %q", index, store.states[index].State, want)
		}
	}
	if !store.states[2].UpdatedAt.After(store.states[1].UpdatedAt) {
		t.Fatal("manifest timestamp did not advance")
	}
}

func TestApplyRejectsStaleTokenBeforeCreatingManifest(t *testing.T) {
	store := &recordingStore{}
	runner, err := New(store, "dev")
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Apply(
		context.Background(),
		adoptionPlan(),
		strings.Repeat("c", 64),
	)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("token error = %v", err)
	}
	if len(store.states) != 0 {
		t.Fatalf("manifest was written: %#v", store.states)
	}
}

func TestApplyRejectsManagedOperationsBeforeCreatingManifest(t *testing.T) {
	store := &recordingStore{}
	runner, err := New(store, "dev")
	if err != nil {
		t.Fatal(err)
	}
	plan := adoptionPlan()
	plan.Resources[0].Ownership = domain.ResourceManaged
	plan.Resources[0].State = domain.ResourcePlanned
	plan.Resources[0].Rollback = domain.RollbackRemove
	_, err = runner.Apply(context.Background(), plan, plan.Token)
	if !errors.Is(err, ErrManagedOperationsUnsupported) {
		t.Fatalf("managed operation error = %v", err)
	}
	if len(store.states) != 0 {
		t.Fatalf("manifest was written: %#v", store.states)
	}
}

func TestApplyRejectsDifferentManifestTarget(t *testing.T) {
	store := &recordingStore{}
	runner, err := New(store, "dev")
	if err != nil {
		t.Fatal(err)
	}
	plan := adoptionPlan()
	plan.Target.ManifestPath = "/tmp/different-manifest.json"
	_, err = runner.Apply(context.Background(), plan, plan.Token)
	if err == nil || !strings.Contains(err.Error(), "does not match reviewed target") {
		t.Fatalf("manifest target error = %v", err)
	}
	if len(store.states) != 0 {
		t.Fatalf("manifest was written: %#v", store.states)
	}
}

func TestApplyRecordsFailedStateWhenFinalizationFails(t *testing.T) {
	store := &recordingStore{failOnState: domain.InstallationInstalled}
	runner, err := New(store, "dev")
	if err != nil {
		t.Fatal(err)
	}
	plan := adoptionPlan()
	failed, err := runner.Apply(context.Background(), plan, plan.Token)
	if err == nil || !strings.Contains(err.Error(), "finalize") {
		t.Fatalf("finalization error = %v", err)
	}
	if failed.State != domain.InstallationFailed ||
		failed.Generation != 3 {
		t.Fatalf("failed manifest = %#v", failed)
	}
	if got := store.states[len(store.states)-1].State; got != domain.InstallationFailed {
		t.Fatalf("durable state = %q", got)
	}
}
