package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"docklane.local/docklane/internal/config"
	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/reconcile"
	"docklane.local/docklane/internal/store"
	"docklane.local/docklane/internal/traefik"
)

type fakeDiscovery struct {
	containers []docker.Container
	err        error
}

func (f fakeDiscovery) ListContainers(context.Context) ([]docker.Container, error) {
	return f.containers, f.err
}

type networkDiscovery struct {
	fakeDiscovery
}

func (networkDiscovery) InspectNetwork(
	context.Context,
	string,
) (docker.Network, error) {
	return docker.Network{
		ID:     "network123",
		Name:   "proxy",
		Driver: "bridge",
		Scope:  "local",
	}, nil
}

func TestNetworkPlanEndpoints(t *testing.T) {
	repository, err := store.Open(filepath.Join(t.TempDir(), "docklane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	discovery := networkDiscovery{}
	reconciler := reconcile.New(
		repository,
		discovery,
		time.Second,
		reconcile.WithNetworkAttachments("proxy", nil),
	)
	handler := New(
		config.Config{
			BaseDomain:     "docker.home.arpa",
			ProxyNetwork:   "proxy",
			ReconcileEvery: time.Second,
		},
		repository,
		discovery,
		reconciler,
	)
	for _, test := range []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/api/v1/network/plan"},
		{
			method: http.MethodPost,
			path:   "/api/v1/network/apply",
			body:   `{"token":"TOKEN"}`,
		},
	} {
		body := strings.NewReader(test.body)
		if test.method == http.MethodPost {
			plan, err := reconciler.NetworkPlan(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			body = strings.NewReader(fmt.Sprintf(`{"token":%q}`, plan.Token))
		}
		request := httptest.NewRequest(test.method, test.path, body)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf(
				"%s %s status = %d, body = %s",
				test.method,
				test.path,
				response.Code,
				response.Body,
			)
		}
	}
	stale := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/network/apply",
		strings.NewReader(`{"token":"stale"}`),
	)
	staleResponse := httptest.NewRecorder()
	handler.ServeHTTP(staleResponse, stale)
	if staleResponse.Code != http.StatusConflict {
		t.Fatalf(
			"stale apply status = %d, body = %s",
			staleResponse.Code,
			staleResponse.Body,
		)
	}
}

func TestCreateRouteAndRenderTraefikConfiguration(t *testing.T) {
	repository, err := store.Open(filepath.Join(t.TempDir(), "docklane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	discovery := fakeDiscovery{
		containers: []docker.Container{{
			ID:             "abc123",
			Name:           "draw-web-7",
			ComposeProject: "draw",
			ComposeService: "web",
			ExposedPorts:   []uint16{80},
		}},
	}
	reconciler := reconcile.New(repository, discovery, time.Second)
	handler := New(
		config.Config{BaseDomain: "docker.home.arpa", ReconcileEvery: time.Second},
		repository,
		discovery,
		reconciler,
	)
	body := bytes.NewBufferString(`{
		"name": "draw",
		"selector": {"composeProject": "draw", "composeService": "web"},
		"port": 80
	}`)
	create := httptest.NewRequest(http.MethodPost, "/api/v1/routes", body)
	create.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, create)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createResponse.Code, createResponse.Body)
	}

	get := httptest.NewRequest(http.MethodGet, "/internal/traefik", nil)
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("Traefik status = %d, body = %s", getResponse.Code, getResponse.Body)
	}
	var configuration traefik.Configuration
	if err := json.NewDecoder(getResponse.Body).Decode(&configuration); err != nil {
		t.Fatal(err)
	}
	server := configuration.HTTP.Services["draw"].LoadBalancer.Servers[0]
	if server.URL != "http://draw-web-7:80" {
		t.Fatalf("server URL = %q", server.URL)
	}
}

func TestRouteRejectsActiveDockerLabelHostnameCollision(t *testing.T) {
	repository, err := store.Open(filepath.Join(t.TempDir(), "docklane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	discovery := fakeDiscovery{containers: []docker.Container{{
		ID:             "abc123",
		Name:           "draw-web-1",
		ComposeProject: "draw",
		ComposeService: "web",
		ExposedPorts:   []uint16{80},
		Labels: map[string]string{
			"traefik.enable":                "true",
			"traefik.http.routers.old.rule": "Host(`draw.docker.home.arpa`)",
		},
	}}}
	reconciler := reconcile.New(
		repository,
		discovery,
		time.Second,
		reconcile.WithBaseDomain("docker.home.arpa"),
	)
	handler := New(
		config.Config{
			BaseDomain:     "docker.home.arpa",
			ReconcileEvery: time.Second,
		},
		repository,
		discovery,
		reconciler,
	)
	enabledBody := `{
		"name": "draw",
		"selector": {"composeProject": "draw", "composeService": "web"},
		"port": 80,
		"enabled": true
	}`
	rejected := httptest.NewRecorder()
	handler.ServeHTTP(
		rejected,
		httptest.NewRequest(
			http.MethodPost,
			"/api/v1/routes",
			strings.NewReader(enabledBody),
		),
	)
	if rejected.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rejected.Body.String(), "Docker-label router old") {
		t.Fatalf("collision response = %d, %s", rejected.Code, rejected.Body)
	}

	disabledBody := strings.Replace(enabledBody, `"enabled": true`, `"enabled": false`, 1)
	created := httptest.NewRecorder()
	handler.ServeHTTP(
		created,
		httptest.NewRequest(
			http.MethodPost,
			"/api/v1/routes",
			strings.NewReader(disabledBody),
		),
	)
	if created.Code != http.StatusCreated {
		t.Fatalf("disabled create status = %d, body = %s", created.Code, created.Body)
	}

	enable := httptest.NewRecorder()
	handler.ServeHTTP(
		enable,
		httptest.NewRequest(
			http.MethodPut,
			"/api/v1/routes/1",
			strings.NewReader(strings.Replace(
				enabledBody,
				`"name": "draw",`,
				`"revision": 1, "name": "draw",`,
				1,
			)),
		),
	)
	if enable.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(enable.Body.String(), "disable or rename") {
		t.Fatalf("enable collision response = %d, %s", enable.Code, enable.Body)
	}
}

func TestTraefikProviderFallsBackToPersistedLastKnownGood(t *testing.T) {
	repository, err := store.Open(filepath.Join(t.TempDir(), "docklane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	_, err = repository.CreateRoute(context.Background(), domain.Route{
		Name:     "draw",
		Port:     80,
		Scheme:   "http",
		Enabled:  true,
		Selector: domain.ContainerSelector{ContainerID: "abc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	discovery := &fakeDiscovery{containers: []docker.Container{{
		ID:           "abc123",
		Name:         "draw",
		ExposedPorts: []uint16{80},
		Networks:     []string{"proxy"},
	}}}
	reconciler := reconcile.New(
		repository,
		discovery,
		time.Second,
		reconcile.WithNetworkAttachments("proxy", nil),
	)
	if err := reconciler.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	handler := New(
		config.Config{
			BaseDomain:     "docker.home.arpa",
			ProxyNetwork:   "proxy",
			ReconcileEvery: time.Second,
		},
		repository,
		discovery,
		reconciler,
	)

	first := httptest.NewRecorder()
	handler.ServeHTTP(
		first,
		httptest.NewRequest(http.MethodGet, "/internal/traefik", nil),
	)
	if first.Code != http.StatusOK {
		t.Fatalf("first provider status = %d, body = %s", first.Code, first.Body)
	}
	snapshot, err := repository.GetProviderSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Fingerprint == "" {
		t.Fatal("provider snapshot has no fingerprint")
	}

	discovery.err = errors.New("Docker temporarily unavailable")
	restartedReconciler := reconcile.New(
		repository,
		discovery,
		time.Second,
		reconcile.WithNetworkAttachments("proxy", nil),
	)
	handler = New(
		config.Config{
			BaseDomain:     "docker.home.arpa",
			ProxyNetwork:   "proxy",
			ReconcileEvery: time.Second,
		},
		repository,
		discovery,
		restartedReconciler,
	)
	fallback := httptest.NewRecorder()
	handler.ServeHTTP(
		fallback,
		httptest.NewRequest(http.MethodGet, "/internal/traefik", nil),
	)
	if fallback.Code != http.StatusOK {
		t.Fatalf(
			"fallback provider status = %d, body = %s",
			fallback.Code,
			fallback.Body,
		)
	}
	var configuration traefik.Configuration
	if err := json.NewDecoder(fallback.Body).Decode(&configuration); err != nil {
		t.Fatal(err)
	}
	if len(configuration.HTTP.Routers) != 1 {
		t.Fatalf("fallback configuration = %#v", configuration)
	}

	health := httptest.NewRecorder()
	handler.ServeHTTP(
		health,
		httptest.NewRequest(http.MethodGet, "/api/v1/health", nil),
	)
	var healthPayload struct {
		Status   string                `json:"status"`
		Provider domain.ProviderStatus `json:"provider"`
	}
	if err := json.NewDecoder(health.Body).Decode(&healthPayload); err != nil {
		t.Fatal(err)
	}
	if healthPayload.Status != "degraded" ||
		healthPayload.Provider.Source != domain.ProviderSourceLastKnownGood ||
		!strings.Contains(healthPayload.Provider.LastError, "temporarily unavailable") {
		t.Fatalf("health = %#v", healthPayload)
	}
}

func TestTraefikProviderIsUnavailableWithoutSnapshot(t *testing.T) {
	repository, err := store.Open(filepath.Join(t.TempDir(), "docklane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	discovery := fakeDiscovery{err: errors.New("Docker unavailable")}
	reconciler := reconcile.New(repository, discovery, time.Second)
	handler := New(
		config.Config{
			BaseDomain:     "docker.home.arpa",
			ProxyNetwork:   "proxy",
			ReconcileEvery: time.Second,
		},
		repository,
		discovery,
		reconciler,
	)

	provider := httptest.NewRecorder()
	handler.ServeHTTP(
		provider,
		httptest.NewRequest(http.MethodGet, "/internal/traefik", nil),
	)
	if provider.Code != http.StatusBadGateway {
		t.Fatalf("provider status = %d, body = %s", provider.Code, provider.Body)
	}

	health := httptest.NewRecorder()
	handler.ServeHTTP(
		health,
		httptest.NewRequest(http.MethodGet, "/api/v1/health", nil),
	)
	var payload struct {
		Status   string                `json:"status"`
		Provider domain.ProviderStatus `json:"provider"`
	}
	if err := json.NewDecoder(health.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "degraded" ||
		payload.Provider.Source != domain.ProviderSourceUnavailable ||
		!strings.Contains(payload.Provider.LastError, "Docker unavailable") {
		t.Fatalf("health = %#v", payload)
	}
}

func TestFrontendIsServed(t *testing.T) {
	repository, err := store.Open(filepath.Join(t.TempDir(), "docklane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	discovery := fakeDiscovery{}
	reconciler := reconcile.New(repository, discovery, time.Second)
	handler := New(
		config.Config{BaseDomain: "docker.home.arpa", ReconcileEvery: time.Second},
		repository,
		discovery,
		reconciler,
	)

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "text/html; charset=utf-8" {
		t.Fatalf("content type = %q", contentType)
	}
}

func TestUpdateDisableAndDeleteRoute(t *testing.T) {
	repository, err := store.Open(filepath.Join(t.TempDir(), "docklane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	discovery := fakeDiscovery{containers: []docker.Container{{
		ID:             "abc123",
		Name:           "draw-web-1",
		ComposeProject: "draw",
		ComposeService: "web",
		ExposedPorts:   []uint16{80, 8080},
	}}}
	reconciler := reconcile.New(repository, discovery, time.Second)
	handler := New(
		config.Config{BaseDomain: "docker.home.arpa", ReconcileEvery: time.Second},
		repository,
		discovery,
		reconciler,
	)

	createBody := bytes.NewBufferString(`{
		"name": "draw",
		"selector": {"composeProject": "draw", "composeService": "web"},
		"port": 80
	}`)
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		createResponse,
		httptest.NewRequest(http.MethodPost, "/api/v1/routes", createBody),
	)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createResponse.Code, createResponse.Body)
	}

	updateBody := bytes.NewBufferString(`{
		"revision": 1,
		"name": "canvas",
		"selector": {"composeProject": "draw", "composeService": "web"},
		"port": 8080,
		"scheme": "http",
		"enabled": false
	}`)
	updateResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		updateResponse,
		httptest.NewRequest(http.MethodPut, "/api/v1/routes/1", updateBody),
	)
	if updateResponse.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updateResponse.Code, updateResponse.Body)
	}
	var updated struct {
		Name     string `json:"name"`
		Enabled  bool   `json:"enabled"`
		Observed struct {
			State string `json:"state"`
		} `json:"observed"`
	}
	if err := json.NewDecoder(updateResponse.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.Name != "canvas" || updated.Enabled || updated.Observed.State != "disabled" {
		t.Fatalf("unexpected updated route: %#v", updated)
	}

	deleteResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		deleteResponse,
		httptest.NewRequest(http.MethodDelete, "/api/v1/routes/1", nil),
	)
	if deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", deleteResponse.Code, deleteResponse.Body)
	}

	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		getResponse,
		httptest.NewRequest(http.MethodGet, "/api/v1/routes/1", nil),
	)
	if getResponse.Code != http.StatusNotFound {
		t.Fatalf("get deleted status = %d", getResponse.Code)
	}
}

func TestUpdateRejectsStaleRevision(t *testing.T) {
	repository, err := store.Open(filepath.Join(t.TempDir(), "docklane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	discovery := fakeDiscovery{}
	reconciler := reconcile.New(repository, discovery, time.Second)
	handler := New(
		config.Config{BaseDomain: "docker.home.arpa", ReconcileEvery: time.Second},
		repository,
		discovery,
		reconciler,
	)

	created, err := repository.CreateRoute(context.Background(), domain.Route{
		Name:     "draw",
		Selector: domain.ContainerSelector{ContainerID: "abc"},
		Port:     80,
		Scheme:   "http",
		Enabled:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstUpdate := fmt.Sprintf(`{
		"revision": %d,
		"name": "canvas",
		"selector": {"containerId": "abc"},
		"port": 80,
		"scheme": "http",
		"enabled": true
	}`, created.Revision)
	firstResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		firstResponse,
		httptest.NewRequest(http.MethodPut, "/api/v1/routes/1", bytes.NewBufferString(firstUpdate)),
	)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first update status = %d, body = %s", firstResponse.Code, firstResponse.Body)
	}

	staleResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		staleResponse,
		httptest.NewRequest(http.MethodPut, "/api/v1/routes/1", bytes.NewBufferString(firstUpdate)),
	)
	if staleResponse.Code != http.StatusConflict {
		t.Fatalf("stale update status = %d, want 409; body = %s", staleResponse.Code, staleResponse.Body)
	}
}

func TestCreateRejectsActiveReverseProxyTarget(t *testing.T) {
	repository, err := store.Open(filepath.Join(t.TempDir(), "docklane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	discovery := fakeDiscovery{containers: []docker.Container{{
		ID:             "proxy123",
		Name:           "traefik",
		ComposeProject: "traefik",
		ComposeService: "traefik",
		SystemRole:     docker.SystemRoleReverseProxy,
		ExposedPorts:   []uint16{80, 443},
	}}}
	reconciler := reconcile.New(repository, discovery, time.Second)
	handler := New(
		config.Config{BaseDomain: "docker.home.arpa", ReconcileEvery: time.Second},
		repository,
		discovery,
		reconciler,
	)

	body := bytes.NewBufferString(`{
		"name": "traefik",
		"selector": {"composeProject": "traefik", "composeService": "traefik"},
		"port": 80,
		"scheme": "http",
		"enabled": true
	}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(
		response,
		httptest.NewRequest(http.MethodPost, "/api/v1/routes", body),
	)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("create status = %d, want 422; body = %s", response.Code, response.Body)
	}
	routes, err := repository.ListRoutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 0 {
		t.Fatalf("saved routes = %#v, want none", routes)
	}

	existing, err := repository.CreateRoute(context.Background(), domain.Route{
		Name: "traefik",
		Selector: domain.ContainerSelector{
			ComposeProject: "traefik",
			ComposeService: "traefik",
		},
		Port:    80,
		Scheme:  "http",
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	disableBody := bytes.NewBufferString(fmt.Sprintf(`{
		"revision": %d,
		"name": "traefik",
		"selector": {"composeProject": "traefik", "composeService": "traefik"},
		"port": 80,
		"scheme": "http",
		"enabled": false
	}`, existing.Revision))
	disableResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		disableResponse,
		httptest.NewRequest(http.MethodPut, "/api/v1/routes/1", disableBody),
	)
	if disableResponse.Code != http.StatusOK {
		t.Fatalf("disable status = %d, want 200; body = %s", disableResponse.Code, disableResponse.Body)
	}
}
