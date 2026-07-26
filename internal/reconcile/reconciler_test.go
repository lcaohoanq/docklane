package reconcile

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
)

type fakeStore struct {
	routes []domain.Route
	err    error
}

func (store fakeStore) ListRoutes(context.Context) ([]domain.Route, error) {
	return store.routes, store.err
}

type fakeDiscovery struct {
	containers []docker.Container
	err        error
}

func (discovery fakeDiscovery) ListContainers(context.Context) ([]docker.Container, error) {
	return discovery.containers, discovery.err
}

func TestRefreshObservesRouteStates(t *testing.T) {
	routes := []domain.Route{
		{
			ID:       1,
			Name:     "ready",
			Port:     80,
			Scheme:   "http",
			Enabled:  true,
			Selector: domain.ContainerSelector{ContainerID: "abc"},
		},
		{
			ID:       2,
			Name:     "missing",
			Port:     80,
			Scheme:   "http",
			Enabled:  true,
			Selector: domain.ContainerSelector{ContainerID: "missing"},
		},
		{
			ID:       3,
			Name:     "disabled",
			Port:     80,
			Scheme:   "http",
			Enabled:  false,
			Selector: domain.ContainerSelector{ContainerID: "abc"},
		},
		{
			ID:       4,
			Name:     "wrong-port",
			Port:     3000,
			Scheme:   "http",
			Enabled:  true,
			Selector: domain.ContainerSelector{ContainerID: "abc"},
		},
	}
	reconciler := New(
		fakeStore{routes: routes},
		fakeDiscovery{containers: []docker.Container{{
			ID: "abc123", Name: "web", ExposedPorts: []uint16{80},
		}}},
		time.Second,
	)
	if err := reconciler.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	observed := reconciler.Enrich(routes)
	if observed[0].Observed.State != domain.RouteStateReady {
		t.Fatalf("ready state = %q", observed[0].Observed.State)
	}
	if observed[0].Observed.UpstreamURL != "http://web:80" {
		t.Fatalf("upstream = %q", observed[0].Observed.UpstreamURL)
	}
	if observed[1].Observed.State != domain.RouteStateUnresolved {
		t.Fatalf("missing state = %q", observed[1].Observed.State)
	}
	if observed[2].Observed.State != domain.RouteStateDisabled {
		t.Fatalf("disabled state = %q", observed[2].Observed.State)
	}
	if observed[3].Observed.State != domain.RouteStateError {
		t.Fatalf("wrong-port state = %q", observed[3].Observed.State)
	}
	if !strings.Contains(observed[3].Observed.Message, "available ports: [80]") {
		t.Fatalf("wrong-port message = %q", observed[3].Observed.Message)
	}
}

func TestDiscoveryFailureMarksRoutesAsError(t *testing.T) {
	routes := []domain.Route{{
		ID:       1,
		Name:     "draw",
		Port:     80,
		Scheme:   "http",
		Enabled:  true,
		Selector: domain.ContainerSelector{ContainerID: "abc"},
	}}
	reconciler := New(
		fakeStore{routes: routes},
		fakeDiscovery{err: errors.New("Docker unavailable")},
		time.Second,
	)
	if err := reconciler.Refresh(context.Background()); err == nil {
		t.Fatal("expected refresh error")
	}
	observed := reconciler.Enrich(routes)
	if observed[0].Observed.State != domain.RouteStateError {
		t.Fatalf("state = %q", observed[0].Observed.State)
	}
}
