package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"docklane.local/docklane/internal/domain"
)

func TestCreateAndListRoute(t *testing.T) {
	repository, err := Open(filepath.Join(t.TempDir(), "docklane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	created, err := repository.CreateRoute(context.Background(), domain.Route{
		Name: "draw",
		Selector: domain.ContainerSelector{
			ComposeProject: "draw",
			ComposeService: "web",
		},
		Port:    80,
		Scheme:  "http",
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	routes, err := repository.ListRoutes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].ID != created.ID || !routes[0].Enabled {
		t.Fatalf("unexpected routes: %#v", routes)
	}

	created.Name = "canvas"
	created.Port = 8080
	created.Enabled = false
	updated, err := repository.UpdateRoute(context.Background(), created)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "canvas" || updated.Port != 8080 || updated.Enabled {
		t.Fatalf("unexpected updated route: %#v", updated)
	}

	if err := repository.DeleteRoute(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetRoute(context.Background(), created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted route error = %v, want ErrNotFound", err)
	}
}

func TestRouteNameConflict(t *testing.T) {
	repository, err := Open(filepath.Join(t.TempDir(), "docklane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	route := domain.Route{
		Name:     "draw",
		Selector: domain.ContainerSelector{ContainerID: "abc"},
		Port:     80,
		Scheme:   "http",
		Enabled:  true,
	}
	if _, err := repository.CreateRoute(context.Background(), route); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateRoute(context.Background(), route); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate error = %v, want ErrConflict", err)
	}
}
