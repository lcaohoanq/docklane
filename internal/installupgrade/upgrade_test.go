package installupgrade

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installmanifest"
)

func TestPlanAndApplyMigratesLegacyManifestWithBackup(t *testing.T) {
	runner, store, legacy := legacyFixture(
		t,
		domain.InstallationRolledBack,
	)
	plan, err := runner.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Ready || plan.Current || len(plan.Operations) != 1 ||
		plan.FromSchemaVersion != 1 ||
		plan.ToSchemaVersion != domain.InstallationManifestSchemaVersion {
		t.Fatalf("plan = %#v", plan)
	}
	applied, err := runner.Apply(context.Background(), plan.Token)
	if err != nil {
		t.Fatal(err)
	}
	if applied.SchemaVersion != domain.InstallationManifestSchemaVersion ||
		applied.Generation != legacy.Generation+1 ||
		len(applied.UpgradeHistory) != 1 {
		t.Fatalf("applied = %#v", applied)
	}
	if _, err := os.Stat(plan.Operations[0].BackupPath); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Generation != applied.Generation {
		t.Fatalf("loaded generation = %d", loaded.Generation)
	}
}

func TestApplyRejectsWrongTokenWithoutChangingLegacyManifest(t *testing.T) {
	runner, store, legacy := legacyFixture(
		t,
		domain.InstallationRolledBack,
	)
	_, err := runner.Apply(context.Background(), strings.Repeat("a", 64))
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("error = %v", err)
	}
	source, err := store.LoadForUpgrade()
	if err != nil {
		t.Fatal(err)
	}
	if source.Manifest.SchemaVersion != 1 ||
		source.Manifest.Generation != legacy.Generation {
		t.Fatalf("source changed = %#v", source.Manifest)
	}
}

func TestPlanBlocksNonTerminalManifest(t *testing.T) {
	runner, _, _ := legacyFixture(t, domain.InstallationPlanned)
	plan, err := runner.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Ready || len(plan.Blockers) != 1 ||
		!strings.Contains(plan.Blockers[0], "not terminal") {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestPlanReportsCurrentManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install-manifest.json")
	store, err := installmanifest.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := installmanifest.New(
		"dev",
		"docker.home.arpa",
		"proxy",
		time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(manifest); err != nil {
		t.Fatal(err)
	}
	runner, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := runner.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Current || !plan.Ready || len(plan.Operations) != 0 {
		t.Fatalf("plan = %#v", plan)
	}
}

func legacyFixture(
	t *testing.T,
	state domain.InstallationState,
) (*Runner, *installmanifest.Store, domain.InstallationManifest) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "install-manifest.json")
	manifest, err := installmanifest.New(
		"dev",
		"docker.home.arpa",
		"proxy",
		time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SchemaVersion = 1
	manifest.State = state
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := installmanifest.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	runner.now = func() time.Time {
		return manifest.UpdatedAt.Add(time.Minute)
	}
	return runner, store, manifest
}
