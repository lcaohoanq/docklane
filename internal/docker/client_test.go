package docker

import (
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
