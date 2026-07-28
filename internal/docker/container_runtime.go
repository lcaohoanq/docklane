package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type ContainerMount struct {
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"readOnly"`
}

type ContainerPortBinding struct {
	ContainerPort uint16 `json:"containerPort"`
	HostIP        string `json:"hostIp"`
	HostPort      uint16 `json:"hostPort"`
}

type ContainerRuntime struct {
	ImageID         string                 `json:"imageId"`
	Running         bool                   `json:"running"`
	Health          string                 `json:"health,omitempty"`
	Command         []string               `json:"command"`
	Mounts          []ContainerMount       `json:"mounts"`
	PortBindings    []ContainerPortBinding `json:"portBindings"`
	ReadOnlyRootFS  bool                   `json:"readOnlyRootFs"`
	Privileged      bool                   `json:"privileged"`
	NoNewPrivileges bool                   `json:"noNewPrivileges"`
	DroppedCaps     []string               `json:"droppedCaps"`
	RestartPolicy   string                 `json:"restartPolicy,omitempty"`
}

type ContainerRuntimeInspector interface {
	InspectContainerRuntime(context.Context, string) (ContainerRuntime, error)
}

func (c *Client) InspectContainerRuntime(
	ctx context.Context,
	containerID string,
) (ContainerRuntime, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodGet,
		"http://docker/containers/"+url.PathEscape(containerID)+"/json",
		nil,
	)
	if err != nil {
		return ContainerRuntime{}, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return ContainerRuntime{}, fmt.Errorf(
			"inspect Docker container %s: %w",
			containerID,
			err,
		)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ContainerRuntime{}, dockerResponseError(
			"inspect Docker container "+containerID,
			response,
		)
	}
	var payload struct {
		Image string `json:"Image"`
		State struct {
			Running bool `json:"Running"`
			Health  struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
		Config struct {
			Cmd []string `json:"Cmd"`
		} `json:"Config"`
		Mounts []struct {
			Type        string `json:"Type"`
			Name        string `json:"Name"`
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
			RW          bool   `json:"RW"`
		} `json:"Mounts"`
		HostConfig struct {
			ReadonlyRootfs bool     `json:"ReadonlyRootfs"`
			Privileged     bool     `json:"Privileged"`
			SecurityOpt    []string `json:"SecurityOpt"`
			CapDrop        []string `json:"CapDrop"`
			RestartPolicy  struct {
				Name string `json:"Name"`
			} `json:"RestartPolicy"`
			PortBindings map[string][]struct {
				HostIP   string `json:"HostIp"`
				HostPort string `json:"HostPort"`
			} `json:"PortBindings"`
		} `json:"HostConfig"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return ContainerRuntime{}, err
	}
	result := ContainerRuntime{
		ImageID:        payload.Image,
		Running:        payload.State.Running,
		Health:         payload.State.Health.Status,
		Command:        append([]string(nil), payload.Config.Cmd...),
		Mounts:         make([]ContainerMount, 0, len(payload.Mounts)),
		PortBindings:   []ContainerPortBinding{},
		ReadOnlyRootFS: payload.HostConfig.ReadonlyRootfs,
		Privileged:     payload.HostConfig.Privileged,
		DroppedCaps:    append([]string(nil), payload.HostConfig.CapDrop...),
		RestartPolicy:  payload.HostConfig.RestartPolicy.Name,
	}
	for _, option := range payload.HostConfig.SecurityOpt {
		if option == "no-new-privileges:true" {
			result.NoNewPrivileges = true
		}
	}
	for _, mount := range payload.Mounts {
		result.Mounts = append(result.Mounts, ContainerMount{
			Type:        mount.Type,
			Name:        mount.Name,
			Source:      mount.Source,
			Destination: mount.Destination,
			ReadOnly:    !mount.RW,
		})
	}
	for containerPort, bindings := range payload.HostConfig.PortBindings {
		port, err := parseDockerTCPPort(containerPort)
		if err != nil {
			continue
		}
		for _, binding := range bindings {
			hostPort, err := parseUint16(binding.HostPort)
			if err != nil {
				continue
			}
			result.PortBindings = append(
				result.PortBindings,
				ContainerPortBinding{
					ContainerPort: port,
					HostIP:        binding.HostIP,
					HostPort:      hostPort,
				},
			)
		}
	}
	return result, nil
}

func parseDockerTCPPort(value string) (uint16, error) {
	const suffix = "/tcp"
	if len(value) <= len(suffix) || value[len(value)-len(suffix):] != suffix {
		return 0, fmt.Errorf("not a TCP port")
	}
	return parseUint16(value[:len(value)-len(suffix)])
}

func parseUint16(value string) (uint16, error) {
	var parsed uint64
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("invalid port")
		}
		parsed = parsed*10 + uint64(digit-'0')
		if parsed > 65535 {
			return 0, fmt.Errorf("port out of range")
		}
	}
	if parsed == 0 {
		return 0, fmt.Errorf("port is required")
	}
	return uint16(parsed), nil
}
