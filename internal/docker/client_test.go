package docker

import (
	"context"
	"encoding/json"
	"errors"
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

func TestResolveContainerByComposeWorkload(t *testing.T) {
	containers := []Container{
		{ID: "one", Name: "unrelated", ComposeProject: "other", ComposeService: "web"},
		{ID: "two", Name: "actual-name", ComposeProject: "draw", ComposeService: "web"},
	}
	got, err := ResolveContainer(domain.ContainerSelector{
		ComposeProject: "draw",
		ComposeService: "web",
	}, containers)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "actual-name" {
		t.Fatalf("name = %q, want actual-name", got.Name)
	}
}

func TestResolveContainerRejectsAmbiguousWorkload(t *testing.T) {
	containers := []Container{
		{ID: "one", ComposeProject: "draw", ComposeService: "web"},
		{ID: "two", ComposeProject: "draw", ComposeService: "web"},
	}
	_, err := ResolveContainer(domain.ContainerSelector{
		ComposeProject: "draw",
		ComposeService: "web",
	}, containers)
	if err == nil {
		t.Fatal("expected ambiguous selector error")
	}
}

func TestValidateTCPPort(t *testing.T) {
	container := Container{Name: "draw-web-1", ExposedPorts: []uint16{80, 8080}}
	if err := ValidateTCPPort(container, 8080); err != nil {
		t.Fatalf("declared port rejected: %v", err)
	}
	if err := ValidateTCPPort(container, 3000); !errors.Is(err, ErrPortNotExposed) {
		t.Fatalf("undeclared port error = %v, want ErrPortNotExposed", err)
	}
}

func TestEvaluateRouteEligibility(t *testing.T) {
	tests := []struct {
		name      string
		container Container
		eligible  bool
		code      string
	}{
		{
			name: "application",
			container: Container{
				Name: "web", ExposedPorts: []uint16{3000},
			},
			eligible: true,
		},
		{
			name: "probe system workload despite inherited image port",
			container: Container{
				Name: "docklane-probe", SystemRole: SystemRoleProbe,
				ExposedPorts: []uint16{4646},
			},
			code: RouteEligibilitySystemWorkload,
		},
		{
			name: "no declared TCP port",
			container: Container{
				Name: "buildx_buildkit_release0",
			},
			code: RouteEligibilityNoTCPPorts,
		},
		{
			name: "explicit opt out",
			container: Container{
				Name:         "postgres",
				ExposedPorts: []uint16{5432},
				Labels:       map[string]string{RouteEnabledLabel: "false"},
			},
			code: RouteEligibilityDisabled,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			eligibility := EvaluateRouteEligibility(test.container)
			if eligibility.Eligible != test.eligible || eligibility.Code != test.code {
				t.Fatalf(
					"eligibility = %#v, want eligible=%t code=%q",
					eligibility,
					test.eligible,
					test.code,
				)
			}
		})
	}
}

func TestWatchContainerEventsFiltersRouteAffectingActions(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			if request.URL.Query().Get("filters") != `{"type":["container"]}` {
				t.Fatalf("filters = %q", request.URL.Query().Get("filters"))
			}
			body := strings.Join([]string{
				`{"Type":"container","Action":"start"}`,
				`{"Type":"container","Action":"exec_start: shell"}`,
				`{"Type":"container","Action":"health_status: healthy"}`,
			}, "\n")
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		},
	)}}

	notifications := 0
	err := client.WatchContainerEvents(context.Background(), func() {
		notifications++
	})
	if err == nil || !strings.Contains(err.Error(), "stream closed") {
		t.Fatalf("stream error = %v, want closed stream", err)
	}
	if notifications != 2 {
		t.Fatalf("notifications = %d, want 2", notifications)
	}
}

func TestNewClientLeavesStreamingRequestsContextControlled(t *testing.T) {
	client := NewClient("/var/run/docker.sock")
	if client.http.Timeout != 0 {
		t.Fatalf("HTTP client timeout = %s, want no whole-request timeout", client.http.Timeout)
	}
}

func TestListContainersRecordsPublishedHostPorts(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripFunc(
		func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(`[
					{
						"Id":"abcdef1234567890",
						"Names":["/traefik"],
						"Image":"traefik:v3.7",
						"State":"running",
						"Status":"Up",
						"Labels":{},
						"Ports":[
							{"PrivatePort":80,"PublicPort":80,"Type":"tcp"},
							{"PrivatePort":443,"PublicPort":443,"Type":"tcp"},
							{"PrivatePort":8080,"PublicPort":0,"Type":"tcp"}
						],
						"NetworkSettings":{"Networks":{"proxy":{}}}
					}
				]`)),
			}, nil
		},
	)}}
	containers, err := client.ListContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 1 ||
		len(containers[0].PublishedPorts) != 2 ||
		containers[0].PublishedPorts[0] != 80 ||
		containers[0].PublishedPorts[1] != 443 ||
		containers[0].SystemRole != SystemRoleReverseProxy {
		t.Fatalf("containers = %#v", containers)
	}
}

func TestNetworkAliasesInspectsContainerEndpoint(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			if request.Method != http.MethodGet ||
				request.URL.Path != "/containers/abc123/json" {
				t.Fatalf("request = %s %s", request.Method, request.URL.Path)
			}
			body := `{"NetworkSettings":{"Networks":{"proxy":{
				"Aliases":["draw","docklane-route-7"]
			}}}}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		},
	)}}
	aliases, err := client.NetworkAliases(context.Background(), "abc123", "proxy")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(aliases, ",") != "draw,docklane-route-7" {
		t.Fatalf("aliases = %#v", aliases)
	}
}

func TestInspectContainerRuntimeReturnsCommandAndMountOwnership(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			if request.Method != http.MethodGet ||
				request.URL.Path != "/containers/traefik123/json" {
				t.Fatalf("request = %s %s", request.Method, request.URL.Path)
			}
			body := `{
				"Image":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"State":{"Running":true,"Health":{"Status":"healthy"}},
				"Config":{"Cmd":["--providers.file.filename=/dynamic/tls.yml"]},
				"Mounts":[
					{"Type":"bind","Source":"/host/dynamic","Destination":"/dynamic","RW":false},
					{"Type":"volume","Name":"certs","Source":"/var/lib/certs","Destination":"/certs","RW":true}
				],
				"HostConfig":{
					"ReadonlyRootfs":true,
					"Privileged":false,
					"SecurityOpt":["no-new-privileges:true"],
					"CapDrop":["ALL"],
					"RestartPolicy":{"Name":"unless-stopped"},
					"PortBindings":{"4646/tcp":[{"HostIp":"127.0.0.1","HostPort":"4646"}]}
				}
			}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		},
	)}}
	runtime, err := client.InspectContainerRuntime(context.Background(), "traefik123")
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.Command) != 1 ||
		runtime.Command[0] != "--providers.file.filename=/dynamic/tls.yml" {
		t.Fatalf("command = %#v", runtime.Command)
	}
	if len(runtime.Mounts) != 2 ||
		!runtime.Mounts[0].ReadOnly ||
		runtime.Mounts[1].ReadOnly ||
		runtime.Mounts[1].Name != "certs" {
		t.Fatalf("mounts = %#v", runtime.Mounts)
	}
	if !runtime.Running ||
		runtime.Health != "healthy" ||
		!runtime.ReadOnlyRootFS ||
		!runtime.NoNewPrivileges ||
		runtime.Privileged ||
		runtime.RestartPolicy != "unless-stopped" ||
		len(runtime.PortBindings) != 1 ||
		runtime.PortBindings[0].HostIP != "127.0.0.1" ||
		!containsString(runtime.DroppedCaps, "ALL") {
		t.Fatalf("runtime = %#v", runtime)
	}
}

func TestInspectVolumeReturnsOwnershipFacts(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			if request.URL.Path != "/volumes/docklane-probe-run" {
				t.Fatalf("path = %q", request.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`{"Name":"docklane-probe-run","Driver":"local","Scope":"local"}`,
				)),
			}, nil
		},
	)}}
	volume, err := client.InspectVolume(context.Background(), "docklane-probe-run")
	if err != nil {
		t.Fatal(err)
	}
	if volume.Name != "docklane-probe-run" ||
		volume.Driver != "local" ||
		volume.Scope != "local" {
		t.Fatalf("volume = %#v", volume)
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func TestDetectActiveTraefikGateway(t *testing.T) {
	if role := detectSystemRole(
		"docklane:local",
		map[string]string{},
		false,
		`/usr/local/bin/docklane probe serve --socket=/run/docklane-probe/probe.sock`,
	); role != SystemRoleProbe {
		t.Fatalf("legacy probe role = %q, want %q", role, SystemRoleProbe)
	}
	if role := detectSystemRole(
		"docklane:local",
		map[string]string{},
		false,
		`/usr/local/bin/docklane serve --listen=0.0.0.0:4646`,
	); role != SystemRoleController {
		t.Fatalf("legacy controller role = %q, want %q", role, SystemRoleController)
	}
	if role := detectSystemRole(
		"docklane:local",
		map[string]string{InstallRoleLabel: "probe"},
		false,
	); role != SystemRoleProbe {
		t.Fatalf("labeled probe role = %q, want %q", role, SystemRoleProbe)
	}
	if role := detectSystemRole(
		"docklane:local",
		map[string]string{InstallRoleLabel: "controller"},
		false,
	); role != SystemRoleController {
		t.Fatalf("labeled controller role = %q, want %q", role, SystemRoleController)
	}
	if role := detectSystemRole(
		"traefik:v3.7",
		map[string]string{"org.opencontainers.image.title": "Traefik"},
		true,
	); role != SystemRoleReverseProxy {
		t.Fatalf("role = %q, want %q", role, SystemRoleReverseProxy)
	}
	if role := detectSystemRole(
		"traefik:v3.7",
		map[string]string{"org.opencontainers.image.title": "Traefik"},
		false,
	); role != "" {
		t.Fatalf("unpublished Traefik role = %q, want empty", role)
	}
	if role := detectSystemRole(
		"example/web:latest",
		map[string]string{"traefik.enable": "true"},
		true,
	); role != "" {
		t.Fatalf("ordinary application role = %q, want empty", role)
	}
}

func TestValidateApplicationTargetRejectsReverseProxy(t *testing.T) {
	err := ValidateApplicationTarget(Container{
		Name:       "traefik",
		SystemRole: SystemRoleReverseProxy,
	})
	if !errors.Is(err, ErrSystemTarget) {
		t.Fatalf("target error = %v, want ErrSystemTarget", err)
	}
}

func TestValidateApplicationTargetRejectsSystemRoleAndOptOut(t *testing.T) {
	if err := ValidateApplicationTarget(Container{
		Name:       "docklane-probe",
		SystemRole: SystemRoleProbe,
	}); !errors.Is(err, ErrSystemTarget) {
		t.Fatalf("probe target error = %v, want ErrSystemTarget", err)
	}
	if err := ValidateApplicationTarget(Container{
		Name:   "postgres",
		Labels: map[string]string{RouteEnabledLabel: "FALSE"},
	}); !errors.Is(err, ErrRouteDisabled) {
		t.Fatalf("opt-out target error = %v, want ErrRouteDisabled", err)
	}
}

func TestConnectNetworkUsesDockerAPI(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			if request.Method != http.MethodPost ||
				request.URL.Path != "/networks/proxy/connect" {
				t.Fatalf("request = %s %s", request.Method, request.URL.Path)
			}
			var payload struct {
				Container string `json:"Container"`
				Endpoint  struct {
					Aliases []string `json:"Aliases"`
				} `json:"EndpointConfig"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Container != "abc123" {
				t.Fatalf("container = %q, want abc123", payload.Container)
			}
			if len(payload.Endpoint.Aliases) != 1 ||
				payload.Endpoint.Aliases[0] != "docklane-route-1" {
				t.Fatalf("aliases = %#v, want docklane-route-1", payload.Endpoint.Aliases)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		},
	)}}
	if err := client.ConnectNetwork(
		context.Background(),
		"proxy",
		"abc123",
		[]string{"docklane-route-1"},
	); err != nil {
		t.Fatal(err)
	}
}

func TestConnectNetworkRejectsAlreadyExistingEndpoint(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Status:     "403 Forbidden",
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`{"message":"endpoint with name draw already exists"}`,
				)),
			}, nil
		},
	)}}
	err := client.ConnectNetwork(
		context.Background(),
		"proxy",
		"abc123",
		[]string{"docklane-route-1"},
	)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("connect error = %v, want existing-endpoint error", err)
	}
}

func TestDisconnectNetworkUsesDockerAPIWithoutForce(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			if request.Method != http.MethodPost ||
				request.URL.Path != "/networks/proxy/disconnect" {
				t.Fatalf("request = %s %s", request.Method, request.URL.Path)
			}
			var payload struct {
				Container string `json:"Container"`
				Force     bool   `json:"Force"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Container != "abc123" {
				t.Fatalf("container = %q, want abc123", payload.Container)
			}
			if payload.Force {
				t.Fatal("disconnect unexpectedly forced")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		},
	)}}
	if err := client.DisconnectNetwork(context.Background(), "proxy", "abc123"); err != nil {
		t.Fatal(err)
	}
}

func TestContainerHasNetwork(t *testing.T) {
	container := Container{Networks: []string{"bridge", "proxy"}}
	if !container.HasNetwork("proxy") || container.HasNetwork("missing") {
		t.Fatalf("unexpected network lookup for %#v", container.Networks)
	}
}

func TestInspectNetworkReadsOwnershipMetadata(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			if request.Method != http.MethodGet ||
				request.URL.Path != "/networks/proxy" {
				t.Fatalf("request = %s %s", request.Method, request.URL.Path)
			}
			body := `{
				"Id":"network123",
				"Name":"proxy",
				"Driver":"bridge",
				"Scope":"local",
				"Labels":{
					"com.docklane.managed":"true",
					"com.docklane.role":"proxy",
					"com.docklane.schema":"1"
				}
			}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		},
	)}}
	network, err := client.InspectNetwork(context.Background(), "proxy")
	if err != nil {
		t.Fatal(err)
	}
	if network.ID != "network123" ||
		network.Labels[NetworkRoleLabel] != NetworkRoleProxy {
		t.Fatalf("network = %#v", network)
	}
}

func TestCreateNetworkUsesManagedLabelsAndInspectsResult(t *testing.T) {
	requests := 0
	client := &Client{http: &http.Client{Transport: roundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			requests++
			if request.Method == http.MethodPost {
				var payload struct {
					Name           string            `json:"Name"`
					Driver         string            `json:"Driver"`
					CheckDuplicate bool              `json:"CheckDuplicate"`
					Labels         map[string]string `json:"Labels"`
				}
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if payload.Name != "docklane-proxy" ||
					payload.Driver != "bridge" ||
					!payload.CheckDuplicate ||
					payload.Labels[NetworkManagedLabel] != "true" {
					t.Fatalf("create payload = %#v", payload)
				}
				return &http.Response{
					StatusCode: http.StatusCreated,
					Status:     "201 Created",
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"Id":"network123"}`)),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{
					"Id":"network123",
					"Name":"docklane-proxy",
					"Driver":"bridge",
					"Scope":"local",
					"Labels":{"com.docklane.managed":"true"}
				}`)),
			}, nil
		},
	)}}
	network, err := client.CreateNetwork(
		context.Background(),
		"docklane-proxy",
		ManagedProxyNetworkLabels(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || network.ID != "network123" {
		t.Fatalf("requests = %d, network = %#v", requests, network)
	}
}
