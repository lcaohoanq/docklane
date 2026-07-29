package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"docklane.local/docklane/internal/domain"
)

func TestAppEnableDryRunResolvesComposeApplicationWithoutMutation(t *testing.T) {
	mutations := 0
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/containers":
			fmt.Fprint(response, `{"containers":[{
				"id":"abcdef123456",
				"name":"draw-web-1",
				"composeProject":"draw",
				"composeService":"web",
				"exposedPorts":[80]
			}]}`)
		case "/api/v1/routes":
			fmt.Fprint(response, `{"routes":[],"baseDomain":"docker.home.arpa"}`)
		default:
			mutations++
			http.Error(response, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	err := appEnable([]string{
		"draw/web",
		"--url", server.URL,
		"--dry-run",
	})
	if err != nil {
		t.Fatal(err)
	}
	if mutations != 0 {
		t.Fatalf("mutation requests = %d", mutations)
	}
}

func TestAppEnableRefusesExistingNameWithDifferentTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/containers":
			fmt.Fprint(response, `{"containers":[{
				"id":"new-container",
				"name":"new-app",
				"exposedPorts":[8080]
			}]}`)
		case "/api/v1/routes":
			fmt.Fprint(response, `{
				"baseDomain":"docker.home.arpa",
				"routes":[{
					"id":7,
					"revision":1,
					"name":"shared",
					"selector":{"containerId":"old-container"},
					"port":8080,
					"scheme":"http",
					"enabled":true
				}]
			}`)
		default:
			http.Error(response, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	err := appEnable([]string{
		"new-app",
		"--url", server.URL,
		"--name", "shared",
		"--wait=0",
	})
	if err == nil || !strings.Contains(err.Error(), "already belongs") {
		t.Fatalf("error = %v", err)
	}
}

func TestFindAppRoutePrefersNumericNameOverID(t *testing.T) {
	route, found := findAppRoute([]domain.Route{
		{ID: 123, Name: "other"},
		{ID: 9, Name: "123"},
	}, "123")
	if !found || route.ID != 9 {
		t.Fatalf("route = %#v, found = %t", route, found)
	}
}
