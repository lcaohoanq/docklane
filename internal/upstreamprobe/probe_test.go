package upstreamprobe

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"docklane.local/docklane/internal/domain"
)

func TestHandleProbeReturnsHTTPResult(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodGet,
		"/v1/probe?url=http%3A%2F%2Fdocklane-route-7%3A80",
		nil,
	)
	response := httptest.NewRecorder()
	handleProbe(
		response,
		request,
		func(_ context.Context, upstreamURL string) (int, error) {
			if upstreamURL != "http://docklane-route-7:80" {
				t.Errorf("upstream URL = %q", upstreamURL)
			}
			return http.StatusNoContent, nil
		},
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	var result domain.UpstreamProbe
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.Reachable ||
		result.HTTPStatus != http.StatusNoContent ||
		result.URL != "http://docklane-route-7:80" {
		t.Fatalf("result = %#v", result)
	}
}

func TestValidateURLRejectsUnsafeTargets(t *testing.T) {
	for _, value := range []string{
		"file:///etc/passwd",
		"http://draw",
		"http://user:pass@draw:80",
		"http://draw:80/private",
	} {
		if err := validateURL(value); err == nil {
			t.Fatalf("validateURL(%q) unexpectedly succeeded", value)
		}
	}
	if err := validateURL("http://docklane-route-7:80"); err != nil {
		t.Fatal(err)
	}
}

func TestHandleProbeReturnsConnectionFailureAsResult(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodGet,
		"/v1/probe?url=http%3A%2F%2Fdocklane-route-7%3A80",
		nil,
	)
	response := httptest.NewRecorder()
	handleProbe(
		response,
		request,
		func(context.Context, string) (int, error) {
			return 0, errors.New("connect: connection refused")
		},
	)
	var result domain.UpstreamProbe
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Reachable || !strings.Contains(result.Error, "connect") {
		t.Fatalf("result = %#v", result)
	}
}
