package installlinks

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installworkflow"
)

func TestWorkflowAtomicallySwitchesAndRestoresSymlink(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "resolv.conf")
	prior := filepath.Join(root, "uplink.conf")
	desired := filepath.Join(root, "stub.conf")
	if err := os.Symlink(prior, link); err != nil {
		t.Fatal(err)
	}
	resource := domain.InstallationResource{
		ID:          "resolver-stub-link",
		Kind:        domain.ResourceSymlink,
		Target:      link,
		LinkTarget:  desired,
		PriorTarget: prior,
		Ownership:   domain.ResourceManaged,
		State:       domain.ResourcePlanned,
		Rollback:    domain.RollbackRestore,
	}
	adapter, err := NewWorkflowAdapter(
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
	if target, err := normalizedTarget(link); err != nil || target != desired {
		t.Fatalf("applied target = %q, error = %v", target, err)
	}
	disposition, _, err := step.Inspect(
		context.Background(),
		&observation,
	)
	if err != nil || disposition != installworkflow.DispositionApplied {
		t.Fatalf("applied disposition = %q, error = %v", disposition, err)
	}
	if err := step.Rollback(context.Background(), observation); err != nil {
		t.Fatal(err)
	}
	if target, err := normalizedTarget(link); err != nil || target != prior {
		t.Fatalf("restored target = %q, error = %v", target, err)
	}
}

func TestWorkflowRefusesUnreviewedSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "resolv.conf")
	if err := os.Symlink(filepath.Join(root, "other"), link); err != nil {
		t.Fatal(err)
	}
	resource := domain.InstallationResource{
		ID:          "resolver-stub-link",
		Kind:        domain.ResourceSymlink,
		Target:      link,
		LinkTarget:  filepath.Join(root, "stub"),
		PriorTarget: filepath.Join(root, "uplink"),
		Ownership:   domain.ResourceManaged,
		State:       domain.ResourcePlanned,
		Rollback:    domain.RollbackRestore,
	}
	adapter, err := NewWorkflowAdapter(
		[]domain.InstallationResource{resource},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Steps[0].Apply(context.Background()); err == nil {
		t.Fatal("apply accepted an unreviewed symlink target")
	}
}
