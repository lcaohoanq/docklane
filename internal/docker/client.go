package docker

import (
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
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Image          string            `json:"image"`
	State          string            `json:"state"`
	Status         string            `json:"status"`
	ComposeProject string            `json:"composeProject,omitempty"`
	ComposeService string            `json:"composeService,omitempty"`
	ExposedPorts   []uint16          `json:"exposedPorts"`
	Labels         map[string]string `json:"-"`
}

type Discovery interface {
	ListContainers(context.Context) ([]Container, error)
}

type EventSource interface {
	WatchContainerEvents(context.Context, func()) error
}

var (
	ErrNoMatch        = errors.New("no running container matches the route selector")
	ErrAmbiguous      = errors.New("route selector matches multiple running containers")
	ErrPortNotExposed = errors.New("configured TCP port is not declared by the container")
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
		ID     string            `json:"Id"`
		Names  []string          `json:"Names"`
		Image  string            `json:"Image"`
		State  string            `json:"State"`
		Status string            `json:"Status"`
		Labels map[string]string `json:"Labels"`
		Ports  []struct {
			PrivatePort uint16 `json:"PrivatePort"`
			Type        string `json:"Type"`
		} `json:"Ports"`
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
		seen := map[uint16]bool{}
		for _, port := range item.Ports {
			if port.Type == "tcp" && !seen[port.PrivatePort] {
				seen[port.PrivatePort] = true
				ports = append(ports, port.PrivatePort)
			}
		}
		sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
		containers = append(containers, Container{
			ID:             item.ID,
			Name:           name,
			Image:          item.Image,
			State:          item.State,
			Status:         item.Status,
			ComposeProject: item.Labels["com.docker.compose.project"],
			ComposeService: item.Labels["com.docker.compose.service"],
			ExposedPorts:   ports,
			Labels:         item.Labels,
		})
	}
	return containers, nil
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
