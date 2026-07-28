package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
)

type Client struct {
	baseURL string
	http    *http.Client
}

type Routes struct {
	Routes     []domain.Route `json:"routes"`
	BaseDomain string         `json:"baseDomain"`
}

func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    http.DefaultClient,
	}
}

func (c *Client) ListContainers(ctx context.Context) ([]docker.Container, error) {
	return c.listContainers(ctx, "/api/v1/containers")
}

func (c *Client) ListContainersWithNetworkAliases(
	ctx context.Context,
) ([]docker.Container, error) {
	return c.listContainers(ctx, "/api/v1/containers?networkAliases=true")
}

func (c *Client) listContainers(
	ctx context.Context,
	path string,
) ([]docker.Container, error) {
	var payload struct {
		Containers []docker.Container `json:"containers"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &payload); err != nil {
		return nil, err
	}
	return payload.Containers, nil
}

func (c *Client) ListRoutes(ctx context.Context) (Routes, error) {
	var payload Routes
	err := c.do(ctx, http.MethodGet, "/api/v1/routes", nil, &payload)
	return payload, err
}

func (c *Client) Health(ctx context.Context) (domain.ControllerHealth, error) {
	var health domain.ControllerHealth
	err := c.do(ctx, http.MethodGet, "/api/v1/health", nil, &health)
	return health, err
}

func (c *Client) NetworkPlan(ctx context.Context) (domain.NetworkPlan, error) {
	var plan domain.NetworkPlan
	err := c.do(ctx, http.MethodGet, "/api/v1/network/plan", nil, &plan)
	return plan, err
}

func (c *Client) ApplyNetworkPlan(
	ctx context.Context,
	token string,
) (domain.NetworkApplyResult, error) {
	var result domain.NetworkApplyResult
	err := c.do(
		ctx,
		http.MethodPost,
		"/api/v1/network/apply",
		struct {
			Token string `json:"token"`
		}{Token: token},
		&result,
	)
	return result, err
}

func (c *Client) GetRoute(ctx context.Context, id int64) (domain.Route, error) {
	var route domain.Route
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/routes/%d", id), nil, &route)
	return route, err
}

func (c *Client) ProbeUpstream(
	ctx context.Context,
	id int64,
) (domain.UpstreamProbe, error) {
	var result domain.UpstreamProbe
	err := c.do(
		ctx,
		http.MethodGet,
		fmt.Sprintf("/api/v1/routes/%d/upstream-probe", id),
		nil,
		&result,
	)
	return result, err
}

func (c *Client) InspectTraefikRuntime(
	ctx context.Context,
	id int64,
) (domain.TraefikRouteRuntime, error) {
	var result domain.TraefikRouteRuntime
	err := c.do(
		ctx,
		http.MethodGet,
		fmt.Sprintf("/api/v1/routes/%d/traefik-runtime", id),
		nil,
		&result,
	)
	return result, err
}

func (c *Client) CreateRoute(ctx context.Context, route domain.Route) (domain.Route, error) {
	var created domain.Route
	err := c.do(ctx, http.MethodPost, "/api/v1/routes", writableRoute(route), &created)
	return created, err
}

func (c *Client) UpdateRoute(ctx context.Context, route domain.Route) (domain.Route, error) {
	var updated domain.Route
	err := c.do(
		ctx,
		http.MethodPut,
		fmt.Sprintf("/api/v1/routes/%d", route.ID),
		writableRoute(route),
		&updated,
	)
	return updated, err
}

func (c *Client) DeleteRoute(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/routes/%d", id), nil, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body, output any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("connect to Docklane at %s: %w", c.baseURL, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(response.Body).Decode(&failure) == nil && failure.Error != "" {
			return fmt.Errorf("%s", failure.Error)
		}
		return fmt.Errorf("Docklane returned %s", response.Status)
	}
	if output == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(output)
}

func writableRoute(route domain.Route) any {
	return struct {
		Revision uint64                   `json:"revision,omitempty"`
		Name     string                   `json:"name"`
		Selector domain.ContainerSelector `json:"selector"`
		Port     uint16                   `json:"port"`
		Scheme   string                   `json:"scheme"`
		Enabled  bool                     `json:"enabled"`
	}{
		Revision: route.Revision,
		Name:     route.Name,
		Selector: route.Selector,
		Port:     route.Port,
		Scheme:   route.Scheme,
		Enabled:  route.Enabled,
	}
}
