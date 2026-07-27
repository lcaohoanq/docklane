package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
)

func (r *Reconciler) NetworkPlan(ctx context.Context) (domain.NetworkPlan, error) {
	plan := domain.NetworkPlan{
		Network: domain.NetworkState{
			Name:       r.network,
			Ownership:  domain.NetworkOwnershipMissing,
			Compatible: true,
		},
		Operations: []domain.NetworkOperation{},
	}
	if r.network == "" {
		return plan, errors.New("proxy network is not configured")
	}
	inspector, ok := r.discovery.(docker.NetworkInspector)
	if !ok {
		return plan, errors.New("Docker network inspection is unavailable")
	}
	network, err := inspector.InspectNetwork(ctx, r.network)
	switch {
	case errors.Is(err, docker.ErrNetworkNotFound):
		plan.Operations = append(plan.Operations, domain.NetworkOperation{
			Action:      domain.NetworkActionCreate,
			Reason:      "configured proxy network does not exist",
			Destructive: false,
		})
	case err != nil:
		return plan, err
	default:
		plan.Network = classifyNetwork(network)
		if !plan.Network.Compatible {
			sealNetworkPlan(&plan)
			return plan, nil
		}
	}

	routes, err := r.store.ListRoutes(ctx)
	if err != nil {
		return plan, fmt.Errorf("list desired routes: %w", err)
	}
	containers, err := r.discovery.ListContainers(ctx)
	if err != nil {
		return plan, fmt.Errorf("discover containers: %w", err)
	}
	checkedAt := time.Now().UTC()
	observations := make(map[int64]domain.RouteObservation, len(routes))
	desiredAliases := make(map[string][]string)
	for _, route := range routes {
		observation := observe(route, containers, checkedAt)
		observations[route.ID] = observation
		if observation.State == domain.RouteStateReady {
			desiredAliases[observation.ContainerID] = append(
				desiredAliases[observation.ContainerID],
				networkAlias(route.ID),
			)
		}
	}
	for containerID := range desiredAliases {
		sort.Strings(desiredAliases[containerID])
	}
	if err := r.hydrateNetworkAliases(ctx, desiredAliases, containers); err != nil {
		return plan, err
	}

	owned := r.ownedAttachments(ctx)
	containerByID := make(map[string]docker.Container, len(containers))
	for _, container := range containers {
		containerByID[container.ID] = container
	}
	planned := map[string]bool{}
	for _, route := range routes {
		observation := observations[route.ID]
		if observation.State != domain.RouteStateReady ||
			planned[observation.ContainerID] {
			continue
		}
		planned[observation.ContainerID] = true
		container := containerByID[observation.ContainerID]
		aliases := desiredAliases[container.ID]
		switch {
		case !container.HasNetwork(r.network):
			plan.Operations = append(plan.Operations, domain.NetworkOperation{
				Action:        domain.NetworkActionConnect,
				ContainerID:   container.ID,
				ContainerName: container.Name,
				Aliases:       aliases,
				Reason:        "enabled route requires proxy-network access",
				Destructive:   false,
			})
		case hasAllAliases(container, r.network, aliases):
			continue
		case owned[container.ID]:
			plan.Operations = append(
				plan.Operations,
				domain.NetworkOperation{
					Action:        domain.NetworkActionDisconnect,
					ContainerID:   container.ID,
					ContainerName: container.Name,
					Reason:        "Docklane-owned endpoint is missing deterministic aliases",
					Destructive:   true,
				},
				domain.NetworkOperation{
					Action:        domain.NetworkActionConnect,
					ContainerID:   container.ID,
					ContainerName: container.Name,
					Aliases:       aliases,
					Reason:        "restore owned endpoint with complete route aliases",
					Destructive:   false,
				},
			)
		default:
			plan.Warnings = append(
				plan.Warnings,
				fmt.Sprintf(
					"container %s has a pre-existing %s attachment; "+
						"Docklane will preserve it and use the container name",
					container.Name,
					r.network,
				),
			)
		}
	}

	needed := map[string]bool{}
	for _, observation := range observations {
		if observation.State == domain.RouteStateReady {
			needed[observation.ContainerID] = true
		}
	}
	if r.attachments != nil {
		attachments, err := r.attachments.ListNetworkAttachments(ctx)
		if err != nil {
			return plan, fmt.Errorf("list managed network attachments: %w", err)
		}
		for _, attachment := range attachments {
			if attachment.Network != r.network || needed[attachment.ContainerID] {
				continue
			}
			container, exists := containerByID[attachment.ContainerID]
			if !exists {
				continue
			}
			plan.Operations = append(plan.Operations, domain.NetworkOperation{
				Action:        domain.NetworkActionDisconnect,
				ContainerID:   container.ID,
				ContainerName: container.Name,
				Reason:        "no ready Docklane route needs this owned attachment",
				Destructive:   true,
			})
		}
	}

	gatewayFound := false
	for _, container := range containers {
		if container.SystemRole != docker.SystemRoleReverseProxy {
			continue
		}
		gatewayFound = true
		if !container.HasNetwork(r.network) {
			plan.Warnings = append(
				plan.Warnings,
				fmt.Sprintf(
					"active reverse proxy %s is not connected to %s; "+
						"configure its persistent deployment before expecting routed traffic",
					container.Name,
					r.network,
				),
			)
		}
	}
	if !gatewayFound {
		plan.Warnings = append(
			plan.Warnings,
			"no active reverse proxy was discovered; the network can be prepared but routes will not be reachable",
		)
	}
	sealNetworkPlan(&plan)
	return plan, nil
}

func (r *Reconciler) ApplyNetworkPlan(
	ctx context.Context,
	expectedToken string,
) (domain.NetworkApplyResult, error) {
	plan, err := r.NetworkPlan(ctx)
	result := domain.NetworkApplyResult{Applied: plan}
	if err != nil {
		return result, err
	}
	if expectedToken == "" || expectedToken != plan.Token {
		return result, errors.New(
			"network plan changed or was not supplied; fetch and review a new plan",
		)
	}
	if !plan.Network.Compatible {
		return result, fmt.Errorf(
			"network %s is incompatible and cannot be applied safely",
			plan.Network.Name,
		)
	}
	for _, operation := range plan.Operations {
		if operation.Action == domain.NetworkActionCreate {
			if r.lifecycle == nil {
				return result, errors.New("Docker network creation is disabled")
			}
			network, err := r.lifecycle.CreateNetwork(
				ctx,
				r.network,
				docker.ManagedProxyNetworkLabels(),
			)
			if err != nil {
				return result, err
			}
			if state := classifyNetwork(network); !state.Compatible {
				return result, fmt.Errorf(
					"network %s appeared during creation but is incompatible",
					r.network,
				)
			}
			continue
		}
		if r.manager == nil {
			return result, errors.New("Docker network attachment management is disabled")
		}
	}
	if err := r.Refresh(ctx); err != nil {
		return result, err
	}
	result.Remaining, err = r.NetworkPlan(ctx)
	return result, err
}

func sealNetworkPlan(plan *domain.NetworkPlan) {
	sort.Strings(plan.Warnings)
	payload, _ := json.Marshal(struct {
		Network    domain.NetworkState
		Operations []domain.NetworkOperation
		Warnings   []string
	}{
		Network:    plan.Network,
		Operations: plan.Operations,
		Warnings:   plan.Warnings,
	})
	sum := sha256.Sum256(payload)
	plan.Token = fmt.Sprintf("%x", sum)
}

func classifyNetwork(network docker.Network) domain.NetworkState {
	state := domain.NetworkState{
		Name:       network.Name,
		ID:         network.ID,
		Driver:     network.Driver,
		Scope:      network.Scope,
		Ownership:  domain.NetworkOwnershipExternal,
		Compatible: network.Driver == "bridge" && network.Scope == "local",
		Labels:     network.Labels,
	}
	managed := strings.EqualFold(network.Labels[docker.NetworkManagedLabel], "true")
	hasDocklaneLabels := managed ||
		network.Labels[docker.NetworkRoleLabel] != "" ||
		network.Labels[docker.NetworkSchemaLabel] != ""
	if managed &&
		network.Labels[docker.NetworkRoleLabel] == docker.NetworkRoleProxy &&
		network.Labels[docker.NetworkSchemaLabel] == docker.NetworkSchemaV1 {
		state.Ownership = domain.NetworkOwnershipManaged
		return state
	}
	if hasDocklaneLabels {
		state.Ownership = domain.NetworkOwnershipConflict
		state.Compatible = false
		return state
	}
	if !state.Compatible {
		state.Ownership = domain.NetworkOwnershipConflict
	}
	return state
}

func hasAllAliases(container docker.Container, network string, aliases []string) bool {
	for _, alias := range aliases {
		if !container.HasNetworkAlias(network, alias) {
			return false
		}
	}
	return true
}
