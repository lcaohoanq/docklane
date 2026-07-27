package reconcile

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
)

type planningDocker struct {
	containers    []docker.Container
	network       docker.Network
	networkExists bool
	createCalls   int
	connectCalls  int
	disconnects   int
}

func (engine *planningDocker) ListContainers(context.Context) ([]docker.Container, error) {
	return append([]docker.Container(nil), engine.containers...), nil
}

func (engine *planningDocker) InspectNetwork(
	context.Context,
	string,
) (docker.Network, error) {
	if !engine.networkExists {
		return docker.Network{}, fmt.Errorf("%w: proxy", docker.ErrNetworkNotFound)
	}
	return engine.network, nil
}

func (engine *planningDocker) CreateNetwork(
	_ context.Context,
	name string,
	labels map[string]string,
) (docker.Network, error) {
	engine.createCalls++
	engine.networkExists = true
	engine.network = docker.Network{
		ID:     "network123",
		Name:   name,
		Driver: "bridge",
		Scope:  "local",
		Labels: labels,
	}
	return engine.network, nil
}

func (engine *planningDocker) NetworkAliases(
	_ context.Context,
	containerID string,
	network string,
) ([]string, error) {
	for _, container := range engine.containers {
		if container.ID == containerID {
			return append([]string(nil), container.NetworkAliases[network]...), nil
		}
	}
	return nil, nil
}

func (engine *planningDocker) ConnectNetwork(
	_ context.Context,
	network string,
	containerID string,
	aliases []string,
) error {
	engine.connectCalls++
	for index := range engine.containers {
		if engine.containers[index].ID != containerID {
			continue
		}
		if !engine.containers[index].HasNetwork(network) {
			engine.containers[index].Networks = append(
				engine.containers[index].Networks,
				network,
			)
		}
		if engine.containers[index].NetworkAliases == nil {
			engine.containers[index].NetworkAliases = map[string][]string{}
		}
		engine.containers[index].NetworkAliases[network] = append(
			[]string(nil),
			aliases...,
		)
	}
	return nil
}

func (engine *planningDocker) DisconnectNetwork(
	_ context.Context,
	network string,
	containerID string,
) error {
	engine.disconnects++
	for index := range engine.containers {
		if engine.containers[index].ID != containerID {
			continue
		}
		var kept []string
		for _, candidate := range engine.containers[index].Networks {
			if candidate != network {
				kept = append(kept, candidate)
			}
		}
		engine.containers[index].Networks = kept
		delete(engine.containers[index].NetworkAliases, network)
	}
	return nil
}

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
	aliases         [][]string
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
	_ context.Context,
	_ string,
	_ string,
	aliases []string,
) error {
	manager.calls++
	manager.aliases = append(manager.aliases, append([]string(nil), aliases...))
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

func TestRefreshExcludesDockerLabelHostnameCollision(t *testing.T) {
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
		}, {
			ID:   "legacy123",
			Name: "legacy",
			Labels: map[string]string{
				"traefik.enable":                   "true",
				"traefik.http.routers.legacy.rule": "Host(`draw.docker.home.arpa`)",
			},
		}}},
		time.Second,
		WithBaseDomain("docker.home.arpa"),
	)
	if err := reconciler.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	route := reconciler.Enrich(routes)[0]
	if route.Observed.State != domain.RouteStateError ||
		route.Observed.UpstreamURL != "" ||
		!strings.Contains(route.Observed.Message, "Docker-label router legacy") {
		t.Fatalf("collision observation = %#v", route.Observed)
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
	if len(manager.aliases) != 1 ||
		len(manager.aliases[0]) != 1 ||
		manager.aliases[0][0] != "docklane-route-1" {
		t.Fatalf("network aliases = %#v, want docklane-route-1", manager.aliases)
	}
	if observed.UpstreamURL != "http://docklane-route-1:80" {
		t.Fatalf("upstream = %q, want deterministic alias", observed.UpstreamURL)
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

func TestRefreshUsesContainerNameForUntrackedAttachment(t *testing.T) {
	routes := []domain.Route{{
		ID:       7,
		Name:     "draw",
		Port:     80,
		Scheme:   "http",
		Enabled:  true,
		Selector: domain.ContainerSelector{ContainerID: "abc"},
	}}
	manager := &fakeNetworkManager{}
	reconciler := New(
		&trackingStore{routes: routes},
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
	observed := reconciler.Enrich(routes)[0].Observed
	if observed.UpstreamURL != "http://draw:80" {
		t.Fatalf("upstream = %q, want safe container-name fallback", observed.UpstreamURL)
	}
	if manager.calls != 0 || manager.disconnectCalls != 0 {
		t.Fatalf(
			"connect calls = %d, disconnect calls = %d; want 0, 0",
			manager.calls,
			manager.disconnectCalls,
		)
	}
}

func TestRefreshUsesExistingDeterministicAlias(t *testing.T) {
	routes := []domain.Route{{
		ID:       7,
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
			Networks:     []string{"bridge", "proxy"},
			NetworkAliases: map[string][]string{
				"proxy": {"docklane-route-7"},
			},
		}}},
		time.Second,
		WithNetworkAttachments("proxy", manager),
	)
	if err := reconciler.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	observed := reconciler.Enrich(routes)[0].Observed
	if observed.UpstreamURL != "http://docklane-route-7:80" {
		t.Fatalf("upstream = %q, want deterministic alias", observed.UpstreamURL)
	}
	if manager.calls != 0 || manager.disconnectCalls != 0 {
		t.Fatalf(
			"connect calls = %d, disconnect calls = %d; want 0, 0",
			manager.calls,
			manager.disconnectCalls,
		)
	}
}

func TestRefreshRepairsAliasOnlyForOwnedAttachment(t *testing.T) {
	routes := []domain.Route{{
		ID:       7,
		Name:     "draw",
		Port:     80,
		Scheme:   "http",
		Enabled:  true,
		Selector: domain.ContainerSelector{ContainerID: "abc"},
	}}
	store := &trackingStore{
		routes: routes,
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
			NetworkAliases: map[string][]string{
				"proxy": {"draw"},
			},
		}}},
		time.Second,
		WithNetworkAttachments("proxy", manager),
	)
	if err := reconciler.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	observed := reconciler.Enrich(routes)[0].Observed
	if observed.UpstreamURL != "http://docklane-route-7:80" {
		t.Fatalf("upstream = %q, want repaired deterministic alias", observed.UpstreamURL)
	}
	if manager.calls != 1 || manager.disconnectCalls != 1 {
		t.Fatalf(
			"connect calls = %d, disconnect calls = %d; want 1, 1",
			manager.calls,
			manager.disconnectCalls,
		)
	}
	if len(manager.aliases[0]) != 1 || manager.aliases[0][0] != "docklane-route-7" {
		t.Fatalf("repair aliases = %#v", manager.aliases)
	}
}

func TestRefreshConnectsAllAliasesForSharedContainer(t *testing.T) {
	routes := []domain.Route{
		{
			ID:       7,
			Name:     "draw",
			Port:     80,
			Scheme:   "http",
			Enabled:  true,
			Selector: domain.ContainerSelector{ContainerID: "abc"},
		},
		{
			ID:       9,
			Name:     "canvas",
			Port:     80,
			Scheme:   "http",
			Enabled:  true,
			Selector: domain.ContainerSelector{ContainerID: "abc"},
		},
	}
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
	if manager.calls != 1 {
		t.Fatalf("network connect calls = %d, want 1", manager.calls)
	}
	if got := strings.Join(manager.aliases[0], ","); got != "docklane-route-7,docklane-route-9" {
		t.Fatalf("aliases = %q", got)
	}
	observed := reconciler.Enrich(routes)
	if observed[0].Observed.UpstreamURL != "http://docklane-route-7:80" ||
		observed[1].Observed.UpstreamURL != "http://docklane-route-9:80" {
		t.Fatalf(
			"upstreams = %q, %q",
			observed[0].Observed.UpstreamURL,
			observed[1].Observed.UpstreamURL,
		)
	}
}

func TestNetworkPlanCreatesMissingNetworkAndConnectsWorkload(t *testing.T) {
	store := &trackingStore{routes: []domain.Route{{
		ID:       7,
		Name:     "draw",
		Port:     80,
		Scheme:   "http",
		Enabled:  true,
		Selector: domain.ContainerSelector{ContainerID: "abc"},
	}}}
	engine := &planningDocker{containers: []docker.Container{{
		ID:           "abc123",
		Name:         "draw",
		ExposedPorts: []uint16{80},
		Networks:     []string{"bridge"},
	}}}
	reconciler := New(
		store,
		engine,
		time.Second,
		WithNetworkAttachments("proxy", engine),
	)
	plan, err := reconciler.NetworkPlan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Network.Ownership != domain.NetworkOwnershipMissing ||
		len(plan.Operations) != 2 ||
		plan.Operations[0].Action != domain.NetworkActionCreate ||
		plan.Operations[1].Action != domain.NetworkActionConnect {
		t.Fatalf("plan = %#v", plan)
	}
	if strings.Join(plan.Operations[1].Aliases, ",") != "docklane-route-7" {
		t.Fatalf("connect aliases = %#v", plan.Operations[1].Aliases)
	}

	result, err := reconciler.ApplyNetworkPlan(context.Background(), plan.Token)
	if err != nil {
		t.Fatal(err)
	}
	if engine.createCalls != 1 || engine.connectCalls != 1 {
		t.Fatalf(
			"create calls = %d, connect calls = %d; want 1, 1",
			engine.createCalls,
			engine.connectCalls,
		)
	}
	if len(result.Remaining.Operations) != 0 {
		t.Fatalf("remaining operations = %#v", result.Remaining.Operations)
	}
	if len(store.attachments) != 1 {
		t.Fatalf("attachments = %#v, want one", store.attachments)
	}
}

func TestNetworkPlanPreservesCompatibleExternalNetwork(t *testing.T) {
	store := &trackingStore{routes: []domain.Route{{
		ID:       7,
		Name:     "draw",
		Port:     80,
		Scheme:   "http",
		Enabled:  true,
		Selector: domain.ContainerSelector{ContainerID: "abc"},
	}}}
	engine := &planningDocker{
		networkExists: true,
		network: docker.Network{
			ID:     "network123",
			Name:   "proxy",
			Driver: "bridge",
			Scope:  "local",
		},
		containers: []docker.Container{
			{
				ID:           "abc123",
				Name:         "draw",
				ExposedPorts: []uint16{80},
				Networks:     []string{"bridge", "proxy"},
			},
			{
				ID:         "proxy123",
				Name:       "traefik",
				SystemRole: docker.SystemRoleReverseProxy,
				Networks:   []string{"proxy"},
			},
		},
	}
	reconciler := New(
		store,
		engine,
		time.Second,
		WithNetworkAttachments("proxy", engine),
	)
	plan, err := reconciler.NetworkPlan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Network.Ownership != domain.NetworkOwnershipExternal ||
		!plan.Network.Compatible {
		t.Fatalf("network state = %#v", plan.Network)
	}
	if len(plan.Operations) != 0 {
		t.Fatalf("operations = %#v, want none", plan.Operations)
	}
	if len(plan.Warnings) != 1 ||
		!strings.Contains(plan.Warnings[0], "pre-existing") {
		t.Fatalf("warnings = %#v", plan.Warnings)
	}
}

func TestNetworkPlanRejectsConflictingDocklaneLabels(t *testing.T) {
	engine := &planningDocker{
		networkExists: true,
		network: docker.Network{
			ID:     "network123",
			Name:   "proxy",
			Driver: "bridge",
			Scope:  "local",
			Labels: map[string]string{
				docker.NetworkManagedLabel: "true",
				docker.NetworkRoleLabel:    "database",
				docker.NetworkSchemaLabel:  docker.NetworkSchemaV1,
			},
		},
	}
	reconciler := New(
		&trackingStore{},
		engine,
		time.Second,
		WithNetworkAttachments("proxy", engine),
	)
	plan, err := reconciler.NetworkPlan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Network.Ownership != domain.NetworkOwnershipConflict ||
		plan.Network.Compatible {
		t.Fatalf("network state = %#v", plan.Network)
	}
	if _, err := reconciler.ApplyNetworkPlan(
		context.Background(),
		plan.Token,
	); err == nil {
		t.Fatal("expected conflicting network apply to fail")
	}
}

func TestNetworkPlanPreviewsOwnedDisconnect(t *testing.T) {
	store := &trackingStore{
		routes: []domain.Route{{
			ID:       7,
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
	engine := &planningDocker{
		networkExists: true,
		network: docker.Network{
			ID:     "network123",
			Name:   "proxy",
			Driver: "bridge",
			Scope:  "local",
		},
		containers: []docker.Container{{
			ID:           "abc123",
			Name:         "draw",
			ExposedPorts: []uint16{80},
			Networks:     []string{"bridge", "proxy"},
		}},
	}
	reconciler := New(
		store,
		engine,
		time.Second,
		WithNetworkAttachments("proxy", engine),
	)
	plan, err := reconciler.NetworkPlan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 1 ||
		plan.Operations[0].Action != domain.NetworkActionDisconnect ||
		!plan.Operations[0].Destructive {
		t.Fatalf("operations = %#v", plan.Operations)
	}
}

func TestApplyRejectsStaleNetworkPlan(t *testing.T) {
	engine := &planningDocker{
		networkExists: true,
		network: docker.Network{
			ID:     "network123",
			Name:   "proxy",
			Driver: "bridge",
			Scope:  "local",
		},
	}
	reconciler := New(
		&trackingStore{},
		engine,
		time.Second,
		WithNetworkAttachments("proxy", engine),
	)
	if _, err := reconciler.ApplyNetworkPlan(
		context.Background(),
		"stale-token",
	); err == nil || !strings.Contains(err.Error(), "fetch and review") {
		t.Fatalf("stale apply error = %v", err)
	}
	if engine.createCalls != 0 || engine.connectCalls != 0 || engine.disconnects != 0 {
		t.Fatal("stale plan unexpectedly mutated Docker")
	}
}
