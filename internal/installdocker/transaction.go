package installdocker

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
)

type Requests struct {
	InstallationID string
	Networks       []docker.ManagedNetworkRequest
	Volume         docker.ManagedVolumeRequest
	Containers     []docker.ManagedContainerRequest
}

type CreatedResource struct {
	Kind     string
	Name     string
	ID       string
	Role     string
	Reverted bool
}

type Transaction struct {
	backend            docker.ManagedLifecycle
	requests           Requests
	Created            []CreatedResource
	observedNetworks   map[string]docker.Network
	observedVolumes    map[string]docker.Volume
	observedContainers map[string]docker.ManagedContainerState
	rolledBack         bool
}

func BuildRequests(
	specification domain.InstallationSpecification,
	installationID string,
) (Requests, error) {
	if err := specification.Validate(); err != nil {
		return Requests{}, err
	}
	if !validInstallationID(installationID) {
		return Requests{}, errors.New(
			"Docker ownership installation ID must use letters, digits, dots, underscores, or hyphens",
		)
	}
	requests := Requests{
		InstallationID: installationID,
		Networks: []docker.ManagedNetworkRequest{
			managedNetwork(specification.ProxyNetwork, "proxy", installationID),
			managedNetwork(specification.ControlNetwork, "control", installationID),
		},
		Volume: docker.ManagedVolumeRequest{
			Name:   specification.ProbeVolume,
			Driver: "local",
			Labels: managedLabels("probe-volume", installationID),
		},
		Containers: []docker.ManagedContainerRequest{},
	}
	byRole := map[string]domain.InstallationContainer{}
	for _, container := range specification.Containers {
		byRole[container.Role] = container
	}
	for _, role := range []string{"probe", "controller", "gateway"} {
		container := byRole[role]
		mounts := make([]docker.ManagedMountRequest, 0, len(container.Mounts))
		for _, mount := range container.Mounts {
			mountType := "bind"
			if mount.Volume {
				mountType = "volume"
			}
			mounts = append(mounts, docker.ManagedMountRequest{
				Type:        mountType,
				Source:      mount.Source,
				Destination: mount.Destination,
				ReadOnly:    mount.ReadOnly,
			})
		}
		ports := make([]docker.ContainerPortBinding, 0, len(container.Ports))
		for _, binding := range container.Ports {
			ports = append(ports, docker.ContainerPortBinding{
				ContainerPort: binding.ContainerPort,
				HostIP:        binding.HostIP,
				HostPort:      binding.HostPort,
			})
		}
		requests.Containers = append(
			requests.Containers,
			docker.ManagedContainerRequest{
				Name:                container.Name,
				Image:               container.Image,
				Command:             append([]string(nil), container.Command...),
				Networks:            append([]string(nil), container.Networks...),
				Mounts:              mounts,
				Ports:               ports,
				ReadOnlyRootFS:      container.ReadOnlyRootFS,
				NoNewPrivileges:     container.NoNewPrivileges,
				DropAllCapabilities: container.DropAllCapabilities,
				RestartPolicy:       container.RestartPolicy,
				Labels:              managedLabels(role, installationID),
			},
		)
	}
	return requests, nil
}

func Apply(
	ctx context.Context,
	backend docker.ManagedLifecycle,
	requests Requests,
) (*Transaction, error) {
	if backend == nil {
		return nil, errors.New("managed Docker lifecycle is required")
	}
	transaction := &Transaction{
		backend:            backend,
		requests:           requests,
		Created:            []CreatedResource{},
		observedNetworks:   map[string]docker.Network{},
		observedVolumes:    map[string]docker.Volume{},
		observedContainers: map[string]docker.ManagedContainerState{},
	}
	if err := preflightAbsent(ctx, backend, requests); err != nil {
		return nil, err
	}
	for _, request := range requests.Networks {
		network, err := backend.CreateManagedNetwork(ctx, request)
		if err != nil {
			return nil, transaction.abort(err)
		}
		if network.ID == "" {
			return nil, transaction.abort(fmt.Errorf(
				"Docker returned an empty ID for network %s",
				request.Name,
			))
		}
		transaction.Created = append(transaction.Created, CreatedResource{
			Kind: "network", Name: request.Name, ID: network.ID,
			Role: request.Labels[docker.InstallRoleLabel],
		})
		transaction.observedNetworks[network.ID] = cloneNetwork(network)
		inspected, err := backend.InspectNetwork(ctx, network.ID)
		if err != nil {
			return nil, transaction.abort(err)
		}
		if inspected.ID != network.ID {
			return nil, transaction.abort(fmt.Errorf(
				"network %s inspect identity changed from %s to %s",
				request.Name,
				network.ID,
				inspected.ID,
			))
		}
		transaction.observedNetworks[network.ID] = cloneNetwork(inspected)
		if err := verifyNetwork(request, inspected); err != nil {
			return nil, transaction.abort(err)
		}
	}
	volume, err := backend.CreateManagedVolume(ctx, requests.Volume)
	if err != nil {
		return nil, transaction.abort(err)
	}
	if volume.Name != requests.Volume.Name {
		return nil, transaction.abort(fmt.Errorf(
			"volume create returned %q for requested %q",
			volume.Name,
			requests.Volume.Name,
		))
	}
	transaction.Created = append(transaction.Created, CreatedResource{
		Kind: "volume", Name: requests.Volume.Name, ID: volume.Name,
		Role: requests.Volume.Labels[docker.InstallRoleLabel],
	})
	transaction.observedVolumes[volume.Name] = cloneVolume(volume)
	inspectedVolume, err := backend.InspectVolume(ctx, volume.Name)
	if err != nil {
		return nil, transaction.abort(err)
	}
	transaction.observedVolumes[volume.Name] = cloneVolume(inspectedVolume)
	if err := verifyVolume(requests.Volume, inspectedVolume); err != nil {
		return nil, transaction.abort(err)
	}
	for _, request := range requests.Containers {
		container, err := backend.CreateManagedContainer(ctx, request)
		if err != nil {
			return nil, transaction.abort(err)
		}
		if container.ID == "" {
			return nil, transaction.abort(fmt.Errorf(
				"Docker returned an empty ID for container %s",
				request.Name,
			))
		}
		transaction.Created = append(transaction.Created, CreatedResource{
			Kind: "container", Name: request.Name, ID: container.ID,
			Role: request.Labels[docker.InstallRoleLabel],
		})
		transaction.observedContainers[container.ID] = cloneContainer(container)
		inspected, err := backend.InspectManagedContainer(ctx, container.ID)
		if err != nil {
			return nil, transaction.abort(err)
		}
		if inspected.ID != container.ID {
			return nil, transaction.abort(fmt.Errorf(
				"container %s inspect identity changed from %s to %s",
				request.Name,
				container.ID,
				inspected.ID,
			))
		}
		transaction.observedContainers[container.ID] = cloneContainer(inspected)
		if err := verifyContainer(request, inspected, false); err != nil {
			return nil, transaction.abort(err)
		}
		if err := backend.StartManagedContainer(ctx, container.ID); err != nil {
			if current, inspectErr := backend.InspectManagedContainer(
				ctx,
				container.ID,
			); inspectErr == nil &&
				current.ID == container.ID &&
				managedLabelsMatch(current.Labels, request.Labels) {
				transaction.observedContainers[container.ID] =
					cloneContainer(current)
			}
			return nil, transaction.abort(err)
		}
		inspected, err = backend.InspectManagedContainer(ctx, container.ID)
		if err != nil {
			return nil, transaction.abort(err)
		}
		if inspected.ID != container.ID {
			return nil, transaction.abort(fmt.Errorf(
				"container %s post-start identity changed from %s to %s",
				request.Name,
				container.ID,
				inspected.ID,
			))
		}
		transaction.observedContainers[container.ID] = cloneContainer(inspected)
		if err := verifyContainer(request, inspected, true); err != nil {
			return nil, transaction.abort(err)
		}
	}
	return transaction, nil
}

func (transaction *Transaction) Rollback() error {
	if transaction == nil || transaction.backend == nil {
		return errors.New("managed Docker transaction is required")
	}
	if transaction.rolledBack {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var rollbackErrors []error
	for index := len(transaction.Created) - 1; index >= 0; index-- {
		resource := &transaction.Created[index]
		if resource.Reverted {
			continue
		}
		var err error
		switch resource.Kind {
		case "container":
			err = transaction.rollbackContainer(ctx, *resource)
		case "volume":
			err = transaction.rollbackVolume(ctx, *resource)
		case "network":
			err = transaction.rollbackNetwork(ctx, *resource)
		default:
			err = fmt.Errorf("unknown created Docker resource kind %q", resource.Kind)
		}
		if err != nil {
			rollbackErrors = append(rollbackErrors, err)
		} else {
			resource.Reverted = true
		}
	}
	if len(rollbackErrors) != 0 {
		return errors.Join(rollbackErrors...)
	}
	transaction.rolledBack = true
	return nil
}

func (transaction *Transaction) abort(cause error) error {
	if rollbackErr := transaction.Rollback(); rollbackErr != nil {
		return errors.Join(cause, fmt.Errorf(
			"rollback managed Docker resources: %w",
			rollbackErr,
		))
	}
	return cause
}

func preflightAbsent(
	ctx context.Context,
	backend docker.ManagedLifecycle,
	requests Requests,
) error {
	for _, request := range requests.Networks {
		if network, err := backend.InspectNetwork(ctx, request.Name); err == nil {
			return fmt.Errorf(
				"managed Docker network target %s already exists as %s",
				request.Name,
				network.ID,
			)
		} else if !errors.Is(err, docker.ErrNetworkNotFound) {
			return err
		}
	}
	if volume, err := backend.InspectVolume(ctx, requests.Volume.Name); err == nil {
		return fmt.Errorf(
			"managed Docker volume target %s already exists",
			volume.Name,
		)
	} else if !errors.Is(err, docker.ErrVolumeNotFound) {
		return err
	}
	for _, request := range requests.Containers {
		if container, err := backend.InspectManagedContainer(
			ctx,
			request.Name,
		); err == nil {
			return fmt.Errorf(
				"managed Docker container target %s already exists as %s",
				request.Name,
				container.ID,
			)
		} else if !errors.Is(err, docker.ErrContainerNotFound) {
			return err
		}
	}
	return nil
}

func (transaction *Transaction) rollbackContainer(
	ctx context.Context,
	resource CreatedResource,
) error {
	request, found := containerRequest(transaction.requests, resource.Name)
	if !found {
		return fmt.Errorf("container rollback contract for %s is missing", resource.Name)
	}
	state, err := transaction.backend.InspectManagedContainer(ctx, resource.ID)
	if errors.Is(err, docker.ErrContainerNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if state.ID != resource.ID {
		return fmt.Errorf("container %s identity changed before rollback", resource.Name)
	}
	observed, found := transaction.observedContainers[resource.ID]
	if !found ||
		!managedLabelsMatch(state.Labels, request.Labels) ||
		!containerStateEqual(state, observed) {
		return fmt.Errorf(
			"refuse container rollback: container %s changed after creation",
			resource.Name,
		)
	}
	if err := transaction.backend.RemoveManagedContainer(ctx, resource.ID); err != nil {
		return err
	}
	if _, err := transaction.backend.InspectManagedContainer(ctx, resource.ID); !errors.Is(
		err,
		docker.ErrContainerNotFound,
	) {
		if err == nil {
			return fmt.Errorf("container %s still exists after removal", resource.Name)
		}
		return err
	}
	return nil
}

func (transaction *Transaction) rollbackVolume(
	ctx context.Context,
	resource CreatedResource,
) error {
	state, err := transaction.backend.InspectVolume(ctx, resource.Name)
	if errors.Is(err, docker.ErrVolumeNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	observed, found := transaction.observedVolumes[resource.Name]
	if !found ||
		!managedLabelsMatch(state.Labels, transaction.requests.Volume.Labels) ||
		!reflect.DeepEqual(state, observed) {
		return fmt.Errorf(
			"refuse volume rollback: volume %s changed after creation",
			resource.Name,
		)
	}
	if err := transaction.backend.RemoveManagedVolume(ctx, resource.Name); err != nil {
		return err
	}
	if _, err := transaction.backend.InspectVolume(ctx, resource.Name); !errors.Is(
		err,
		docker.ErrVolumeNotFound,
	) {
		if err == nil {
			return fmt.Errorf("volume %s still exists after removal", resource.Name)
		}
		return err
	}
	return nil
}

func (transaction *Transaction) rollbackNetwork(
	ctx context.Context,
	resource CreatedResource,
) error {
	request, found := networkRequest(transaction.requests, resource.Name)
	if !found {
		return fmt.Errorf("network rollback contract for %s is missing", resource.Name)
	}
	state, err := transaction.backend.InspectNetwork(ctx, resource.ID)
	if errors.Is(err, docker.ErrNetworkNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if state.ID != resource.ID {
		return fmt.Errorf("network %s identity changed before rollback", resource.Name)
	}
	observed, found := transaction.observedNetworks[resource.ID]
	if !found ||
		!managedLabelsMatch(state.Labels, request.Labels) ||
		!reflect.DeepEqual(state, observed) {
		return fmt.Errorf(
			"refuse network rollback: network %s changed after creation",
			resource.Name,
		)
	}
	if err := transaction.backend.RemoveManagedNetwork(ctx, resource.ID); err != nil {
		return err
	}
	if _, err := transaction.backend.InspectNetwork(ctx, resource.ID); !errors.Is(
		err,
		docker.ErrNetworkNotFound,
	) {
		if err == nil {
			return fmt.Errorf("network %s still exists after removal", resource.Name)
		}
		return err
	}
	return nil
}

func verifyNetwork(
	request docker.ManagedNetworkRequest,
	state docker.Network,
) error {
	if state.Name != request.Name ||
		state.Driver != request.Driver ||
		state.Scope != "local" ||
		state.Internal != request.Internal ||
		state.Attachable != request.Attachable ||
		!managedLabelsMatch(state.Labels, request.Labels) {
		return fmt.Errorf("network %s does not match its managed contract", request.Name)
	}
	return nil
}

func verifyVolume(
	request docker.ManagedVolumeRequest,
	state docker.Volume,
) error {
	if state.Name != request.Name ||
		state.Driver != request.Driver ||
		state.Scope != "local" ||
		!managedLabelsMatch(state.Labels, request.Labels) {
		return fmt.Errorf("volume %s does not match its managed contract", request.Name)
	}
	return nil
}

func verifyContainer(
	request docker.ManagedContainerRequest,
	state docker.ManagedContainerState,
	running bool,
) error {
	expectedNetworks := append([]string(nil), request.Networks...)
	sort.Strings(expectedNetworks)
	expectedMounts := make([]docker.ContainerMount, 0, len(request.Mounts))
	for _, mount := range request.Mounts {
		expectedMounts = append(expectedMounts, docker.ContainerMount{
			Type:        mount.Type,
			Name:        volumeName(mount),
			Source:      mount.Source,
			Destination: mount.Destination,
			ReadOnly:    mount.ReadOnly,
		})
	}
	sort.Slice(expectedMounts, func(i, j int) bool {
		return expectedMounts[i].Destination < expectedMounts[j].Destination
	})
	expectedPorts := append([]docker.ContainerPortBinding(nil), request.Ports...)
	sort.Slice(expectedPorts, func(i, j int) bool {
		left, right := expectedPorts[i], expectedPorts[j]
		if left.ContainerPort != right.ContainerPort {
			return left.ContainerPort < right.ContainerPort
		}
		if left.HostIP != right.HostIP {
			return left.HostIP < right.HostIP
		}
		return left.HostPort < right.HostPort
	})
	var expectedCaps []string
	if request.DropAllCapabilities {
		expectedCaps = []string{"ALL"}
	}
	if state.Name != request.Name ||
		state.Image != request.Image ||
		!slices.Equal(state.Networks, expectedNetworks) ||
		!slices.Equal(state.Runtime.Command, request.Command) ||
		!slices.Equal(state.Runtime.Mounts, expectedMounts) ||
		!slices.Equal(state.Runtime.PortBindings, expectedPorts) ||
		state.Runtime.ReadOnlyRootFS != request.ReadOnlyRootFS ||
		state.Runtime.Privileged ||
		state.Runtime.NoNewPrivileges != request.NoNewPrivileges ||
		!slices.Equal(state.Runtime.DroppedCaps, expectedCaps) ||
		state.Runtime.RestartPolicy != request.RestartPolicy ||
		state.Runtime.Running != running ||
		!managedLabelsMatch(state.Labels, request.Labels) {
		return fmt.Errorf(
			"container %s does not match its managed contract",
			request.Name,
		)
	}
	return nil
}

func managedNetwork(
	name string,
	role string,
	installationID string,
) docker.ManagedNetworkRequest {
	return docker.ManagedNetworkRequest{
		Name:       name,
		Driver:     "bridge",
		Internal:   role == "control",
		Attachable: false,
		Labels:     managedLabels(role, installationID),
	}
}

func managedLabels(role string, installationID string) map[string]string {
	return map[string]string{
		docker.InstallManagedLabel: "true",
		docker.InstallRoleLabel:    role,
		docker.InstallSchemaLabel:  docker.InstallSchemaV1,
		docker.InstallIDLabel:      installationID,
	}
}

func validInstallationID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '.' ||
			character == '_' ||
			character == '-' {
			continue
		}
		return false
	}
	return true
}

func managedLabelsMatch(actual map[string]string, expected map[string]string) bool {
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	for key := range actual {
		if strings.HasPrefix(key, "com.docklane.") {
			if _, found := expected[key]; !found {
				return false
			}
		}
	}
	return true
}

func volumeName(mount docker.ManagedMountRequest) string {
	if mount.Type == "volume" {
		return mount.Source
	}
	return ""
}

func containerRequest(
	requests Requests,
	name string,
) (docker.ManagedContainerRequest, bool) {
	for _, request := range requests.Containers {
		if request.Name == name {
			return request, true
		}
	}
	return docker.ManagedContainerRequest{}, false
}

func networkRequest(
	requests Requests,
	name string,
) (docker.ManagedNetworkRequest, bool) {
	for _, request := range requests.Networks {
		if request.Name == name {
			return request, true
		}
	}
	return docker.ManagedNetworkRequest{}, false
}

func cloneNetwork(value docker.Network) docker.Network {
	value.Labels = cloneLabels(value.Labels)
	return value
}

func cloneVolume(value docker.Volume) docker.Volume {
	value.Labels = cloneLabels(value.Labels)
	return value
}

func cloneContainer(
	value docker.ManagedContainerState,
) docker.ManagedContainerState {
	value.Networks = append([]string(nil), value.Networks...)
	value.Labels = cloneLabels(value.Labels)
	value.Runtime.Command = append([]string(nil), value.Runtime.Command...)
	value.Runtime.Mounts = append(
		[]docker.ContainerMount(nil),
		value.Runtime.Mounts...,
	)
	value.Runtime.PortBindings = append(
		[]docker.ContainerPortBinding(nil),
		value.Runtime.PortBindings...,
	)
	value.Runtime.DroppedCaps = append(
		[]string(nil),
		value.Runtime.DroppedCaps...,
	)
	return value
}

func cloneLabels(labels map[string]string) map[string]string {
	cloned := make(map[string]string, len(labels))
	for key, value := range labels {
		cloned[key] = value
	}
	return cloned
}

func containerStateEqual(
	left docker.ManagedContainerState,
	right docker.ManagedContainerState,
) bool {
	left.Runtime.Running = false
	right.Runtime.Running = false
	left.Runtime.Health = ""
	right.Runtime.Health = ""
	return reflect.DeepEqual(left, right)
}
