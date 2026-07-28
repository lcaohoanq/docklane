package installplan

import (
	"strings"
	"testing"
	"time"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installspec"
)

func adoptionReport() domain.PreflightReport {
	return domain.PreflightReport{
		Status: domain.DiagnosticPass,
		Target: domain.PreflightTarget{
			BaseDomain:   "docker.home.arpa",
			ProxyNetwork: "proxy",
			DockerSocket: "/var/run/docker.sock",
			ManifestPath: "/var/lib/docklane/install-manifest.json",
		},
		Inventory: domain.PreflightInventory{
			Gateway: domain.PreflightGateway{
				Disposition:   domain.PreflightAdopt,
				ContainerID:   "abc123",
				ContainerName: "traefik",
				Image:         "traefik:v3.7",
			},
			Network: domain.PreflightNetwork{
				Disposition: domain.PreflightAdopt,
				Name:        "proxy",
				ID:          "network123",
			},
			DNS: domain.PreflightDNS{
				Disposition:   domain.PreflightAdopt,
				MappingPaths:  []string{"/etc/dnsmasq.d/lab.conf"},
				ConfigPaths:   []string{"/etc/dnsmasq.conf", "/etc/dnsmasq.d/lab.conf"},
				ServiceActive: true,
			},
			Resolver: domain.PreflightResolver{
				Disposition: domain.PreflightAdopt,
				Addresses:   []string{"127.0.0.1"},
			},
			TLS: domain.PreflightTLS{
				Disposition:            domain.PreflightAdopt,
				CertificatePath:        "/opt/traefik/certs/local.crt",
				PrivateKeyPath:         "/opt/traefik/certs/local.key",
				TrustAnchorPath:        "/etc/ssl/certs/local-root.crt",
				CertificateFingerprint: strings.Repeat("a", 64),
				PrivateKeyFingerprint:  strings.Repeat("b", 64),
				TrustFingerprint:       strings.Repeat("c", 64),
			},
			Runtime: domain.PreflightRuntime{
				Disposition: domain.PreflightAdopt,
				Controller: domain.PreflightRuntimeContainer{
					ContainerName:    "docklane",
					ImageFingerprint: strings.Repeat("d", 64),
				},
				Probe: domain.PreflightRuntimeContainer{
					ContainerName:    "docklane-probe",
					ImageFingerprint: strings.Repeat("d", 64),
				},
				ControlNetwork: domain.PreflightNetwork{
					Disposition: domain.PreflightAdopt,
					Name:        "docklane-control",
				},
				ProbeVolume: domain.PreflightVolume{
					Disposition: domain.PreflightAdopt,
					Name:        "docklane-probe-run",
					Driver:      "local",
				},
				DataDisposition: domain.PreflightAdopt,
				DataPath:        "/var/lib/docklane",
			},
		},
		Checks: []domain.DiagnosticCheck{},
	}
}

func TestBuildAdoptionPlanPreservesExistingResources(t *testing.T) {
	report := adoptionReport()
	plan, err := Build(report, Options{
		DnsmasqTarget:        "/etc/dnsmasq.d/docklane.conf",
		DnsmasqService:       "dnsmasq",
		ManagedSpecification: testManagedSpecification(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Ready || plan.Status != domain.DiagnosticPass {
		t.Fatalf("plan = %#v", plan)
	}
	if !plan.Complete || len(plan.Pending) != 0 {
		t.Fatalf("coverage = complete:%t pending:%v", plan.Complete, plan.Pending)
	}
	if plan.ManagedSpecification != nil {
		t.Fatalf("adoption plan claimed managed specification: %#v", plan.ManagedSpecification)
	}
	if len(plan.ManagedArtifacts) != 0 {
		t.Fatalf("adoption plan claimed managed artifacts: %#v", plan.ManagedArtifacts)
	}
	if len(plan.Resources) != 13 || len(plan.Operations) != 14 {
		t.Fatalf(
			"resources = %d, operations = %d",
			len(plan.Resources),
			len(plan.Operations),
		)
	}
	for _, resource := range plan.Resources {
		if resource.Ownership != domain.ResourceAdopted ||
			resource.Rollback != domain.RollbackPreserve ||
			resource.State != domain.ResourceVerified {
			t.Fatalf("unsafe adopted resource = %#v", resource)
		}
	}
	if plan.Operations[0].Action != domain.InstallationCreateManifest {
		t.Fatalf("first operation = %#v", plan.Operations[0])
	}
	if operationIndex(plan, "adopt-docklane-control-network") >
		operationIndex(plan, "adopt-docklane-probe") ||
		operationIndex(plan, "adopt-docklane-probe") >
			operationIndex(plan, "adopt-docklane-controller") {
		t.Fatalf("runtime dependency order = %#v", plan.Operations)
	}
	if plan.Token == "" {
		t.Fatal("plan token is empty")
	}
	report.GeneratedAt = time.Now()
	repeated, err := Build(report, Options{
		DnsmasqTarget:  "/etc/dnsmasq.d/docklane.conf",
		DnsmasqService: "dnsmasq",
	})
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Token != plan.Token {
		t.Fatalf("token changed: %s != %s", repeated.Token, plan.Token)
	}
}

func testManagedSpecification(t *testing.T) domain.InstallationSpecification {
	t.Helper()
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
	return specification
}

func TestBuildCleanHostPlanCreatesRestorableResources(t *testing.T) {
	report := adoptionReport()
	report.Status = domain.DiagnosticWarn
	report.Inventory.Gateway = domain.PreflightGateway{
		Disposition: domain.PreflightCreate,
	}
	report.Inventory.Network = domain.PreflightNetwork{
		Disposition: domain.PreflightCreate,
		Name:        "proxy",
	}
	report.Inventory.DNS = domain.PreflightDNS{
		Disposition:   domain.PreflightCreate,
		MappingPaths:  []string{},
		ConfigPaths:   []string{"/etc/dnsmasq.conf"},
		ServiceActive: false,
	}
	report.Inventory.Resolver = domain.PreflightResolver{
		Disposition: domain.PreflightCreate,
		Addresses:   []string{},
	}
	report.Inventory.TLS = domain.PreflightTLS{
		Disposition: domain.PreflightCreate,
	}
	report.Inventory.Runtime = domain.PreflightRuntime{
		Disposition: domain.PreflightCreate,
		ControlNetwork: domain.PreflightNetwork{
			Disposition: domain.PreflightCreate,
			Name:        "docklane-control",
		},
		ProbeVolume: domain.PreflightVolume{
			Disposition: domain.PreflightCreate,
			Name:        "docklane-probe-run",
		},
		DataDisposition: domain.PreflightCreate,
		DataPath:        "/var/lib/docklane/data",
	}
	plan, err := Build(report, Options{
		DnsmasqTarget:        "/etc/dnsmasq.d/docklane.conf",
		DnsmasqService:       "dnsmasq",
		ManagedSpecification: testManagedSpecification(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Ready || plan.Status != domain.DiagnosticWarn {
		t.Fatalf("plan = %#v", plan)
	}
	if !plan.Complete || len(plan.Pending) != 0 {
		t.Fatalf("managed coverage = complete:%t pending:%v", plan.Complete, plan.Pending)
	}
	if plan.ManagedSpecification == nil {
		t.Fatal("managed specification is missing")
	}
	if len(plan.ManagedArtifacts) != 13 {
		t.Fatalf("managed artifacts = %d, want 13", len(plan.ManagedArtifacts))
	}
	for _, artifact := range plan.ManagedArtifacts {
		if artifact.Kind == domain.ArtifactContainerSpec {
			continue
		}
		found := false
		for _, resource := range plan.Resources {
			if resource.ID == artifact.ID &&
				resource.Target == artifact.Target &&
				resource.Ownership == domain.ResourceManaged {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("artifact %s has no managed resource", artifact.ID)
		}
	}
	changedSpecification := testManagedSpecification(t)
	changedSpecification.Images.Traefik = "traefik:v3.8"
	for index := range changedSpecification.Containers {
		if changedSpecification.Containers[index].Role == "gateway" {
			changedSpecification.Containers[index].Image = "traefik:v3.8"
		}
	}
	changed, err := Build(report, Options{
		DnsmasqTarget:        "/etc/dnsmasq.d/docklane.conf",
		DnsmasqService:       "dnsmasq",
		ManagedSpecification: changedSpecification,
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed.Token == plan.Token {
		t.Fatal("managed image change did not change plan token")
	}
	assertResource(
		t,
		plan,
		"dnsmasq-domain",
		domain.ResourceManaged,
		domain.RollbackRemove,
	)
	assertResource(
		t,
		plan,
		"dnsmasq-service",
		domain.ResourceManaged,
		domain.RollbackRestore,
	)
	assertResource(
		t,
		plan,
		"resolver-domain",
		domain.ResourceManaged,
		domain.RollbackRestore,
	)
	for _, directoryID := range []string{
		"docklane-traefik-directory",
		"docklane-traefik-dynamic-directory",
		"docklane-traefik-certs-directory",
		"docklane-pki-directory",
		"docklane-secrets-directory",
		"docklane-backup-directory",
	} {
		assertResource(
			t,
			plan,
			directoryID,
			domain.ResourceManaged,
			domain.RollbackRemove,
		)
	}
}

func TestBuildHybridPlanDoesNotManageAdoptedFileArtifacts(t *testing.T) {
	report := adoptionReport()
	report.Status = domain.DiagnosticWarn
	report.Inventory.Runtime = domain.PreflightRuntime{
		Disposition: domain.PreflightCreate,
		ControlNetwork: domain.PreflightNetwork{
			Disposition: domain.PreflightCreate,
			Name:        "docklane-control",
		},
		ProbeVolume: domain.PreflightVolume{
			Disposition: domain.PreflightCreate,
			Name:        "docklane-probe-run",
		},
		DataDisposition: domain.PreflightCreate,
		DataPath:        "/var/lib/docklane/data",
	}
	plan, err := Build(report, Options{
		DnsmasqTarget:        "/etc/dnsmasq.d/docklane.conf",
		DnsmasqService:       "dnsmasq",
		ManagedSpecification: testManagedSpecification(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Ready || !plan.Complete {
		t.Fatalf("hybrid plan = %#v", plan)
	}
	if len(plan.ManagedArtifacts) != 2 {
		t.Fatalf("hybrid artifacts = %#v", plan.ManagedArtifacts)
	}
	for _, artifact := range plan.ManagedArtifacts {
		if artifact.ID != "container-controller" &&
			artifact.ID != "container-probe" {
			t.Fatalf("adopted component artifact became managed: %#v", artifact)
		}
	}
	for _, resource := range plan.Resources {
		if resource.Ownership == domain.ResourceManaged &&
			(resource.Kind == domain.ResourceFile ||
				resource.Kind == domain.ResourceTrustAnchor) {
			t.Fatalf("hybrid plan manages adopted file: %#v", resource)
		}
	}
}

func TestBuildBlocksConflictAndExistingManifest(t *testing.T) {
	report := adoptionReport()
	report.Status = domain.DiagnosticFail
	report.Checks = []domain.DiagnosticCheck{{
		ID: "dnsmasq-domain", Status: domain.DiagnosticFail,
	}}
	report.Inventory.DNS.Disposition = domain.PreflightConflict
	report.Inventory.Manifest = domain.PreflightManifest{
		Exists:         true,
		InstallationID: "018f5e52-4f22-4a6e-8ad8-d3e4450d1957",
		State:          domain.InstallationInstalled,
		Generation:     3,
	}
	plan, err := Build(report, Options{
		DnsmasqTarget:  "/etc/dnsmasq.d/docklane.conf",
		DnsmasqService: "dnsmasq",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Ready || plan.Status != domain.DiagnosticFail {
		t.Fatalf("plan = %#v", plan)
	}
	for _, expected := range []string{
		"dnsmasq-domain",
		"dnsmasq-mapping-ownership",
		"existing-install-manifest",
	} {
		found := false
		for _, blocker := range plan.Blockers {
			if blocker == expected {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing blocker %q in %v", expected, plan.Blockers)
		}
	}
}

func assertResource(
	t *testing.T,
	plan domain.InstallationPlan,
	id string,
	ownership domain.ResourceOwnership,
	rollback domain.RollbackStrategy,
) {
	t.Helper()
	for _, resource := range plan.Resources {
		if resource.ID == id {
			if resource.Ownership != ownership || resource.Rollback != rollback {
				t.Fatalf("resource %s = %#v", id, resource)
			}
			return
		}
	}
	t.Fatalf("resource %s not found", id)
}

func operationIndex(plan domain.InstallationPlan, id string) int {
	for index, operation := range plan.Operations {
		if operation.ID == id {
			return index
		}
	}
	return len(plan.Operations)
}
