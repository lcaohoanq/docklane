package installdocker

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installmanifest"
	"docklane.local/docklane/internal/installworkflow"
)

func TestWorkflowAdapterUsesResourceDependencyOrder(t *testing.T) {
	specification := managedSpecification(t)
	resources := managedDockerResources(specification)
	adapter, err := NewWorkflowAdapter(
		newFakeLifecycle(),
		specification,
		"installation-test",
		resources,
	)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, step := range adapter.Steps {
		ids = append(ids, step.ResourceID)
		if step.Stage != domain.ExecutionDocker ||
			step.IntentFingerprint == "" {
			t.Fatalf("incomplete Docker step = %#v", step)
		}
	}
	expected := []string{
		proxyNetworkResourceID,
		controlNetworkResourceID,
		probeVolumeResourceID,
		probeResourceID,
		controllerResourceID,
		gatewayResourceID,
	}
	if !reflect.DeepEqual(ids, expected) {
		t.Fatalf("step order = %v", ids)
	}
}

func TestWorkflowFailureRollsBackDockerResourcesInReverseOrder(t *testing.T) {
	specification := managedSpecification(t)
	resources := append(
		managedDockerResources(specification),
		domain.InstallationResource{
			ID:        "verify-runtime",
			Kind:      domain.ResourceResolverRule,
			Target:    specification.BaseDomain,
			Ownership: domain.ResourceManaged,
			State:     domain.ResourcePlanned,
			Rollback:  domain.RollbackRestore,
		},
	)
	backend := newFakeLifecycle()
	adapter, err := NewWorkflowAdapter(
		backend,
		specification,
		"installation-test",
		resources,
	)
	if err != nil {
		t.Fatal(err)
	}
	steps := append(
		adapter.Steps,
		failingVerificationStep(resources[len(resources)-1]),
	)
	manifest, err := installmanifest.New(
		"dev",
		specification.BaseDomain,
		specification.ProxyNetwork,
		time.Date(2026, 7, 29, 1, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Resources = resources
	manifest.ManagedSpecification = &specification
	store, err := installmanifest.NewStore(
		filepath.Join(t.TempDir(), "install-manifest.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(manifest); err != nil {
		t.Fatal(err)
	}
	runner, err := installworkflow.New(store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), manifest, steps)
	if err == nil || !strings.Contains(err.Error(), "injected verification") {
		t.Fatalf("workflow error = %v", err)
	}
	if result.State != domain.InstallationRolledBack {
		t.Fatalf("result state = %s", result.State)
	}
	expected := []string{
		"create-network:proxy",
		"create-network:control",
		"create-volume:probe-volume",
		"create-container:probe",
		"start-container:probe",
		"create-container:controller",
		"start-container:controller",
		"create-container:gateway",
		"start-container:gateway",
		"remove-container:gateway",
		"remove-container:controller",
		"remove-container:probe",
		"remove-volume:probe-volume",
		"remove-network:control",
		"remove-network:proxy",
	}
	if !reflect.DeepEqual(backend.mutations, expected) {
		t.Fatalf("mutations = %v", backend.mutations)
	}
}

func TestWorkflowRecoversCreatedContainerAfterLostCheckpoint(t *testing.T) {
	specification := managedSpecification(t)
	resource := managedDockerResources(specification)[3]
	backend := newFakeLifecycle()
	adapter, err := NewWorkflowAdapter(
		backend,
		specification,
		"installation-test",
		[]domain.InstallationResource{resource},
	)
	if err != nil {
		t.Fatal(err)
	}
	step := adapter.Steps[0]
	observation, err := step.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	disposition, recovered, err := step.Inspect(
		context.Background(),
		nil,
	)
	if err != nil ||
		disposition != installworkflow.DispositionApplied ||
		!sameObservation(observation, recovered) {
		t.Fatalf(
			"recovery = disposition:%s observation:%#v error:%v",
			disposition,
			recovered,
			err,
		)
	}
	replayed, err := step.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !sameObservation(observation, replayed) {
		t.Fatalf("replayed observation = %#v", replayed)
	}
	if countMutation(backend.mutations, "create-container:probe") != 1 {
		t.Fatalf("container was recreated: %v", backend.mutations)
	}
	if err := step.Rollback(context.Background(), observation); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowRollbackRefusesDockerDrift(t *testing.T) {
	specification := managedSpecification(t)
	resource := managedDockerResources(specification)[5]
	backend := newFakeLifecycle()
	adapter, err := NewWorkflowAdapter(
		backend,
		specification,
		"installation-test",
		[]domain.InstallationResource{resource},
	)
	if err != nil {
		t.Fatal(err)
	}
	step := adapter.Steps[0]
	observation, err := step.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gateway := backend.containers[specification.Containers[0].Name]
	if gateway.Name == "" {
		gateway = backend.containers["traefik"]
	}
	gateway.Labels["external-change"] = "true"
	backend.containers[gateway.Name] = gateway
	err = step.Rollback(context.Background(), observation)
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("drift rollback error = %v", err)
	}
	if _, found := backend.containers[gateway.Name]; !found {
		t.Fatal("drifted container was removed")
	}
}

func TestWorkflowCompensatesMalformedCreatedContainer(t *testing.T) {
	specification := managedSpecification(t)
	resource := managedDockerResources(specification)[3]
	backend := newFakeLifecycle()
	backend.malformedRole = "probe"
	adapter, err := NewWorkflowAdapter(
		backend,
		specification,
		"installation-test",
		[]domain.InstallationResource{resource},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Steps[0].Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("malformed create error = %v", err)
	}
	if len(backend.containers) != 0 {
		t.Fatalf("malformed container remains: %#v", backend.containers)
	}
}

func managedDockerResources(
	specification domain.InstallationSpecification,
) []domain.InstallationResource {
	requests, err := BuildRequests(specification, "installation-test")
	if err != nil {
		panic(err)
	}
	return []domain.InstallationResource{
		managedDockerResourceFixture(
			proxyNetworkResourceID,
			domain.ResourceDockerNetwork,
			requests.Networks[0].Name,
		),
		managedDockerResourceFixture(
			controlNetworkResourceID,
			domain.ResourceDockerNetwork,
			requests.Networks[1].Name,
		),
		managedDockerResourceFixture(
			probeVolumeResourceID,
			domain.ResourceDockerVolume,
			requests.Volume.Name,
		),
		managedDockerResourceFixture(
			probeResourceID,
			domain.ResourceDockerContainer,
			requests.Containers[0].Name,
		),
		managedDockerResourceFixture(
			controllerResourceID,
			domain.ResourceDockerContainer,
			requests.Containers[1].Name,
		),
		managedDockerResourceFixture(
			gatewayResourceID,
			domain.ResourceDockerContainer,
			requests.Containers[2].Name,
		),
	}
}

func managedDockerResourceFixture(
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

func failingVerificationStep(
	resource domain.InstallationResource,
) installworkflow.Step {
	return installworkflow.Step{
		ID:         "verify-runtime",
		ResourceID: resource.ID,
		Target:     resource.Target,
		Stage:      domain.ExecutionVerify,
		Apply: func(context.Context) (
			domain.InstallationObservation,
			error,
		) {
			return domain.InstallationObservation{},
				errors.New("injected verification failure")
		},
		Inspect: func(
			context.Context,
			*domain.InstallationObservation,
		) (installworkflow.Disposition, domain.InstallationObservation, error) {
			return installworkflow.DispositionPending,
				domain.InstallationObservation{},
				nil
		},
		Rollback: func(
			context.Context,
			domain.InstallationObservation,
		) error {
			return nil
		},
	}
}

func countMutation(mutations []string, expected string) int {
	count := 0
	for _, mutation := range mutations {
		if mutation == expected {
			count++
		}
	}
	return count
}
