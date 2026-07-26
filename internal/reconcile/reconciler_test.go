package reconcile

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
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

func (store fakeStore) RecordNetworkAttachment(
	context.Context,
	domain.NetworkAttachment,
) error {
	return nil
}

func (store fakeStore) ListNetworkAttachments(
	context.Context,
) ([]domain.NetworkAttachment, error) {
	return nil, nil
}

func (store fakeStore) DeleteNetworkAttachment(context.Context, string, string) error {
	return nil
}

type fakeDiscovery struct {
	containers []docker.Container
	err        error
}

type trackingStore struct {
	routes      []domain.Route
	attachments []domain.NetworkAttachment
}

func (store *trackingStore) ListRoutes(context.Context) ([]domain.Route, error) {
	return store.routes, nil
}

func (store *trackingStore) RecordNetworkAttachment(
	_ context.Context,
	attachment domain.NetworkAttachment,
) error {
	store.attachments = append(store.attachments, attachment)
	return nil
}

func (store *trackingStore) ListNetworkAttachments(
	context.Context,
) ([]domain.NetworkAttachment, error) {
	return append([]domain.NetworkAttachment(nil), store.attachments...), nil
}

func (store *trackingStore) DeleteNetworkAttachment(
	_ context.Context,
	containerID string,
	network string,
) error {
	var kept []domain.NetworkAttachment
	for _, attachment := range store.attachments {
		if attachment.ContainerID != containerID || attachment.Network != network {
			kept = append(kept, attachment)
		}
	}
	store.attachments = kept
	return nil
}

type eventDiscovery struct {
	listCalls atomic.Int32
}

type fakeNetworkManager struct {
	calls           int
	disconnectCalls int
	err             error
}

func (manager *fakeNetworkManager) DisconnectNetwork(
	context.Context,
	string,
	string,
) error {
	manager.disconnectCalls++
	return manager.err
}

func (manager *fakeNetworkManager) ConnectNetwork(
	context.Context,
	string,
	string,
) error {
	manager.calls++
	return manager.err
}

func (discovery *eventDiscovery) ListContainers(context.Context) ([]docker.Container, error) {
	discovery.listCalls.Add(1)
	return []docker.Container{{
		ID:           "abc123",
		Name:         "web",
		ExposedPorts: []uint16{80},
	}}, nil
}

func (discovery *eventDiscovery) WatchContainerEvents(ctx context.Context, notify func()) error {
	notify()
	notify()
	notify()
	<-ctx.Done()
	return ctx.Err()
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
		{
			ID:       5,
			Name:     "proxy-loop",
			Port:     80,
			Scheme:   "http",
			Enabled:  true,
			Selector: domain.ContainerSelector{ContainerID: "proxy"},
		},
	}
	reconciler := New(
		fakeStore{routes: routes},
		fakeDiscovery{containers: []docker.Container{{
			ID: "abc123", Name: "web", ExposedPorts: []uint16{80},
		}, {
			ID: "proxy123", Name: "traefik", ExposedPorts: []uint16{80, 443},
			SystemRole: docker.SystemRoleReverseProxy,
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
	if observed[4].Observed.State != domain.RouteStateError {
		t.Fatalf("proxy-loop state = %q", observed[4].Observed.State)
	}
	if !strings.Contains(observed[4].Observed.Message, "routing it to itself creates a loop") {
		t.Fatalf("proxy-loop message = %q", observed[4].Observed.Message)
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

func TestRunDebouncesDockerEventBurst(t *testing.T) {
	discovery := &eventDiscovery{}
	reconciler := New(
		fakeStore{routes: []domain.Route{{
			ID:       1,
			Name:     "draw",
			Port:     80,
			Scheme:   "http",
			Enabled:  true,
			Selector: domain.ContainerSelector{ContainerID: "abc"},
		}}},
		discovery,
		time.Hour,
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		reconciler.Run(ctx)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for discovery.listCalls.Load() < 2 {
		select {
		case <-deadline:
			cancel()
			t.Fatalf("list calls = %d, want initial and event refresh", discovery.listCalls.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	time.Sleep(100 * time.Millisecond)
	if calls := discovery.listCalls.Load(); calls != 2 {
		t.Fatalf("list calls = %d, want one debounced event refresh", calls)
	}
	cancel()
	<-done
}

func TestRefreshAttachesMissingProxyNetwork(t *testing.T) {
	routes := []domain.Route{{
		ID:       1,
		Name:     "draw",
		Port:     80,
		Scheme:   "http",
		Enabled:  true,
		Selector: domain.ContainerSelector{ContainerID: "abc"},
	}}
	manager := &fakeNetworkManager{}
	reconciler := New(
		fakeStore{routes: routes},
		fakeDiscovery{containers: []docker.Container{{
			ID:           "abc123",
			Name:         "draw",
			ExposedPorts: []uint16{80},
			Networks:     []string{"bridge"},
		}}},
		time.Second,
		WithNetworkAttachments("proxy", manager),
	)
	if err := reconciler.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	observed := reconciler.Enrich(routes)[0].Observed
	if observed.State != domain.RouteStateReady {
		t.Fatalf("state = %q, message = %q", observed.State, observed.Message)
	}
	if manager.calls != 1 {
		t.Fatalf("network connect calls = %d, want 1", manager.calls)
	}
	if !strings.Contains(observed.Message, `attached to network "proxy"`) {
		t.Fatalf("message = %q", observed.Message)
	}
}

func TestRefreshReportsMissingNetworkWhenManagementDisabled(t *testing.T) {
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
		fakeDiscovery{containers: []docker.Container{{
			ID:           "abc123",
			Name:         "draw",
			ExposedPorts: []uint16{80},
			Networks:     []string{"bridge"},
		}}},
		time.Second,
		WithNetworkAttachments("proxy", nil),
	)
	if err := reconciler.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	observed := reconciler.Enrich(routes)[0].Observed
	if observed.State != domain.RouteStateError {
		t.Fatalf("state = %q, want error", observed.State)
	}
}

func TestRefreshDisconnectsOnlyTrackedUnusedAttachment(t *testing.T) {
	store := &trackingStore{
		routes: []domain.Route{{
			ID:       1,
			Name:     "draw",
			Port:     80,
			Scheme:   "http",
			Enabled:  false,
			Selector: domain.ContainerSelector{ContainerID: "abc"},
		}},
		attachments: []domain.NetworkAttachment{{
			ContainerID: "abc123",
			Network:     "proxy",
		}},
	}
	manager := &fakeNetworkManager{}
	reconciler := New(
		store,
		fakeDiscovery{containers: []docker.Container{{
			ID:           "abc123",
			Name:         "draw",
			ExposedPorts: []uint16{80},
			Networks:     []string{"bridge", "proxy"},
		}}},
		time.Second,
		WithNetworkAttachments("proxy", manager),
	)
	if err := reconciler.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.disconnectCalls != 1 {
		t.Fatalf("disconnect calls = %d, want 1", manager.disconnectCalls)
	}
	if len(store.attachments) != 0 {
		t.Fatalf("attachments = %#v, want none", store.attachments)
	}
}

func TestRefreshRecordsAttachmentCreatedByDocklane(t *testing.T) {
	store := &trackingStore{routes: []domain.Route{{
		ID:       1,
		Name:     "draw",
		Port:     80,
		Scheme:   "http",
		Enabled:  true,
		Selector: domain.ContainerSelector{ContainerID: "abc"},
	}}}
	manager := &fakeNetworkManager{}
	reconciler := New(
		store,
		fakeDiscovery{containers: []docker.Container{{
			ID:           "abc123",
			Name:         "draw",
			ExposedPorts: []uint16{80},
			Networks:     []string{"bridge"},
		}}},
		time.Second,
		WithNetworkAttachments("proxy", manager),
	)
	if err := reconciler.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.calls != 1 || manager.disconnectCalls != 0 {
		t.Fatalf(
			"connect calls = %d, disconnect calls = %d; want 1, 0",
			manager.calls,
			manager.disconnectCalls,
		)
	}
	if len(store.attachments) != 1 ||
		store.attachments[0].ContainerID != "abc123" ||
		store.attachments[0].Network != "proxy" {
		t.Fatalf("attachments = %#v, want owned proxy attachment", store.attachments)
	}
}

func TestRefreshPreservesUntrackedAttachment(t *testing.T) {
	store := &trackingStore{routes: []domain.Route{{
		ID:       1,
		Name:     "draw",
		Port:     80,
		Scheme:   "http",
		Enabled:  false,
		Selector: domain.ContainerSelector{ContainerID: "abc"},
	}}}
	manager := &fakeNetworkManager{}
	reconciler := New(
		store,
		fakeDiscovery{containers: []docker.Container{{
			ID:           "abc123",
			Name:         "draw",
			ExposedPorts: []uint16{80},
			Networks:     []string{"bridge", "proxy"},
		}}},
		time.Second,
		WithNetworkAttachments("proxy", manager),
	)
	if err := reconciler.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.disconnectCalls != 0 {
		t.Fatalf("disconnect calls = %d, want 0", manager.disconnectCalls)
	}
}
