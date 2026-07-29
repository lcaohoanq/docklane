package installmanaged_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installhost"
	"docklane.local/docklane/internal/installmanaged"
	"docklane.local/docklane/internal/installmanifest"
	"docklane.local/docklane/internal/installplan"
	"docklane.local/docklane/internal/installspec"
	"docklane.local/docklane/internal/installuninstall"
	"docklane.local/docklane/internal/uninstallplan"
)

func TestManagedRunnerInstallsCleanPlanAndClearsMaterial(t *testing.T) {
	fixture := newManagedRunnerFixture(t)
	installed, err := fixture.runner.Apply(
		context.Background(),
		fixture.plan,
		fixture.plan.Token,
	)
	if err != nil {
		t.Fatal(err)
	}
	if installed.State != domain.InstallationInstalled ||
		installed.Execution == nil ||
		installed.Execution.Phase != domain.ExecutionComplete {
		t.Fatalf("installed manifest = %#v", installed)
	}
	if installed.MaterialCache == nil ||
		installed.MaterialCache.State != domain.MaterialCacheCleared {
		t.Fatalf("material cache = %#v", installed.MaterialCache)
	}
	if _, err := os.Lstat(
		installed.MaterialCache.Directory,
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("material cache remains: %v", err)
	}
	for _, resource := range installed.Resources {
		if resource.Ownership == domain.ResourceManaged &&
			resource.State != domain.ResourceVerified {
			t.Fatalf("unverified resource = %#v", resource)
		}
	}
	if len(fixture.docker.networks) != 2 ||
		len(fixture.docker.volumes) != 1 ||
		len(fixture.docker.containers) != 3 {
		t.Fatalf(
			"Docker state = networks:%d volumes:%d containers:%d",
			len(fixture.docker.networks),
			len(fixture.docker.volumes),
			len(fixture.docker.containers),
		)
	}
	resumed, err := fixture.runner.Resume(
		context.Background(),
		fixture.plan.Token,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Generation != installed.Generation {
		t.Fatalf(
			"terminal resume changed generation: %d != %d",
			resumed.Generation,
			installed.Generation,
		)
	}
}

func TestManagedRunnerRejectsTokenBeforeManifestCreation(t *testing.T) {
	fixture := newManagedRunnerFixture(t)
	_, err := fixture.runner.Apply(
		context.Background(),
		fixture.plan,
		strings.Repeat("0", 64),
	)
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("token error = %v", err)
	}
	if _, loadErr := fixture.store.Load(); !errors.Is(
		loadErr,
		installmanifest.ErrNotFound,
	) {
		t.Fatalf("manifest was created: %v", loadErr)
	}
	if len(fixture.docker.mutations) != 0 {
		t.Fatalf("Docker mutated before token validation: %v", fixture.docker.mutations)
	}
}

func TestManagedInstallThenReviewedUninstallRollsBackEverything(t *testing.T) {
	fixture := newManagedRunnerFixture(t)
	installed, err := fixture.runner.Apply(
		context.Background(),
		fixture.plan,
		fixture.plan.Token,
	)
	if err != nil {
		t.Fatal(err)
	}
	persistentFile := filepath.Join(
		installed.ManagedSpecification.Paths.DataDirectory,
		"docklane.db",
	)
	if err := os.WriteFile(
		persistentFile,
		[]byte("persistent test data"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	plan, err := uninstallplan.Build(installed, fixture.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Ready {
		t.Fatalf("uninstall plan = %#v", plan)
	}
	uninstaller, err := installuninstall.New(
		fixture.store,
		fixture.docker,
		fixture.host,
	)
	if err != nil {
		t.Fatal(err)
	}
	rolledBack, err := uninstaller.Apply(
		context.Background(),
		installed,
		plan,
		plan.Token,
	)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.State != domain.InstallationRolledBack ||
		rolledBack.Execution == nil ||
		rolledBack.Execution.Phase != domain.ExecutionRolledBack ||
		rolledBack.RollbackToken != plan.Token {
		t.Fatalf("rolled-back manifest = %#v", rolledBack)
	}
	if len(fixture.docker.networks) != 0 ||
		len(fixture.docker.volumes) != 0 ||
		len(fixture.docker.containers) != 0 {
		t.Fatalf("uninstall leaked Docker state")
	}
	if fixture.host.services["dnsmasq"].Active ||
		!fixture.host.services["systemd-resolved"].Active {
		t.Fatalf("uninstall host state = %#v", fixture.host.services)
	}
	for _, resource := range rolledBack.Resources {
		if resource.Ownership == domain.ResourceManaged &&
			resource.State != domain.ResourceRolledBack {
			t.Fatalf("resource was not rolled back: %#v", resource)
		}
		if (resource.Kind == domain.ResourceFile ||
			resource.Kind == domain.ResourceTrustAnchor ||
			resource.Kind == domain.ResourceDirectory) &&
			resource.Ownership == domain.ResourceManaged {
			if resource.ID == "docklane-data" {
				content, readErr := os.ReadFile(persistentFile)
				if readErr != nil ||
					string(content) != "persistent test data" {
					t.Fatalf(
						"persistent data was not retained: %q, %v",
						content,
						readErr,
					)
				}
				entries, readErr := os.ReadDir(resource.Target)
				if readErr != nil ||
					len(entries) != 1 ||
					entries[0].Name() != "docklane.db" {
					t.Fatalf(
						"released data directory = %v, %v",
						entries,
						readErr,
					)
				}
				continue
			}
			if _, statErr := os.Lstat(resource.Target); !errors.Is(
				statErr,
				os.ErrNotExist,
			) {
				t.Fatalf("uninstalled path %s remains: %v", resource.Target, statErr)
			}
		}
	}
	resumed, err := uninstaller.Resume(
		context.Background(),
		plan.Token,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Generation != rolledBack.Generation {
		t.Fatalf("terminal uninstall resume changed generation")
	}
}

func TestManagedUninstallResumeReconcilesRemovedDockerObject(t *testing.T) {
	fixture := newManagedRunnerFixture(t)
	installed, err := fixture.runner.Apply(
		context.Background(),
		fixture.plan,
		fixture.plan.Token,
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := uninstallplan.Build(installed, fixture.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	failing := &failingUninstallStore{
		Store:  fixture.store,
		failAt: 3,
	}
	first, err := installuninstall.New(
		failing,
		fixture.docker,
		fixture.host,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = first.Apply(
		context.Background(),
		installed,
		plan,
		plan.Token,
	)
	if err == nil || !strings.Contains(err.Error(), "injected checkpoint") {
		t.Fatalf("interrupted uninstall error = %v", err)
	}
	if _, exists := fixture.docker.containers["traefik"]; exists {
		t.Fatal("gateway was not removed before checkpoint failure")
	}
	current, err := fixture.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	lastOperation := current.Execution.Operations[len(current.Execution.Operations)-1]
	if current.State != domain.InstallationRollingBack ||
		lastOperation.State != domain.OperationRollingBack {
		t.Fatalf("interrupted journal = %#v", current.Execution)
	}
	recovery, err := installuninstall.New(
		fixture.store,
		fixture.docker,
		fixture.host,
	)
	if err != nil {
		t.Fatal(err)
	}
	rolledBack, err := recovery.Resume(
		context.Background(),
		plan.Token,
	)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.State != domain.InstallationRolledBack {
		t.Fatalf("recovered state = %s", rolledBack.State)
	}
	if countString(
		fixture.docker.mutations,
		"remove-container:traefik",
	) != 1 {
		t.Fatalf(
			"gateway removal repeated: %v",
			fixture.docker.mutations,
		)
	}
}

func TestManagedUninstallResumeAfterHostAndFileRestoration(t *testing.T) {
	fixture := newManagedRunnerFixture(t)
	installed, err := fixture.runner.Apply(
		context.Background(),
		fixture.plan,
		fixture.plan.Token,
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := uninstallplan.Build(installed, fixture.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	failing := &failingUninstallStore{
		Store:  fixture.store,
		failAt: 18,
	}
	first, err := installuninstall.New(
		failing,
		fixture.docker,
		fixture.host,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = first.Apply(
		context.Background(),
		installed,
		plan,
		plan.Token,
	)
	if err == nil || !strings.Contains(err.Error(), "injected checkpoint") {
		t.Fatalf("interrupted host uninstall error = %v", err)
	}
	current, err := fixture.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range current.Execution.Operations {
		if operation.Stage == domain.ExecutionHost &&
			operation.State != domain.OperationRolledBack {
			t.Fatalf("host operation did not checkpoint rollback: %#v", operation)
		}
	}
	recovery, err := installuninstall.New(
		fixture.store,
		fixture.docker,
		fixture.host,
	)
	if err != nil {
		t.Fatal(err)
	}
	rolledBack, err := recovery.Resume(
		context.Background(),
		plan.Token,
	)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.State != domain.InstallationRolledBack {
		t.Fatalf("recovered state = %s", rolledBack.State)
	}
}

func TestManagedRunnerFailureRestoresHostAndDockerState(t *testing.T) {
	fixture := newManagedRunnerFixture(t)
	fixture.docker.failContainer = "traefik"
	rolledBack, err := fixture.runner.Apply(
		context.Background(),
		fixture.plan,
		fixture.plan.Token,
	)
	if err == nil || !strings.Contains(err.Error(), "injected container") {
		t.Fatalf("managed apply error = %v", err)
	}
	if rolledBack.State != domain.InstallationRolledBack ||
		rolledBack.Execution == nil ||
		rolledBack.Execution.Phase != domain.ExecutionRolledBack {
		t.Fatalf("rolled-back manifest = %#v", rolledBack)
	}
	if rolledBack.MaterialCache == nil ||
		rolledBack.MaterialCache.State != domain.MaterialCacheCleared {
		t.Fatalf("rolled-back material cache = %#v", rolledBack.MaterialCache)
	}
	if len(fixture.docker.networks) != 0 ||
		len(fixture.docker.volumes) != 0 ||
		len(fixture.docker.containers) != 0 {
		t.Fatalf("Docker rollback leaked state")
	}
	if fixture.host.services["dnsmasq"].Active ||
		!fixture.host.services["systemd-resolved"].Active {
		t.Fatalf("host state = %#v", fixture.host.services)
	}
	for _, resource := range rolledBack.Resources {
		if resource.Ownership == domain.ResourceManaged &&
			resource.State != domain.ResourceRolledBack {
			t.Fatalf("resource was not rolled back: %#v", resource)
		}
		if (resource.Kind == domain.ResourceFile ||
			resource.Kind == domain.ResourceTrustAnchor ||
			resource.Kind == domain.ResourceDirectory) &&
			resource.Ownership == domain.ResourceManaged {
			if _, statErr := os.Lstat(resource.Target); !errors.Is(
				statErr,
				os.ErrNotExist,
			) {
				t.Fatalf("managed path %s remains: %v", resource.Target, statErr)
			}
		}
	}
}

type managedRunnerFixture struct {
	runner *installmanaged.Runner
	store  *installmanifest.Store
	plan   domain.InstallationPlan
	docker *managedDockerFake
	host   *managedHostFake
}

type failingUninstallStore struct {
	*installmanifest.Store
	failAt int
	saves  int
}

func (store *failingUninstallStore) Save(
	generation uint64,
	manifest domain.InstallationManifest,
) error {
	store.saves++
	if store.saves == store.failAt {
		return errors.New("injected checkpoint failure")
	}
	return store.Store.Save(generation, manifest)
}

func newManagedRunnerFixture(t *testing.T) managedRunnerFixture {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "state")
	for _, directory := range []string{
		filepath.Join(root, "dns"),
		filepath.Join(root, "resolved"),
		filepath.Join(root, "trust"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	specification, err := installspec.Build(installspec.Config{
		BaseDomain:      "docker.home.arpa",
		ProxyNetwork:    "proxy",
		DockerSocket:    "/var/run/docker.sock",
		StateDirectory:  state,
		DataDirectory:   filepath.Join(state, "data"),
		DnsmasqConfig:   filepath.Join(root, "dns", "docklane.conf"),
		ResolverConfig:  filepath.Join(root, "resolved", "docklane.conf"),
		TrustAnchorPath: filepath.Join(root, "trust", "root.crt"),
		TraefikImage:    "traefik:v3.7",
		DocklaneImage:   "docklane:local",
	})
	if err != nil {
		t.Fatal(err)
	}
	report := domain.PreflightReport{
		Status: domain.DiagnosticWarn,
		Target: domain.PreflightTarget{
			BaseDomain:   specification.BaseDomain,
			ProxyNetwork: specification.ProxyNetwork,
			DockerSocket: specification.DockerSocket,
			ManifestPath: filepath.Join(state, "install-manifest.json"),
		},
		Inventory: domain.PreflightInventory{
			Gateway: domain.PreflightGateway{
				Disposition: domain.PreflightCreate,
			},
			Network: domain.PreflightNetwork{
				Disposition: domain.PreflightCreate,
				Name:        specification.ProxyNetwork,
			},
			DNS: domain.PreflightDNS{
				Disposition:   domain.PreflightCreate,
				MappingPaths:  []string{},
				ConfigPaths:   []string{},
				ServiceActive: false,
			},
			Resolver: domain.PreflightResolver{
				Disposition:       domain.PreflightCreate,
				Addresses:         []string{},
				ServiceActive:     true,
				ServiceStateKnown: true,
			},
			TLS: domain.PreflightTLS{
				Disposition: domain.PreflightCreate,
			},
			Runtime: domain.PreflightRuntime{
				Disposition: domain.PreflightCreate,
				ControlNetwork: domain.PreflightNetwork{
					Disposition: domain.PreflightCreate,
					Name:        specification.ControlNetwork,
				},
				ProbeVolume: domain.PreflightVolume{
					Disposition: domain.PreflightCreate,
					Name:        specification.ProbeVolume,
				},
				DataDisposition: domain.PreflightCreate,
				DataPath:        specification.Paths.DataDirectory,
			},
		},
		Checks: []domain.DiagnosticCheck{},
	}
	plan, err := installplan.Build(report, installplan.Options{
		DnsmasqTarget:        specification.Paths.DnsmasqConfig,
		DnsmasqService:       specification.Host.DNSService,
		ManagedSpecification: specification,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Ready || !plan.Complete {
		t.Fatalf("managed test plan = %#v", plan)
	}
	store, err := installmanifest.NewStore(report.Target.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	dockerBackend := newManagedDockerFake()
	hostBackend := &managedHostFake{
		services: map[string]installhost.ServiceState{
			specification.Host.DNSService:      {},
			specification.Host.ResolverService: {Active: true},
		},
	}
	runner, err := installmanaged.New(
		store,
		"dev",
		dockerBackend,
		hostBackend,
	)
	if err != nil {
		t.Fatal(err)
	}
	return managedRunnerFixture{
		runner: runner,
		store:  store,
		plan:   plan,
		docker: dockerBackend,
		host:   hostBackend,
	}
}

type managedHostFake struct {
	services map[string]installhost.ServiceState
}

func (host *managedHostFake) ServiceState(
	_ context.Context,
	name string,
) (installhost.ServiceState, error) {
	return host.services[name], nil
}

func (*managedHostFake) ValidateDNSConfiguration(context.Context) error {
	return nil
}

func (*managedHostFake) RefreshTrustStore(context.Context, string) error {
	return nil
}

func (host *managedHostFake) StartService(
	_ context.Context,
	name string,
) error {
	host.services[name] = installhost.ServiceState{Active: true}
	return nil
}

func (host *managedHostFake) RestartService(
	_ context.Context,
	name string,
) error {
	host.services[name] = installhost.ServiceState{Active: true}
	return nil
}

func (host *managedHostFake) StopService(
	_ context.Context,
	name string,
) error {
	host.services[name] = installhost.ServiceState{}
	return nil
}

func (*managedHostFake) FlushResolverCache(context.Context, string) error {
	return nil
}

func (*managedHostFake) LookupHost(
	context.Context,
	string,
) ([]string, error) {
	return []string{"127.0.0.1"}, nil
}

func (*managedHostFake) VerifyTrustAnchor(
	context.Context,
	string,
	string,
) error {
	return nil
}

type managedDockerFake struct {
	networks      map[string]docker.Network
	volumes       map[string]docker.Volume
	containers    map[string]docker.ManagedContainerState
	mutations     []string
	failContainer string
}

func newManagedDockerFake() *managedDockerFake {
	return &managedDockerFake{
		networks:   map[string]docker.Network{},
		volumes:    map[string]docker.Volume{},
		containers: map[string]docker.ManagedContainerState{},
	}
}

func (backend *managedDockerFake) InspectNetwork(
	_ context.Context,
	nameOrID string,
) (docker.Network, error) {
	for _, network := range backend.networks {
		if network.Name == nameOrID || network.ID == nameOrID {
			return network, nil
		}
	}
	return docker.Network{}, docker.ErrNetworkNotFound
}

func (backend *managedDockerFake) CreateManagedNetwork(
	_ context.Context,
	request docker.ManagedNetworkRequest,
) (docker.Network, error) {
	network := docker.Network{
		ID: request.Name + "-id", Name: request.Name,
		Driver: request.Driver, Scope: "local",
		Internal: request.Internal, Attachable: request.Attachable,
		Labels: cloneStringMap(request.Labels),
	}
	backend.networks[request.Name] = network
	backend.mutations = append(backend.mutations, "network:"+request.Name)
	return network, nil
}

func (backend *managedDockerFake) RemoveManagedNetwork(
	_ context.Context,
	id string,
) error {
	for name, network := range backend.networks {
		if network.ID == id || network.Name == id {
			backend.mutations = append(
				backend.mutations,
				"remove-network:"+network.Name,
			)
			delete(backend.networks, name)
		}
	}
	return nil
}

func (backend *managedDockerFake) InspectVolume(
	_ context.Context,
	name string,
) (docker.Volume, error) {
	volume, exists := backend.volumes[name]
	if !exists {
		return docker.Volume{}, docker.ErrVolumeNotFound
	}
	return volume, nil
}

func (backend *managedDockerFake) CreateManagedVolume(
	_ context.Context,
	request docker.ManagedVolumeRequest,
) (docker.Volume, error) {
	volume := docker.Volume{
		Name: request.Name, Driver: request.Driver, Scope: "local",
		Labels: cloneStringMap(request.Labels),
	}
	backend.volumes[request.Name] = volume
	backend.mutations = append(backend.mutations, "volume:"+request.Name)
	return volume, nil
}

func (backend *managedDockerFake) RemoveManagedVolume(
	_ context.Context,
	name string,
) error {
	if _, exists := backend.volumes[name]; exists {
		backend.mutations = append(
			backend.mutations,
			"remove-volume:"+name,
		)
	}
	delete(backend.volumes, name)
	return nil
}

func (backend *managedDockerFake) InspectManagedContainer(
	_ context.Context,
	nameOrID string,
) (docker.ManagedContainerState, error) {
	for _, container := range backend.containers {
		if container.Name == nameOrID || container.ID == nameOrID {
			return container, nil
		}
	}
	return docker.ManagedContainerState{}, docker.ErrContainerNotFound
}

func (backend *managedDockerFake) CreateManagedContainer(
	_ context.Context,
	request docker.ManagedContainerRequest,
) (docker.ManagedContainerState, error) {
	if backend.failContainer == request.Name {
		return docker.ManagedContainerState{}, errors.New(
			"injected container creation failure",
		)
	}
	state := managedContainerState(request)
	backend.containers[request.Name] = state
	backend.mutations = append(backend.mutations, "container:"+request.Name)
	return state, nil
}

func (backend *managedDockerFake) StartManagedContainer(
	_ context.Context,
	id string,
) error {
	for name, container := range backend.containers {
		if container.ID == id {
			container.Runtime.Running = true
			backend.containers[name] = container
			return nil
		}
	}
	return docker.ErrContainerNotFound
}

func (backend *managedDockerFake) RemoveManagedContainer(
	_ context.Context,
	id string,
) error {
	for name, container := range backend.containers {
		if container.ID == id || container.Name == id {
			backend.mutations = append(
				backend.mutations,
				"remove-container:"+container.Name,
			)
			delete(backend.containers, name)
		}
	}
	return nil
}

func managedContainerState(
	request docker.ManagedContainerRequest,
) docker.ManagedContainerState {
	networks := append([]string(nil), request.Networks...)
	sort.Strings(networks)
	mounts := make([]docker.ContainerMount, 0, len(request.Mounts))
	for _, mount := range request.Mounts {
		name := ""
		if mount.Type == "volume" {
			name = mount.Source
		}
		mounts = append(mounts, docker.ContainerMount{
			Type: mount.Type, Name: name, Source: mount.Source,
			Destination: mount.Destination, ReadOnly: mount.ReadOnly,
		})
	}
	sort.Slice(mounts, func(i, j int) bool {
		return mounts[i].Destination < mounts[j].Destination
	})
	ports := append([]docker.ContainerPortBinding(nil), request.Ports...)
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].ContainerPort != ports[j].ContainerPort {
			return ports[i].ContainerPort < ports[j].ContainerPort
		}
		if ports[i].HostIP != ports[j].HostIP {
			return ports[i].HostIP < ports[j].HostIP
		}
		return ports[i].HostPort < ports[j].HostPort
	})
	caps := []string{}
	if request.DropAllCapabilities {
		caps = []string{"ALL"}
	}
	return docker.ManagedContainerState{
		ID: request.Name + "-id", Name: request.Name, Image: request.Image,
		Networks: networks, Labels: cloneStringMap(request.Labels),
		Runtime: docker.ContainerRuntime{
			ImageID: request.Image + "-id",
			Command: append([]string(nil), request.Command...),
			Mounts:  mounts, PortBindings: ports,
			ReadOnlyRootFS:  request.ReadOnlyRootFS,
			NoNewPrivileges: request.NoNewPrivileges,
			DroppedCaps:     caps, RestartPolicy: request.RestartPolicy,
		},
	}
}

func cloneStringMap(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func countString(values []string, expected string) int {
	count := 0
	for _, value := range values {
		if value == expected {
			count++
		}
	}
	return count
}
