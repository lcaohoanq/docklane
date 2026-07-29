package preflight

import (
	"bytes"
	"context"
	"crypto"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
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
	BaseDomain      string
	ProxyNetwork    string
	DockerSocket    string
	ManifestPath    string
	DnsmasqConfig   string
	DnsmasqDir      string
	DnsmasqService  string
	TrustAnchorPath string
	RuntimeDataPath string
}

type DockerInspector interface {
	ListContainers(context.Context) ([]docker.Container, error)
	InspectNetwork(context.Context, string) (docker.Network, error)
	InspectContainerRuntime(context.Context, string) (docker.ContainerRuntime, error)
	InspectVolume(context.Context, string) (docker.Volume, error)
}

type HostFileInfo struct {
	Mode os.FileMode
}

type HostInspector interface {
	ProbePort(context.Context, uint16) (PortState, error)
	LookupHost(context.Context, string) ([]string, error)
	LookPath(string) (string, error)
	ReadFile(string) ([]byte, error)
	ListConfigFiles(string) ([]string, error)
	ServiceActive(context.Context, string) (bool, error)
	Stat(string) (HostFileInfo, error)
	ProbeTLSCertificate(context.Context, string) ([]byte, error)
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

func (SystemInspector) Stat(path string) (HostFileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return HostFileInfo{}, err
	}
	return HostFileInfo{Mode: info.Mode()}, nil
}

func (SystemInspector) ProbeTLSCertificate(
	ctx context.Context,
	hostname string,
) ([]byte, error) {
	dialer := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: checkTimeout},
		Config: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: hostname,
		},
	}
	connection, err := dialer.DialContext(
		ctx,
		"tcp",
		net.JoinHostPort(hostname, "443"),
	)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	tlsConnection, ok := connection.(*tls.Conn)
	if !ok {
		return nil, fmt.Errorf("TLS probe returned non-TLS connection")
	}
	certificates := tlsConnection.ConnectionState().PeerCertificates
	if len(certificates) == 0 {
		return nil, fmt.Errorf("TLS server returned no certificate")
	}
	return append([]byte(nil), certificates[0].Raw...), nil
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
	if !filepath.IsAbs(config.TrustAnchorPath) {
		return nil, fmt.Errorf("preflight trust anchor path must be absolute")
	}
	if !filepath.IsAbs(config.RuntimeDataPath) {
		return nil, fmt.Errorf("preflight runtime data path must be absolute")
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
	report.Inventory.Gateway = gatewayInventory(gateways, dockerErr)
	add(&report, gatewayCheck(gateways, runner.config.ProxyNetwork, dockerErr))
	for _, port := range []uint16{80, 443} {
		add(&report, runner.portCheck(ctx, port, gateways, dockerErr))
	}
	networkCheck, networkInventory := runner.networkCheck(ctx, dockerErr)
	report.Inventory.Network = networkInventory
	add(&report, networkCheck)
	add(&report, runner.dnsmasqBinaryCheck())
	serviceCheck, serviceActive := runner.dnsmasqServiceCheck(ctx)
	report.Inventory.DNS.ServiceActive = serviceActive
	add(&report, serviceCheck)
	add(&report, runner.dnsmasqIncludeCheck())
	mappingCheck, dnsInventory := runner.dnsmasqMappingCheck()
	dnsInventory.ServiceActive = serviceActive
	report.Inventory.DNS = dnsInventory
	add(&report, mappingCheck)
	resolverCheck, resolverInventory := runner.resolverCheck(ctx)
	report.Inventory.Resolver = resolverInventory
	add(&report, resolverCheck)
	manifestCheck, manifestInventory := runner.manifestCheck()
	report.Inventory.Manifest = manifestInventory
	add(&report, manifestCheck)
	tlsChecks, tlsInventory := runner.tlsChecks(ctx, gateways, dockerErr)
	report.Inventory.TLS = tlsInventory
	for _, check := range tlsChecks {
		add(&report, check)
	}
	runtimeChecks, runtimeInventory := runner.runtimeChecks(
		ctx,
		containers,
		dockerErr,
	)
	report.Inventory.Runtime = runtimeInventory
	for _, check := range runtimeChecks {
		add(&report, check)
	}
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

func gatewayInventory(
	gateways []docker.Container,
	dockerErr error,
) domain.PreflightGateway {
	if dockerErr != nil {
		return domain.PreflightGateway{
			Disposition: domain.PreflightUnknown,
		}
	}
	if len(gateways) == 0 {
		return domain.PreflightGateway{
			Disposition: domain.PreflightCreate,
		}
	}
	if len(gateways) != 1 {
		return domain.PreflightGateway{
			Disposition: domain.PreflightConflict,
		}
	}
	return domain.PreflightGateway{
		Disposition:   domain.PreflightAdopt,
		ContainerID:   gateways[0].ID,
		ContainerName: gateways[0].Name,
		Image:         gateways[0].Image,
	}
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
) (domain.DiagnosticCheck, domain.PreflightNetwork) {
	inventory := domain.PreflightNetwork{Name: runner.config.ProxyNetwork}
	if dockerErr != nil {
		inventory.Disposition = domain.PreflightUnknown
		return warn(
			"proxy-network",
			"docker",
			"Proxy network compatibility could not be inspected",
			"Resolve Docker access and rerun preflight.",
		), inventory
	}
	network, err := runner.docker.InspectNetwork(ctx, runner.config.ProxyNetwork)
	if errors.Is(err, docker.ErrNetworkNotFound) {
		inventory.Disposition = domain.PreflightCreate
		return warn(
			"proxy-network",
			"docker",
			fmt.Sprintf("Proxy network %s does not exist yet", runner.config.ProxyNetwork),
			"The reviewed install plan may create a Docklane-owned bridge network.",
		), inventory
	}
	if err != nil {
		inventory.Disposition = domain.PreflightUnknown
		return fail(
			"proxy-network",
			"docker",
			fmt.Sprintf("Proxy network %s could not be inspected", runner.config.ProxyNetwork),
			err.Error(),
			"Resolve the Docker network inspection error before installation.",
		), inventory
	}
	inventory.ID = network.ID
	if network.Driver != "bridge" ||
		network.Scope != "local" ||
		network.Internal {
		inventory.Disposition = domain.PreflightConflict
		return fail(
			"proxy-network",
			"docker",
			fmt.Sprintf("Existing network %s is incompatible", network.Name),
			fmt.Sprintf("driver=%s scope=%s internal=%t", network.Driver, network.Scope, network.Internal),
			"Choose a local non-internal bridge network or a different network name.",
		), inventory
	}
	inventory.Disposition = domain.PreflightAdopt
	return pass(
		"proxy-network",
		"docker",
		fmt.Sprintf("Existing network %s is compatible and will be preserved", network.Name),
	), inventory
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
) (domain.DiagnosticCheck, bool) {
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
		), false
	}
	if !active {
		return warn(
			"dnsmasq-service",
			"dns",
			"dnsmasq is installed but its service is not active",
			"The reviewed install plan must start it and record the prior service state.",
		), false
	}
	return pass(
		"dnsmasq-service",
		"dns",
		"dnsmasq service is active",
	), true
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

func (runner *Runner) dnsmasqMappingCheck() (
	domain.DiagnosticCheck,
	domain.PreflightDNS,
) {
	inventory := domain.PreflightDNS{
		MappingPaths: []string{},
		ConfigPaths:  []string{},
	}
	paths, err := runner.host.ListConfigFiles(runner.config.DnsmasqDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		inventory.Disposition = domain.PreflightUnknown
		return fail(
			"dnsmasq-domain",
			"dns",
			"dnsmasq include directory could not be inspected",
			err.Error(),
			"Verify permissions and the configured include directory.",
		), inventory
	}
	paths = append(paths, runner.config.DnsmasqConfig)
	sort.Strings(paths)
	inventory.ConfigPaths = append([]string(nil), paths...)
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
		inventory.Disposition = domain.PreflightUnknown
		return fail(
			"dnsmasq-domain",
			"dns",
			"One or more dnsmasq configuration files could not be inspected",
			strings.Join(unreadable, "; "),
			"Fix file permissions before checking for conflicting local-domain mappings.",
		), inventory
	}
	if len(mappings) == 0 {
		inventory.Disposition = domain.PreflightCreate
		return warn(
			"dnsmasq-domain",
			"dns",
			"No dnsmasq wildcard mapping exists for "+runner.config.BaseDomain,
			"The installation plan may create a managed mapping to 127.0.0.1.",
		), inventory
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
		inventory.Disposition = domain.PreflightConflict
		return fail(
			"dnsmasq-domain",
			"dns",
			"Conflicting dnsmasq mappings exist for "+runner.config.BaseDomain,
			strings.Join(conflicting, "; "),
			"Remove or reconcile conflicting mappings before installation.",
		), inventory
	}
	if len(correct) > 1 {
		inventory.Disposition = domain.PreflightConflict
		return fail(
			"dnsmasq-domain",
			"dns",
			"Duplicate correct dnsmasq mappings exist for "+runner.config.BaseDomain,
			strings.Join(correct, "; "),
			"Keep one mapping before Docklane records ownership: "+strings.Join(correct, "; "),
		), inventory
	}
	inventory.Disposition = domain.PreflightAdopt
	inventory.MappingPaths = []string{mappings[0].path}
	return pass(
		"dnsmasq-domain",
		"dns",
		"dnsmasq maps "+runner.config.BaseDomain+" to 127.0.0.1 via "+mappings[0].path,
	), inventory
}

func (runner *Runner) resolverCheck(ctx context.Context) (
	domain.DiagnosticCheck,
	domain.PreflightResolver,
) {
	inventory := domain.PreflightResolver{Addresses: []string{}}
	serviceCtx, serviceCancel := context.WithTimeout(ctx, checkTimeout)
	active, serviceErr := runner.host.ServiceActive(
		serviceCtx,
		"systemd-resolved",
	)
	serviceCancel()
	if serviceErr != nil {
		inventory.Disposition = domain.PreflightConflict
		return fail(
			"resolver-service",
			"resolver",
			"systemd-resolved service state could not be determined",
			serviceErr.Error(),
			"Verify systemd-resolved before applying managed resolver changes.",
		), inventory
	}
	inventory.ServiceActive = active
	inventory.ServiceStateKnown = true
	hostname := "docklane-preflight." + runner.config.BaseDomain
	checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	addresses, err := runner.host.LookupHost(checkCtx, hostname)
	if err != nil {
		inventory.Disposition = domain.PreflightCreate
		return warn(
			"resolver-domain",
			"resolver",
			"The system resolver does not currently resolve "+runner.config.BaseDomain,
			"The reviewed install plan must configure split DNS before verification.",
		), inventory
	}
	inventory.Addresses = append([]string(nil), addresses...)
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
		inventory.Disposition = domain.PreflightConflict
		return fail(
			"resolver-domain",
			"resolver",
			"The local domain resolves away from loopback",
			strings.Join(addresses, ", "),
			"Remove the conflicting resolver rule before installation.",
		), inventory
	}
	if len(other) > 0 {
		inventory.Disposition = domain.PreflightConflict
		return fail(
			"resolver-domain",
			"resolver",
			"The local domain has mixed loopback and non-loopback answers",
			strings.Join(addresses, ", "),
			"Reconcile resolver sources: "+strings.Join(addresses, ", "),
		), inventory
	}
	inventory.Disposition = domain.PreflightAdopt
	return pass(
		"resolver-domain",
		"resolver",
		"System resolver maps "+hostname+" to "+strings.Join(loopback, ", "),
	), inventory
}

func (runner *Runner) manifestCheck() (
	domain.DiagnosticCheck,
	domain.PreflightManifest,
) {
	inventory := domain.PreflightManifest{}
	manifest, err := runner.manifest.Load()
	if errors.Is(err, installmanifest.ErrNotFound) {
		return pass(
			"install-manifest",
			"state",
			"No existing installation manifest; a new planned manifest may be created",
		), inventory
	}
	if err != nil {
		return fail(
			"install-manifest",
			"state",
			"Existing installation manifest is invalid or unreadable",
			err.Error(),
			"Repair or explicitly recover the manifest before installation.",
		), inventory
	}
	inventory.Exists = true
	inventory.InstallationID = manifest.InstallationID
	inventory.State = manifest.State
	inventory.Generation = manifest.Generation
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
	), inventory
}

const (
	runtimeComposeProject = "docklane"
	runtimeControllerName = "docklane"
	runtimeProbeName      = "docklane-probe"
	runtimeControlNetwork = "docklane-control"
	runtimeProbeVolume    = "docklane-probe-run"
	runtimeProbeSocket    = "/run/docklane-probe/probe.sock"
)

func (runner *Runner) runtimeChecks(
	ctx context.Context,
	containers []docker.Container,
	dockerErr error,
) ([]domain.DiagnosticCheck, domain.PreflightRuntime) {
	inventory := domain.PreflightRuntime{
		Disposition: domain.PreflightUnknown,
		ControlNetwork: domain.PreflightNetwork{
			Name: runtimeControlNetwork,
		},
		ProbeVolume: domain.PreflightVolume{
			Name: runtimeProbeVolume,
		},
		DataDisposition: domain.PreflightUnknown,
		DataPath:        runner.config.RuntimeDataPath,
	}
	if dockerErr != nil {
		return []domain.DiagnosticCheck{fail(
			"docklane-runtime",
			"runtime",
			"Docklane runtime ownership cannot be inspected",
			dockerErr.Error(),
			"Restore Docker access before planning the controller runtime.",
		)}, inventory
	}
	controllers, probes := runtimeContainers(containers)
	if len(controllers) == 0 && len(probes) == 0 {
		inventory.Disposition = domain.PreflightCreate
		checks := []domain.DiagnosticCheck{warn(
			"docklane-runtime",
			"runtime",
			"No existing Docklane controller runtime was detected",
			"The reviewed installation plan may create the controller and restricted probe.",
		)}
		checks = append(
			checks,
			runner.runtimeFoundationChecks(ctx, &inventory, false)...,
		)
		return checks, inventory
	}
	if len(controllers) != 1 || len(probes) != 1 {
		inventory.Disposition = domain.PreflightConflict
		return []domain.DiagnosticCheck{fail(
			"docklane-runtime",
			"runtime",
			"Docklane runtime is partial or ambiguous",
			fmt.Sprintf(
				"controllers=%s probes=%s",
				containerNames(controllers),
				containerNames(probes),
			),
			"Keep exactly one compatible controller and one restricted probe before adoption.",
		)}, inventory
	}
	controller := controllers[0]
	probe := probes[0]
	inventory.Controller = runtimeContainerFact(controller)
	inventory.Probe = runtimeContainerFact(probe)
	controllerRuntime, controllerErr := runner.docker.InspectContainerRuntime(
		ctx,
		controller.ID,
	)
	probeRuntime, probeErr := runner.docker.InspectContainerRuntime(ctx, probe.ID)
	if controllerErr != nil || probeErr != nil {
		inventory.Disposition = domain.PreflightConflict
		return []domain.DiagnosticCheck{fail(
			"docklane-runtime",
			"runtime",
			"Docklane runtime details could not be inspected",
			fmt.Sprintf("controller=%v probe=%v", controllerErr, probeErr),
			"Restore Docker inspect access before adopting the runtime.",
		)}, inventory
	}
	inventory.Controller.Health = controllerRuntime.Health
	inventory.Probe.Health = probeRuntime.Health
	inventory.Controller.ImageFingerprint = dockerFingerprint(controllerRuntime.ImageID)
	inventory.Probe.ImageFingerprint = dockerFingerprint(probeRuntime.ImageID)
	checks := []domain.DiagnosticCheck{}
	valid := true
	if err := runner.validateController(controller, controllerRuntime, &inventory); err != nil {
		valid = false
		checks = append(checks, fail(
			"docklane-controller",
			"runtime",
			"Existing Docklane controller is not safe to adopt",
			err.Error(),
			"Align the controller with Docklane's loopback-only, read-only runtime contract.",
		))
	} else {
		checks = append(checks, pass(
			"docklane-controller",
			"runtime",
			"Controller is healthy, loopback-only, and uses restricted mounts",
		))
	}
	if err := runner.validateProbe(probe, probeRuntime); err != nil {
		valid = false
		checks = append(checks, fail(
			"docklane-probe",
			"runtime",
			"Existing Docklane probe is not safe to adopt",
			err.Error(),
			"Keep the probe on proxy only, without ports or Docker access, and drop all capabilities.",
		))
	} else {
		checks = append(checks, pass(
			"docklane-probe",
			"runtime",
			"Probe is healthy and isolated to the proxy network with no published port",
		))
	}
	if controllerRuntime.ImageID == "" ||
		controllerRuntime.ImageID != probeRuntime.ImageID {
		valid = false
		checks = append(checks, fail(
			"docklane-runtime-image",
			"runtime",
			"Controller and probe do not use the same immutable image",
			fmt.Sprintf(
				"controller=%s probe=%s",
				controllerRuntime.ImageID,
				probeRuntime.ImageID,
			),
			"Deploy both runtime roles from the same Docklane image.",
		))
	} else {
		checks = append(checks, pass(
			"docklane-runtime-image",
			"runtime",
			"Controller and probe use the same immutable image",
		))
	}
	foundationChecks := runner.runtimeFoundationChecks(ctx, &inventory, true)
	for _, check := range foundationChecks {
		if check.Status == domain.DiagnosticFail {
			valid = false
		}
	}
	checks = append(checks, foundationChecks...)
	if valid {
		inventory.Disposition = domain.PreflightAdopt
		checks = append([]domain.DiagnosticCheck{pass(
			"docklane-runtime",
			"runtime",
			"Existing controller and probe are safe adoption candidates",
		)}, checks...)
	} else {
		inventory.Disposition = domain.PreflightConflict
	}
	return checks, inventory
}

func runtimeContainers(
	containers []docker.Container,
) ([]docker.Container, []docker.Container) {
	var controllers, probes []docker.Container
	for _, container := range containers {
		if container.Name == runtimeControllerName ||
			(container.ComposeProject == runtimeComposeProject &&
				container.ComposeService == runtimeControllerName) {
			controllers = append(controllers, container)
		}
		if container.Name == runtimeProbeName ||
			(container.ComposeProject == runtimeComposeProject &&
				container.ComposeService == "probe") {
			probes = append(probes, container)
		}
	}
	return controllers, probes
}

func runtimeContainerFact(container docker.Container) domain.PreflightRuntimeContainer {
	return domain.PreflightRuntimeContainer{
		ContainerID:   container.ID,
		ContainerName: container.Name,
		Image:         container.Image,
	}
}

func containerNames(containers []docker.Container) string {
	names := make([]string, 0, len(containers))
	for _, container := range containers {
		names = append(names, container.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func (runner *Runner) validateController(
	container docker.Container,
	runtime docker.ContainerRuntime,
	inventory *domain.PreflightRuntime,
) error {
	if container.ComposeProject != runtimeComposeProject ||
		container.ComposeService != runtimeControllerName {
		return fmt.Errorf("controller lacks the expected Compose ownership labels")
	}
	if !runtime.Running || runtime.Health != "healthy" {
		return fmt.Errorf("controller health is %q", runtime.Health)
	}
	if runtime.Privileged || !runtime.ReadOnlyRootFS || !runtime.NoNewPrivileges {
		return fmt.Errorf("controller security boundary is weakened")
	}
	if runtime.RestartPolicy != "unless-stopped" {
		return fmt.Errorf("controller restart policy is %q", runtime.RestartPolicy)
	}
	if len(container.Networks) != 1 || !container.HasNetwork(runtimeControlNetwork) {
		return fmt.Errorf("controller networks are %v", container.Networks)
	}
	if !hasCommand(runtime.Command, "serve") ||
		commandFlag(runtime.Command, "--base-domain") != runner.config.BaseDomain ||
		commandFlag(runtime.Command, "--proxy-network") != runner.config.ProxyNetwork ||
		commandFlag(runtime.Command, "--docker-socket") != runner.config.DockerSocket ||
		commandFlag(runtime.Command, "--probe-socket") != runtimeProbeSocket {
		return fmt.Errorf("controller command does not match installation settings")
	}
	if len(runtime.PortBindings) != 1 ||
		runtime.PortBindings[0].ContainerPort != 4646 ||
		runtime.PortBindings[0].HostPort != 4646 ||
		runtime.PortBindings[0].HostIP != "127.0.0.1" {
		return fmt.Errorf("controller port bindings are %#v", runtime.PortBindings)
	}
	socket, ok := runtimeMount(runtime.Mounts, runner.config.DockerSocket)
	if !ok || !socket.ReadOnly || socket.Source != runner.config.DockerSocket {
		return fmt.Errorf("Docker socket is not bind-mounted read-only")
	}
	data, ok := runtimeMount(runtime.Mounts, "/data")
	if !ok || data.ReadOnly || data.Type != "bind" || !filepath.IsAbs(data.Source) {
		return fmt.Errorf("controller data directory is not a writable host bind mount")
	}
	dataInfo, err := runner.host.Stat(data.Source)
	if err != nil || !dataInfo.Mode.IsDir() {
		return fmt.Errorf("controller data bind source is not a readable directory")
	}
	inventory.DataPath = filepath.Clean(data.Source)
	inventory.DataDisposition = domain.PreflightAdopt
	shared, ok := runtimeMount(runtime.Mounts, filepath.Dir(runtimeProbeSocket))
	if !ok || shared.ReadOnly || shared.Name != runtimeProbeVolume {
		return fmt.Errorf("controller does not use the expected probe socket volume")
	}
	for _, destination := range []string{
		"/run/secrets/traefik-dashboard-password",
		"/run/secrets/traefik-local-ca.crt",
	} {
		mount, ok := runtimeMount(runtime.Mounts, destination)
		if !ok || !mount.ReadOnly {
			return fmt.Errorf("%s is not mounted read-only", destination)
		}
	}
	return nil
}

func (runner *Runner) validateProbe(
	container docker.Container,
	runtime docker.ContainerRuntime,
) error {
	if container.ComposeProject != runtimeComposeProject ||
		container.ComposeService != "probe" {
		return fmt.Errorf("probe lacks the expected Compose ownership labels")
	}
	if !runtime.Running || runtime.Health != "healthy" {
		return fmt.Errorf("probe health is %q", runtime.Health)
	}
	if runtime.Privileged || !runtime.ReadOnlyRootFS || !runtime.NoNewPrivileges {
		return fmt.Errorf("probe security boundary is weakened")
	}
	if runtime.RestartPolicy != "unless-stopped" {
		return fmt.Errorf("probe restart policy is %q", runtime.RestartPolicy)
	}
	if len(container.Networks) != 1 || !container.HasNetwork(runner.config.ProxyNetwork) {
		return fmt.Errorf("probe networks are %v", container.Networks)
	}
	if len(runtime.PortBindings) != 0 || len(container.PublishedPorts) != 0 {
		return fmt.Errorf("probe publishes host ports")
	}
	if !hasCommand(runtime.Command, "probe", "serve") ||
		commandFlag(runtime.Command, "--socket") != runtimeProbeSocket {
		return fmt.Errorf("probe command does not match the socket contract")
	}
	if !containsFold(runtime.DroppedCaps, "ALL") {
		return fmt.Errorf("probe does not drop all Linux capabilities")
	}
	if _, ok := runtimeMount(runtime.Mounts, runner.config.DockerSocket); ok {
		return fmt.Errorf("probe has Docker socket access")
	}
	shared, ok := runtimeMount(runtime.Mounts, filepath.Dir(runtimeProbeSocket))
	if !ok || shared.ReadOnly || shared.Name != runtimeProbeVolume {
		return fmt.Errorf("probe does not use the expected shared socket volume")
	}
	return nil
}

func (runner *Runner) runtimeFoundationChecks(
	ctx context.Context,
	inventory *domain.PreflightRuntime,
	requireExisting bool,
) []domain.DiagnosticCheck {
	checks := []domain.DiagnosticCheck{}
	network, err := runner.docker.InspectNetwork(ctx, runtimeControlNetwork)
	switch {
	case errors.Is(err, docker.ErrNetworkNotFound):
		inventory.ControlNetwork.Disposition = domain.PreflightCreate
		status := domain.DiagnosticWarn
		if requireExisting {
			status = domain.DiagnosticFail
		}
		checks = append(checks, domain.DiagnosticCheck{
			ID:         "docklane-control-network",
			Layer:      "runtime",
			Status:     status,
			Summary:    "Docklane control network does not exist",
			Suggestion: "The reviewed plan must create a private bridge network.",
		})
	case err != nil:
		inventory.ControlNetwork.Disposition = domain.PreflightUnknown
		checks = append(checks, fail(
			"docklane-control-network",
			"runtime",
			"Docklane control network could not be inspected",
			err.Error(),
			"Restore Docker network inspection before installation.",
		))
	case network.Driver != "bridge" || network.Scope != "local":
		inventory.ControlNetwork.Disposition = domain.PreflightConflict
		checks = append(checks, fail(
			"docklane-control-network",
			"runtime",
			"Existing Docklane control network is incompatible",
			fmt.Sprintf("driver=%s scope=%s", network.Driver, network.Scope),
			"Remove or rename the conflicting network before installation.",
		))
	default:
		inventory.ControlNetwork.Disposition = domain.PreflightAdopt
		inventory.ControlNetwork.ID = network.ID
		checks = append(checks, pass(
			"docklane-control-network",
			"runtime",
			"Existing private Docklane control network is compatible",
		))
	}
	volume, err := runner.docker.InspectVolume(ctx, runtimeProbeVolume)
	switch {
	case errors.Is(err, docker.ErrVolumeNotFound):
		inventory.ProbeVolume.Disposition = domain.PreflightCreate
		status := domain.DiagnosticWarn
		if requireExisting {
			status = domain.DiagnosticFail
		}
		checks = append(checks, domain.DiagnosticCheck{
			ID:         "docklane-probe-volume",
			Layer:      "runtime",
			Status:     status,
			Summary:    "Docklane probe socket volume does not exist",
			Suggestion: "The reviewed plan must create the shared socket volume.",
		})
	case err != nil:
		inventory.ProbeVolume.Disposition = domain.PreflightUnknown
		checks = append(checks, fail(
			"docklane-probe-volume",
			"runtime",
			"Docklane probe socket volume could not be inspected",
			err.Error(),
			"Restore Docker volume inspection before installation.",
		))
	case volume.Driver != "local" || volume.Scope != "local":
		inventory.ProbeVolume.Disposition = domain.PreflightConflict
		checks = append(checks, fail(
			"docklane-probe-volume",
			"runtime",
			"Existing Docklane probe volume is incompatible",
			fmt.Sprintf("driver=%s scope=%s", volume.Driver, volume.Scope),
			"Remove or rename the conflicting volume before installation.",
		))
	default:
		inventory.ProbeVolume.Disposition = domain.PreflightAdopt
		inventory.ProbeVolume.Driver = volume.Driver
		checks = append(checks, pass(
			"docklane-probe-volume",
			"runtime",
			"Existing local probe socket volume is compatible",
		))
	}
	if inventory.DataDisposition != domain.PreflightAdopt {
		info, statErr := runner.host.Stat(inventory.DataPath)
		switch {
		case errors.Is(statErr, os.ErrNotExist):
			inventory.DataDisposition = domain.PreflightCreate
			checks = append(checks, warn(
				"docklane-data",
				"runtime",
				"Docklane data directory does not exist",
				"The reviewed plan must create the private controller data directory.",
			))
		case statErr != nil:
			inventory.DataDisposition = domain.PreflightUnknown
			checks = append(checks, fail(
				"docklane-data",
				"runtime",
				"Docklane data path could not be inspected",
				statErr.Error(),
				"Resolve the data path before installation.",
			))
		case !info.Mode.IsDir():
			inventory.DataDisposition = domain.PreflightConflict
			checks = append(checks, fail(
				"docklane-data",
				"runtime",
				"Docklane data path is not a directory",
				inventory.DataPath,
				"Choose an empty dedicated directory for controller data.",
			))
		default:
			inventory.DataDisposition = domain.PreflightAdopt
			checks = append(checks, pass(
				"docklane-data",
				"runtime",
				"Existing Docklane data directory will be preserved",
			))
		}
	} else {
		checks = append(checks, pass(
			"docklane-data",
			"runtime",
			"Controller data bind mount will be preserved",
		))
	}
	return checks
}

func runtimeMount(
	mounts []docker.ContainerMount,
	destination string,
) (docker.ContainerMount, bool) {
	for _, mount := range mounts {
		if filepath.Clean(mount.Destination) == filepath.Clean(destination) {
			return mount, true
		}
	}
	return docker.ContainerMount{}, false
}

func hasCommand(command []string, expected ...string) bool {
	if len(command) < len(expected) {
		return false
	}
	for index, value := range expected {
		if command[index] != value {
			return false
		}
	}
	return true
}

func commandFlag(command []string, name string) string {
	for index, argument := range command {
		if argument == name && index+1 < len(command) {
			return command[index+1]
		}
		if strings.HasPrefix(argument, name+"=") {
			return strings.TrimPrefix(argument, name+"=")
		}
	}
	return ""
}

func containsFold(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(value, expected) {
			return true
		}
	}
	return false
}

func dockerFingerprint(imageID string) string {
	return strings.TrimPrefix(imageID, "sha256:")
}

func (runner *Runner) tlsChecks(
	ctx context.Context,
	gateways []docker.Container,
	dockerErr error,
) ([]domain.DiagnosticCheck, domain.PreflightTLS) {
	inventory := domain.PreflightTLS{
		Disposition:     domain.PreflightUnknown,
		TrustAnchorPath: runner.config.TrustAnchorPath,
		DNSNames:        []string{},
	}
	if dockerErr != nil || len(gateways) != 1 {
		if dockerErr == nil && len(gateways) == 0 {
			inventory.Disposition = domain.PreflightCreate
			return []domain.DiagnosticCheck{warn(
				"tls-inventory",
				"tls",
				"No adopted Traefik certificate can be inventoried yet",
				"The clean-install TLS planner must generate and wire a local certificate.",
			)}, inventory
		}
		return []domain.DiagnosticCheck{fail(
			"tls-inventory",
			"tls",
			"Traefik TLS ownership cannot be determined",
			"",
			"Resolve gateway ownership before inventorying TLS.",
		)}, inventory
	}
	runtime, err := runner.docker.InspectContainerRuntime(
		ctx,
		gateways[0].ID,
	)
	if err != nil {
		return []domain.DiagnosticCheck{fail(
			"tls-wiring",
			"tls",
			"Traefik runtime mounts could not be inspected",
			err.Error(),
			"Verify Docker inspect access before adopting TLS files.",
		)}, inventory
	}
	dynamicContainerPath := traefikDynamicConfigPath(runtime.Command)
	dynamicHostPath, dynamicReadOnly, ok := mapContainerPath(
		runtime.Mounts,
		dynamicContainerPath,
	)
	if !ok || !dynamicReadOnly {
		return []domain.DiagnosticCheck{fail(
			"tls-wiring",
			"tls",
			"Traefik file-provider configuration is not read-only host-mounted",
			dynamicContainerPath,
			"Use an explicit read-only file-provider mount before adoption.",
		)}, inventory
	}
	dynamicConfig, err := runner.host.ReadFile(dynamicHostPath)
	if err != nil {
		return []domain.DiagnosticCheck{fail(
			"tls-wiring",
			"tls",
			"Traefik TLS configuration is not readable",
			err.Error(),
			"Verify the adopted file-provider configuration path.",
		)}, inventory
	}
	certContainerPath, keyContainerPath := tlsFilePaths(string(dynamicConfig))
	certPath, certReadOnly, certMapped := mapContainerPath(
		runtime.Mounts,
		certContainerPath,
	)
	keyPath, keyReadOnly, keyMapped := mapContainerPath(
		runtime.Mounts,
		keyContainerPath,
	)
	if !certMapped || !keyMapped || !certReadOnly || !keyReadOnly {
		return []domain.DiagnosticCheck{fail(
			"tls-wiring",
			"tls",
			"Traefik certificate and key are not both read-only host-mounted",
			fmt.Sprintf("cert=%s key=%s", certContainerPath, keyContainerPath),
			"Use explicit read-only certificate mounts before adoption.",
		)}, inventory
	}
	inventory.CertificatePath = certPath
	inventory.PrivateKeyPath = keyPath
	checks := []domain.DiagnosticCheck{pass(
		"tls-wiring",
		"tls",
		fmt.Sprintf("Traefik loads %s and %s through read-only mounts", certPath, keyPath),
	)}
	certificate, certFingerprint, err := runner.readLeafCertificate(certPath)
	if err != nil {
		checks = append(checks, fail(
			"tls-certificate",
			"tls",
			"Configured TLS certificate is invalid",
			err.Error(),
			"Replace it with a valid local leaf certificate.",
		))
		return checks, inventory
	}
	inventory.CertificateFingerprint = certFingerprint
	inventory.NotAfter = certificate.NotAfter.UTC()
	inventory.DNSNames = append([]string(nil), certificate.DNSNames...)
	sort.Strings(inventory.DNSNames)
	requiredNames := []string{
		runner.config.BaseDomain,
		"*." + runner.config.BaseDomain,
	}
	for _, hostname := range requiredNames {
		if err := certificate.VerifyHostname(hostname); err != nil {
			checks = append(checks, fail(
				"tls-certificate",
				"tls",
				"Certificate SANs do not cover "+hostname,
				err.Error(),
				"Rotate the leaf certificate with explicit base-domain and wildcard SANs.",
			))
			return checks, inventory
		}
	}
	now := runner.now().UTC()
	if now.Before(certificate.NotBefore) ||
		!certificate.NotAfter.After(now.Add(30*24*time.Hour)) {
		checks = append(checks, fail(
			"tls-certificate",
			"tls",
			"Certificate is not currently valid for at least 30 days",
			fmt.Sprintf("valid %s through %s", certificate.NotBefore, certificate.NotAfter),
			"Rotate the certificate before installation adoption.",
		))
		return checks, inventory
	}
	checks = append(checks, pass(
		"tls-certificate",
		"tls",
		"Certificate SANs cover the base domain and wildcard through "+
			certificate.NotAfter.UTC().Format(time.DateOnly),
	))
	keyContent, err := runner.host.ReadFile(keyPath)
	if err != nil {
		checks = append(checks, fail(
			"tls-private-key", "tls", "TLS private key is not readable",
			err.Error(), "Verify the configured key path and permissions.",
		))
		return checks, inventory
	}
	keyInfo, err := runner.host.Stat(keyPath)
	if err != nil || keyInfo.Mode.Perm()&0o077 != 0 {
		detail := fmt.Sprintf("mode=%o", keyInfo.Mode.Perm())
		if err != nil {
			detail = err.Error()
		}
		checks = append(checks, fail(
			"tls-private-key",
			"tls",
			"TLS private key permissions are too broad or unreadable",
			detail,
			"Restrict the key to its owner, normally mode 0600.",
		))
		return checks, inventory
	}
	if err := privateKeyMatches(certificate, keyContent); err != nil {
		checks = append(checks, fail(
			"tls-private-key", "tls", "TLS private key does not match the certificate",
			err.Error(), "Install the matching private key before adoption.",
		))
		return checks, inventory
	}
	inventory.PrivateKeyFingerprint = sha256Hex(keyContent)
	checks = append(checks, pass(
		"tls-private-key", "tls",
		fmt.Sprintf("TLS private key matches the certificate with mode %o", keyInfo.Mode.Perm()),
	))
	trustCertificate, trustFingerprint, err := runner.readLeafCertificate(
		runner.config.TrustAnchorPath,
	)
	if err != nil {
		checks = append(checks, fail(
			"tls-trust-anchor", "tls", "Configured trust anchor is invalid",
			err.Error(), "Install the issuing local root CA in the system trust store.",
		))
		return checks, inventory
	}
	if err := certificate.CheckSignatureFrom(trustCertificate); err != nil {
		checks = append(checks, fail(
			"tls-trust-anchor", "tls", "Trust anchor did not issue the leaf certificate",
			err.Error(), "Select the exact issuing root CA before adoption.",
		))
		return checks, inventory
	}
	inventory.TrustFingerprint = trustFingerprint
	checks = append(checks, pass(
		"tls-trust-anchor", "tls",
		"Configured trust anchor issued the Traefik leaf certificate",
	))
	probeCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	servedDER, err := runner.host.ProbeTLSCertificate(
		probeCtx,
		"docklane-preflight."+runner.config.BaseDomain,
	)
	if err != nil {
		checks = append(checks, fail(
			"tls-served-certificate", "tls", "Traefik served certificate could not be inspected",
			err.Error(), "Verify DNS, port 443, and Traefik TLS wiring.",
		))
		return checks, inventory
	}
	servedFingerprint := sha256Hex(servedDER)
	if servedFingerprint != certFingerprint {
		checks = append(checks, fail(
			"tls-served-certificate", "tls", "Traefik is serving a different certificate",
			"configured="+certFingerprint+" served="+servedFingerprint,
			"Reload the adopted TLS configuration before installation.",
		))
		return checks, inventory
	}
	checks = append(checks, pass(
		"tls-served-certificate", "tls",
		"Traefik serves the exact inventoried certificate",
	))
	inventory.Disposition = domain.PreflightAdopt
	return checks, inventory
}

func (runner *Runner) readLeafCertificate(
	path string,
) (*x509.Certificate, string, error) {
	content, err := runner.host.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	block, _ := pem.Decode(content)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, "", fmt.Errorf("%s contains no PEM certificate", path)
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, "", err
	}
	return certificate, sha256Hex(certificate.Raw), nil
}

func privateKeyMatches(certificate *x509.Certificate, content []byte) error {
	block, _ := pem.Decode(content)
	if block == nil {
		return fmt.Errorf("private key contains no PEM block")
	}
	var value any
	var err error
	switch block.Type {
	case "RSA PRIVATE KEY":
		value, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		value, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		value, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	}
	if err != nil {
		return err
	}
	signer, ok := value.(crypto.Signer)
	if !ok {
		return fmt.Errorf("PEM block is not a supported private key")
	}
	got, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return err
	}
	want, err := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("public keys differ")
	}
	return nil
}

func traefikDynamicConfigPath(command []string) string {
	const prefix = "--providers.file.filename="
	for _, argument := range command {
		if strings.HasPrefix(argument, prefix) {
			return strings.TrimPrefix(argument, prefix)
		}
	}
	return ""
}

func tlsFilePaths(content string) (string, string) {
	var certificate, key string
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if strings.HasPrefix(line, "certFile:") {
			certificate = strings.Trim(strings.TrimSpace(
				strings.TrimPrefix(line, "certFile:"),
			), `"'`)
		}
		if strings.HasPrefix(line, "keyFile:") {
			key = strings.Trim(strings.TrimSpace(
				strings.TrimPrefix(line, "keyFile:"),
			), `"'`)
		}
	}
	return certificate, key
}

func mapContainerPath(
	mounts []docker.ContainerMount,
	containerPath string,
) (string, bool, bool) {
	best := docker.ContainerMount{}
	for _, mount := range mounts {
		destination := strings.TrimSuffix(mount.Destination, "/")
		if containerPath == destination ||
			strings.HasPrefix(containerPath, destination+"/") {
			if len(destination) > len(best.Destination) {
				best = mount
			}
		}
	}
	if best.Destination == "" {
		return "", false, false
	}
	relative := strings.TrimPrefix(
		containerPath,
		strings.TrimSuffix(best.Destination, "/"),
	)
	relative = strings.TrimPrefix(relative, "/")
	return filepath.Join(best.Source, relative), best.ReadOnly, true
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func dnsmasqIncludesDirectory(content string, directory string) bool {
	expected := filepath.Clean(directory)
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		value := ""
		switch {
		case strings.HasPrefix(line, "conf-dir="):
			value = strings.TrimPrefix(line, "conf-dir=")
		case strings.HasPrefix(line, "CONFIG_DIR="):
			value = strings.Trim(
				strings.TrimPrefix(line, "CONFIG_DIR="),
				`"'`,
			)
		default:
			continue
		}
		value = strings.TrimSpace(strings.SplitN(value, ",", 2)[0])
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
