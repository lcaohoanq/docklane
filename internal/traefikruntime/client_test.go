package traefikruntime

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestInspectRouteReturnsRuntimeComponents(t *testing.T) {
	responses := map[string]string{
		"/api/overview": `{"providers":["Docker","File","HTTP"]}`,
		"/api/http/routers/draw@http": `{
			"name":"draw@http",
			"status":"enabled"
		}`,
		"/api/http/services/draw@http": `{
			"name":"draw@http",
			"status":"enabled",
			"serverStatus":{"http://docklane-route-7:80":"UP"}
		}`,
	}
	client := &Client{
		baseURL:  "https://dashboard.test",
		username: "diagnostics",
		password: "secret",
		http: &http.Client{Transport: roundTripFunc(func(
			request *http.Request,
		) (*http.Response, error) {
			username, password, ok := request.BasicAuth()
			if !ok || username != "diagnostics" || password != "secret" {
				t.Fatalf("basic auth = %q, %q, %v", username, password, ok)
			}
			body, found := responses[request.URL.Path]
			if !found {
				return response(http.StatusNotFound, "not found"), nil
			}
			return response(http.StatusOK, body), nil
		})},
	}
	result, err := client.InspectRoute(context.Background(), "draw")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Router.Present ||
		!result.Service.Present ||
		result.ServerStatus["http://docklane-route-7:80"] != "UP" {
		t.Fatalf("result = %#v", result)
	}
}

func TestInspectRoutePreservesMissingComponent(t *testing.T) {
	client := &Client{
		baseURL: "https://dashboard.test",
		http: &http.Client{Transport: roundTripFunc(func(
			request *http.Request,
		) (*http.Response, error) {
			if request.URL.Path == "/api/overview" {
				return response(http.StatusOK, `{"providers":["HTTP"]}`), nil
			}
			return response(http.StatusNotFound, "not found"), nil
		})},
	}
	result, err := client.InspectRoute(context.Background(), "missing")
	if err != nil {
		t.Fatal(err)
	}
	if result.Router.Present ||
		result.Router.Name != "missing@http" ||
		result.Service.Present {
		t.Fatalf("result = %#v", result)
	}
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}
