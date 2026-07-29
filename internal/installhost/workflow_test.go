package installhost

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installworkflow"
)

type staticManifestLoader struct {
	manifest domain.InstallationManifest
	err      error
}

func (loader staticManifestLoader) Load() (
	domain.InstallationManifest,
	error,
) {
	return loader.manifest, loader.err
}

func TestHostWorkflowRestoresFilesBeforeServices(t *testing.T) {
	backend := newFakeBackend()
	backend.services["dnsmasq"] = ServiceState{}
	fileObservation := domain.InstallationObservation{
		ExternalID:  "/managed/file",
		Fingerprint: strings.Repeat("a", 64),
		Created:     true,
	}
	loader := staticManifestLoader{
		manifest: domain.InstallationManifest{
			Execution: &domain.InstallationExecution{
				Operations: []domain.InstallationExecutionOperation{{
					ID:          "write-managed-file",
					ResourceID:  "managed-file",
					Stage:       domain.ExecutionFiles,
					State:       domain.OperationApplied,
					Observation: &fileObservation,
				}},
			},
		},
	}
	fileRestored := false
	fileStep := installworkflow.Step{
		ID:         "write-managed-file",
		ResourceID: "managed-file",
		Stage:      domain.ExecutionFiles,
		Inspect: func(
			context.Context,
			*domain.InstallationObservation,
		) (installworkflow.Disposition, domain.InstallationObservation, error) {
			if fileRestored {
				return installworkflow.DispositionRolledBack,
					fileObservation,
					nil
			}
			return installworkflow.DispositionApplied, fileObservation, nil
		},
		Rollback: func(
			context.Context,
			domain.InstallationObservation,
		) error {
			backend.events = append(backend.events, "restore-file")
			fileRestored = true
			return nil
		},
	}
	files, err := NewManagedFileRestorer(loader, []installworkflow.Step{fileStep})
	if err != nil {
		t.Fatal(err)
	}
	resources := []domain.InstallationResource{
		managedHostResourceFixture(
			dnsServiceResourceID,
			domain.ResourceSystemService,
			"dnsmasq",
			false,
		),
		managedHostResourceFixture(
			resolverResourceID,
			domain.ResourceResolverRule,
			"docker.home.arpa",
			true,
		),
	}
	adapter, err := NewWorkflowAdapter(
		backend,
		files,
		validContract(),
		resources,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(adapter.Steps) != 2 {
		t.Fatalf("host steps = %#v", adapter.Steps)
	}
	dnsObservation, err := adapter.Steps[0].Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	resolverObservation, err := adapter.Steps[1].Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	applyCount := len(backend.events)
	if err := adapter.Steps[1].Rollback(
		context.Background(),
		resolverObservation,
	); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Steps[0].Rollback(
		context.Background(),
		dnsObservation,
	); err != nil {
		t.Fatal(err)
	}
	expectedRollback := []string{
		"restore-file",
		"refresh-trust:p11-kit",
		"restart:systemd-resolved",
		"flush:systemd-resolved",
		"refresh-trust:p11-kit",
		"validate-dns",
		"stop:dnsmasq",
	}
	if !reflect.DeepEqual(
		backend.events[applyCount:],
		expectedRollback,
	) {
		t.Fatalf("rollback events = %v", backend.events[applyCount:])
	}
	if backend.services["dnsmasq"].Active ||
		!backend.services["systemd-resolved"].Active {
		t.Fatalf("restored services = %#v", backend.services)
	}
}

func TestHostWorkflowInspectionUsesReviewedPriorState(t *testing.T) {
	backend := newFakeBackend()
	files, err := NewManagedFileRestorer(
		staticManifestLoader{},
		[]installworkflow.Step{},
	)
	if err != nil {
		t.Fatal(err)
	}
	resource := managedHostResourceFixture(
		resolverResourceID,
		domain.ResourceResolverRule,
		"docker.home.arpa",
		true,
	)
	adapter, err := NewWorkflowAdapter(
		backend,
		files,
		validContract(),
		[]domain.InstallationResource{resource},
	)
	if err != nil {
		t.Fatal(err)
	}
	disposition, _, err := adapter.Steps[0].Inspect(
		context.Background(),
		nil,
	)
	if err != nil || disposition != installworkflow.DispositionPending {
		t.Fatalf(
			"active reviewed service inspection = %s, %v",
			disposition,
			err,
		)
	}
}

func TestHostWorkflowRecoversInactiveServiceActivation(t *testing.T) {
	backend := newFakeBackend()
	backend.services["dnsmasq"] = ServiceState{}
	files, err := NewManagedFileRestorer(
		staticManifestLoader{},
		[]installworkflow.Step{},
	)
	if err != nil {
		t.Fatal(err)
	}
	resource := managedHostResourceFixture(
		dnsServiceResourceID,
		domain.ResourceSystemService,
		"dnsmasq",
		false,
	)
	adapter, err := NewWorkflowAdapter(
		backend,
		files,
		validContract(),
		[]domain.InstallationResource{resource},
	)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := adapter.Steps[0].Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	disposition, recovered, err := adapter.Steps[0].Inspect(
		context.Background(),
		nil,
	)
	if err != nil ||
		disposition != installworkflow.DispositionApplied ||
		!reflect.DeepEqual(observation, recovered) {
		t.Fatalf(
			"activation recovery = %s %#v %v",
			disposition,
			recovered,
			err,
		)
	}
}

func TestManagedFileRestorerRefusesUnjournaledState(t *testing.T) {
	files, err := NewManagedFileRestorer(
		staticManifestLoader{
			manifest: domain.InstallationManifest{
				Execution: &domain.InstallationExecution{
					Operations: []domain.InstallationExecutionOperation{{
						ID:         "write-managed-file",
						ResourceID: "managed-file",
						Stage:      domain.ExecutionFiles,
						State:      domain.OperationApplying,
					}},
				},
			},
		},
		[]installworkflow.Step{{
			ID:         "write-managed-file",
			ResourceID: "managed-file",
			Stage:      domain.ExecutionFiles,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	err = files.Rollback(context.Background())
	if err == nil || !strings.Contains(err.Error(), "is applying") {
		t.Fatalf("unjournaled rollback error = %v", err)
	}
}

func TestManagedFileRestorerPropagatesLoadFailure(t *testing.T) {
	files, err := NewManagedFileRestorer(
		staticManifestLoader{err: errors.New("injected load failure")},
		[]installworkflow.Step{},
	)
	if err != nil {
		t.Fatal(err)
	}
	err = files.Rollback(context.Background())
	if err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("load failure = %v", err)
	}
}

func managedHostResourceFixture(
	id string,
	kind domain.ResourceKind,
	target string,
	priorActive bool,
) domain.InstallationResource {
	return domain.InstallationResource{
		ID:        id,
		Kind:      kind,
		Target:    target,
		Ownership: domain.ResourceManaged,
		State:     domain.ResourcePlanned,
		Rollback:  domain.RollbackRestore,
		Fingerprint: serviceStateFingerprint(
			ServiceState{Active: priorActive},
		),
	}
}
