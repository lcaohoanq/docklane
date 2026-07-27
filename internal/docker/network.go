package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	NetworkManagedLabel = "com.docklane.managed"
	NetworkRoleLabel    = "com.docklane.role"
	NetworkSchemaLabel  = "com.docklane.schema"
	NetworkRoleProxy    = "proxy"
	NetworkSchemaV1     = "1"
)

var ErrNetworkNotFound = errors.New("Docker network not found")

type Network struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Scope      string            `json:"scope"`
	Internal   bool              `json:"internal"`
	Attachable bool              `json:"attachable"`
	Labels     map[string]string `json:"labels"`
}

type NetworkInspector interface {
	InspectNetwork(context.Context, string) (Network, error)
}

type NetworkLifecycle interface {
	NetworkInspector
	CreateNetwork(context.Context, string, map[string]string) (Network, error)
}

func ManagedProxyNetworkLabels() map[string]string {
	return map[string]string{
		NetworkManagedLabel: "true",
		NetworkRoleLabel:    NetworkRoleProxy,
		NetworkSchemaLabel:  NetworkSchemaV1,
	}
}

func (c *Client) InspectNetwork(ctx context.Context, name string) (Network, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodGet,
		"http://docker/networks/"+url.PathEscape(name),
		nil,
	)
	if err != nil {
		return Network{}, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return Network{}, fmt.Errorf("inspect Docker network %s: %w", name, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return Network{}, fmt.Errorf("%w: %s", ErrNetworkNotFound, name)
	}
	if response.StatusCode != http.StatusOK {
		return Network{}, dockerResponseError("inspect Docker network "+name, response)
	}
	var payload struct {
		ID         string            `json:"Id"`
		Name       string            `json:"Name"`
		Driver     string            `json:"Driver"`
		Scope      string            `json:"Scope"`
		Internal   bool              `json:"Internal"`
		Attachable bool              `json:"Attachable"`
		Labels     map[string]string `json:"Labels"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return Network{}, fmt.Errorf("decode Docker network %s: %w", name, err)
	}
	return Network{
		ID:         payload.ID,
		Name:       payload.Name,
		Driver:     payload.Driver,
		Scope:      payload.Scope,
		Internal:   payload.Internal,
		Attachable: payload.Attachable,
		Labels:     payload.Labels,
	}, nil
}

func (c *Client) CreateNetwork(
	ctx context.Context,
	name string,
	labels map[string]string,
) (Network, error) {
	payload, err := json.Marshal(map[string]any{
		"Name":           name,
		"Driver":         "bridge",
		"CheckDuplicate": true,
		"Labels":         labels,
	})
	if err != nil {
		return Network{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		"http://docker/networks/create",
		bytes.NewReader(payload),
	)
	if err != nil {
		return Network{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return Network{}, fmt.Errorf("create Docker network %s: %w", name, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		network, inspectErr := c.InspectNetwork(ctx, name)
		if inspectErr == nil {
			return network, nil
		}
	}
	if response.StatusCode != http.StatusCreated {
		return Network{}, dockerResponseError("create Docker network "+name, response)
	}
	return c.InspectNetwork(ctx, name)
}

func dockerResponseError(action string, response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = response.Status
	}
	return fmt.Errorf("%s: %s", action, message)
}
