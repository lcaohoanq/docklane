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
	Source      string `json:"source"`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"readOnly"`
}

type ContainerRuntime struct {
	Command []string         `json:"command"`
	Mounts  []ContainerMount `json:"mounts"`
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
		Config struct {
			Cmd []string `json:"Cmd"`
		} `json:"Config"`
		Mounts []struct {
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
			RW          bool   `json:"RW"`
		} `json:"Mounts"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return ContainerRuntime{}, err
	}
	result := ContainerRuntime{
		Command: append([]string(nil), payload.Config.Cmd...),
		Mounts:  make([]ContainerMount, 0, len(payload.Mounts)),
	}
	for _, mount := range payload.Mounts {
		result.Mounts = append(result.Mounts, ContainerMount{
			Source:      mount.Source,
			Destination: mount.Destination,
			ReadOnly:    !mount.RW,
		})
	}
	return result, nil
}
