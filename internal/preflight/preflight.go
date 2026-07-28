package preflight

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installmanifest"
)

const checkTimeout = 5 * time.Second

type PortState string

const (
	PortAvailable PortState = "available"
	PortInUse     PortState = "in_use"
)

type Config struct {
	BaseDomain     string
	ProxyNetwork   string
	DockerSocket   string
	ManifestPath   string
	DnsmasqConfig  string
	DnsmasqDir     string
	DnsmasqService string
}

type DockerInspector interface {
	ListContainers(context.Context) ([]docker.Container, error)
	InspectNetwork(context.Context, string) (docker.Network, error)
}

type HostInspector interface {
	ProbePort(context.Context, uint16) (PortState, error)
	LookupHost(context.Context, string) ([]string, error)
	LookPath(string) (string, error)
	ReadFile(string) ([]byte, error)
	ListConfigFiles(string) ([]string, error)
	ServiceActive(context.Context, string) (bool, error)
}

type SystemInspector struct{}

func (SystemInspector) ProbePort(
	ctx context.Context,
	port uint16,
) (PortState, error) {
	inspected := false
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		content, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("inspect listeners in %s: %w", path, err)
		}
		inspected = true
		if procNetTCPHasListener(string(content), port) {
			return PortInUse, nil
		}
	}
	listener, err := (&net.ListenConfig{}).Listen(
		ctx,
		"tcp4",
		fmt.Sprintf("0.0.0.0:%d", port),
	)
	if err == nil {
		listener.Close()
		return PortAvailable, nil
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return PortInUse, nil
	}
	if inspected && (errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.EPERM)) {
		return PortAvailable, nil
	}
	return "", err
}

func procNetTCPHasListener(content string, port uint16) bool {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[3] != "0A" {
			continue
		}
		addressParts := strings.Split(fields[1], ":")
		if len(addressParts) != 2 {
			continue
		}
		value, err := strconv.ParseUint(addressParts[1], 16, 16)
		if err == nil && uint16(value) == port {
			return true
		}
	}
	return false
}

func (SystemInspector) LookupHost(
	ctx context.Context,
	hostname string,
) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, hostname)
}

func (SystemInspector) LookPath(name string) (string, error) {
	return lookPath(name)
}

func (SystemInspector) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (SystemInspector) ListConfigFiles(directory string) ([]string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() &&
			strings.HasSuffix(strings.ToLower(entry.Name()), ".conf") {
			paths = append(paths, filepath.Join(directory, entry.Name()))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func (SystemInspector) ServiceActive(
	ctx context.Context,
	service string,
) (bool, error) {
	return serviceActive(ctx, service)
}

type Runner struct {
	config   Config
	docker   DockerInspector
	host     HostInspector
	manifest *installmanifest.Store
	now      func() time.Time
}

func New(
	config Config,
	dockerInspector DockerInspector,
	hostInspector HostInspector,
) (*Runner, error) {
	if err := (domain.InstallationSettings{
		BaseDomain:   config.BaseDomain,
		ProxyNetwork: config.ProxyNetwork,
	}).Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.DockerSocket) == "" {
		return nil, fmt.Errorf("preflight Docker socket is required")
	}
	if !filepath.IsAbs(config.DockerSocket) {
		return nil, fmt.Errorf("preflight Docker socket must be absolute")
	}
	if strings.TrimSpace(config.DnsmasqConfig) == "" ||
		strings.TrimSpace(config.DnsmasqDir) == "" ||
		strings.TrimSpace(config.DnsmasqService) == "" {
		return nil, fmt.Errorf("preflight dnsmasq settings are required")
	}
	if !filepath.IsAbs(config.DnsmasqConfig) ||
		!filepath.IsAbs(config.DnsmasqDir) {
		return nil, fmt.Errorf("preflight dnsmasq paths must be absolute")
	}
	manifestStore, err := installmanifest.NewStore(config.ManifestPath)
	if err != nil {
		return nil, err
	}
	return &Runner{
		config:   config,
		docker:   dockerInspector,
		host:     hostInspector,
		manifest: manifestStore,
		now:      time.Now,
	}, nil
}

func (runner *Runner) Run(ctx context.Context) domain.PreflightReport {
	report := domain.PreflightReport{
		Status:      domain.DiagnosticPass,
		GeneratedAt: runner.now().UTC(),
		Target: domain.PreflightTarget{
			BaseDomain:   runner.config.BaseDomain,
			ProxyNetwork: runner.config.ProxyNetwork,
			DockerSocket: runner.config.DockerSocket,
			ManifestPath: runner.config.ManifestPath,
		},
		Checks: []domain.DiagnosticCheck{},
	}
	containers, dockerErr := runner.docker.ListContainers(ctx)
	if dockerErr != nil {
		add(&report, fail(
			"docker-access",
			"docker",
			"Docker Engine is not accessible",
			dockerErr.Error(),
			"Verify the Docker daemon and permission to read the configured socket.",
		))
	} else {
		add(&report, pass(
			"docker-access",
			"docker",
			fmt.Sprintf("Docker Engine is accessible with %d running container(s)", len(containers)),
		))
	}
	gateways := reverseProxies(containers)
	add(&report, gatewayCheck(gateways, runner.config.ProxyNetwork, dockerErr))
	for _, port := range []uint16{80, 443} {
		add(&report, runner.portCheck(ctx, port, gateways, dockerErr))
	}
	add(&report, runner.networkCheck(ctx, dockerErr))
	add(&report, runner.dnsmasqBinaryCheck())
	add(&report, runner.dnsmasqServiceCheck(ctx))
	add(&report, runner.dnsmasqIncludeCheck())
	add(&report, runner.dnsmasqMappingCheck())
	add(&report, runner.resolverCheck(ctx))
	add(&report, runner.manifestCheck())
	return report
}

func add(report *domain.PreflightReport, check domain.DiagnosticCheck) {
	report.Checks = append(report.Checks, check)
	if check.Status == domain.DiagnosticFail {
		report.Status = domain.DiagnosticFail
	} else if check.Status == domain.DiagnosticWarn &&
		report.Status == domain.DiagnosticPass {
		report.Status = domain.DiagnosticWarn
	}
}

func reverseProxies(containers []docker.Container) []docker.Container {
	var gateways []docker.Container
	for _, container := range containers {
		if container.SystemRole == docker.SystemRoleReverseProxy {
			gateways = append(gateways, container)
		}
	}
	sort.Slice(gateways, func(i, j int) bool {
		return gateways[i].Name < gateways[j].Name
	})
	return gateways
}

func gatewayCheck(
	gateways []docker.Container,
	network string,
	dockerErr error,
) domain.DiagnosticCheck {
	if dockerErr != nil {
		return warn(
			"gateway",
			"traefik",
			"Existing Traefik ownership could not be inspected",
			"Resolve Docker access before deciding whether to create or adopt Traefik.",
		)
	}
	switch len(gateways) {
	case 0:
		return pass(
			"gateway",
			"traefik",
			"No existing global Traefik was detected; installation may create one",
		)
	case 1:
		gateway := gateways[0]
		if !gateway.HasNetwork(network) {
			return warn(
				"gateway",
				"traefik",
				fmt.Sprintf("Existing Traefik %s is an adoption candidate but is not on network %s", gateway.Name, network),
				"Review a plan to attach the adopted gateway without replacing its existing networks.",
			)
		}
		return pass(
			"gateway",
			"traefik",
			fmt.Sprintf("Existing Traefik %s is an adoption candidate on network %s", gateway.Name, network),
		)
	default:
		names := make([]string, 0, len(gateways))
		for _, gateway := range gateways {
			names = append(names, gateway.Name)
		}
		return fail(
			"gateway",
			"traefik",
			"Multiple global reverse-proxy candidates were detected",
			strings.Join(names, ", "),
			"Choose one gateway explicitly before installation.",
		)
	}
}

func (runner *Runner) portCheck(
	ctx context.Context,
	port uint16,
	gateways []docker.Container,
	dockerErr error,
) domain.DiagnosticCheck {
	checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	state, err := runner.host.ProbePort(checkCtx, port)
	id := fmt.Sprintf("port-%d", port)
	if err != nil {
		return fail(
			id,
			"host",
			fmt.Sprintf("Port %d availability could not be verified", port),
			err.Error(),
			"Run preflight with permission to inspect and bind host gateway ports.",
		)
	}
	if state == PortAvailable {
		return pass(
			id,
			"host",
			fmt.Sprintf("Host port %d is available for a new gateway", port),
		)
	}
	if dockerErr == nil &&
		len(gateways) == 1 &&
		publishesPort(gateways[0], port) {
		return pass(
			id,
			"host",
			fmt.Sprintf("Host port %d is occupied by the existing Traefik adoption candidate", port),
		)
	}
	return fail(
		id,
		"host",
		fmt.Sprintf("Host port %d is already in use without one adoptable Traefik", port),
		"",
		"Stop the conflicting listener or explicitly adopt the compatible Traefik that owns it.",
	)
}

func publishesPort(container docker.Container, port uint16) bool {
	for _, published := range container.PublishedPorts {
		if published == port {
			return true
		}
	}
	return false
}

func (runner *Runner) networkCheck(
	ctx context.Context,
	dockerErr error,
) domain.DiagnosticCheck {
	if dockerErr != nil {
		return warn(
			"proxy-network",
			"docker",
			"Proxy network compatibility could not be inspected",
			"Resolve Docker access and rerun preflight.",
		)
	}
	network, err := runner.docker.InspectNetwork(ctx, runner.config.ProxyNetwork)
	if errors.Is(err, docker.ErrNetworkNotFound) {
		return warn(
			"proxy-network",
			"docker",
			fmt.Sprintf("Proxy network %s does not exist yet", runner.config.ProxyNetwork),
			"The reviewed install plan may create a Docklane-owned bridge network.",
		)
	}
	if err != nil {
		return fail(
			"proxy-network",
			"docker",
			fmt.Sprintf("Proxy network %s could not be inspected", runner.config.ProxyNetwork),
			err.Error(),
			"Resolve the Docker network inspection error before installation.",
		)
	}
	if network.Driver != "bridge" ||
		network.Scope != "local" ||
		network.Internal {
		return fail(
			"proxy-network",
			"docker",
			fmt.Sprintf("Existing network %s is incompatible", network.Name),
			fmt.Sprintf("driver=%s scope=%s internal=%t", network.Driver, network.Scope, network.Internal),
			"Choose a local non-internal bridge network or a different network name.",
		)
	}
	return pass(
		"proxy-network",
		"docker",
		fmt.Sprintf("Existing network %s is compatible and will be preserved", network.Name),
	)
}

func (runner *Runner) dnsmasqBinaryCheck() domain.DiagnosticCheck {
	path, err := runner.host.LookPath("dnsmasq")
	if err != nil {
		return fail(
			"dnsmasq-binary",
			"dns",
			"dnsmasq is not installed",
			err.Error(),
			"Install dnsmasq before applying the local DNS plan.",
		)
	}
	return pass(
		"dnsmasq-binary",
		"dns",
		"dnsmasq is available at "+path,
	)
}

func (runner *Runner) dnsmasqServiceCheck(
	ctx context.Context,
) domain.DiagnosticCheck {
	checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	active, err := runner.host.ServiceActive(
		checkCtx,
		runner.config.DnsmasqService,
	)
	if err != nil {
		return warn(
			"dnsmasq-service",
			"dns",
			"dnsmasq service state could not be determined",
			"Verify the service manager before apply: "+err.Error(),
		)
	}
	if !active {
		return warn(
			"dnsmasq-service",
			"dns",
			"dnsmasq is installed but its service is not active",
			"The reviewed install plan must start it and record the prior service state.",
		)
	}
	return pass(
		"dnsmasq-service",
		"dns",
		"dnsmasq service is active",
	)
}

func (runner *Runner) dnsmasqIncludeCheck() domain.DiagnosticCheck {
	content, err := runner.host.ReadFile(runner.config.DnsmasqConfig)
	if err != nil {
		return fail(
			"dnsmasq-includes",
			"dns",
			"Primary dnsmasq configuration is not readable",
			err.Error(),
			"Verify the configured dnsmasq.conf path before installation.",
		)
	}
	if dnsmasqIncludesDirectory(string(content), runner.config.DnsmasqDir) {
		return pass(
			"dnsmasq-includes",
			"dns",
			"Primary dnsmasq configuration loads "+runner.config.DnsmasqDir,
		)
	}
	return warn(
		"dnsmasq-includes",
		"dns",
		"Primary dnsmasq configuration does not load "+runner.config.DnsmasqDir,
		"The installation plan must update the primary configuration or choose its active include directory.",
	)
}

type dnsmasqMapping struct {
	path    string
	address string
}

func (runner *Runner) dnsmasqMappingCheck() domain.DiagnosticCheck {
	paths, err := runner.host.ListConfigFiles(runner.config.DnsmasqDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(
			"dnsmasq-domain",
			"dns",
			"dnsmasq include directory could not be inspected",
			err.Error(),
			"Verify permissions and the configured include directory.",
		)
	}
	paths = append(paths, runner.config.DnsmasqConfig)
	sort.Strings(paths)
	var mappings []dnsmasqMapping
	var unreadable []string
	for _, path := range paths {
		content, readErr := runner.host.ReadFile(path)
		if readErr != nil {
			unreadable = append(unreadable, path+": "+readErr.Error())
			continue
		}
		for _, address := range dnsmasqAddresses(
			string(content),
			runner.config.BaseDomain,
		) {
			mappings = append(mappings, dnsmasqMapping{
				path: path, address: address,
			})
		}
	}
	if len(unreadable) > 0 {
		return fail(
			"dnsmasq-domain",
			"dns",
			"One or more dnsmasq configuration files could not be inspected",
			strings.Join(unreadable, "; "),
			"Fix file permissions before checking for conflicting local-domain mappings.",
		)
	}
	if len(mappings) == 0 {
		return warn(
			"dnsmasq-domain",
			"dns",
			"No dnsmasq wildcard mapping exists for "+runner.config.BaseDomain,
			"The installation plan may create a managed mapping to 127.0.0.1.",
		)
	}
	var correct, conflicting []string
	for _, mapping := range mappings {
		value := mapping.path + " -> " + mapping.address
		if mapping.address == "127.0.0.1" {
			correct = append(correct, value)
		} else {
			conflicting = append(conflicting, value)
		}
	}
	if len(conflicting) > 0 {
		return fail(
			"dnsmasq-domain",
			"dns",
			"Conflicting dnsmasq mappings exist for "+runner.config.BaseDomain,
			strings.Join(conflicting, "; "),
			"Remove or reconcile conflicting mappings before installation.",
		)
	}
	if len(correct) > 1 {
		return warn(
			"dnsmasq-domain",
			"dns",
			"Duplicate correct dnsmasq mappings exist for "+runner.config.BaseDomain,
			"Keep one mapping before Docklane records ownership: "+strings.Join(correct, "; "),
		)
	}
	return pass(
		"dnsmasq-domain",
		"dns",
		"dnsmasq maps "+runner.config.BaseDomain+" to 127.0.0.1 via "+mappings[0].path,
	)
}

func (runner *Runner) resolverCheck(ctx context.Context) domain.DiagnosticCheck {
	hostname := "docklane-preflight." + runner.config.BaseDomain
	checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	addresses, err := runner.host.LookupHost(checkCtx, hostname)
	if err != nil {
		return warn(
			"resolver-domain",
			"resolver",
			"The system resolver does not currently resolve "+runner.config.BaseDomain,
			"The reviewed install plan must configure split DNS before verification.",
		)
	}
	var loopback, other []string
	for _, address := range addresses {
		ip := net.ParseIP(address)
		if ip != nil && ip.IsLoopback() {
			loopback = append(loopback, address)
		} else {
			other = append(other, address)
		}
	}
	if len(loopback) == 0 {
		return fail(
			"resolver-domain",
			"resolver",
			"The local domain resolves away from loopback",
			strings.Join(addresses, ", "),
			"Remove the conflicting resolver rule before installation.",
		)
	}
	if len(other) > 0 {
		return warn(
			"resolver-domain",
			"resolver",
			"The local domain has mixed loopback and non-loopback answers",
			"Reconcile resolver sources: "+strings.Join(addresses, ", "),
		)
	}
	return pass(
		"resolver-domain",
		"resolver",
		"System resolver maps "+hostname+" to "+strings.Join(loopback, ", "),
	)
}

func (runner *Runner) manifestCheck() domain.DiagnosticCheck {
	manifest, err := runner.manifest.Load()
	if errors.Is(err, installmanifest.ErrNotFound) {
		return pass(
			"install-manifest",
			"state",
			"No existing installation manifest; a new planned manifest may be created",
		)
	}
	if err != nil {
		return fail(
			"install-manifest",
			"state",
			"Existing installation manifest is invalid or unreadable",
			err.Error(),
			"Repair or explicitly recover the manifest before installation.",
		)
	}
	return warn(
		"install-manifest",
		"state",
		fmt.Sprintf(
			"Installation manifest %s already exists in state %s at generation %d",
			manifest.InstallationID,
			manifest.State,
			manifest.Generation,
		),
		"Use the future upgrade or recovery workflow instead of creating a second installation.",
	)
}

func dnsmasqIncludesDirectory(content string, directory string) bool {
	expected := filepath.Clean(directory)
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		if !strings.HasPrefix(line, "conf-dir=") {
			continue
		}
		value := strings.TrimSpace(strings.SplitN(
			strings.TrimPrefix(line, "conf-dir="),
			",",
			2,
		)[0])
		if filepath.Clean(value) == expected {
			return true
		}
	}
	return false
}

func dnsmasqAddresses(content string, baseDomain string) []string {
	var addresses []string
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		if !strings.HasPrefix(line, "address=/") {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(line, "address=/"), "/")
		if len(parts) < 2 {
			continue
		}
		expected := strings.ToLower(strings.TrimSuffix(baseDomain, "."))
		matches := false
		for _, candidate := range parts[:len(parts)-1] {
			domainName := strings.TrimPrefix(
				strings.TrimSuffix(strings.ToLower(strings.TrimSpace(candidate)), "."),
				".",
			)
			if domainName == expected {
				matches = true
				break
			}
		}
		if matches {
			addresses = append(
				addresses,
				strings.TrimSpace(parts[len(parts)-1]),
			)
		}
	}
	return addresses
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
