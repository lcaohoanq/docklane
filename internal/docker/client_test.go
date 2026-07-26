package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"docklane.local/docklane/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestResolveContainerByComposeWorkload(t *testing.T) {
	containers := []Container{
		{ID: "one", Name: "unrelated", ComposeProject: "other", ComposeService: "web"},
		{ID: "two", Name: "actual-name", ComposeProject: "draw", ComposeService: "web"},
	}
	got, err := ResolveContainer(domain.ContainerSelector{
		ComposeProject: "draw",
		ComposeService: "web",
	}, containers)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "actual-name" {
		t.Fatalf("name = %q, want actual-name", got.Name)
	}
}

func TestResolveContainerRejectsAmbiguousWorkload(t *testing.T) {
	containers := []Container{
		{ID: "one", ComposeProject: "draw", ComposeService: "web"},
		{ID: "two", ComposeProject: "draw", ComposeService: "web"},
	}
	_, err := ResolveContainer(domain.ContainerSelector{
		ComposeProject: "draw",
		ComposeService: "web",
	}, containers)
	if err == nil {
		t.Fatal("expected ambiguous selector error")
	}
}

func TestValidateTCPPort(t *testing.T) {
	container := Container{Name: "draw-web-1", ExposedPorts: []uint16{80, 8080}}
	if err := ValidateTCPPort(container, 8080); err != nil {
		t.Fatalf("declared port rejected: %v", err)
	}
	if err := ValidateTCPPort(container, 3000); !errors.Is(err, ErrPortNotExposed) {
		t.Fatalf("undeclared port error = %v, want ErrPortNotExposed", err)
	}
}

func TestWatchContainerEventsFiltersRouteAffectingActions(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			if request.URL.Query().Get("filters") != `{"type":["container"]}` {
				t.Fatalf("filters = %q", request.URL.Query().Get("filters"))
			}
			body := strings.Join([]string{
				`{"Type":"container","Action":"start"}`,
				`{"Type":"container","Action":"exec_start: shell"}`,
				`{"Type":"container","Action":"health_status: healthy"}`,
			}, "\n")
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		},
	)}}

	notifications := 0
	err := client.WatchContainerEvents(context.Background(), func() {
		notifications++
	})
	if err == nil || !strings.Contains(err.Error(), "stream closed") {
		t.Fatalf("stream error = %v, want closed stream", err)
	}
	if notifications != 2 {
		t.Fatalf("notifications = %d, want 2", notifications)
	}
}

func TestNewClientLeavesStreamingRequestsContextControlled(t *testing.T) {
	client := NewClient("/var/run/docker.sock")
	if client.http.Timeout != 0 {
		t.Fatalf("HTTP client timeout = %s, want no whole-request timeout", client.http.Timeout)
	}
}

func TestNetworkAliasesInspectsContainerEndpoint(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			if request.Method != http.MethodGet ||
				request.URL.Path != "/containers/abc123/json" {
				t.Fatalf("request = %s %s", request.Method, request.URL.Path)
			}
			body := `{"NetworkSettings":{"Networks":{"proxy":{
				"Aliases":["draw","docklane-route-7"]
			}}}}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		},
	)}}
	aliases, err := client.NetworkAliases(context.Background(), "abc123", "proxy")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(aliases, ",") != "draw,docklane-route-7" {
		t.Fatalf("aliases = %#v", aliases)
	}
}

func TestDetectActiveTraefikGateway(t *testing.T) {
	if role := detectSystemRole(
		"traefik:v3.7",
		map[string]string{"org.opencontainers.image.title": "Traefik"},
		true,
	); role != SystemRoleReverseProxy {
		t.Fatalf("role = %q, want %q", role, SystemRoleReverseProxy)
	}
	if role := detectSystemRole(
		"traefik:v3.7",
		map[string]string{"org.opencontainers.image.title": "Traefik"},
		false,
	); role != "" {
		t.Fatalf("unpublished Traefik role = %q, want empty", role)
	}
	if role := detectSystemRole(
		"example/web:latest",
		map[string]string{"traefik.enable": "true"},
		true,
	); role != "" {
		t.Fatalf("ordinary application role = %q, want empty", role)
	}
}

func TestValidateApplicationTargetRejectsReverseProxy(t *testing.T) {
	err := ValidateApplicationTarget(Container{
		Name:       "traefik",
		SystemRole: SystemRoleReverseProxy,
	})
	if !errors.Is(err, ErrSystemTarget) {
		t.Fatalf("target error = %v, want ErrSystemTarget", err)
	}
}

func TestConnectNetworkUsesDockerAPI(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			if request.Method != http.MethodPost ||
				request.URL.Path != "/networks/proxy/connect" {
				t.Fatalf("request = %s %s", request.Method, request.URL.Path)
			}
			var payload struct {
				Container string `json:"Container"`
				Endpoint  struct {
					Aliases []string `json:"Aliases"`
				} `json:"EndpointConfig"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Container != "abc123" {
				t.Fatalf("container = %q, want abc123", payload.Container)
			}
			if len(payload.Endpoint.Aliases) != 1 ||
				payload.Endpoint.Aliases[0] != "docklane-route-1" {
				t.Fatalf("aliases = %#v, want docklane-route-1", payload.Endpoint.Aliases)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		},
	)}}
	if err := client.ConnectNetwork(
		context.Background(),
		"proxy",
		"abc123",
		[]string{"docklane-route-1"},
	); err != nil {
		t.Fatal(err)
	}
}

func TestConnectNetworkRejectsAlreadyExistingEndpoint(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Status:     "403 Forbidden",
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`{"message":"endpoint with name draw already exists"}`,
				)),
			}, nil
		},
	)}}
	err := client.ConnectNetwork(
		context.Background(),
		"proxy",
		"abc123",
		[]string{"docklane-route-1"},
	)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("connect error = %v, want existing-endpoint error", err)
	}
}

func TestDisconnectNetworkUsesDockerAPIWithoutForce(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			if request.Method != http.MethodPost ||
				request.URL.Path != "/networks/proxy/disconnect" {
				t.Fatalf("request = %s %s", request.Method, request.URL.Path)
			}
			var payload struct {
				Container string `json:"Container"`
				Force     bool   `json:"Force"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Container != "abc123" {
				t.Fatalf("container = %q, want abc123", payload.Container)
			}
			if payload.Force {
				t.Fatal("disconnect unexpectedly forced")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		},
	)}}
	if err := client.DisconnectNetwork(context.Background(), "proxy", "abc123"); err != nil {
		t.Fatal(err)
	}
}

func TestContainerHasNetwork(t *testing.T) {
	container := Container{Networks: []string{"bridge", "proxy"}}
	if !container.HasNetwork("proxy") || container.HasNetwork("missing") {
		t.Fatalf("unexpected network lookup for %#v", container.Networks)
	}
}
