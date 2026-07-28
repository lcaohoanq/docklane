package uninstallplan

import (
	"strings"
	"testing"
	"time"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installmanifest"
)

func installedManifest(t *testing.T) domain.InstallationManifest {
	t.Helper()
	manifest, err := installmanifest.New(
		"dev",
		"docker.home.arpa",
		"proxy",
		time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest.State = domain.InstallationInstalled
	manifest.Resources = []domain.InstallationResource{
		{
			ID:        "global-traefik",
			Kind:      domain.ResourceDockerContainer,
			Target:    "traefik",
			Ownership: domain.ResourceAdopted,
			State:     domain.ResourceVerified,
			Rollback:  domain.RollbackPreserve,
		},
		{
			ID:        "proxy-network",
			Kind:      domain.ResourceDockerNetwork,
			Target:    "proxy",
			Ownership: domain.ResourceManaged,
			State:     domain.ResourceVerified,
			Rollback:  domain.RollbackRemove,
		},
		{
			ID:        "dnsmasq-domain",
			Kind:      domain.ResourceFile,
			Target:    "/etc/dnsmasq.d/docklane.conf",
			Ownership: domain.ResourceManaged,
			State:     domain.ResourceVerified,
			Rollback:  domain.RollbackRestore,
			Backup: &domain.ResourceBackup{
				Path:        "/var/lib/docklane/backups/dnsmasq.conf",
				Fingerprint: strings.Repeat("a", 64),
			},
		},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestBuildReversesDependenciesAndPreservesAdoptedResources(t *testing.T) {
	manifest := installedManifest(t)
	plan, err := Build(manifest, "/var/lib/docklane/install-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Ready ||
		plan.Status != domain.DiagnosticPass ||
		len(plan.Operations) != 3 ||
		plan.Token == "" {
		t.Fatalf("plan = %#v", plan)
	}
	want := []domain.InstallationAction{
		domain.InstallationRestore,
		domain.InstallationRemove,
		domain.InstallationPreserve,
	}
	for index, action := range want {
		if plan.Operations[index].Action != action {
			t.Fatalf("operation %d = %#v", index, plan.Operations[index])
		}
	}
	if plan.Operations[2].Mutating {
		t.Fatalf("adopted resource mutates: %#v", plan.Operations[2])
	}
	if plan.Operations[0].Backup == nil ||
		plan.Operations[0].Backup.Path !=
			"/var/lib/docklane/backups/dnsmasq.conf" {
		t.Fatalf("restore backup = %#v", plan.Operations[0].Backup)
	}
	repeated, err := Build(manifest, "/var/lib/docklane/install-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Token != plan.Token {
		t.Fatalf("token changed: %s != %s", repeated.Token, plan.Token)
	}
}

func TestBuildBlocksNonInstalledManifest(t *testing.T) {
	manifest, err := installmanifest.New(
		"dev",
		"docker.home.arpa",
		"proxy",
		time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Build(manifest, "/var/lib/docklane/install-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Ready ||
		plan.Status != domain.DiagnosticFail ||
		len(plan.Blockers) != 1 ||
		plan.Blockers[0] != "installation-state-planned" {
		t.Fatalf("plan = %#v", plan)
	}
}
