package installhost

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"docklane.local/docklane/internal/installspec"
)

type fakeFiles struct {
	events     *[]string
	fail       bool
	rolledBack bool
}

func (files *fakeFiles) Rollback() error {
	*files.events = append(*files.events, "restore-files")
	if files.fail {
		return errors.New("injected file rollback failure")
	}
	files.rolledBack = true
	return nil
}

type fakeBackend struct {
	events        []string
	services      map[string]ServiceState
	addresses     map[string][]string
	failOperation string
	trustError    error
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		services: map[string]ServiceState{
			"dnsmasq":          {Active: true},
			"systemd-resolved": {Active: true},
		},
		addresses: map[string][]string{
			"docker.home.arpa":                    {"127.0.0.1"},
			"docklane-preflight.docker.home.arpa": {"127.0.0.1"},
		},
	}
}

func (backend *fakeBackend) ServiceState(
	_ context.Context,
	service string,
) (ServiceState, error) {
	state, found := backend.services[service]
	if !found {
		return ServiceState{}, errors.New("unknown service")
	}
	return state, nil
}

func (backend *fakeBackend) ValidateDNSConfiguration(context.Context) error {
	return backend.operation("validate-dns", nil)
}

func (backend *fakeBackend) RefreshTrustStore(
	_ context.Context,
	profile string,
) error {
	return backend.operation("refresh-trust:"+profile, nil)
}

func (backend *fakeBackend) StartService(
	_ context.Context,
	service string,
) error {
	return backend.operation("start:"+service, func() {
		backend.services[service] = ServiceState{Active: true}
	})
}

func (backend *fakeBackend) RestartService(
	_ context.Context,
	service string,
) error {
	return backend.operation("restart:"+service, func() {
		backend.services[service] = ServiceState{Active: true}
	})
}

func (backend *fakeBackend) StopService(
	_ context.Context,
	service string,
) error {
	return backend.operation("stop:"+service, func() {
		backend.services[service] = ServiceState{}
	})
}

func (backend *fakeBackend) FlushResolverCache(
	_ context.Context,
	profile string,
) error {
	return backend.operation("flush:"+profile, nil)
}

func (backend *fakeBackend) LookupHost(
	_ context.Context,
	hostname string,
) ([]string, error) {
	backend.events = append(backend.events, "lookup:"+hostname)
	addresses, found := backend.addresses[hostname]
	if !found {
		return nil, errors.New("not found")
	}
	return append([]string(nil), addresses...), nil
}

func (backend *fakeBackend) VerifyTrustAnchor(
	_ context.Context,
	_ string,
	_ string,
) error {
	backend.events = append(backend.events, "verify-trust")
	return backend.trustError
}

func (backend *fakeBackend) operation(
	name string,
	success func(),
) error {
	backend.events = append(backend.events, name)
	if backend.failOperation == name {
		backend.failOperation = ""
		return errors.New("injected " + name + " failure")
	}
	if success != nil {
		success()
	}
	return nil
}

func validContract() Contract {
	return Contract{
		BaseDomain:             "docker.home.arpa",
		DNSService:             "dnsmasq",
		ResolverService:        "systemd-resolved",
		TrustProfile:           TrustProfileP11Kit,
		ResolverProfile:        ResolverProfileSystemd,
		TrustAnchorPath:        "/etc/ca-certificates/trust-source/anchors/docklane-local-root-ca.crt",
		TrustAnchorFingerprint: strings.Repeat("a", 64),
	}
}

func TestBuildContractUsesManagedHostProfile(t *testing.T) {
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
	contract, err := BuildContract(specification, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	if contract.DNSService != "dnsmasq" ||
		contract.ResolverService != "systemd-resolved" ||
		contract.TrustProfile != TrustProfileP11Kit ||
		contract.ResolverProfile != ResolverProfileSystemd ||
		contract.TrustAnchorPath != specification.PKI.TrustAnchorPath {
		t.Fatalf("contract = %#v", contract)
	}
}

func TestApplyAndRollbackRestoreActiveServices(t *testing.T) {
	backend := newFakeBackend()
	files := &fakeFiles{events: &backend.events}
	transaction, err := Apply(
		context.Background(),
		backend,
		files,
		validContract(),
	)
	if err != nil {
		t.Fatal(err)
	}
	expectedApply := []string{
		"validate-dns",
		"refresh-trust:p11-kit",
		"verify-trust",
		"restart:dnsmasq",
		"restart:systemd-resolved",
		"flush:systemd-resolved",
		"lookup:docker.home.arpa",
		"lookup:docklane-preflight.docker.home.arpa",
	}
	if !reflect.DeepEqual(backend.events, expectedApply) {
		t.Fatalf("apply events = %v", backend.events)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	expectedRollback := []string{
		"restore-files",
		"refresh-trust:p11-kit",
		"validate-dns",
		"restart:systemd-resolved",
		"restart:dnsmasq",
		"flush:systemd-resolved",
	}
	if !reflect.DeepEqual(
		backend.events[len(expectedApply):],
		expectedRollback,
	) {
		t.Fatalf("rollback events = %v", backend.events[len(expectedApply):])
	}
	if !files.rolledBack ||
		!backend.services["dnsmasq"].Active ||
		!backend.services["systemd-resolved"].Active {
		t.Fatalf("rollback state = files:%t services:%v", files.rolledBack, backend.services)
	}
}

func TestRollbackStopsServicesThatWereInitiallyInactive(t *testing.T) {
	backend := newFakeBackend()
	backend.services["dnsmasq"] = ServiceState{}
	backend.services["systemd-resolved"] = ServiceState{}
	files := &fakeFiles{events: &backend.events}
	transaction, err := Apply(
		context.Background(),
		backend,
		files,
		validContract(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if backend.services["dnsmasq"].Active ||
		backend.services["systemd-resolved"].Active {
		t.Fatalf("inactive services were not restored: %v", backend.services)
	}
	events := strings.Join(backend.events, "\n")
	if !strings.Contains(events, "start:dnsmasq") ||
		!strings.Contains(events, "start:systemd-resolved") ||
		!strings.Contains(events, "stop:systemd-resolved") ||
		!strings.Contains(events, "stop:dnsmasq") {
		t.Fatalf("inactive service lifecycle = %v", backend.events)
	}
}

func TestActivationFailureRollsBackFilesAndServices(t *testing.T) {
	backend := newFakeBackend()
	backend.failOperation = "restart:systemd-resolved"
	files := &fakeFiles{events: &backend.events}
	_, err := Apply(
		context.Background(),
		backend,
		files,
		validContract(),
	)
	if err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("apply error = %v", err)
	}
	if !files.rolledBack ||
		!backend.services["dnsmasq"].Active ||
		!backend.services["systemd-resolved"].Active {
		t.Fatalf("failed apply did not restore state")
	}
}

func TestDNSVerificationFailureRollsBack(t *testing.T) {
	backend := newFakeBackend()
	backend.addresses["docklane-preflight.docker.home.arpa"] =
		[]string{"192.0.2.10"}
	files := &fakeFiles{events: &backend.events}
	_, err := Apply(
		context.Background(),
		backend,
		files,
		validContract(),
	)
	if err == nil || !strings.Contains(err.Error(), "outside 127.0.0.1") {
		t.Fatalf("DNS verification error = %v", err)
	}
	if !files.rolledBack {
		t.Fatal("DNS verification failure did not restore files")
	}
}

func TestTrustVerificationFailureRollsBackBeforeServices(t *testing.T) {
	backend := newFakeBackend()
	backend.trustError = errors.New("anchor missing from bundle")
	files := &fakeFiles{events: &backend.events}
	_, err := Apply(
		context.Background(),
		backend,
		files,
		validContract(),
	)
	if err == nil || !strings.Contains(err.Error(), "anchor missing") {
		t.Fatalf("trust verification error = %v", err)
	}
	if !files.rolledBack {
		t.Fatal("trust verification failure did not restore files")
	}
	filesRestored := false
	for _, event := range backend.events {
		if event == "restore-files" {
			filesRestored = true
		}
		if (strings.HasPrefix(event, "restart:") ||
			strings.HasPrefix(event, "start:")) &&
			!filesRestored {
			t.Fatalf("service activated before rollback restored files: %v", backend.events)
		}
	}
}

func TestRollbackRefusesServiceDriftBeforeFiles(t *testing.T) {
	backend := newFakeBackend()
	files := &fakeFiles{events: &backend.events}
	transaction, err := Apply(
		context.Background(),
		backend,
		files,
		validContract(),
	)
	if err != nil {
		t.Fatal(err)
	}
	backend.services["systemd-resolved"] = ServiceState{}
	err = transaction.Rollback()
	if err == nil || !strings.Contains(err.Error(), "changed state") {
		t.Fatalf("service drift error = %v", err)
	}
	if files.rolledBack {
		t.Fatal("service drift allowed file rollback")
	}
	backend.services["systemd-resolved"] = ServiceState{Active: true}
	if err := transaction.Rollback(); err != nil {
		t.Fatalf("retry rollback after repairing drift: %v", err)
	}
}

func TestFileRollbackFailureStopsHostReload(t *testing.T) {
	backend := newFakeBackend()
	files := &fakeFiles{events: &backend.events, fail: true}
	transaction, err := Apply(
		context.Background(),
		backend,
		files,
		validContract(),
	)
	if err != nil {
		t.Fatal(err)
	}
	eventCount := len(backend.events)
	err = transaction.Rollback()
	if err == nil || !strings.Contains(err.Error(), "file rollback") {
		t.Fatalf("file rollback error = %v", err)
	}
	if len(backend.events) != eventCount+1 ||
		backend.events[eventCount] != "restore-files" {
		t.Fatalf("host reloaded after failed file restore: %v", backend.events[eventCount:])
	}
}
