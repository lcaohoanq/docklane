package traefik

import (
	"testing"

	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
)

func TestBuild(t *testing.T) {
	config := Build([]domain.Route{{
		Name:    "excalidraw",
		Port:    80,
		Scheme:  "http",
		Enabled: true,
		Selector: domain.ContainerSelector{
			ComposeProject: "excalidraw",
			ComposeService: "excalidraw",
		},
	}}, []docker.Container{{
		ID:             "abc123",
		Name:           "actual-container-name",
		ComposeProject: "excalidraw",
		ComposeService: "excalidraw",
		ExposedPorts:   []uint16{80},
	}}, "docker.home.arpa")

	router := config.HTTP.Routers["excalidraw"]
	if got, want := router.Rule, "Host(`excalidraw.docker.home.arpa`)"; got != want {
		t.Fatalf("rule = %q, want %q", got, want)
	}
	server := config.HTTP.Services["excalidraw"].LoadBalancer.Servers[0]
	if got, want := server.URL, "http://actual-container-name:80"; got != want {
		t.Fatalf("server URL = %q, want %q", got, want)
	}
}

func TestBuildSkipsRouteWithUndeclaredPort(t *testing.T) {
	config := Build([]domain.Route{{
		Name:     "draw",
		Port:     3000,
		Scheme:   "http",
		Enabled:  true,
		Selector: domain.ContainerSelector{ContainerID: "abc"},
	}}, []docker.Container{{
		ID:           "abc123",
		Name:         "draw-web-1",
		ExposedPorts: []uint16{80},
	}}, "docker.home.arpa")

	if len(config.HTTP.Routers) != 0 || len(config.HTTP.Services) != 0 {
		t.Fatalf("expected undeclared port route to be omitted, got %#v", config)
	}
}

func TestBuildSkipsActiveReverseProxyTarget(t *testing.T) {
	config := Build([]domain.Route{{
		Name:     "traefik",
		Port:     80,
		Scheme:   "http",
		Enabled:  true,
		Selector: domain.ContainerSelector{ContainerID: "proxy"},
	}}, []docker.Container{{
		ID:           "proxy123",
		Name:         "traefik",
		SystemRole:   docker.SystemRoleReverseProxy,
		ExposedPorts: []uint16{80, 443},
	}}, "docker.home.arpa")

	if len(config.HTTP.Routers) != 0 || len(config.HTTP.Services) != 0 {
		t.Fatalf("expected reverse proxy route to be omitted, got %#v", config)
	}
}

func TestBuildSkipsContainerOutsideTargetNetwork(t *testing.T) {
	config := Build([]domain.Route{{
		Name:     "draw",
		Port:     80,
		Scheme:   "http",
		Enabled:  true,
		Selector: domain.ContainerSelector{ContainerID: "abc"},
	}}, []docker.Container{{
		ID:           "abc123",
		Name:         "draw",
		ExposedPorts: []uint16{80},
		Networks:     []string{"bridge"},
	}}, "docker.home.arpa", "proxy")

	if len(config.HTTP.Routers) != 0 || len(config.HTTP.Services) != 0 {
		t.Fatalf("expected out-of-network route to be omitted, got %#v", config)
	}
}

func TestBuildSkipsUnresolvedAndDisabledRoutes(t *testing.T) {
	config := Build([]domain.Route{
		{
			Name:     "missing",
			Port:     80,
			Scheme:   "http",
			Enabled:  true,
			Selector: domain.ContainerSelector{ContainerID: "missing"},
		},
		{
			Name:     "disabled",
			Port:     80,
			Scheme:   "http",
			Enabled:  false,
			Selector: domain.ContainerSelector{ContainerID: "abc"},
		},
	}, []docker.Container{{ID: "abc123", Name: "example"}}, "docker.home.arpa")

	if len(config.HTTP.Routers) != 0 || len(config.HTTP.Services) != 0 {
		t.Fatalf("expected empty configuration, got %#v", config)
	}
}

func TestBuildUsesReconciledDeterministicUpstream(t *testing.T) {
	config := Build([]domain.Route{{
		ID:       7,
		Name:     "draw",
		Port:     80,
		Scheme:   "http",
		Enabled:  true,
		Selector: domain.ContainerSelector{ContainerID: "abc"},
		Observed: domain.RouteObservation{
			State:       domain.RouteStateReady,
			UpstreamURL: "http://docklane-route-7:80",
		},
	}}, []docker.Container{{
		ID:           "abc123",
		Name:         "generated-container-name",
		ExposedPorts: []uint16{80},
		Networks:     []string{"proxy"},
	}}, "docker.home.arpa", "proxy")

	server := config.HTTP.Services["draw"].LoadBalancer.Servers[0]
	if server.URL != "http://docklane-route-7:80" {
		t.Fatalf("server URL = %q", server.URL)
	}
}
