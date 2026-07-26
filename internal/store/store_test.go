package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
	if version := databaseVersion(t, repository.db); version != 2 {
		t.Fatalf("schema version = %d, want 2", version)
	}

	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "backups", "*.db"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backups = %v, want one pre-migration backup", backups)
	}
	info, err := os.Stat(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if permission := info.Mode().Perm(); permission != 0o600 {
		t.Fatalf("backup permission = %o, want 600", permission)
	}
	databaseInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	databaseOwner := databaseInfo.Sys().(*syscall.Stat_t)
	backupOwner := info.Sys().(*syscall.Stat_t)
	if databaseOwner.Uid != backupOwner.Uid || databaseOwner.Gid != backupOwner.Gid {
		t.Fatalf(
			"backup owner = %d:%d, want database owner %d:%d",
			backupOwner.Uid,
			backupOwner.Gid,
			databaseOwner.Uid,
			databaseOwner.Gid,
		)
	}
	backupDB, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backupDB.Close()
	var name string
	if err := backupDB.QueryRow(`SELECT name FROM routes WHERE id = 1`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "draw" {
		t.Fatalf("backup route name = %q, want draw", name)
	}
	if hasColumn(t, backupDB, "revision") {
		t.Fatal("pre-migration backup unexpectedly contains revision column")
	}
}

func TestOpenAdoptsUnversionedCurrentSchemaWithBackup(t *testing.T) {
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
			updated_at TEXT NOT NULL,
			revision INTEGER NOT NULL DEFAULT 1
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
	if version := databaseVersion(t, repository.db); version != 2 {
		t.Fatalf("schema version = %d, want 2", version)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(path), "backups", "*.db"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backups = %v, want one baseline backup", backups)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	backupsAfterReopen, err := filepath.Glob(filepath.Join(filepath.Dir(path), "backups", "*.db"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backupsAfterReopen) != 1 {
		t.Fatalf("reopen created another backup: %v", backupsAfterReopen)
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "docklane.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = Open(path)
	if err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("Open error = %v, want newer-schema rejection", err)
	}
}

func databaseVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func hasColumn(t *testing.T, db *sql.DB, wanted string) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(routes)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var columnID, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(
			&columnID,
			&name,
			&columnType,
			&notNull,
			&defaultValue,
			&primaryKey,
		); err != nil {
			t.Fatal(err)
		}
		if name == wanted {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return false
}
