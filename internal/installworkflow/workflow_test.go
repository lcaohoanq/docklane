package installworkflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installmanifest"
	"docklane.local/docklane/internal/installspec"
)

var errInjectedCheckpoint = errors.New("injected checkpoint failure")

type memoryStore struct {
	current    domain.InstallationManifest
	saveCalls  int
	failOnCall int
	failError  error
	failed     bool
}

func (store *memoryStore) Save(
	expected uint64,
	next domain.InstallationManifest,
) error {
	store.saveCalls++
	if store.failOnCall == store.saveCalls && !store.failed {
		store.failed = true
		if store.failError != nil {
			return store.failError
		}
		return errInjectedCheckpoint
	}
	if store.current.Generation != expected {
		return installmanifest.ErrGenerationConflict
	}
	if next.Generation != expected+1 {
		return fmt.Errorf("invalid generation %d", next.Generation)
	}
	if err := next.Validate(); err != nil {
		return err
	}
	store.current = cloneManifest(next)
	return nil
}

type fakeExternal struct {
	states        []Disposition
	applyCalls    []int
	rollbackCalls []int
	rollbackOrder []int
	applyError    map[int]error
	applyOnError  map[int]bool
}

func fakeSteps(external *fakeExternal, count int) []Step {
	steps := make([]Step, count)
	for index := range steps {
		index := index
		steps[index] = Step{
			ID:         fmt.Sprintf("create-resource-%d", index),
			ResourceID: fmt.Sprintf("resource-%d", index),
			Target:     fmt.Sprintf("target-%d", index),
			Stage:      domain.ExecutionDocker,
			Apply: func(context.Context) (domain.InstallationObservation, error) {
				external.applyCalls[index]++
				if err := external.applyError[index]; err != nil {
					if external.applyOnError[index] {
						external.states[index] = DispositionApplied
					}
					return domain.InstallationObservation{}, err
				}
				external.states[index] = DispositionApplied
				return observation(index), nil
			},
			Inspect: func(
				_ context.Context,
				_ *domain.InstallationObservation,
			) (Disposition, domain.InstallationObservation, error) {
				return external.states[index], observation(index), nil
			},
			Rollback: func(
				context.Context,
				domain.InstallationObservation,
			) error {
				external.rollbackCalls[index]++
				external.rollbackOrder = append(
					external.rollbackOrder,
					index,
				)
				external.states[index] = DispositionRolledBack
				return nil
			},
		}
	}
	return steps
}

func workflowManifest(t *testing.T, count int) domain.InstallationManifest {
	t.Helper()
	manifest, err := installmanifest.New(
		"dev",
		"docker.home.arpa",
		"proxy",
		time.Date(2026, time.July, 28, 8, 0, 0, 0, time.UTC),
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
	for index := range count {
		manifest.Resources = append(
			manifest.Resources,
			domain.InstallationResource{
				ID:        fmt.Sprintf("resource-%d", index),
				Kind:      domain.ResourceDockerNetwork,
				Target:    fmt.Sprintf("target-%d", index),
				Ownership: domain.ResourceManaged,
				State:     domain.ResourcePlanned,
				Rollback:  domain.RollbackRemove,
			},
		)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func observation(index int) domain.InstallationObservation {
	return domain.InstallationObservation{
		ExternalID: fmt.Sprintf("external-%d", index),
		Created:    true,
		SnapshotFingerprint: strings.Repeat(
			fmt.Sprintf("%x", index+1),
			64,
		)[:64],
	}
}

func TestResumeReconcilesApplyBeforeRepeatingMutation(t *testing.T) {
	manifest := workflowManifest(t, 1)
	store := &memoryStore{
		current:    manifest,
		failOnCall: 3,
	}
	external := &fakeExternal{
		states:        []Disposition{DispositionPending},
		applyCalls:    make([]int, 1),
		rollbackCalls: make([]int, 1),
		applyError:    map[int]error{},
	}
	runner, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(
		context.Background(),
		manifest,
		fakeSteps(external, 1),
	); !errors.Is(err, errInjectedCheckpoint) {
		t.Fatalf("first run error = %v", err)
	}
	if external.applyCalls[0] != 1 ||
		store.current.Execution.Operations[0].State != domain.OperationApplying {
		t.Fatalf(
			"apply calls = %v, durable state = %#v",
			external.applyCalls,
			store.current.Execution,
		)
	}
	recovered, err := runner.Run(
		context.Background(),
		store.current,
		fakeSteps(external, 1),
	)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != domain.InstallationInstalled ||
		recovered.Execution.Phase != domain.ExecutionComplete ||
		external.applyCalls[0] != 1 {
		t.Fatalf(
			"recovered = %#v, apply calls = %v",
			recovered.Execution,
			external.applyCalls,
		)
	}
}

func TestResumeReconcilesRollbackBeforeRepeatingDeletion(t *testing.T) {
	manifest := workflowManifest(t, 2)
	store := &memoryStore{
		current:    manifest,
		failOnCall: 8,
	}
	external := &fakeExternal{
		states:        []Disposition{DispositionPending, DispositionPending},
		applyCalls:    make([]int, 2),
		rollbackCalls: make([]int, 2),
		applyError: map[int]error{
			1: errors.New("create failed"),
		},
	}
	runner, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(
		context.Background(),
		manifest,
		fakeSteps(external, 2),
	); err == nil {
		t.Fatal("injected rollback checkpoint failure was accepted")
	}
	if external.rollbackCalls[0] != 1 ||
		store.current.Execution.Operations[0].State != domain.OperationRollingBack {
		t.Fatalf(
			"rollback calls = %v, durable state = %#v",
			external.rollbackCalls,
			store.current.Execution,
		)
	}
	recovered, err := runner.Run(
		context.Background(),
		store.current,
		fakeSteps(external, 2),
	)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != domain.InstallationRolledBack ||
		external.rollbackCalls[0] != 1 {
		t.Fatalf(
			"recovered = %#v, rollback calls = %v",
			recovered.Execution,
			external.rollbackCalls,
		)
	}
}

func TestApplyFailureRollsBackInReverseOrder(t *testing.T) {
	manifest := workflowManifest(t, 3)
	store := &memoryStore{current: manifest}
	external := &fakeExternal{
		states:        make([]Disposition, 3),
		applyCalls:    make([]int, 3),
		rollbackCalls: make([]int, 3),
		applyError: map[int]error{
			2: errors.New("third create failed"),
		},
	}
	for index := range external.states {
		external.states[index] = DispositionPending
	}
	runner, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(
		context.Background(),
		manifest,
		fakeSteps(external, 3),
	)
	if err == nil || !strings.Contains(err.Error(), "third create failed") {
		t.Fatalf("run error = %v", err)
	}
	if fmt.Sprint(external.rollbackOrder) != "[1 0]" {
		t.Fatalf("rollback order = %v", external.rollbackOrder)
	}
	if result.State != domain.InstallationRolledBack ||
		result.Execution.Phase != domain.ExecutionRolledBack {
		t.Fatalf("result = %#v", result.Execution)
	}
}

func TestRecoveryConflictFailsWithoutFurtherMutation(t *testing.T) {
	manifest := workflowManifest(t, 1)
	store := &memoryStore{
		current:    manifest,
		failOnCall: 3,
	}
	external := &fakeExternal{
		states:        []Disposition{DispositionPending},
		applyCalls:    make([]int, 1),
		rollbackCalls: make([]int, 1),
		applyError:    map[int]error{},
	}
	runner, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = runner.Run(
		context.Background(),
		manifest,
		fakeSteps(external, 1),
	)
	external.states[0] = DispositionConflict
	failed, err := runner.Run(
		context.Background(),
		store.current,
		fakeSteps(external, 1),
	)
	if !errors.Is(err, ErrObservationConflict) {
		t.Fatalf("recovery error = %v", err)
	}
	if failed.State != domain.InstallationFailed ||
		external.applyCalls[0] != 1 ||
		external.rollbackCalls[0] != 0 {
		t.Fatalf(
			"failed = %#v, apply = %v rollback = %v",
			failed.Execution,
			external.applyCalls,
			external.rollbackCalls,
		)
	}
}

func TestGenerationConflictStopsBeforeMutation(t *testing.T) {
	manifest := workflowManifest(t, 1)
	store := &memoryStore{
		current:    manifest,
		failOnCall: 2,
		failError:  installmanifest.ErrGenerationConflict,
	}
	external := &fakeExternal{
		states:        []Disposition{DispositionPending},
		applyCalls:    make([]int, 1),
		rollbackCalls: make([]int, 1),
		applyError:    map[int]error{},
	}
	runner, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(
		context.Background(),
		manifest,
		fakeSteps(external, 1),
	)
	if !errors.Is(err, installmanifest.ErrGenerationConflict) {
		t.Fatalf("run error = %v", err)
	}
	if external.applyCalls[0] != 0 {
		t.Fatalf("apply calls = %v", external.applyCalls)
	}
}

func TestResumeRetriesAfterInspectingAbsentMutation(t *testing.T) {
	manifest := workflowManifest(t, 1)
	manifest.Generation = 2
	manifest.State = domain.InstallationApplying
	manifest.UpdatedAt = manifest.UpdatedAt.Add(time.Second)
	manifest.Execution = newExecution(fakeSteps(&fakeExternal{}, 1))
	manifest.Execution.Operations[0].State = domain.OperationApplying
	manifest.Execution.Operations[0].Attempt = 1
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{current: manifest}
	external := &fakeExternal{
		states:        []Disposition{DispositionPending},
		applyCalls:    make([]int, 1),
		rollbackCalls: make([]int, 1),
		applyError:    map[int]error{},
	}
	runner, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(
		context.Background(),
		manifest,
		fakeSteps(external, 1),
	)
	if err != nil {
		t.Fatal(err)
	}
	if external.applyCalls[0] != 1 ||
		result.Execution.Operations[0].Attempt != 2 {
		t.Fatalf(
			"apply calls = %v, operation = %#v",
			external.applyCalls,
			result.Execution.Operations[0],
		)
	}
}

func TestApplyErrorAfterExternalSuccessIsRecordedThenRolledBack(t *testing.T) {
	manifest := workflowManifest(t, 1)
	store := &memoryStore{current: manifest}
	external := &fakeExternal{
		states:        []Disposition{DispositionPending},
		applyCalls:    make([]int, 1),
		rollbackCalls: make([]int, 1),
		applyError: map[int]error{
			0: errors.New("response was lost"),
		},
		applyOnError: map[int]bool{0: true},
	}
	runner, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(
		context.Background(),
		manifest,
		fakeSteps(external, 1),
	)
	if err == nil || !strings.Contains(err.Error(), "response was lost") {
		t.Fatalf("run error = %v", err)
	}
	if result.State != domain.InstallationRolledBack ||
		external.rollbackCalls[0] != 1 {
		t.Fatalf(
			"result = %#v, rollback calls = %v",
			result.Execution,
			external.rollbackCalls,
		)
	}
}

func TestChangedWorkflowIsRejectedBeforeMutation(t *testing.T) {
	manifest := workflowManifest(t, 1)
	store := &memoryStore{current: manifest}
	external := &fakeExternal{
		states:        []Disposition{DispositionPending},
		applyCalls:    make([]int, 1),
		rollbackCalls: make([]int, 1),
		applyError:    map[int]error{},
	}
	runner, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	steps := fakeSteps(external, 1)
	journaled, err := runner.checkpoint(
		manifest,
		func(next *domain.InstallationManifest) {
			next.State = domain.InstallationApplying
			next.Execution = newExecution(steps)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	steps[0].ID = "renamed-operation"
	if _, err := runner.Run(
		context.Background(),
		journaled,
		steps,
	); err == nil || !strings.Contains(err.Error(), "does not match workflow") {
		t.Fatalf("run error = %v", err)
	}
	if external.applyCalls[0] != 0 {
		t.Fatalf("apply calls = %v", external.applyCalls)
	}
}
