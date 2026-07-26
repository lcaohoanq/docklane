package client

import (
	"context"
	"encoding/json"
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

func TestCreateRouteSendsOnlyWritableFields(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"id", "revision", "createdAt", "updatedAt"} {
			if _, exists := payload[field]; exists {
				t.Errorf("request contains read-only field %q", field)
			}
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"id": 1,
				"name": "draw",
				"selector": {"containerId": "abc"},
				"port": 80,
				"scheme": "http",
				"enabled": true
			}`)),
		}, nil
	})}
	apiClient := New("http://docklane.test")
	apiClient.http = httpClient

	_, err := apiClient.CreateRoute(context.Background(), domain.Route{
		ID:       99,
		Name:     "draw",
		Selector: domain.ContainerSelector{ContainerID: "abc"},
		Port:     80,
		Scheme:   "http",
		Enabled:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpdateRouteSendsRevision(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload struct {
			Revision uint64 `json:"revision"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Revision != 7 {
			t.Errorf("revision = %d, want 7", payload.Revision)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"id": 1,
				"revision": 8,
				"name": "draw",
				"selector": {"containerId": "abc"},
				"port": 80,
				"scheme": "http",
				"enabled": true
			}`)),
		}, nil
	})}
	apiClient := New("http://docklane.test")
	apiClient.http = httpClient

	updated, err := apiClient.UpdateRoute(context.Background(), domain.Route{
		ID:       1,
		Revision: 7,
		Name:     "draw",
		Selector: domain.ContainerSelector{ContainerID: "abc"},
		Port:     80,
		Scheme:   "http",
		Enabled:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 8 {
		t.Fatalf("updated revision = %d, want 8", updated.Revision)
	}
}
