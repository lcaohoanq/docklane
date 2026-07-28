package installdocker

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installspec"
)

type fakeLifecycle struct {
	networks      map[string]docker.Network
	volumes       map[string]docker.Volume
	containers    map[string]docker.ManagedContainerState
	mutations     []string
	failMutation  string
	malformedRole string
}

func newFakeLifecycle() *fakeLifecycle {
	return &fakeLifecycle{
		networks:   map[string]docker.Network{},
		volumes:    map[string]docker.Volume{},
		containers: map[string]docker.ManagedContainerState{},
	}
}

func (backend *fakeLifecycle) InspectNetwork(
	_ context.Context,
	nameOrID string,
) (docker.Network, error) {
	for _, network := range backend.networks {
		if network.Name == nameOrID || network.ID == nameOrID {
			return fakeCloneNetwork(network), nil
		}
	}
	return docker.Network{}, docker.ErrNetworkNotFound
}

func (backend *fakeLifecycle) CreateManagedNetwork(
	_ context.Context,
	request docker.ManagedNetworkRequest,
) (docker.Network, error) {
	operation := "create-network:" + request.Labels[docker.InstallRoleLabel]
	if err := backend.fail(operation); err != nil {
		return docker.Network{}, err
	}
	if _, found := backend.networks[request.Name]; found {
		return docker.Network{}, errors.New("network conflict")
	}
	network := docker.Network{
		ID:         request.Name + "-id",
		Name:       request.Name,
		Driver:     request.Driver,
		Scope:      "local",
		Internal:   request.Internal,
		Attachable: request.Attachable,
		Labels:     fakeCloneLabels(request.Labels),
	}
	backend.networks[request.Name] = network
	return fakeCloneNetwork(network), nil
}

func (backend *fakeLifecycle) RemoveManagedNetwork(
	_ context.Context,
	id string,
) error {
	network, name, found := backend.findNetwork(id)
	if !found {
		return nil
	}
	operation := "remove-network:" + network.Labels[docker.InstallRoleLabel]
	if err := backend.fail(operation); err != nil {
		return err
	}
	for _, container := range backend.containers {
		if contains(container.Networks, network.Name) {
			return errors.New("network has active endpoints")
		}
	}
	delete(backend.networks, name)
	return nil
}

func (backend *fakeLifecycle) InspectVolume(
	_ context.Context,
	name string,
) (docker.Volume, error) {
	volume, found := backend.volumes[name]
	if !found {
		return docker.Volume{}, docker.ErrVolumeNotFound
	}
	return fakeCloneVolume(volume), nil
}

func (backend *fakeLifecycle) CreateManagedVolume(
	_ context.Context,
	request docker.ManagedVolumeRequest,
) (docker.Volume, error) {
	operation := "create-volume:" + request.Labels[docker.InstallRoleLabel]
	if err := backend.fail(operation); err != nil {
		return docker.Volume{}, err
	}
	if _, found := backend.volumes[request.Name]; found {
		return docker.Volume{}, errors.New("volume conflict")
	}
	volume := docker.Volume{
		Name: request.Name, Driver: request.Driver, Scope: "local",
		Labels: fakeCloneLabels(request.Labels),
	}
	backend.volumes[request.Name] = volume
	return fakeCloneVolume(volume), nil
}

func (backend *fakeLifecycle) RemoveManagedVolume(
	_ context.Context,
	name string,
) error {
	volume, found := backend.volumes[name]
	if !found {
		return nil
	}
	operation := "remove-volume:" + volume.Labels[docker.InstallRoleLabel]
	if err := backend.fail(operation); err != nil {
		return err
	}
	for _, container := range backend.containers {
		for _, mount := range container.Runtime.Mounts {
			if mount.Type == "volume" && mount.Source == name {
				return errors.New("volume is in use")
			}
		}
	}
	delete(backend.volumes, name)
	return nil
}

func (backend *fakeLifecycle) InspectManagedContainer(
	_ context.Context,
	nameOrID string,
) (docker.ManagedContainerState, error) {
	for _, container := range backend.containers {
		if container.Name == nameOrID || container.ID == nameOrID {
			return fakeCloneContainer(container), nil
		}
	}
	return docker.ManagedContainerState{}, docker.ErrContainerNotFound
}

func (backend *fakeLifecycle) CreateManagedContainer(
	_ context.Context,
	request docker.ManagedContainerRequest,
) (docker.ManagedContainerState, error) {
	role := request.Labels[docker.InstallRoleLabel]
	if err := backend.fail("create-container:" + role); err != nil {
		return docker.ManagedContainerState{}, err
	}
	if _, _, found := backend.findContainer(request.Name); found {
		return docker.ManagedContainerState{}, errors.New("container conflict")
	}
	container := stateFromRequest(request)
	if backend.malformedRole == role {
		container.Runtime.ReadOnlyRootFS = false
	}
	backend.containers[request.Name] = container
	return fakeCloneContainer(container), nil
}

func (backend *fakeLifecycle) StartManagedContainer(
	_ context.Context,
	id string,
) error {
	container, name, found := backend.findContainer(id)
	if !found {
		return docker.ErrContainerNotFound
	}
	role := container.Labels[docker.InstallRoleLabel]
	if err := backend.fail("start-container:" + role); err != nil {
		return err
	}
	container.Runtime.Running = true
	backend.containers[name] = container
	return nil
}

func (backend *fakeLifecycle) RemoveManagedContainer(
	_ context.Context,
	id string,
) error {
	container, name, found := backend.findContainer(id)
	if !found {
		return nil
	}
	role := container.Labels[docker.InstallRoleLabel]
	if err := backend.fail("remove-container:" + role); err != nil {
		return err
	}
	delete(backend.containers, name)
	return nil
}

func (backend *fakeLifecycle) fail(operation string) error {
	backend.mutations = append(backend.mutations, operation)
	if backend.failMutation == operation {
		backend.failMutation = ""
		return errors.New("injected " + operation + " failure")
	}
	return nil
}

func (backend *fakeLifecycle) findNetwork(
	id string,
) (docker.Network, string, bool) {
	for name, network := range backend.networks {
		if network.ID == id || network.Name == id {
			return network, name, true
		}
	}
	return docker.Network{}, "", false
}

func (backend *fakeLifecycle) findContainer(
	id string,
) (docker.ManagedContainerState, string, bool) {
	for name, container := range backend.containers {
		if container.ID == id || container.Name == id {
			return container, name, true
		}
	}
	return docker.ManagedContainerState{}, "", false
}

func managedSpecification(t *testing.T) domain.InstallationSpecification {
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

func TestBuildRequestsUsesDependencyAndSecurityContract(t *testing.T) {
	requests, err := BuildRequests(managedSpecification(t), "installation-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(requests.Networks) != 2 ||
		requests.Networks[0].Labels[docker.InstallRoleLabel] != "proxy" ||
		requests.Networks[0].Internal ||
		requests.Networks[1].Labels[docker.InstallRoleLabel] != "control" ||
		!requests.Networks[1].Internal {
		t.Fatalf("networks = %#v", requests.Networks)
	}
	if requests.Volume.Name != "docklane-probe-run" ||
		requests.Volume.Labels[docker.InstallManagedLabel] != "true" ||
		requests.Volume.Labels[docker.InstallIDLabel] != "installation-test" {
		t.Fatalf("volume = %#v", requests.Volume)
	}
	roles := []string{}
	for _, container := range requests.Containers {
		roles = append(roles, container.Labels[docker.InstallRoleLabel])
		if !container.ReadOnlyRootFS ||
			!container.NoNewPrivileges ||
			container.RestartPolicy != "unless-stopped" {
			t.Fatalf("insecure container request = %#v", container)
		}
	}
	if !reflect.DeepEqual(roles, []string{"probe", "controller", "gateway"}) {
		t.Fatalf("container order = %v", roles)
	}
	if !requests.Containers[0].DropAllCapabilities {
		t.Fatal("probe capabilities were not dropped")
	}
}

func TestApplyAndRollbackUseReverseDependencyOrder(t *testing.T) {
	requests, err := BuildRequests(managedSpecification(t), "installation-test")
	if err != nil {
		t.Fatal(err)
	}
	backend := newFakeLifecycle()
	transaction, err := Apply(context.Background(), backend, requests)
	if err != nil {
		t.Fatal(err)
	}
	expectedApply := []string{
		"create-network:proxy",
		"create-network:control",
		"create-volume:probe-volume",
		"create-container:probe",
		"start-container:probe",
		"create-container:controller",
		"start-container:controller",
		"create-container:gateway",
		"start-container:gateway",
	}
	if !reflect.DeepEqual(backend.mutations, expectedApply) {
		t.Fatalf("apply order = %v", backend.mutations)
	}
	if len(transaction.Created) != 6 {
		t.Fatalf("created resources = %#v", transaction.Created)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	expectedRollback := []string{
		"remove-container:gateway",
		"remove-container:controller",
		"remove-container:probe",
		"remove-volume:probe-volume",
		"remove-network:control",
		"remove-network:proxy",
	}
	if !reflect.DeepEqual(
		backend.mutations[len(expectedApply):],
		expectedRollback,
	) {
		t.Fatalf("rollback order = %v", backend.mutations[len(expectedApply):])
	}
	if len(backend.networks) != 0 ||
		len(backend.volumes) != 0 ||
		len(backend.containers) != 0 {
		t.Fatalf(
			"resources remain: networks=%v volumes=%v containers=%v",
			backend.networks,
			backend.volumes,
			backend.containers,
		)
	}
}

func TestApplyFailureRollsBackCreatedResources(t *testing.T) {
	requests, err := BuildRequests(managedSpecification(t), "installation-test")
	if err != nil {
		t.Fatal(err)
	}
	backend := newFakeLifecycle()
	backend.failMutation = "start-container:controller"
	_, err = Apply(context.Background(), backend, requests)
	if err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("apply error = %v", err)
	}
	if len(backend.networks) != 0 ||
		len(backend.volumes) != 0 ||
		len(backend.containers) != 0 {
		t.Fatalf("failed apply leaked resources")
	}
}

func TestVerificationFailureRemovesExactCreatedObject(t *testing.T) {
	requests, err := BuildRequests(managedSpecification(t), "installation-test")
	if err != nil {
		t.Fatal(err)
	}
	backend := newFakeLifecycle()
	backend.malformedRole = "probe"
	_, err = Apply(context.Background(), backend, requests)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("verification error = %v", err)
	}
	if len(backend.networks) != 0 ||
		len(backend.volumes) != 0 ||
		len(backend.containers) != 0 {
		t.Fatal("verification failure leaked exact created objects")
	}
}

func TestPreflightConflictDoesNotMutateDocker(t *testing.T) {
	requests, err := BuildRequests(managedSpecification(t), "installation-test")
	if err != nil {
		t.Fatal(err)
	}
	backend := newFakeLifecycle()
	backend.networks["proxy"] = docker.Network{
		ID: "external", Name: "proxy", Driver: "bridge", Scope: "local",
	}
	_, err = Apply(context.Background(), backend, requests)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("conflict error = %v", err)
	}
	if len(backend.mutations) != 0 {
		t.Fatalf("conflict caused mutations: %v", backend.mutations)
	}
	if backend.networks["proxy"].ID != "external" {
		t.Fatal("external network was changed")
	}
}

func TestRollbackRefusesContainerDrift(t *testing.T) {
	requests, err := BuildRequests(managedSpecification(t), "installation-test")
	if err != nil {
		t.Fatal(err)
	}
	backend := newFakeLifecycle()
	transaction, err := Apply(context.Background(), backend, requests)
	if err != nil {
		t.Fatal(err)
	}
	gateway := backend.containers["traefik"]
	gateway.Labels["external-change"] = "true"
	backend.containers["traefik"] = gateway
	err = transaction.Rollback()
	if err == nil || !strings.Contains(err.Error(), "changed after creation") {
		t.Fatalf("rollback drift error = %v", err)
	}
	if _, found := backend.containers["traefik"]; !found {
		t.Fatal("drifted gateway was removed")
	}
	gateway = backend.containers["traefik"]
	delete(gateway.Labels, "external-change")
	backend.containers["traefik"] = gateway
	if err := transaction.Rollback(); err != nil {
		t.Fatalf("retry rollback after repairing drift: %v", err)
	}
	if len(backend.networks) != 0 ||
		len(backend.volumes) != 0 ||
		len(backend.containers) != 0 {
		t.Fatal("retry rollback left managed Docker resources")
	}
}

func stateFromRequest(
	request docker.ManagedContainerRequest,
) docker.ManagedContainerState {
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
	networks := append([]string(nil), request.Networks...)
	sort.Strings(networks)
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
		Networks: networks, Labels: fakeCloneLabels(request.Labels),
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

func fakeCloneNetwork(value docker.Network) docker.Network {
	value.Labels = fakeCloneLabels(value.Labels)
	return value
}

func fakeCloneVolume(value docker.Volume) docker.Volume {
	value.Labels = fakeCloneLabels(value.Labels)
	return value
}

func fakeCloneContainer(
	value docker.ManagedContainerState,
) docker.ManagedContainerState {
	value.Networks = append([]string(nil), value.Networks...)
	value.Labels = fakeCloneLabels(value.Labels)
	value.Runtime.Command = append([]string(nil), value.Runtime.Command...)
	value.Runtime.Mounts = append([]docker.ContainerMount(nil), value.Runtime.Mounts...)
	value.Runtime.PortBindings = append(
		[]docker.ContainerPortBinding(nil),
		value.Runtime.PortBindings...,
	)
	value.Runtime.DroppedCaps = append([]string(nil), value.Runtime.DroppedCaps...)
	return value
}

func fakeCloneLabels(labels map[string]string) map[string]string {
	cloned := map[string]string{}
	for key, value := range labels {
		cloned[key] = value
	}
	return cloned
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
