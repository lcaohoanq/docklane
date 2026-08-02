package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"docklane.local/docklane/internal/domain"
)

type Container struct {
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	Image            string              `json:"image"`
	State            string              `json:"state"`
	Status           string              `json:"status"`
	SystemRole       string              `json:"systemRole,omitempty"`
	ComposeProject   string              `json:"composeProject,omitempty"`
	ComposeService   string              `json:"composeService,omitempty"`
	ExposedPorts     []uint16            `json:"exposedPorts"`
	PublishedPorts   []uint16            `json:"publishedPorts,omitempty"`
	Labels           map[string]string   `json:"-"`
	Networks         []string            `json:"networks"`
	NetworkAliases   map[string][]string `json:"networkAliases,omitempty"`
	RouteEligibility RouteEligibility    `json:"routeEligibility"`
}

// RouteEligibility describes whether a discovered container can back an HTTP route.
type RouteEligibility struct {
	Eligible bool   `json:"eligible"`
	Code     string `json:"code,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

const (
	// SystemRoleReverseProxy identifies the active HTTP gateway container.
	SystemRoleReverseProxy = "reverse-proxy"
	// SystemRoleController identifies the Docklane control-plane container.
	SystemRoleController = "controller"
	// SystemRoleProbe identifies the restricted upstream probe container.
	SystemRoleProbe = "probe"

	// RouteEnabledLabel lets a container explicitly opt out of HTTP routing.
	RouteEnabledLabel = "com.docklane.route"

	// RouteEligibilitySystemWorkload marks an internal Docklane workload.
	RouteEligibilitySystemWorkload = "system-workload"
	// RouteEligibilityDisabled marks a container with routing explicitly disabled.
	RouteEligibilityDisabled = "routing-disabled"
	// RouteEligibilityNoTCPPorts marks a container without declared TCP ports.
	RouteEligibilityNoTCPPorts = "no-tcp-ports"
)

type Discovery interface {
	ListContainers(context.Context) ([]Container, error)
}

type NetworkAliasDiscovery interface {
	NetworkAliases(context.Context, string, string) ([]string, error)
}

type EventSource interface {
	WatchContainerEvents(context.Context, func()) error
}

type NetworkManager interface {
	ConnectNetwork(context.Context, string, string, []string) error
	DisconnectNetwork(context.Context, string, string) error
}

var (
	ErrNoMatch        = errors.New("no running container matches the route selector")
	ErrAmbiguous      = errors.New("route selector matches multiple running containers")
	ErrPortNotExposed = errors.New("configured TCP port is not declared by the container")
	ErrSystemTarget   = errors.New("system container cannot be used as an application route target")
	ErrRouteDisabled  = errors.New("container routing is disabled by label")
)

func ResolveContainer(selector domain.ContainerSelector, containers []Container) (Container, error) {
	var matches []Container
	for _, container := range containers {
		if selector.ContainerID != "" {
			if container.ID == selector.ContainerID || strings.HasPrefix(container.ID, selector.ContainerID) {
				matches = append(matches, container)
			}
			continue
		}
		if container.ComposeProject == selector.ComposeProject &&
			container.ComposeService == selector.ComposeService {
			matches = append(matches, container)
		}
	}
	switch len(matches) {
	case 0:
		return Container{}, ErrNoMatch
	case 1:
		return matches[0], nil
	default:
		return Container{}, fmt.Errorf("%w: %d matches", ErrAmbiguous, len(matches))
	}
}

func ValidateTCPPort(container Container, port uint16) error {
	for _, exposed := range container.ExposedPorts {
		if exposed == port {
			return nil
		}
	}
	if len(container.ExposedPorts) == 0 {
		return fmt.Errorf(
			"%w: %s declares no TCP ports; choose a declared internal port",
			ErrPortNotExposed,
			container.Name,
		)
	}
	return fmt.Errorf(
		"%w: %s does not declare TCP port %d; available ports: %v",
		ErrPortNotExposed,
		container.Name,
		port,
		container.ExposedPorts,
	)
}

func ValidateApplicationTarget(container Container) error {
	if container.SystemRole != "" {
		if container.SystemRole != SystemRoleReverseProxy {
			return fmt.Errorf(
				"%w: %s is a Docklane %s system workload",
				ErrSystemTarget,
				container.Name,
				container.SystemRole,
			)
		}
		return fmt.Errorf(
			"%w: %s is the active reverse proxy and routing it to itself creates a loop; "+
				"use a Traefik api@internal dashboard router instead",
			ErrSystemTarget,
			container.Name,
		)
	}
	if strings.EqualFold(
		strings.TrimSpace(container.Labels[RouteEnabledLabel]),
		"false",
	) {
		return fmt.Errorf(
			"%w: %s sets %s=false",
			ErrRouteDisabled,
			container.Name,
			RouteEnabledLabel,
		)
	}
	return nil
}

// EvaluateRouteEligibility classifies a container without removing it from inventory.
func EvaluateRouteEligibility(container Container) RouteEligibility {
	if err := ValidateApplicationTarget(container); err != nil {
		if errors.Is(err, ErrSystemTarget) {
			return RouteEligibility{
				Code:   RouteEligibilitySystemWorkload,
				Reason: "System workload",
			}
		}
		return RouteEligibility{
			Code:   RouteEligibilityDisabled,
			Reason: "Routing disabled",
		}
	}
	if len(container.ExposedPorts) == 0 {
		return RouteEligibility{
			Code:   RouteEligibilityNoTCPPorts,
			Reason: "No TCP ports declared",
		}
	}
	return RouteEligibility{Eligible: true}
}

type Client struct {
	http *http.Client
}

func NewClient(socketPath string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socketPath)
		},
	}
	return &Client{http: &http.Client{Transport: transport}}
}

func (c *Client) ListContainers(ctx context.Context) ([]Container, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodGet,
		"http://docker/containers/json",
		nil,
	)
	if err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("list Docker containers: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Docker returned %s", response.Status)
	}

	var raw []struct {
		ID      string            `json:"Id"`
		Names   []string          `json:"Names"`
		Image   string            `json:"Image"`
		Command string            `json:"Command"`
		State   string            `json:"State"`
		Status  string            `json:"Status"`
		Labels  map[string]string `json:"Labels"`
		Ports   []struct {
			PrivatePort uint16 `json:"PrivatePort"`
			PublicPort  uint16 `json:"PublicPort"`
			Type        string `json:"Type"`
		} `json:"Ports"`
		NetworkSettings struct {
			Networks map[string]json.RawMessage `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := json.NewDecoder(response.Body).Decode(&raw); err != nil {
		return nil, err
	}

	containers := make([]Container, 0, len(raw))
	for _, item := range raw {
		name := item.ID[:12]
		if len(item.Names) > 0 {
			name = strings.TrimPrefix(item.Names[0], "/")
		}
		ports := make([]uint16, 0, len(item.Ports))
		publishedPorts := make([]uint16, 0, len(item.Ports))
		seen := map[uint16]bool{}
		seenPublished := map[uint16]bool{}
		publishesGatewayPort := false
		for _, port := range item.Ports {
			if port.Type == "tcp" && !seen[port.PrivatePort] {
				seen[port.PrivatePort] = true
				ports = append(ports, port.PrivatePort)
			}
			if port.Type == "tcp" &&
				((port.PrivatePort == 80 && port.PublicPort == 80) ||
					(port.PrivatePort == 443 && port.PublicPort == 443)) {
				publishesGatewayPort = true
			}
			if port.Type == "tcp" &&
				port.PublicPort != 0 &&
				!seenPublished[port.PublicPort] {
				seenPublished[port.PublicPort] = true
				publishedPorts = append(publishedPorts, port.PublicPort)
			}
		}
		sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
		sort.Slice(
			publishedPorts,
			func(i, j int) bool { return publishedPorts[i] < publishedPorts[j] },
		)
		networks := make([]string, 0, len(item.NetworkSettings.Networks))
		for network := range item.NetworkSettings.Networks {
			networks = append(networks, network)
		}
		sort.Strings(networks)
		containers = append(containers, Container{
			ID:     item.ID,
			Name:   name,
			Image:  item.Image,
			State:  item.State,
			Status: item.Status,
			SystemRole: detectSystemRole(
				item.Image,
				item.Labels,
				publishesGatewayPort,
				item.Command,
			),
			ComposeProject: item.Labels["com.docker.compose.project"],
			ComposeService: item.Labels["com.docker.compose.service"],
			ExposedPorts:   ports,
			PublishedPorts: publishedPorts,
			Labels:         item.Labels,
			Networks:       networks,
		})
	}
	return containers, nil
}

func (c Container) HasNetwork(name string) bool {
	for _, network := range c.Networks {
		if network == name {
			return true
		}
	}
	return false
}

func (c Container) HasNetworkAlias(network, alias string) bool {
	for _, candidate := range c.NetworkAliases[network] {
		if candidate == alias {
			return true
		}
	}
	return false
}

func (c *Client) NetworkAliases(
	ctx context.Context,
	containerID string,
	network string,
) ([]string, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodGet,
		"http://docker/containers/"+url.PathEscape(containerID)+"/json",
		nil,
	)
	if err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("inspect Docker container %s: %w", containerID, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = response.Status
		}
		return nil, fmt.Errorf("inspect Docker container %s: %s", containerID, message)
	}
	var payload struct {
		NetworkSettings struct {
			Networks map[string]struct {
				Aliases []string `json:"Aliases"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Docker container %s: %w", containerID, err)
	}
	settings, ok := payload.NetworkSettings.Networks[network]
	if !ok {
		return nil, nil
	}
	return append([]string(nil), settings.Aliases...), nil
}

func (c *Client) ConnectNetwork(
	ctx context.Context,
	network string,
	containerID string,
	aliases []string,
) error {
	payload, err := json.Marshal(map[string]any{
		"Container": containerID,
		"EndpointConfig": map[string]any{
			"Aliases": aliases,
		},
	})
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		"http://docker/networks/"+url.PathEscape(network)+"/connect",
		bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("connect %s to network %s: %w", containerID, network, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = response.Status
	}
	return fmt.Errorf("connect container to network %s: %s", network, message)
}

func (c *Client) DisconnectNetwork(ctx context.Context, network, containerID string) error {
	payload, err := json.Marshal(map[string]any{
		"Container": containerID,
		"Force":     false,
	})
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		"http://docker/networks/"+url.PathEscape(network)+"/disconnect",
		bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("disconnect %s from network %s: %w", containerID, network, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusNotFound {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = response.Status
	}
	return fmt.Errorf("disconnect container from network %s: %s", network, message)
}

func detectSystemRole(
	image string,
	labels map[string]string,
	publishesGatewayPort bool,
	commands ...string,
) string {
	switch strings.ToLower(strings.TrimSpace(labels[InstallRoleLabel])) {
	case "gateway":
		return SystemRoleReverseProxy
	case SystemRoleController:
		return SystemRoleController
	case SystemRoleProbe:
		return SystemRoleProbe
	}
	if len(commands) > 0 {
		if role := detectDocklaneCommandRole(commands[0]); role != "" {
			return role
		}
	}
	if !publishesGatewayPort {
		return ""
	}
	if strings.EqualFold(labels["org.opencontainers.image.title"], "Traefik") {
		return SystemRoleReverseProxy
	}
	imageName := strings.ToLower(strings.Split(image, "@")[0])
	imageName = strings.TrimPrefix(imageName, "docker.io/")
	imageName = strings.TrimPrefix(imageName, "library/")
	if imageName == "traefik" || strings.HasPrefix(imageName, "traefik:") {
		return SystemRoleReverseProxy
	}
	return ""
}

func detectDocklaneCommandRole(command string) string {
	fields := strings.Fields(command)
	if len(fields) < 2 {
		return ""
	}
	binary := strings.Trim(fields[0], `"'`)
	if binary != "docklane" && !strings.HasSuffix(binary, "/docklane") {
		return ""
	}
	switch strings.ToLower(strings.Trim(fields[1], `"'`)) {
	case "serve":
		return SystemRoleController
	case "probe":
		return SystemRoleProbe
	default:
		return ""
	}
}

func (c *Client) WatchContainerEvents(ctx context.Context, notify func()) error {
	filters := `{"type":["container"]}`
	endpoint := "http://docker/events?" + url.Values{"filters": {filters}}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("subscribe to Docker events: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Docker event stream returned %s", response.Status)
	}

	decoder := json.NewDecoder(response.Body)
	for {
		var event struct {
			Type   string `json:"Type"`
			Action string `json:"Action"`
		}
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("Docker event stream closed")
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("decode Docker event: %w", err)
		}
		if event.Type == "container" && affectsRoutes(event.Action) {
			notify()
		}
	}
}

func affectsRoutes(action string) bool {
	switch action {
	case "create", "start", "die", "destroy", "rename", "pause", "unpause",
		"health_status: healthy", "health_status: unhealthy":
		return true
	default:
		return false
	}
}
