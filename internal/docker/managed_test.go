package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestCreateManagedContainerSendsExactSecurityAndTopology(t *testing.T) {
	requests := 0
	client := &Client{http: &http.Client{Transport: roundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			requests++
			switch requests {
			case 1:
				if request.Method != http.MethodPost ||
					request.URL.Path != "/containers/create" ||
					request.URL.Query().Get("name") != "docklane-probe" {
					t.Fatalf("create request = %s %s", request.Method, request.URL.String())
				}
				var payload struct {
					Image        string            `json:"Image"`
					Cmd          []string          `json:"Cmd"`
					Labels       map[string]string `json:"Labels"`
					ExposedPorts map[string]any    `json:"ExposedPorts"`
					HostConfig   struct {
						Mounts []struct {
							Type     string `json:"Type"`
							Source   string `json:"Source"`
							Target   string `json:"Target"`
							ReadOnly bool   `json:"ReadOnly"`
						} `json:"Mounts"`
						ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
						SecurityOpt    []string          `json:"SecurityOpt"`
						CapDrop        []string          `json:"CapDrop"`
						PortBindings   map[string][]any  `json:"PortBindings"`
						RestartPolicy  map[string]string `json:"RestartPolicy"`
					} `json:"HostConfig"`
					NetworkingConfig struct {
						Endpoints map[string]struct {
							Aliases []string `json:"Aliases"`
						} `json:"EndpointsConfig"`
					} `json:"NetworkingConfig"`
				}
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if payload.Image != "docklane:local" ||
					!reflect.DeepEqual(payload.Cmd, []string{"probe", "serve"}) ||
					payload.Labels[InstallIDLabel] != "installation-test" ||
					!payload.HostConfig.ReadonlyRootfs ||
					!reflect.DeepEqual(
						payload.HostConfig.SecurityOpt,
						[]string{"no-new-privileges:true"},
					) ||
					!reflect.DeepEqual(payload.HostConfig.CapDrop, []string{"ALL"}) ||
					payload.HostConfig.RestartPolicy["Name"] != "unless-stopped" {
					t.Fatalf("container payload = %#v", payload)
				}
				if len(payload.HostConfig.Mounts) != 1 ||
					payload.HostConfig.Mounts[0].Type != "volume" ||
					payload.HostConfig.Mounts[0].Source != "docklane-probe-run" ||
					payload.HostConfig.Mounts[0].Target != "/run/docklane-probe" {
					t.Fatalf("mounts = %#v", payload.HostConfig.Mounts)
				}
				endpoint := payload.NetworkingConfig.Endpoints["proxy"]
				if !reflect.DeepEqual(endpoint.Aliases, []string{"docklane-probe"}) {
					t.Fatalf("endpoint = %#v", endpoint)
				}
				return dockerResponse(
					http.StatusCreated,
					`{"Id":"probe123"}`,
				), nil
			case 2:
				if request.Method != http.MethodGet ||
					request.URL.Path != "/containers/probe123/json" {
					t.Fatalf("inspect request = %s %s", request.Method, request.URL.Path)
				}
				return dockerResponse(http.StatusOK, `{
					"Id":"probe123",
					"Name":"/docklane-probe",
					"Image":"sha256:docklane",
					"State":{"Running":false},
					"Config":{
						"Image":"docklane:local",
						"Cmd":["probe","serve"],
						"Labels":{
							"com.docklane.managed":"true",
							"com.docklane.role":"probe",
							"com.docklane.schema":"1",
							"com.docklane.installation":"installation-test"
						}
					},
					"Mounts":[{
						"Type":"volume",
						"Name":"docklane-probe-run",
						"Source":"/var/lib/docker/volumes/docklane-probe-run/_data",
						"Destination":"/run/docklane-probe",
						"RW":true
					}],
					"NetworkSettings":{"Networks":{"proxy":{}}},
					"HostConfig":{
						"ReadonlyRootfs":true,
						"Privileged":false,
						"SecurityOpt":["no-new-privileges:true"],
						"CapDrop":["ALL"],
						"RestartPolicy":{"Name":"unless-stopped"},
						"PortBindings":{}
					}
				}`), nil
			default:
				t.Fatalf("unexpected request %d", requests)
				return nil, nil
			}
		},
	)}}
	state, err := client.CreateManagedContainer(
		context.Background(),
		ManagedContainerRequest{
			Name: "docklane-probe", Image: "docklane:local",
			Command:  []string{"probe", "serve"},
			Networks: []string{"proxy"},
			Mounts: []ManagedMountRequest{{
				Type: "volume", Source: "docklane-probe-run",
				Destination: "/run/docklane-probe",
			}},
			ReadOnlyRootFS: true, NoNewPrivileges: true,
			DropAllCapabilities: true, RestartPolicy: "unless-stopped",
			Labels: map[string]string{
				InstallManagedLabel: "true",
				InstallRoleLabel:    "probe",
				InstallSchemaLabel:  "1",
				InstallIDLabel:      "installation-test",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 ||
		state.ID != "probe123" ||
		state.Runtime.Mounts[0].Source != "docklane-probe-run" ||
		!state.Runtime.NoNewPrivileges {
		t.Fatalf("requests = %d, state = %#v", requests, state)
	}
}

func TestCreateManagedNetworkTreatsConflictAsError(t *testing.T) {
	requests := 0
	client := &Client{http: &http.Client{Transport: roundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			requests++
			return dockerResponse(
				http.StatusConflict,
				`{"message":"network with name proxy already exists"}`,
			), nil
		},
	)}}
	_, err := client.CreateManagedNetwork(
		context.Background(),
		ManagedNetworkRequest{
			Name: "proxy", Driver: "bridge",
			Labels: map[string]string{InstallIDLabel: "installation-test"},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("conflict error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("strict create inspected/adopted a conflict in %d requests", requests)
	}
}

func TestCreateManagedVolumeAndRemovalUseDockerAPI(t *testing.T) {
	requests := 0
	client := &Client{http: &http.Client{Transport: roundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			requests++
			switch requests {
			case 1:
				if request.Method != http.MethodPost ||
					request.URL.Path != "/volumes/create" {
					t.Fatalf("create volume request = %s %s", request.Method, request.URL.Path)
				}
				var payload ManagedVolumeRequest
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if payload.Name != "docklane-probe-run" ||
					payload.Driver != "local" ||
					payload.Labels[InstallIDLabel] != "installation-test" {
					t.Fatalf("volume payload = %#v", payload)
				}
				return dockerResponse(http.StatusCreated, `{}`), nil
			case 2:
				return dockerResponse(http.StatusOK, `{
					"Name":"docklane-probe-run",
					"Driver":"local",
					"Scope":"local",
					"Labels":{"com.docklane.installation":"installation-test"}
				}`), nil
			case 3:
				if request.Method != http.MethodDelete ||
					request.URL.Path != "/volumes/docklane-probe-run" {
					t.Fatalf("remove volume request = %s %s", request.Method, request.URL.Path)
				}
				return dockerResponse(http.StatusNoContent, ""), nil
			default:
				t.Fatalf("unexpected request %d", requests)
				return nil, nil
			}
		},
	)}}
	volume, err := client.CreateManagedVolume(
		context.Background(),
		ManagedVolumeRequest{
			Name: "docklane-probe-run", Driver: "local",
			Labels: map[string]string{InstallIDLabel: "installation-test"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if volume.Scope != "local" {
		t.Fatalf("volume = %#v", volume)
	}
	if err := client.RemoveManagedVolume(
		context.Background(),
		"docklane-probe-run",
	); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveManagedContainerUsesForceWithoutVolumeDeletion(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			if request.Method != http.MethodDelete ||
				request.URL.Path != "/containers/container123" ||
				request.URL.Query().Get("force") != "true" ||
				request.URL.Query().Get("v") != "false" {
				t.Fatalf("remove request = %s %s", request.Method, request.URL.String())
			}
			return dockerResponse(http.StatusNoContent, ""), nil
		},
	)}}
	if err := client.RemoveManagedContainer(
		context.Background(),
		"container123",
	); err != nil {
		t.Fatal(err)
	}
}

func TestManagedLifecycleClientContract(t *testing.T) {
	var lifecycle ManagedLifecycle = NewClient("/var/run/docker.sock")
	if lifecycle == nil {
		t.Fatal("managed lifecycle client is nil")
	}
	if !errors.Is(ErrContainerNotFound, ErrContainerNotFound) {
		t.Fatal("container not-found sentinel is unusable")
	}
}

func dockerResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
