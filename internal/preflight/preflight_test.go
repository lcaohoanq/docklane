package preflight

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
)

type fakeDockerInspector struct {
	containers []docker.Container
	listErr    error
	network    docker.Network
	networkErr error
}

func (inspector fakeDockerInspector) ListContainers(
	context.Context,
) ([]docker.Container, error) {
	return inspector.containers, inspector.listErr
}

func (inspector fakeDockerInspector) InspectNetwork(
	context.Context,
	string,
) (docker.Network, error) {
	return inspector.network, inspector.networkErr
}

type fakeHostInspector struct {
	ports       map[uint16]PortState
	portErr     error
	addresses   []string
	lookupErr   error
	executable  string
	lookPathErr error
	files       map[string]string
	configFiles []string
	listErr     error
	active      bool
	serviceErr  error
}

func (inspector fakeHostInspector) ProbePort(
	context.Context,
	uint16,
) (PortState, error) {
	return "", inspector.portErr
}

func (inspector fakeHostInspector) LookupHost(
	context.Context,
	string,
) ([]string, error) {
	return inspector.addresses, inspector.lookupErr
}

func (inspector fakeHostInspector) LookPath(string) (string, error) {
	return inspector.executable, inspector.lookPathErr
}

func (inspector fakeHostInspector) ReadFile(path string) ([]byte, error) {
	content, exists := inspector.files[path]
	if !exists {
		return nil, os.ErrNotExist
	}
	return []byte(content), nil
}

func (inspector fakeHostInspector) ListConfigFiles(
	string,
) ([]string, error) {
	return inspector.configFiles, inspector.listErr
}

func (inspector fakeHostInspector) ServiceActive(
	context.Context,
	string,
) (bool, error) {
	return inspector.active, inspector.serviceErr
}

type portAwareHost struct {
	fakeHostInspector
}

func (inspector portAwareHost) ProbePort(
	_ context.Context,
	port uint16,
) (PortState, error) {
	if inspector.portErr != nil {
		return "", inspector.portErr
	}
	return inspector.ports[port], nil
}

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		BaseDomain:     "docker.home.arpa",
		ProxyNetwork:   "proxy",
		DockerSocket:   "/var/run/docker.sock",
		ManifestPath:   filepath.Join(t.TempDir(), "install-manifest.json"),
		DnsmasqConfig:  "/etc/dnsmasq.conf",
		DnsmasqDir:     "/etc/dnsmasq.d",
		DnsmasqService: "dnsmasq",
	}
}

func healthyHost() portAwareHost {
	return portAwareHost{fakeHostInspector: fakeHostInspector{
		ports:      map[uint16]PortState{80: PortInUse, 443: PortInUse},
		addresses:  []string{"127.0.0.1"},
		executable: "/usr/bin/dnsmasq",
		files: map[string]string{
			"/etc/dnsmasq.conf":       "conf-dir=/etc/dnsmasq.d,.bak\n",
			"/etc/dnsmasq.d/lab.conf": "address=/.docker.home.arpa/127.0.0.1\n",
		},
		configFiles: []string{"/etc/dnsmasq.d/lab.conf"},
		active:      true,
	}}
}

func TestRunPassesForCompatibleExistingGateway(t *testing.T) {
	config := testConfig(t)
	dockerInspector := fakeDockerInspector{
		containers: []docker.Container{{
			ID:             "traefik123",
			Name:           "traefik",
			Image:          "traefik:v3.7",
			SystemRole:     docker.SystemRoleReverseProxy,
			PublishedPorts: []uint16{80, 443},
			Networks:       []string{"proxy"},
		}},
		network: docker.Network{
			ID: "network123", Name: "proxy", Driver: "bridge", Scope: "local",
		},
	}
	runner, err := New(config, dockerInspector, healthyHost())
	if err != nil {
		t.Fatal(err)
	}
	runner.now = func() time.Time {
		return time.Date(2026, time.July, 28, 5, 0, 0, 0, time.UTC)
	}
	report := runner.Run(context.Background())
	if report.Status != domain.DiagnosticPass {
		t.Fatalf("report = %#v", report)
	}
	assertCheck(t, report, "gateway", domain.DiagnosticPass, "adoption candidate")
	assertCheck(t, report, "port-80", domain.DiagnosticPass, "existing Traefik")
	assertCheck(t, report, "dnsmasq-domain", domain.DiagnosticPass, "127.0.0.1")
	assertCheck(t, report, "resolver-domain", domain.DiagnosticPass, "127.0.0.1")
}

func TestRunWarnsForCleanUnconfiguredHost(t *testing.T) {
	config := testConfig(t)
	host := healthyHost()
	host.ports = map[uint16]PortState{80: PortAvailable, 443: PortAvailable}
	host.active = false
	host.lookupErr = errors.New("not found")
	host.configFiles = nil
	host.files = map[string]string{
		"/etc/dnsmasq.conf": "conf-dir=/etc/dnsmasq.d\n",
	}
	runner, err := New(
		config,
		fakeDockerInspector{networkErr: docker.ErrNetworkNotFound},
		host,
	)
	if err != nil {
		t.Fatal(err)
	}
	report := runner.Run(context.Background())
	if report.Status != domain.DiagnosticWarn {
		t.Fatalf("status = %q, checks = %#v", report.Status, report.Checks)
	}
	assertCheck(t, report, "gateway", domain.DiagnosticPass, "may create")
	assertCheck(t, report, "port-443", domain.DiagnosticPass, "available")
	assertCheck(t, report, "proxy-network", domain.DiagnosticWarn, "does not exist")
	assertCheck(t, report, "dnsmasq-domain", domain.DiagnosticWarn, "No dnsmasq")
	assertCheck(t, report, "resolver-domain", domain.DiagnosticWarn, "does not currently")
}

func TestRunBlocksConflictingHostState(t *testing.T) {
	config := testConfig(t)
	host := healthyHost()
	host.addresses = []string{"192.0.2.50"}
	host.files["/etc/dnsmasq.d/lab.conf"] =
		"address=/.docker.home.arpa/192.0.2.50\n"
	runner, err := New(
		config,
		fakeDockerInspector{network: docker.Network{
			Name: "proxy", Driver: "overlay", Scope: "swarm", Internal: true,
		}},
		host,
	)
	if err != nil {
		t.Fatal(err)
	}
	report := runner.Run(context.Background())
	if report.Status != domain.DiagnosticFail {
		t.Fatalf("status = %q, checks = %#v", report.Status, report.Checks)
	}
	assertCheck(t, report, "port-80", domain.DiagnosticFail, "without one adoptable")
	assertCheck(t, report, "proxy-network", domain.DiagnosticFail, "incompatible")
	assertCheck(t, report, "dnsmasq-domain", domain.DiagnosticFail, "Conflicting")
	assertCheck(t, report, "resolver-domain", domain.DiagnosticFail, "away from loopback")
}

func TestRunDoesNotAttributeAnotherListenerToTraefik(t *testing.T) {
	config := testConfig(t)
	host := healthyHost()
	runner, err := New(
		config,
		fakeDockerInspector{
			containers: []docker.Container{{
				Name:           "traefik",
				SystemRole:     docker.SystemRoleReverseProxy,
				PublishedPorts: []uint16{443},
				Networks:       []string{"proxy"},
			}},
			network: docker.Network{
				Name: "proxy", Driver: "bridge", Scope: "local",
			},
		},
		host,
	)
	if err != nil {
		t.Fatal(err)
	}
	report := runner.Run(context.Background())
	assertCheck(t, report, "port-80", domain.DiagnosticFail, "without one adoptable")
	assertCheck(t, report, "port-443", domain.DiagnosticPass, "existing Traefik")
}

func TestRunBlocksWhenDockerIsUnavailable(t *testing.T) {
	config := testConfig(t)
	host := healthyHost()
	host.ports = map[uint16]PortState{80: PortAvailable, 443: PortAvailable}
	runner, err := New(
		config,
		fakeDockerInspector{listErr: errors.New("permission denied")},
		host,
	)
	if err != nil {
		t.Fatal(err)
	}
	report := runner.Run(context.Background())
	assertCheck(t, report, "docker-access", domain.DiagnosticFail, "not accessible")
	assertCheck(t, report, "gateway", domain.DiagnosticWarn, "could not")
}

func TestDnsmasqParsers(t *testing.T) {
	content := `
# conf-dir=/wrong
conf-dir=/etc/dnsmasq.d,.bak
address=/.docker.home.arpa/127.0.0.1
address=/docker.home.arpa/127.0.0.2
address=/unrelated.test/docker.home.arpa/127.0.0.3
address=/.other.test/192.0.2.1
`
	if !dnsmasqIncludesDirectory(content, "/etc/dnsmasq.d") {
		t.Fatal("include directory not detected")
	}
	addresses := dnsmasqAddresses(content, "docker.home.arpa")
	if strings.Join(addresses, ",") != "127.0.0.1,127.0.0.2,127.0.0.3" {
		t.Fatalf("addresses = %v", addresses)
	}
}

func TestProcNetTCPListenerParser(t *testing.T) {
	content := `  sl  local_address rem_address   st
   0: 00000000:0050 00000000:0000 0A
   1: 0100007F:01BB 00000000:0000 0A
   2: 0100007F:1234 00000000:0000 01
`
	if !procNetTCPHasListener(content, 80) {
		t.Fatal("port 80 listener not detected")
	}
	if !procNetTCPHasListener(content, 443) {
		t.Fatal("port 443 listener not detected")
	}
	if procNetTCPHasListener(content, 0x1234) {
		t.Fatal("non-listening socket detected")
	}
}

func assertCheck(
	t *testing.T,
	report domain.PreflightReport,
	id string,
	status domain.DiagnosticStatus,
	contains string,
) {
	t.Helper()
	for _, check := range report.Checks {
		if check.ID != id {
			continue
		}
		if check.Status != status ||
			!strings.Contains(check.Summary+" "+check.Detail, contains) {
			t.Fatalf("check %s = %#v", id, check)
		}
		return
	}
	t.Fatalf("check %s not found", id)
}
