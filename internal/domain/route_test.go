package domain

import "testing"

func TestRouteValidate(t *testing.T) {
	valid := Route{
		Name:   "excalidraw",
		Port:   80,
		Scheme: "http",
		Selector: ContainerSelector{
			ComposeProject: "excalidraw",
			ComposeService: "excalidraw",
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid route rejected: %v", err)
	}

	invalid := valid
	invalid.Name = "Not Valid"
	if err := invalid.Validate(); err == nil {
		t.Fatal("invalid route name accepted")
	}
}

func TestRouteHostname(t *testing.T) {
	route := Route{Name: "excalidraw"}
	if got, want := route.Hostname("docker.home.arpa"), "excalidraw.docker.home.arpa"; got != want {
		t.Fatalf("Hostname() = %q, want %q", got, want)
	}
}
