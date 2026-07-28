package diagnostics

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"docklane.local/docklane/internal/client"
	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
)

const probeTimeout = 5 * time.Second

type Controller interface {
	Health(context.Context) (domain.ControllerHealth, error)
	ListContainers(context.Context) ([]docker.Container, error)
	ListRoutes(context.Context) (client.Routes, error)
}

type Prober interface {
	LookupHost(context.Context, string) ([]string, error)
	DialTCP(context.Context, string) error
	ProbeTLS(context.Context, string) (time.Time, error)
	GetHTTPS(context.Context, string) (int, error)
}

type SystemProber struct{}

type diagnosticReport domain.DiagnosticReport

func (SystemProber) LookupHost(ctx context.Context, hostname string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, hostname)
}

func (SystemProber) DialTCP(ctx context.Context, address string) error {
	connection, err := (&net.Dialer{Timeout: probeTimeout}).DialContext(
		ctx,
		"tcp",
		address,
	)
	if err != nil {
		return err
	}
	return connection.Close()
}

func (SystemProber) ProbeTLS(ctx context.Context, hostname string) (time.Time, error) {
	connection, err := (&tls.Dialer{
		NetDialer: &net.Dialer{Timeout: probeTimeout},
		Config: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: hostname,
		},
	}).DialContext(ctx, "tcp", net.JoinHostPort(hostname, "443"))
	if err != nil {
		return time.Time{}, err
	}
	defer connection.Close()
	tlsConnection, ok := connection.(*tls.Conn)
	if !ok {
		return time.Time{}, errors.New("TLS probe returned a non-TLS connection")
	}
	state := tlsConnection.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return time.Time{}, errors.New("server returned no certificate")
	}
	return state.PeerCertificates[0].NotAfter, nil
}

func (SystemProber) GetHTTPS(ctx context.Context, hostname string) (int, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"https://"+hostname+"/",
		nil,
	)
	if err != nil {
		return 0, err
	}
	httpClient := &http.Client{
		Timeout: probeTimeout,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: (&net.Dialer{
				Timeout: probeTimeout,
			}).DialContext,
			TLSHandshakeTimeout: probeTimeout,
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
		},
		CheckRedirect: func(_ *http.Request, previous []*http.Request) error {
			if len(previous) >= 5 {
				return errors.New("stopped after 5 redirects")
			}
			return nil
		},
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	return response.StatusCode, nil
}

func Run(
	ctx context.Context,
	controller Controller,
	prober Prober,
	target string,
) domain.DiagnosticReport {
	report := diagnosticReport{
		Status:      domain.DiagnosticPass,
		Target:      target,
		GeneratedAt: time.Now().UTC(),
		Checks:      []domain.DiagnosticCheck{},
	}
	health, err := controller.Health(ctx)
	if err != nil {
		report.add(fail(
			"controller",
			"control-plane",
			"Docklane controller is unreachable",
			err.Error(),
			"Start Docklane or set --url/DOCKLANE_URL to the active controller.",
		))
		return domain.DiagnosticReport(report)
	}
	report.add(pass(
		"controller",
		"control-plane",
		"Docklane controller responded",
	))
	if health.LastReconcileError != "" {
		report.add(fail(
			"reconciliation",
			"docker",
			"Container reconciliation is failing",
			health.LastReconcileError,
			"Check Docker socket access and inspect the affected container state.",
		))
	} else {
		report.add(pass(
			"reconciliation",
			"docker",
			"Container reconciliation is healthy",
		))
	}
	report.add(providerCheck(health.Provider))

	containers, discoveryErr := controller.ListContainers(ctx)
	if discoveryErr != nil {
		report.add(fail(
			"docker-discovery",
			"docker",
			"Docker container discovery failed",
			discoveryErr.Error(),
			"Verify that Docklane can access /var/run/docker.sock.",
		))
	} else {
		report.add(pass(
			"docker-discovery",
			"docker",
			fmt.Sprintf("Discovered %d running container(s)", len(containers)),
		))
	}
	if target == "" {
		return domain.DiagnosticReport(report)
	}

	routes, err := controller.ListRoutes(ctx)
	if err != nil {
		report.add(fail(
			"route",
			"route",
			"Saved routes could not be loaded",
			err.Error(),
			"Check controller health and the Docklane database.",
		))
		return domain.DiagnosticReport(report)
	}
	route, found := findRoute(target, routes)
	if !found {
		report.add(fail(
			"route",
			"route",
			fmt.Sprintf("Route %q does not exist", target),
			"",
			"Run `docklane route list` and use a route ID, name, or full hostname.",
		))
		return domain.DiagnosticReport(report)
	}
	report.Target = route.Name
	report.Hostname = route.Hostname(routes.BaseDomain)
	if route.Enabled {
		report.add(pass("route-enabled", "route", "Route is enabled"))
	} else {
		report.add(warn(
			"route-enabled",
			"route",
			"Route is disabled",
			"Enable it with `docklane route enable "+strconv.FormatInt(route.ID, 10)+"`.",
		))
	}
	report.add(observationCheck(route))
	if discoveryErr == nil {
		report.addWorkloadChecks(route, containers, health.ProxyNetwork)
	}
	report.addHostChecks(ctx, prober)
	return domain.DiagnosticReport(report)
}

func (report *diagnosticReport) add(check domain.DiagnosticCheck) {
	report.Checks = append(report.Checks, check)
	if check.Status == domain.DiagnosticFail {
		report.Status = domain.DiagnosticFail
	} else if check.Status == domain.DiagnosticWarn &&
		report.Status == domain.DiagnosticPass {
		report.Status = domain.DiagnosticWarn
	}
}

func (report *diagnosticReport) addWorkloadChecks(
	route domain.Route,
	containers []docker.Container,
	proxyNetwork string,
) {
	container, err := docker.ResolveContainer(route.Selector, containers)
	if err != nil {
		report.add(fail(
			"workload-selector",
			"docker",
			"Route selector does not resolve to one running container",
			err.Error(),
			"Start the workload or update the route selector.",
		))
		return
	}
	report.add(pass(
		"workload-selector",
		"docker",
		"Route resolves to container "+container.Name,
	))
	if err := docker.ValidateTCPPort(container, route.Port); err != nil {
		report.add(fail(
			"upstream-port",
			"upstream",
			"Configured upstream port is not declared",
			err.Error(),
			"Select one of the container's declared TCP ports.",
		))
	} else {
		report.add(pass(
			"upstream-port",
			"upstream",
			fmt.Sprintf("Container declares TCP port %d", route.Port),
		))
	}
	if proxyNetwork == "" {
		report.add(warn(
			"proxy-network",
			"network",
			"No shared proxy network is configured",
			"Configure --proxy-network on the Docklane controller.",
		))
	} else if container.HasNetwork(proxyNetwork) {
		report.add(pass(
			"proxy-network",
			"network",
			fmt.Sprintf("Container is attached to %s", proxyNetwork),
		))
	} else {
		report.add(fail(
			"proxy-network",
			"network",
			fmt.Sprintf("Container is not attached to %s", proxyNetwork),
			"",
			"Review `docklane network plan`, then apply the reviewed plan.",
		))
	}
}

func (report *diagnosticReport) addHostChecks(
	ctx context.Context,
	prober Prober,
) {
	hostname := report.Hostname
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	addresses, err := prober.LookupHost(probeCtx, hostname)
	cancel()
	if err != nil || len(addresses) == 0 {
		detail := "no address returned"
		if err != nil {
			detail = err.Error()
		}
		report.add(fail(
			"dns",
			"dns",
			"Hostname does not resolve",
			detail,
			"Check the local wildcard DNS rule and query the configured resolver.",
		))
		return
	}
	report.add(pass(
		"dns",
		"dns",
		fmt.Sprintf("%s resolves to %s", hostname, strings.Join(addresses, ", ")),
	))

	probeCtx, cancel = context.WithTimeout(ctx, probeTimeout)
	err = prober.DialTCP(probeCtx, net.JoinHostPort(hostname, "443"))
	cancel()
	if err != nil {
		report.add(fail(
			"tcp-443",
			"network",
			"TCP port 443 is unreachable",
			err.Error(),
			"Verify Traefik is running and publishing host port 443.",
		))
		return
	}
	report.add(pass("tcp-443", "network", "TCP port 443 accepts connections"))

	probeCtx, cancel = context.WithTimeout(ctx, probeTimeout)
	expiresAt, err := prober.ProbeTLS(probeCtx, hostname)
	cancel()
	if err != nil {
		report.add(fail(
			"tls",
			"tls",
			"TLS certificate validation failed",
			err.Error(),
			"Verify local CA trust and that the certificate SAN covers the hostname.",
		))
		return
	}
	report.add(pass(
		"tls",
		"tls",
		"Trusted certificate covers the hostname and expires "+expiresAt.Format(time.DateOnly),
	))

	probeCtx, cancel = context.WithTimeout(ctx, probeTimeout)
	statusCode, err := prober.GetHTTPS(probeCtx, hostname)
	cancel()
	if err != nil {
		report.add(fail(
			"https",
			"http",
			"HTTPS request failed",
			err.Error(),
			"Inspect redirects and the Traefik access log for this hostname.",
		))
		return
	}
	switch {
	case statusCode == http.StatusNotFound:
		report.add(fail(
			"https",
			"http",
			"HTTPS returned 404 Not Found",
			"",
			"Verify the route is present in Traefik and the hostname rule matches.",
		))
	case statusCode >= 500:
		report.add(fail(
			"https",
			"http",
			fmt.Sprintf("HTTPS returned %d", statusCode),
			"",
			"Check the shared network, upstream port, and application logs.",
		))
	case statusCode >= 400:
		report.add(warn(
			"https",
			"http",
			fmt.Sprintf("HTTPS reached a service that returned %d", statusCode),
			"Inspect application or router authentication rules if this is unexpected.",
		))
	default:
		report.add(pass(
			"https",
			"http",
			fmt.Sprintf("HTTPS returned %d", statusCode),
		))
	}
}

func findRoute(target string, routes client.Routes) (domain.Route, bool) {
	normalized := strings.ToLower(strings.TrimSpace(target))
	normalized = strings.TrimSuffix(normalized, ".")
	for _, route := range routes.Routes {
		if normalized == strconv.FormatInt(route.ID, 10) ||
			normalized == strings.ToLower(route.Name) ||
			normalized == strings.ToLower(route.Hostname(routes.BaseDomain)) {
			return route, true
		}
	}
	return domain.Route{}, false
}

func observationCheck(route domain.Route) domain.DiagnosticCheck {
	switch route.Observed.State {
	case domain.RouteStateReady:
		return pass(
			"route-observation",
			"route",
			"Route is eligible for provider publication",
		)
	case domain.RouteStateDisabled:
		return warn(
			"route-observation",
			"route",
			"Disabled route is not published",
			"Enable the route after resolving any hostname-label migration.",
		)
	case domain.RouteStateUnresolved:
		return fail(
			"route-observation",
			"route",
			"Route workload is unresolved",
			route.Observed.Message,
			"Start the selected Compose service or update the route selector.",
		)
	case domain.RouteStateAmbiguous:
		return fail(
			"route-observation",
			"route",
			"Route selector is ambiguous",
			route.Observed.Message,
			"Use a selector that identifies exactly one running workload.",
		)
	default:
		return fail(
			"route-observation",
			"route",
			"Route reconciliation reports an error",
			route.Observed.Message,
			"Resolve the reported route error and wait for reconciliation.",
		)
	}
}

func providerCheck(status domain.ProviderStatus) domain.DiagnosticCheck {
	switch status.Source {
	case domain.ProviderSourceLive:
		if status.LastError == "" {
			return pass("provider", "traefik", "Traefik provider document is live")
		}
		return warn(
			"provider",
			"traefik",
			"Live provider snapshot could not be persisted",
			status.LastError,
		)
	case domain.ProviderSourceLastKnownGood:
		return warn(
			"provider",
			"traefik",
			"Provider is serving last-known-good configuration",
			status.LastError,
		)
	case domain.ProviderSourceUnavailable:
		return fail(
			"provider",
			"traefik",
			"Traefik provider configuration is unavailable",
			status.LastError,
			"Restore Docker/controller state, then verify /internal/traefik returns HTTP 200.",
		)
	default:
		return warn(
			"provider",
			"traefik",
			"Traefik has not polled the provider since controller startup",
			"Wait for the provider poll interval and run doctor again.",
		)
	}
}

func pass(id, layer, summary string) domain.DiagnosticCheck {
	return domain.DiagnosticCheck{
		ID: id, Layer: layer, Status: domain.DiagnosticPass, Summary: summary,
	}
}

func warn(id, layer, summary, suggestion string) domain.DiagnosticCheck {
	return domain.DiagnosticCheck{
		ID: id, Layer: layer, Status: domain.DiagnosticWarn,
		Summary: summary, Suggestion: suggestion,
	}
}

func fail(
	id string,
	layer string,
	summary string,
	detail string,
	suggestion string,
) domain.DiagnosticCheck {
	return domain.DiagnosticCheck{
		ID: id, Layer: layer, Status: domain.DiagnosticFail,
		Summary: summary, Detail: detail, Suggestion: suggestion,
	}
}
