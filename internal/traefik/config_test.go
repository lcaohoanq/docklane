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
