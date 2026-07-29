package appflow

import (
	"errors"
	"strings"
	"testing"

	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
)

func TestResolvePrefersStableComposeSelector(t *testing.T) {
	application, err := Resolve("draw/excalidraw", []docker.Container{{
		ID:             "abcdef123456",
		Name:           "draw-excalidraw-1",
		ComposeProject: "draw",
		ComposeService: "excalidraw",
		ExposedPorts:   []uint16{80},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if application.Identity != "draw/excalidraw" ||
		application.Name != "excalidraw" ||
		application.Selector.ComposeProject != "draw" ||
		application.Selector.ComposeService != "excalidraw" ||
		application.Selector.ContainerID != "" {
		t.Fatalf("application = %#v", application)
	}
}

func TestResolveRejectsAmbiguousService(t *testing.T) {
	_, err := Resolve("web", []docker.Container{
		{ID: "one", Name: "alpha-web-1", ComposeProject: "alpha", ComposeService: "web"},
		{ID: "two", Name: "beta-web-1", ComposeProject: "beta", ComposeService: "web"},
	})
	if !errors.Is(err, ErrTargetAmbiguous) ||
		!strings.Contains(err.Error(), "alpha/web, beta/web") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveRejectsReverseProxy(t *testing.T) {
	_, err := Resolve("traefik", []docker.Container{{
		ID:         "proxy",
		Name:       "traefik",
		SystemRole: docker.SystemRoleReverseProxy,
	}})
	if !errors.Is(err, docker.ErrSystemTarget) {
		t.Fatalf("error = %v", err)
	}
}

func TestSelectPortInfersOnlyUnambiguousPort(t *testing.T) {
	container := docker.Container{
		Name:         "draw",
		ExposedPorts: []uint16{80, 80},
	}
	port, err := SelectPort(container, 0)
	if err != nil || port != 80 {
		t.Fatalf("port = %d, error = %v", port, err)
	}
	container.ExposedPorts = []uint16{80, 8080}
	port, err = SelectPort(container, 0)
	if err != nil || port != 80 {
		t.Fatalf("port = %d, error = %v", port, err)
	}
	container.ExposedPorts = []uint16{7000, 9000}
	if _, err := SelectPort(container, 0); err == nil ||
		!strings.Contains(err.Error(), "none is a recognizable HTTP default") {
		t.Fatalf("error = %v", err)
	}
}

func TestRecommendedHTTPPortRanksCommonApplicationPorts(t *testing.T) {
	tests := []struct {
		ports []uint16
		want  uint16
	}{
		{ports: []uint16{3000, 8080}, want: 8080},
		{ports: []uint16{5173, 3000}, want: 3000},
		{ports: []uint16{9000}, want: 9000},
		{ports: []uint16{443, 8443}, want: 443},
		{ports: []uint16{7000, 9000}, want: 0},
		{ports: nil, want: 0},
	}
	for _, test := range tests {
		if got := RecommendedHTTPPort(test.ports); got != test.want {
			t.Fatalf("RecommendedHTTPPort(%v) = %d, want %d", test.ports, got, test.want)
		}
	}
}

func TestRecommendedSchemeUsesTLSForConventionalTLSPorts(t *testing.T) {
	if got := RecommendedScheme(443); got != "https" {
		t.Fatalf("scheme for 443 = %q", got)
	}
	if got := RecommendedScheme(8080); got != "http" {
		t.Fatalf("scheme for 8080 = %q", got)
	}
}

func TestSelectPortValidatesExplicitPort(t *testing.T) {
	container := docker.Container{Name: "draw", ExposedPorts: []uint16{80}}
	if _, err := SelectPort(container, 8080); !errors.Is(err, docker.ErrPortNotExposed) {
		t.Fatalf("error = %v", err)
	}
}

func TestRouteNameProducesDNSLabel(t *testing.T) {
	if got := RouteName("My_App.Web #1"); got != "my-app-web-1" {
		t.Fatalf("name = %q", got)
	}
	if got := RouteName("Café"); got != "caf" {
		t.Fatalf("non-ASCII name = %q", got)
	}
}

func TestSameSelectorRequiresExactStableIdentity(t *testing.T) {
	left := domain.ContainerSelector{ComposeProject: "draw", ComposeService: "web"}
	if !SameSelector(left, left) {
		t.Fatal("equal selector was not recognized")
	}
	if SameSelector(left, domain.ContainerSelector{ContainerID: "abc"}) {
		t.Fatal("different selectors were treated as equal")
	}
}

func TestComposeGuidanceDoesNotPublishHostPortOrAddTraefikLabels(t *testing.T) {
	guidance := ComposeGuidance(Application{
		Container: docker.Container{
			Name:           "draw-web-1",
			ComposeProject: "draw",
			ComposeService: "web",
			ExposedPorts:   []uint16{80},
		},
		Identity: "draw/web",
		Name:     "web",
	}, 80)
	if strings.Contains(guidance, "ports:") ||
		strings.Contains(guidance, "traefik.") ||
		!strings.Contains(guidance, "docklane app enable draw/web --name web --port 80") {
		t.Fatalf("guidance = %q", guidance)
	}
}

func TestComposeGuidanceAddsExposeWhenRequestedPortIsUndeclared(t *testing.T) {
	guidance := ComposeGuidance(Application{
		Container: docker.Container{
			Name:           "draw-web-1",
			ComposeProject: "draw",
			ComposeService: "web",
		},
		Identity: "draw/web",
		Name:     "web",
	}, 8080)
	if !strings.Contains(guidance, "expose:\n      - \"8080\"") ||
		strings.Contains(guidance, "ports:") {
		t.Fatalf("guidance = %q", guidance)
	}
}

func TestComposeGuidanceIncludesHTTPSForTLSPort(t *testing.T) {
	guidance := ComposeGuidance(Application{
		Container: docker.Container{Name: "secure", ExposedPorts: []uint16{443}},
		Identity:  "secure",
		Name:      "secure",
	}, 443)
	if !strings.Contains(guidance, "--port 443 --scheme https") {
		t.Fatalf("guidance = %q", guidance)
	}
}
