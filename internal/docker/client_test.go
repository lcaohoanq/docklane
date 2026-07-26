package docker

import (
	"errors"
	"testing"

	"docklane.local/docklane/internal/domain"
)

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
