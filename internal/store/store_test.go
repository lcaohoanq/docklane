package store

import (
	"context"
	"database/sql"
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
	if created.Revision != 1 {
		t.Fatalf("created revision = %d, want 1", created.Revision)
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
	if updated.Revision != 2 {
		t.Fatalf("updated revision = %d, want 2", updated.Revision)
	}
	created.Name = "stale"
	if _, err := repository.UpdateRoute(context.Background(), created); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale update error = %v, want ErrRevisionConflict", err)
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

func TestOpenAddsRevisionToExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "docklane.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE routes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			compose_project TEXT NOT NULL DEFAULT '',
			compose_service TEXT NOT NULL DEFAULT '',
			container_id TEXT NOT NULL DEFAULT '',
			port INTEGER NOT NULL,
			scheme TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		INSERT INTO routes (
			name, compose_project, compose_service, container_id,
			port, scheme, enabled, created_at, updated_at
		) VALUES (
			'draw', 'draw', 'web', '', 80, 'http', 1,
			'2026-07-26T00:00:00Z', '2026-07-26T00:00:00Z'
		);
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	repository, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	route, err := repository.GetRoute(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if route.Revision != 1 {
		t.Fatalf("migrated revision = %d, want 1", route.Revision)
	}
}
