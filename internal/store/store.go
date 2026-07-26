package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"docklane.local/docklane/internal/domain"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

var (
	ErrNotFound         = errors.New("route not found")
	ErrConflict         = errors.New("route name already exists")
	ErrRevisionConflict = errors.New("route was changed by another client; refresh it and try again")
)

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS routes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			revision INTEGER NOT NULL DEFAULT 1,
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
	`); err != nil {
		return err
	}

	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(routes)`)
	if err != nil {
		return err
	}
	hasRevision := false
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
			rows.Close()
			return err
		}
		if name == "revision" {
			hasRevision = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if hasRevision {
		return nil
	}
	_, err = s.db.ExecContext(
		ctx,
		`ALTER TABLE routes ADD COLUMN revision INTEGER NOT NULL DEFAULT 1`,
	)
	return err
}

func (s *Store) ListRoutes(ctx context.Context) ([]domain.Route, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, revision, name, compose_project, compose_service, container_id,
		       port, scheme, enabled, created_at, updated_at
		FROM routes
		ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var routes []domain.Route
	for rows.Next() {
		route, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		routes = append(routes, route)
	}
	return routes, rows.Err()
}

func (s *Store) GetRoute(ctx context.Context, id int64) (domain.Route, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, revision, name, compose_project, compose_service, container_id,
		       port, scheme, enabled, created_at, updated_at
		FROM routes
		WHERE id = ?
	`, id)
	route, err := scanRoute(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Route{}, ErrNotFound
	}
	return route, err
}

func (s *Store) CreateRoute(ctx context.Context, route domain.Route) (domain.Route, error) {
	if err := route.Validate(); err != nil {
		return domain.Route{}, err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO routes (
			name, compose_project, compose_service, container_id,
			port, scheme, enabled, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		route.Name,
		route.Selector.ComposeProject,
		route.Selector.ComposeService,
		route.Selector.ContainerID,
		route.Port,
		route.Scheme,
		route.Enabled,
		now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return domain.Route{}, ErrConflict
		}
		return domain.Route{}, fmt.Errorf("create route: %w", err)
	}
	route.ID, err = result.LastInsertId()
	route.Revision = 1
	route.CreatedAt = now
	route.UpdatedAt = now
	return route, err
}

func (s *Store) UpdateRoute(ctx context.Context, route domain.Route) (domain.Route, error) {
	if route.ID <= 0 {
		return domain.Route{}, ErrNotFound
	}
	if err := route.Validate(); err != nil {
		return domain.Route{}, err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE routes
		SET name = ?, compose_project = ?, compose_service = ?,
		    container_id = ?, port = ?, scheme = ?, enabled = ?,
		    revision = revision + 1, updated_at = ?
		WHERE id = ? AND revision = ?
	`,
		route.Name,
		route.Selector.ComposeProject,
		route.Selector.ComposeService,
		route.Selector.ContainerID,
		route.Port,
		route.Scheme,
		route.Enabled,
		now.Format(time.RFC3339Nano),
		route.ID,
		route.Revision,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return domain.Route{}, ErrConflict
		}
		return domain.Route{}, fmt.Errorf("update route: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return domain.Route{}, err
	}
	if affected == 0 {
		if _, getErr := s.GetRoute(ctx, route.ID); errors.Is(getErr, ErrNotFound) {
			return domain.Route{}, ErrNotFound
		} else if getErr != nil {
			return domain.Route{}, getErr
		}
		return domain.Route{}, ErrRevisionConflict
	}
	return s.GetRoute(ctx, route.ID)
}

func (s *Store) DeleteRoute(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM routes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete route: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

type scanner interface {
	Scan(...any) error
}

func scanRoute(source scanner) (domain.Route, error) {
	var route domain.Route
	var port int
	var enabled bool
	var createdAt, updatedAt string
	if err := source.Scan(
		&route.ID,
		&route.Revision,
		&route.Name,
		&route.Selector.ComposeProject,
		&route.Selector.ComposeService,
		&route.Selector.ContainerID,
		&port,
		&route.Scheme,
		&enabled,
		&createdAt,
		&updatedAt,
	); err != nil {
		return domain.Route{}, err
	}
	if port < 1 || port > 65535 {
		return domain.Route{}, fmt.Errorf("stored route %d has invalid port %d", route.ID, port)
	}
	route.Port = uint16(port)
	route.Enabled = enabled
	var err error
	route.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return domain.Route{}, fmt.Errorf("parse route created time: %w", err)
	}
	route.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return domain.Route{}, fmt.Errorf("parse route updated time: %w", err)
	}
	return route, nil
}

func isUniqueConstraint(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed: routes.name")
}
