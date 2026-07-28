package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"docklane.local/docklane/internal/domain"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type migration struct {
	version     int
	description string
	statements  []string
}

var migrations = []migration{
	{
		version:     1,
		description: "create routes table",
		statements: []string{`
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
			)
		`},
	},
	{
		version:     2,
		description: "add route revision",
		statements: []string{
			`ALTER TABLE routes ADD COLUMN revision INTEGER NOT NULL DEFAULT 1`,
		},
	},
	{
		version:     3,
		description: "track managed network attachments",
		statements: []string{`
			CREATE TABLE network_attachments (
				container_id TEXT NOT NULL,
				network TEXT NOT NULL,
				created_at TEXT NOT NULL,
				PRIMARY KEY (container_id, network)
			)
		`},
	},
	{
		version:     4,
		description: "persist last-known-good Traefik provider configuration",
		statements: []string{`
			CREATE TABLE provider_snapshots (
				id INTEGER PRIMARY KEY CHECK (id = 1),
				configuration BLOB NOT NULL,
				fingerprint TEXT NOT NULL,
				generated_at TEXT NOT NULL
			)
		`},
	},
	{
		version:     5,
		description: "add bounded route health history",
		statements: []string{
			`CREATE TABLE health_snapshots (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				route_id INTEGER NOT NULL,
				status TEXT NOT NULL,
				report BLOB NOT NULL,
				recorded_at TEXT NOT NULL
			)`,
			`CREATE INDEX health_snapshots_route_recorded
			 ON health_snapshots (route_id, recorded_at DESC, id DESC)`,
		},
	},
}

var (
	ErrNotFound                 = errors.New("route not found")
	ErrConflict                 = errors.New("route name already exists")
	ErrRevisionConflict         = errors.New("route was changed by another client; refresh it and try again")
	ErrProviderSnapshotNotFound = errors.New("provider snapshot not found")
)

func Open(path string) (*Store, error) {
	databaseExisted, err := existingDatabase(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	backupPath, err := store.migrate(context.Background(), path, databaseExisted)
	if err != nil {
		db.Close()
		return nil, err
	}
	if backupPath != "" {
		log.Printf("Docklane database backup created at %s", backupPath)
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func existingDatabase(path string) (bool, error) {
	info, err := os.Stat(path)
	switch {
	case err == nil:
		return info.Size() > 0, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

func (s *Store) migrate(
	ctx context.Context,
	path string,
	databaseExisted bool,
) (string, error) {
	storedVersion, err := s.schemaVersion(ctx)
	if err != nil {
		return "", err
	}
	latestVersion := migrations[len(migrations)-1].version
	if storedVersion > latestVersion {
		return "", fmt.Errorf(
			"database schema version %d is newer than supported version %d",
			storedVersion,
			latestVersion,
		)
	}

	currentVersion := storedVersion
	needsBaselineStamp := false
	if storedVersion == 0 {
		currentVersion, err = s.inferLegacyVersion(ctx)
		if err != nil {
			return "", err
		}
		needsBaselineStamp = currentVersion > 0
	}

	needsMigration := currentVersion < latestVersion
	backupPath := ""
	if databaseExisted && (needsBaselineStamp || needsMigration) {
		backupPath, err = s.backup(ctx, path, storedVersion, latestVersion)
		if err != nil {
			return "", fmt.Errorf("back up database before migration: %w", err)
		}
	}

	if needsBaselineStamp {
		if err := s.setSchemaVersion(ctx, currentVersion); err != nil {
			return backupPath, fmt.Errorf("record legacy schema version: %w", err)
		}
	}
	for _, item := range migrations {
		if item.version <= currentVersion {
			continue
		}
		if err := s.applyMigration(ctx, item); err != nil {
			return backupPath, fmt.Errorf(
				"apply schema migration %d (%s): %w",
				item.version,
				item.description,
				err,
			)
		}
		currentVersion = item.version
	}
	return backupPath, nil
}

func (s *Store) schemaVersion(ctx context.Context) (int, error) {
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read database schema version: %w", err)
	}
	return version, nil
}

func (s *Store) inferLegacyVersion(ctx context.Context) (int, error) {
	var routesTable bool
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT EXISTS (
			SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'routes'
		)`,
	).Scan(&routesTable); err != nil {
		return 0, fmt.Errorf("inspect legacy routes table: %w", err)
	}
	if !routesTable {
		return 0, nil
	}

	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(routes)`)
	if err != nil {
		return 0, err
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
			return 0, err
		}
		if name == "revision" {
			return 2, nil
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return 1, nil
}

func (s *Store) applyMigration(ctx context.Context, item migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range item.statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(
		ctx,
		fmt.Sprintf("PRAGMA user_version = %d", item.version),
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) setSchemaVersion(ctx context.Context, version int) error {
	_, err := s.db.ExecContext(
		ctx,
		fmt.Sprintf("PRAGMA user_version = %d", version),
	)
	return err
}

func (s *Store) backup(
	ctx context.Context,
	path string,
	fromVersion int,
	toVersion int,
) (string, error) {
	databaseInfo, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(filepath.Dir(path), "backups")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	timestamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	backupPath := filepath.Join(
		directory,
		fmt.Sprintf(
			"%s-v%d-before-v%d-%s.db",
			base,
			fromVersion,
			toVersion,
			timestamp,
		),
	)
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, backupPath); err != nil {
		return "", err
	}
	if ownership, ok := databaseInfo.Sys().(*syscall.Stat_t); ok {
		if err := os.Chown(backupPath, int(ownership.Uid), int(ownership.Gid)); err != nil {
			return "", err
		}
	}
	if err := os.Chmod(backupPath, 0o600); err != nil {
		return "", err
	}
	return backupPath, nil
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM health_snapshots WHERE route_id = ?`,
		id,
	); err != nil {
		return fmt.Errorf("delete route health history: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM routes WHERE id = ?`, id)
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
	return tx.Commit()
}

func (s *Store) RecordNetworkAttachment(
	ctx context.Context,
	attachment domain.NetworkAttachment,
) error {
	if attachment.ContainerID == "" || attachment.Network == "" {
		return fmt.Errorf("container ID and network are required")
	}
	if attachment.CreatedAt.IsZero() {
		attachment.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO network_attachments (container_id, network, created_at)
		VALUES (?, ?, ?)
		ON CONFLICT (container_id, network) DO NOTHING
	`,
		attachment.ContainerID,
		attachment.Network,
		attachment.CreatedAt.Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) ListNetworkAttachments(
	ctx context.Context,
) ([]domain.NetworkAttachment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT container_id, network, created_at
		FROM network_attachments
		ORDER BY network, container_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var attachments []domain.NetworkAttachment
	for rows.Next() {
		var attachment domain.NetworkAttachment
		var createdAt string
		if err := rows.Scan(
			&attachment.ContainerID,
			&attachment.Network,
			&createdAt,
		); err != nil {
			return nil, err
		}
		attachment.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, rows.Err()
}

func (s *Store) DeleteNetworkAttachment(
	ctx context.Context,
	containerID string,
	network string,
) error {
	_, err := s.db.ExecContext(
		ctx,
		`DELETE FROM network_attachments WHERE container_id = ? AND network = ?`,
		containerID,
		network,
	)
	return err
}

func (s *Store) SaveProviderSnapshot(
	ctx context.Context,
	snapshot domain.ProviderSnapshot,
) (domain.ProviderSnapshot, error) {
	if len(snapshot.Configuration) == 0 ||
		snapshot.Fingerprint == "" ||
		snapshot.GeneratedAt.IsZero() {
		return domain.ProviderSnapshot{}, errors.New(
			"provider configuration, fingerprint, and generation time are required",
		)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO provider_snapshots (
			id, configuration, fingerprint, generated_at
		) VALUES (1, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			configuration = excluded.configuration,
			fingerprint = excluded.fingerprint,
			generated_at = excluded.generated_at
		WHERE provider_snapshots.fingerprint <> excluded.fingerprint
	`,
		snapshot.Configuration,
		snapshot.Fingerprint,
		snapshot.GeneratedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return domain.ProviderSnapshot{}, err
	}
	return s.GetProviderSnapshot(ctx)
}

func (s *Store) GetProviderSnapshot(
	ctx context.Context,
) (domain.ProviderSnapshot, error) {
	var snapshot domain.ProviderSnapshot
	var generatedAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT configuration, fingerprint, generated_at
		FROM provider_snapshots
		WHERE id = 1
	`).Scan(
		&snapshot.Configuration,
		&snapshot.Fingerprint,
		&generatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ProviderSnapshot{}, ErrProviderSnapshotNotFound
	}
	if err != nil {
		return domain.ProviderSnapshot{}, err
	}
	snapshot.GeneratedAt, err = time.Parse(time.RFC3339Nano, generatedAt)
	if err != nil {
		return domain.ProviderSnapshot{}, fmt.Errorf(
			"parse provider snapshot generation time: %w",
			err,
		)
	}
	return snapshot, nil
}

func (s *Store) SaveHealthSnapshot(
	ctx context.Context,
	snapshot domain.HealthSnapshot,
	retention int,
) (domain.HealthSnapshot, error) {
	if snapshot.RouteID <= 0 {
		return domain.HealthSnapshot{}, errors.New("health snapshot route ID is required")
	}
	if retention <= 0 {
		return domain.HealthSnapshot{}, errors.New("health snapshot retention must be positive")
	}
	switch snapshot.Report.Status {
	case domain.DiagnosticPass, domain.DiagnosticWarn, domain.DiagnosticFail:
	default:
		return domain.HealthSnapshot{}, errors.New("health snapshot status is invalid")
	}
	if snapshot.RecordedAt.IsZero() {
		snapshot.RecordedAt = time.Now().UTC()
	}
	snapshot.Status = snapshot.Report.Status
	encoded, err := json.Marshal(snapshot.Report)
	if err != nil {
		return domain.HealthSnapshot{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.HealthSnapshot{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO health_snapshots (route_id, status, report, recorded_at)
		VALUES (?, ?, ?, ?)
	`,
		snapshot.RouteID,
		snapshot.Status,
		encoded,
		snapshot.RecordedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return domain.HealthSnapshot{}, err
	}
	snapshot.ID, err = result.LastInsertId()
	if err != nil {
		return domain.HealthSnapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM health_snapshots
		WHERE route_id = ? AND id NOT IN (
			SELECT id
			FROM health_snapshots
			WHERE route_id = ?
			ORDER BY recorded_at DESC, id DESC
			LIMIT ?
		)
	`, snapshot.RouteID, snapshot.RouteID, retention); err != nil {
		return domain.HealthSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.HealthSnapshot{}, err
	}
	return snapshot, nil
}

func (s *Store) ListHealthSnapshots(
	ctx context.Context,
	routeID int64,
	limit int,
) ([]domain.HealthSnapshot, error) {
	if routeID <= 0 || limit <= 0 {
		return nil, errors.New("route ID and history limit must be positive")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, status, report, recorded_at
		FROM health_snapshots
		WHERE route_id = ?
		ORDER BY recorded_at DESC, id DESC
		LIMIT ?
	`, routeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	snapshots := []domain.HealthSnapshot{}
	for rows.Next() {
		var snapshot domain.HealthSnapshot
		var encoded []byte
		var recordedAt string
		snapshot.RouteID = routeID
		if err := rows.Scan(
			&snapshot.ID,
			&snapshot.Status,
			&encoded,
			&recordedAt,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(encoded, &snapshot.Report); err != nil {
			return nil, fmt.Errorf("decode health snapshot %d: %w", snapshot.ID, err)
		}
		snapshot.RecordedAt, err = time.Parse(time.RFC3339Nano, recordedAt)
		if err != nil {
			return nil, fmt.Errorf("parse health snapshot %d time: %w", snapshot.ID, err)
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, rows.Err()
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
