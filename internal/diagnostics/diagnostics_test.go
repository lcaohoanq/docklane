package diagnostics

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"docklane.local/docklane/internal/client"
	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
)

type fakeController struct {
	health     domain.ControllerHealth
	healthErr  error
	containers []docker.Container
	routes     client.Routes
	runtime    domain.TraefikRouteRuntime
	runtimeErr error
	probe      domain.UpstreamProbe
	probeErr   error
}

func (controller fakeController) InspectTraefikRuntime(
	context.Context,
	int64,
) (domain.TraefikRouteRuntime, error) {
	return controller.runtime, controller.runtimeErr
}

func (controller fakeController) Health(
	context.Context,
) (domain.ControllerHealth, error) {
	return controller.health, controller.healthErr
}

func (controller fakeController) ListContainersWithNetworkAliases(
	context.Context,
) ([]docker.Container, error) {
	return controller.containers, nil
}

func (controller fakeController) ListRoutes(context.Context) (client.Routes, error) {
	return controller.routes, nil
}

func (controller fakeController) ProbeUpstream(
	context.Context,
	int64,
) (domain.UpstreamProbe, error) {
	return controller.probe, controller.probeErr
}

type fakeProber struct {
	addresses   []string
	dnsErr      error
	tcp80Err    error
	tcp443Err   error
	redirect    int
	location    string
	redirectErr error
	tlsErr      error
	status      int
	httpErr     error
}

func (prober fakeProber) LookupHost(context.Context, string) ([]string, error) {
	return prober.addresses, prober.dnsErr
}

func (prober fakeProber) DialTCP(_ context.Context, address string) error {
	if strings.HasSuffix(address, ":80") {
		return prober.tcp80Err
	}
	return prober.tcp443Err
}

func (prober fakeProber) ProbeHTTPRedirect(
	context.Context,
	string,
) (int, string, error) {
	return prober.redirect, prober.location, prober.redirectErr
}

func (prober fakeProber) ProbeTLS(context.Context, string) (time.Time, error) {
	return time.Date(2027, 7, 28, 0, 0, 0, 0, time.UTC), prober.tlsErr
}

func (prober fakeProber) GetHTTPS(context.Context, string) (int, error) {
	return prober.status, prober.httpErr
}

func TestRunHealthyRoute(t *testing.T) {
	route := domain.Route{
		ID:       7,
		Revision: 2,
		Name:     "draw",
		Selector: domain.ContainerSelector{ContainerID: "abc"},
		Port:     80,
		Scheme:   "http",
		Enabled:  true,
		Observed: domain.RouteObservation{
			State:       domain.RouteStateReady,
			UpstreamURL: "http://docklane-route-7:80",
		},
	}
	report := Run(
		context.Background(),
		fakeController{
			health: domain.ControllerHealth{
				Status:       "ok",
				BaseDomain:   "docker.home.arpa",
				ProxyNetwork: "proxy",
				Provider: domain.ProviderStatus{
					Source: domain.ProviderSourceLive,
				},
			},
			containers: []docker.Container{{
				ID:           "abc123",
				Name:         "draw",
				ExposedPorts: []uint16{80},
				Networks:     []string{"proxy"},
				NetworkAliases: map[string][]string{
					"proxy": {"docklane-route-7"},
				},
			}},
			routes: client.Routes{
				BaseDomain: "docker.home.arpa",
				Routes:     []domain.Route{route},
			},
			runtime: domain.TraefikRouteRuntime{
				Providers: []string{"Docker", "File", "HTTP"},
				Router: domain.TraefikRuntimeComponent{
					Name:    "draw@http",
					Present: true,
					Status:  "enabled",
				},
				Service: domain.TraefikRuntimeComponent{
					Name:    "draw@http",
					Present: true,
					Status:  "enabled",
				},
				ServerStatus: map[string]string{
					"http://docklane-route-7:80": "UP",
				},
			},
			probe: domain.UpstreamProbe{
				Reachable:  true,
				HTTPStatus: 200,
				DurationMS: 2,
			},
		},
		fakeProber{
			addresses: []string{"127.0.0.1"},
			redirect:  308,
			location:  "https://draw.docker.home.arpa/",
			status:    200,
		},
		"draw.docker.home.arpa",
	)
	if report.Status != domain.DiagnosticPass ||
		report.Target != "draw" ||
		report.Hostname != "draw.docker.home.arpa" {
		t.Fatalf("report = %#v", report)
	}
	for _, check := range report.Checks {
		if check.Status != domain.DiagnosticPass {
			t.Fatalf("check = %#v", check)
		}
	}
}

func TestTraefikRuntimeChecksExplainMissingRouter(t *testing.T) {
	report := diagnosticReport{
		Status: domain.DiagnosticPass,
		Checks: []domain.DiagnosticCheck{},
	}
	report.addTraefikRuntimeChecks(domain.TraefikRouteRuntime{
		Providers: []string{"Docker", "File"},
		Router: domain.TraefikRuntimeComponent{
			Name: "draw@http",
		},
		Service: domain.TraefikRuntimeComponent{
			Name: "draw@http",
		},
	}, nil)
	result := domain.DiagnosticReport(report)
	assertCheck(
		t,
		result,
		"traefik-provider-runtime",
		domain.DiagnosticFail,
		"not loaded",
	)
	assertCheck(
		t,
		result,
		"traefik-router-runtime",
		domain.DiagnosticFail,
		"is missing",
	)
	assertCheck(
		t,
		result,
		"traefik-service-runtime",
		domain.DiagnosticFail,
		"is missing",
	)
}

func TestRunExplainsUnresolvedRouteAndDNSFailure(t *testing.T) {
	report := Run(
		context.Background(),
		fakeController{
			health: domain.ControllerHealth{
				BaseDomain: "docker.home.arpa",
				Provider: domain.ProviderStatus{
					Source: domain.ProviderSourceLastKnownGood,
				},
			},
			routes: client.Routes{
				BaseDomain: "docker.home.arpa",
				Routes: []domain.Route{{
					ID:       9,
					Name:     "missing",
					Selector: domain.ContainerSelector{ContainerID: "gone"},
					Port:     8080,
					Scheme:   "http",
					Enabled:  true,
					Observed: domain.RouteObservation{
						State:   domain.RouteStateUnresolved,
						Message: "no running container matches",
					},
				}},
			},
		},
		fakeProber{dnsErr: errors.New("no such host")},
		"9",
	)
	if report.Status != domain.DiagnosticFail {
		t.Fatalf("status = %q", report.Status)
	}
	assertCheck(t, report, "provider", domain.DiagnosticWarn, "last-known-good")
	assertCheck(t, report, "workload-selector", domain.DiagnosticFail, "does not resolve")
	assertCheck(t, report, "dns", domain.DiagnosticFail, "does not resolve")
	if findCheck(report, "tcp-443") != nil {
		t.Fatal("TCP probe ran after DNS failure")
	}
}

func TestRunReportsUnreachableControllerWithoutFurtherChecks(t *testing.T) {
	report := Run(
		context.Background(),
		fakeController{healthErr: errors.New("connection refused")},
		fakeProber{},
		"",
	)
	if report.Status != domain.DiagnosticFail || len(report.Checks) != 1 {
		t.Fatalf("report = %#v", report)
	}
	assertCheck(t, report, "controller", domain.DiagnosticFail, "unreachable")
}

func TestAliasCheckRejectsMissingContainerNameFallback(t *testing.T) {
	check := aliasCheck(
		domain.Route{Observed: domain.RouteObservation{
			UpstreamURL: "http://draw:80",
		}},
		docker.Container{Name: "draw"},
		"proxy",
	)
	if check.Status != domain.DiagnosticFail ||
		!strings.Contains(check.Summary, "draw is missing") {
		t.Fatalf("check = %#v", check)
	}
}

func assertCheck(
	t *testing.T,
	report domain.DiagnosticReport,
	id string,
	status domain.DiagnosticStatus,
	text string,
) {
	t.Helper()
	check := findCheck(report, id)
	if check == nil ||
		check.Status != status ||
		!strings.Contains(check.Summary+" "+check.Detail, text) {
		t.Fatalf("check %s = %#v", id, check)
	}
}

func findCheck(
	report domain.DiagnosticReport,
	id string,
) *domain.DiagnosticCheck {
	for index := range report.Checks {
		if report.Checks[index].ID == id {
			return &report.Checks[index]
		}
	}
	return nil
}
