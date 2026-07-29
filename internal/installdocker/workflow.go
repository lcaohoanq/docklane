package installdocker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installworkflow"
)

const (
	proxyNetworkResourceID   = "proxy-network"
	controlNetworkResourceID = "docklane-control-network"
	probeVolumeResourceID    = "docklane-probe-volume"
	probeResourceID          = "docklane-probe"
	controllerResourceID     = "docklane-controller"
	gatewayResourceID        = "global-traefik"
)

type WorkflowAdapter struct {
	Steps []installworkflow.Step
}

func NewWorkflowAdapter(
	backend docker.ManagedLifecycle,
	specification domain.InstallationSpecification,
	installationID string,
	resources []domain.InstallationResource,
) (*WorkflowAdapter, error) {
	if backend == nil {
		return nil, errors.New("managed Docker lifecycle is required")
	}
	requests, err := BuildRequests(specification, installationID)
	if err != nil {
		return nil, err
	}
	adapter := &WorkflowAdapter{Steps: []installworkflow.Step{}}
	byID := resourceByID(resources)
	for index, request := range requests.Networks {
		resourceID := proxyNetworkResourceID
		if index == 1 {
			resourceID = controlNetworkResourceID
		}
		resource, managed, err := managedDockerResource(
			byID,
			resourceID,
			domain.ResourceDockerNetwork,
			request.Name,
		)
		if err != nil {
			return nil, err
		}
		if managed {
			adapter.Steps = append(
				adapter.Steps,
				networkStep(backend, resource, request),
			)
		}
	}
	resource, managed, err := managedDockerResource(
		byID,
		probeVolumeResourceID,
		domain.ResourceDockerVolume,
		requests.Volume.Name,
	)
	if err != nil {
		return nil, err
	}
	if managed {
		adapter.Steps = append(
			adapter.Steps,
			volumeStep(backend, resource, requests.Volume),
		)
	}
	containerIDs := []string{
		probeResourceID,
		controllerResourceID,
		gatewayResourceID,
	}
	for index, request := range requests.Containers {
		resource, managed, err = managedDockerResource(
			byID,
			containerIDs[index],
			domain.ResourceDockerContainer,
			request.Name,
		)
		if err != nil {
			return nil, err
		}
		if managed {
			adapter.Steps = append(
				adapter.Steps,
				containerStep(backend, resource, request),
			)
		}
	}
	known := map[string]bool{
		proxyNetworkResourceID:   true,
		controlNetworkResourceID: true,
		probeVolumeResourceID:    true,
		probeResourceID:          true,
		controllerResourceID:     true,
		gatewayResourceID:        true,
	}
	for _, resource := range resources {
		if resource.Ownership == domain.ResourceManaged &&
			(resource.Kind == domain.ResourceDockerNetwork ||
				resource.Kind == domain.ResourceDockerVolume ||
				resource.Kind == domain.ResourceDockerContainer) &&
			!known[resource.ID] {
			return nil, fmt.Errorf(
				"managed Docker resource %s has no workflow request",
				resource.ID,
			)
		}
	}
	return adapter, nil
}

func networkStep(
	backend docker.ManagedLifecycle,
	resource domain.InstallationResource,
	request docker.ManagedNetworkRequest,
) installworkflow.Step {
	return installworkflow.Step{
		ID:                "create-" + resource.ID,
		ResourceID:        resource.ID,
		Target:            resource.Target,
		Stage:             domain.ExecutionDocker,
		IntentFingerprint: requestFingerprint(request),
		Apply: func(ctx context.Context) (
			domain.InstallationObservation,
			error,
		) {
			disposition, observation, err := inspectNetwork(
				ctx,
				backend,
				request,
				nil,
			)
			if err != nil {
				return domain.InstallationObservation{}, err
			}
			switch disposition {
			case installworkflow.DispositionApplied:
				return observation, nil
			case installworkflow.DispositionConflict:
				return domain.InstallationObservation{}, fmt.Errorf(
					"managed Docker network %s conflicts",
					request.Name,
				)
			}
			created, err := backend.CreateManagedNetwork(ctx, request)
			if err != nil {
				return domain.InstallationObservation{}, err
			}
			if err := verifyNetwork(request, created); err != nil {
				return domain.InstallationObservation{}, errors.Join(
					err,
					removeCreatedNetwork(ctx, backend, request, created),
				)
			}
			return networkObservation(created), nil
		},
		Inspect: func(
			ctx context.Context,
			recorded *domain.InstallationObservation,
		) (installworkflow.Disposition, domain.InstallationObservation, error) {
			return inspectNetwork(ctx, backend, request, recorded)
		},
		Rollback: func(
			ctx context.Context,
			observation domain.InstallationObservation,
		) error {
			current, err := backend.InspectNetwork(ctx, observation.ExternalID)
			if errors.Is(err, docker.ErrNetworkNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := verifyNetwork(request, current); err != nil ||
				!sameObservation(observation, networkObservation(current)) {
				return fmt.Errorf(
					"refuse network rollback: network %s changed after creation",
					request.Name,
				)
			}
			if err := backend.RemoveManagedNetwork(
				ctx,
				observation.ExternalID,
			); err != nil {
				return err
			}
			return verifyNetworkAbsent(ctx, backend, observation.ExternalID)
		},
	}
}

func volumeStep(
	backend docker.ManagedLifecycle,
	resource domain.InstallationResource,
	request docker.ManagedVolumeRequest,
) installworkflow.Step {
	return installworkflow.Step{
		ID:                "create-" + resource.ID,
		ResourceID:        resource.ID,
		Target:            resource.Target,
		Stage:             domain.ExecutionDocker,
		IntentFingerprint: requestFingerprint(request),
		Apply: func(ctx context.Context) (
			domain.InstallationObservation,
			error,
		) {
			disposition, observation, err := inspectVolume(
				ctx,
				backend,
				request,
				nil,
			)
			if err != nil {
				return domain.InstallationObservation{}, err
			}
			switch disposition {
			case installworkflow.DispositionApplied:
				return observation, nil
			case installworkflow.DispositionConflict:
				return domain.InstallationObservation{}, fmt.Errorf(
					"managed Docker volume %s conflicts",
					request.Name,
				)
			}
			created, err := backend.CreateManagedVolume(ctx, request)
			if err != nil {
				return domain.InstallationObservation{}, err
			}
			if err := verifyVolume(request, created); err != nil {
				return domain.InstallationObservation{}, errors.Join(
					err,
					removeCreatedVolume(ctx, backend, request, created),
				)
			}
			return volumeObservation(created), nil
		},
		Inspect: func(
			ctx context.Context,
			recorded *domain.InstallationObservation,
		) (installworkflow.Disposition, domain.InstallationObservation, error) {
			return inspectVolume(ctx, backend, request, recorded)
		},
		Rollback: func(
			ctx context.Context,
			observation domain.InstallationObservation,
		) error {
			current, err := backend.InspectVolume(ctx, request.Name)
			if errors.Is(err, docker.ErrVolumeNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := verifyVolume(request, current); err != nil ||
				!sameObservation(observation, volumeObservation(current)) {
				return fmt.Errorf(
					"refuse volume rollback: volume %s changed after creation",
					request.Name,
				)
			}
			if err := backend.RemoveManagedVolume(ctx, request.Name); err != nil {
				return err
			}
			return verifyVolumeAbsent(ctx, backend, request.Name)
		},
	}
}

func containerStep(
	backend docker.ManagedLifecycle,
	resource domain.InstallationResource,
	request docker.ManagedContainerRequest,
) installworkflow.Step {
	return installworkflow.Step{
		ID:                "create-" + resource.ID,
		ResourceID:        resource.ID,
		Target:            resource.Target,
		Stage:             domain.ExecutionDocker,
		IntentFingerprint: requestFingerprint(request),
		Apply: func(ctx context.Context) (
			domain.InstallationObservation,
			error,
		) {
			disposition, observation, err := inspectContainer(
				ctx,
				backend,
				request,
				nil,
			)
			if err != nil {
				return domain.InstallationObservation{}, err
			}
			if disposition == installworkflow.DispositionConflict {
				return domain.InstallationObservation{}, fmt.Errorf(
					"managed Docker container %s conflicts",
					request.Name,
				)
			}
			if disposition == installworkflow.DispositionPending {
				created, createErr := backend.CreateManagedContainer(ctx, request)
				if createErr != nil {
					return domain.InstallationObservation{}, createErr
				}
				if err := verifyContainer(request, created, false); err != nil {
					return domain.InstallationObservation{}, errors.Join(
						err,
						removeCreatedContainer(
							ctx,
							backend,
							request,
							created,
						),
					)
				}
				observation = containerObservation(created)
			}
			current, err := backend.InspectManagedContainer(
				ctx,
				observation.ExternalID,
			)
			if err != nil {
				return domain.InstallationObservation{}, err
			}
			if !current.Runtime.Running {
				if err := backend.StartManagedContainer(
					ctx,
					observation.ExternalID,
				); err != nil {
					return domain.InstallationObservation{}, err
				}
				current, err = backend.InspectManagedContainer(
					ctx,
					observation.ExternalID,
				)
				if err != nil {
					return domain.InstallationObservation{}, err
				}
			}
			if err := verifyContainer(request, current, true); err != nil {
				return domain.InstallationObservation{}, errors.Join(
					err,
					removeCreatedContainer(
						ctx,
						backend,
						request,
						current,
					),
				)
			}
			return containerObservation(current), nil
		},
		Inspect: func(
			ctx context.Context,
			recorded *domain.InstallationObservation,
		) (installworkflow.Disposition, domain.InstallationObservation, error) {
			return inspectContainer(ctx, backend, request, recorded)
		},
		Rollback: func(
			ctx context.Context,
			observation domain.InstallationObservation,
		) error {
			current, err := backend.InspectManagedContainer(
				ctx,
				observation.ExternalID,
			)
			if errors.Is(err, docker.ErrContainerNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := verifyContainer(
				request,
				current,
				current.Runtime.Running,
			); err != nil ||
				!sameObservation(
					observation,
					containerObservation(current),
				) {
				return fmt.Errorf(
					"refuse container rollback: container %s changed after creation",
					request.Name,
				)
			}
			if err := backend.RemoveManagedContainer(
				ctx,
				observation.ExternalID,
			); err != nil {
				return err
			}
			return verifyContainerAbsent(ctx, backend, observation.ExternalID)
		},
	}
}

func inspectNetwork(
	ctx context.Context,
	backend docker.ManagedLifecycle,
	request docker.ManagedNetworkRequest,
	recorded *domain.InstallationObservation,
) (installworkflow.Disposition, domain.InstallationObservation, error) {
	current, err := backend.InspectNetwork(ctx, request.Name)
	if errors.Is(err, docker.ErrNetworkNotFound) {
		return absentDisposition(recorded)
	}
	if err != nil {
		return "", domain.InstallationObservation{}, err
	}
	observation := networkObservation(current)
	if err := verifyNetwork(request, current); err != nil ||
		(recorded != nil && !sameObservation(*recorded, observation)) {
		return installworkflow.DispositionConflict,
			domain.InstallationObservation{},
			nil
	}
	return installworkflow.DispositionApplied, observation, nil
}

func inspectVolume(
	ctx context.Context,
	backend docker.ManagedLifecycle,
	request docker.ManagedVolumeRequest,
	recorded *domain.InstallationObservation,
) (installworkflow.Disposition, domain.InstallationObservation, error) {
	current, err := backend.InspectVolume(ctx, request.Name)
	if errors.Is(err, docker.ErrVolumeNotFound) {
		return absentDisposition(recorded)
	}
	if err != nil {
		return "", domain.InstallationObservation{}, err
	}
	observation := volumeObservation(current)
	if err := verifyVolume(request, current); err != nil ||
		(recorded != nil && !sameObservation(*recorded, observation)) {
		return installworkflow.DispositionConflict,
			domain.InstallationObservation{},
			nil
	}
	return installworkflow.DispositionApplied, observation, nil
}

func inspectContainer(
	ctx context.Context,
	backend docker.ManagedLifecycle,
	request docker.ManagedContainerRequest,
	recorded *domain.InstallationObservation,
) (installworkflow.Disposition, domain.InstallationObservation, error) {
	current, err := backend.InspectManagedContainer(ctx, request.Name)
	if errors.Is(err, docker.ErrContainerNotFound) {
		return absentDisposition(recorded)
	}
	if err != nil {
		return "", domain.InstallationObservation{}, err
	}
	observation := containerObservation(current)
	if err := verifyContainer(
		request,
		current,
		current.Runtime.Running,
	); err != nil ||
		(recorded != nil && !sameObservation(*recorded, observation)) {
		return installworkflow.DispositionConflict,
			domain.InstallationObservation{},
			nil
	}
	return installworkflow.DispositionApplied, observation, nil
}

func absentDisposition(
	recorded *domain.InstallationObservation,
) (installworkflow.Disposition, domain.InstallationObservation, error) {
	if recorded == nil {
		return installworkflow.DispositionPending,
			domain.InstallationObservation{},
			nil
	}
	return installworkflow.DispositionRolledBack, *recorded, nil
}

func networkObservation(
	state docker.Network,
) domain.InstallationObservation {
	return domain.InstallationObservation{
		ExternalID:  state.ID,
		Fingerprint: stateFingerprint(state),
		Created:     true,
	}
}

func volumeObservation(
	state docker.Volume,
) domain.InstallationObservation {
	return domain.InstallationObservation{
		ExternalID:  state.Name,
		Fingerprint: stateFingerprint(state),
		Created:     true,
	}
}

func containerObservation(
	state docker.ManagedContainerState,
) domain.InstallationObservation {
	state.Runtime.Running = false
	state.Runtime.Health = ""
	return domain.InstallationObservation{
		ExternalID:  state.ID,
		Fingerprint: stateFingerprint(state),
		Created:     true,
	}
}

func sameObservation(
	left domain.InstallationObservation,
	right domain.InstallationObservation,
) bool {
	return left.ExternalID == right.ExternalID &&
		left.Fingerprint == right.Fingerprint &&
		left.Created == right.Created &&
		left.Backup == nil &&
		right.Backup == nil &&
		left.SnapshotFingerprint == "" &&
		right.SnapshotFingerprint == ""
}

func stateFingerprint(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func requestFingerprint(value any) string {
	return stateFingerprint(value)
}

func verifyNetworkAbsent(
	ctx context.Context,
	backend docker.ManagedLifecycle,
	id string,
) error {
	if _, err := backend.InspectNetwork(ctx, id); !errors.Is(
		err,
		docker.ErrNetworkNotFound,
	) {
		if err == nil {
			return errors.New("managed Docker network remains after rollback")
		}
		return err
	}
	return nil
}

func verifyVolumeAbsent(
	ctx context.Context,
	backend docker.ManagedLifecycle,
	name string,
) error {
	if _, err := backend.InspectVolume(ctx, name); !errors.Is(
		err,
		docker.ErrVolumeNotFound,
	) {
		if err == nil {
			return errors.New("managed Docker volume remains after rollback")
		}
		return err
	}
	return nil
}

func verifyContainerAbsent(
	ctx context.Context,
	backend docker.ManagedLifecycle,
	id string,
) error {
	if _, err := backend.InspectManagedContainer(ctx, id); !errors.Is(
		err,
		docker.ErrContainerNotFound,
	) {
		if err == nil {
			return errors.New("managed Docker container remains after rollback")
		}
		return err
	}
	return nil
}

func removeCreatedNetwork(
	ctx context.Context,
	backend docker.ManagedLifecycle,
	request docker.ManagedNetworkRequest,
	created docker.Network,
) error {
	if created.ID == "" ||
		created.Name != request.Name ||
		!managedLabelsMatch(created.Labels, request.Labels) {
		return errors.New(
			"cannot compensate malformed Docker network without exact ownership",
		)
	}
	if err := backend.RemoveManagedNetwork(ctx, created.ID); err != nil {
		return fmt.Errorf("compensate managed Docker network: %w", err)
	}
	return verifyNetworkAbsent(ctx, backend, created.ID)
}

func removeCreatedVolume(
	ctx context.Context,
	backend docker.ManagedLifecycle,
	request docker.ManagedVolumeRequest,
	created docker.Volume,
) error {
	if created.Name != request.Name ||
		!managedLabelsMatch(created.Labels, request.Labels) {
		return errors.New(
			"cannot compensate malformed Docker volume without exact ownership",
		)
	}
	if err := backend.RemoveManagedVolume(ctx, created.Name); err != nil {
		return fmt.Errorf("compensate managed Docker volume: %w", err)
	}
	return verifyVolumeAbsent(ctx, backend, created.Name)
}

func removeCreatedContainer(
	ctx context.Context,
	backend docker.ManagedLifecycle,
	request docker.ManagedContainerRequest,
	created docker.ManagedContainerState,
) error {
	if created.ID == "" ||
		created.Name != request.Name ||
		!managedLabelsMatch(created.Labels, request.Labels) {
		return errors.New(
			"cannot compensate malformed Docker container without exact ownership",
		)
	}
	if err := backend.RemoveManagedContainer(ctx, created.ID); err != nil {
		return fmt.Errorf("compensate managed Docker container: %w", err)
	}
	return verifyContainerAbsent(ctx, backend, created.ID)
}

func managedDockerResource(
	resources map[string]domain.InstallationResource,
	id string,
	kind domain.ResourceKind,
	target string,
) (domain.InstallationResource, bool, error) {
	resource, exists := resources[id]
	if !exists || resource.Ownership != domain.ResourceManaged {
		return domain.InstallationResource{}, false, nil
	}
	if err := resource.Validate(); err != nil {
		return domain.InstallationResource{}, false, err
	}
	if resource.Kind != kind ||
		resource.Target != target ||
		resource.Rollback != domain.RollbackRemove {
		return domain.InstallationResource{}, false, fmt.Errorf(
			"managed Docker resource %s does not match its request",
			id,
		)
	}
	return resource, true, nil
}

func resourceByID(
	resources []domain.InstallationResource,
) map[string]domain.InstallationResource {
	result := make(map[string]domain.InstallationResource, len(resources))
	for _, resource := range resources {
		result[resource.ID] = resource
	}
	return result
}
