package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
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

var (
	ErrNoMatch   = errors.New("no running container matches the route selector")
	ErrAmbiguous = errors.New("route selector matches multiple running containers")
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

type Client struct {
	http *http.Client
}

func NewClient(socketPath string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socketPath)
		},
	}
	return &Client{http: &http.Client{Transport: transport, Timeout: 10 * time.Second}}
}

func (c *Client) ListContainers(ctx context.Context) ([]Container, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/json", nil)
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
