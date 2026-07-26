package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
}

func (f fakeDiscovery) ListContainers(context.Context) ([]docker.Container, error) {
	return f.containers, nil
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
