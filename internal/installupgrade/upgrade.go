package installupgrade

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installmanifest"
)

const PlanSchemaVersion = 1

type Operation struct {
	Action            string `json:"action"`
	FromSchemaVersion int    `json:"fromSchemaVersion"`
	ToSchemaVersion   int    `json:"toSchemaVersion"`
	BackupPath        string `json:"backupPath"`
	Reason            string `json:"reason"`
}

type Plan struct {
	SchemaVersion     int                      `json:"schemaVersion"`
	Token             string                   `json:"token"`
	Ready             bool                     `json:"ready"`
	Current           bool                     `json:"current"`
	ManifestPath      string                   `json:"manifestPath"`
	InstallationID    string                   `json:"installationId"`
	Generation        uint64                   `json:"generation"`
	State             domain.InstallationState `json:"state"`
	FromSchemaVersion int                      `json:"fromSchemaVersion"`
	ToSchemaVersion   int                      `json:"toSchemaVersion"`
	SourceFingerprint string                   `json:"sourceFingerprint"`
	Operations        []Operation              `json:"operations"`
	Blockers          []string                 `json:"blockers,omitempty"`
}

type Runner struct {
	store *installmanifest.Store
	now   func() time.Time
}

func New(store *installmanifest.Store) (*Runner, error) {
	if store == nil {
		return nil, errors.New("installation manifest store is required")
	}
	return &Runner{store: store, now: time.Now}, nil
}

func (runner *Runner) Plan(ctx context.Context) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	source, err := runner.store.LoadForUpgrade()
	if err != nil {
		return Plan{}, err
	}
	manifest := source.Manifest
	plan := Plan{
		SchemaVersion:     PlanSchemaVersion,
		Ready:             true,
		Current:           manifest.SchemaVersion == domain.InstallationManifestSchemaVersion,
		ManifestPath:      runner.store.Path(),
		InstallationID:    manifest.InstallationID,
		Generation:        manifest.Generation,
		State:             manifest.State,
		FromSchemaVersion: manifest.SchemaVersion,
		ToSchemaVersion:   domain.InstallationManifestSchemaVersion,
		SourceFingerprint: source.Fingerprint,
		Operations:        []Operation{},
	}
	if manifest.SchemaVersion < domain.InstallationManifestSchemaVersion {
		if manifest.SchemaVersion != 1 {
			plan.Blockers = append(
				plan.Blockers,
				fmt.Sprintf(
					"no migration path exists from schema v%d to v%d",
					manifest.SchemaVersion,
					domain.InstallationManifestSchemaVersion,
				),
			)
		} else {
			plan.Operations = append(plan.Operations, Operation{
				Action:            "migrate-manifest",
				FromSchemaVersion: manifest.SchemaVersion,
				ToSchemaVersion:   domain.InstallationManifestSchemaVersion,
				BackupPath: runner.store.UpgradeBackupPath(
					manifest.SchemaVersion,
					manifest.Generation,
				),
				Reason: "record an auditable upgrade ledger for future installation schema changes",
			})
		}
	}
	if !plan.Current &&
		manifest.State != domain.InstallationInstalled &&
		manifest.State != domain.InstallationRolledBack {
		plan.Blockers = append(
			plan.Blockers,
			fmt.Sprintf(
				"manifest state %s is not terminal; use the Docklane binary that "+
					"created its journal to resume installation or rollback before upgrading",
				manifest.State,
			),
		)
	}
	plan.Ready = len(plan.Blockers) == 0
	plan.Token = planToken(plan)
	return plan, nil
}

func (runner *Runner) Apply(
	ctx context.Context,
	expectedToken string,
) (domain.InstallationManifest, error) {
	if err := ctx.Err(); err != nil {
		return domain.InstallationManifest{}, err
	}
	plan, err := runner.Plan(ctx)
	if err != nil {
		return domain.InstallationManifest{}, err
	}
	if !tokenMatches(plan.Token, expectedToken) {
		return domain.InstallationManifest{}, errors.New(
			"upgrade token does not match the fresh reviewed plan",
		)
	}
	if plan.Current {
		return domain.InstallationManifest{}, errors.New(
			"installation manifest is already at the current schema",
		)
	}
	if !plan.Ready {
		return domain.InstallationManifest{}, fmt.Errorf(
			"installation manifest cannot be upgraded: %s",
			plan.Blockers[0],
		)
	}
	source, err := runner.store.LoadForUpgrade()
	if err != nil {
		return domain.InstallationManifest{}, err
	}
	if source.Manifest.InstallationID != plan.InstallationID ||
		source.Manifest.Generation != plan.Generation ||
		source.Manifest.SchemaVersion != plan.FromSchemaVersion ||
		source.Fingerprint != plan.SourceFingerprint {
		return domain.InstallationManifest{}, errors.New(
			"installation manifest changed after upgrade review",
		)
	}
	appliedAt := runner.now().UTC()
	if !appliedAt.After(source.Manifest.UpdatedAt) {
		appliedAt = source.Manifest.UpdatedAt.Add(time.Nanosecond)
	}
	upgraded := source.Manifest
	upgraded.SchemaVersion = domain.InstallationManifestSchemaVersion
	upgraded.Generation++
	upgraded.UpdatedAt = appliedAt
	upgraded.UpgradeHistory = append(
		append(
			[]domain.InstallationUpgradeRecord(nil),
			source.Manifest.UpgradeHistory...,
		),
		domain.InstallationUpgradeRecord{
			FromSchemaVersion: source.Manifest.SchemaVersion,
			ToSchemaVersion:   domain.InstallationManifestSchemaVersion,
			AppliedAt:         appliedAt,
			SourceBackup: domain.ResourceBackup{
				Path: runner.store.UpgradeBackupPath(
					source.Manifest.SchemaVersion,
					source.Manifest.Generation,
				),
				Fingerprint: source.Fingerprint,
			},
		},
	)
	if err := runner.store.ApplyUpgrade(
		source.Manifest.Generation,
		source.Manifest.SchemaVersion,
		source.Fingerprint,
		upgraded,
	); err != nil {
		return domain.InstallationManifest{}, err
	}
	return upgraded, nil
}

func planToken(plan Plan) string {
	payload, _ := json.Marshal(struct {
		SchemaVersion     int
		ManifestPath      string
		InstallationID    string
		Generation        uint64
		State             domain.InstallationState
		FromSchemaVersion int
		ToSchemaVersion   int
		SourceFingerprint string
		Operations        []Operation
		Blockers          []string
	}{
		SchemaVersion:     plan.SchemaVersion,
		ManifestPath:      plan.ManifestPath,
		InstallationID:    plan.InstallationID,
		Generation:        plan.Generation,
		State:             plan.State,
		FromSchemaVersion: plan.FromSchemaVersion,
		ToSchemaVersion:   plan.ToSchemaVersion,
		SourceFingerprint: plan.SourceFingerprint,
		Operations:        plan.Operations,
		Blockers:          plan.Blockers,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func tokenMatches(expected, actual string) bool {
	if len(expected) != sha256.Size*2 || len(actual) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare(
		[]byte(expected),
		[]byte(actual),
	) == 1
}
