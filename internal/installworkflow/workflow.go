package installworkflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"docklane.local/docklane/internal/domain"
)

var ErrObservationConflict = errors.New(
	"external state conflicts with the installation journal",
)

type ManifestStore interface {
	Save(uint64, domain.InstallationManifest) error
}

type Disposition string

const (
	DispositionPending    Disposition = "pending"
	DispositionApplied    Disposition = "applied"
	DispositionRolledBack Disposition = "rolled_back"
	DispositionConflict   Disposition = "conflict"
)

type Step struct {
	ID                string
	ResourceID        string
	Target            string
	Stage             domain.InstallationExecutionStage
	IntentFingerprint string
	IntentMode        uint32
	Apply             func(context.Context) (domain.InstallationObservation, error)
	Inspect           func(
		context.Context,
		*domain.InstallationObservation,
	) (Disposition, domain.InstallationObservation, error)
	Rollback func(context.Context, domain.InstallationObservation) error
}

type Runner struct {
	store           ManifestStore
	now             func() time.Time
	rollbackTimeout time.Duration
}

func New(store ManifestStore) (*Runner, error) {
	if store == nil {
		return nil, errors.New("manifest store is required")
	}
	return &Runner{
		store:           store,
		now:             time.Now,
		rollbackTimeout: 30 * time.Second,
	}, nil
}

func (runner *Runner) Run(
	ctx context.Context,
	manifest domain.InstallationManifest,
	steps []Step,
) (domain.InstallationManifest, error) {
	if err := validateSteps(manifest, steps); err != nil {
		return manifest, err
	}
	if err := ctx.Err(); err != nil {
		return manifest, err
	}
	var err error
	if manifest.Execution == nil {
		manifest, err = runner.checkpoint(
			manifest,
			func(next *domain.InstallationManifest) {
				next.State = domain.InstallationApplying
				next.Execution = newExecution(steps)
			},
		)
		if err != nil {
			return manifest, fmt.Errorf("create execution journal: %w", err)
		}
	} else if err := verifyTopology(*manifest.Execution, steps); err != nil {
		return manifest, err
	}
	switch manifest.Execution.Phase {
	case domain.ExecutionForward:
		return runner.runForward(ctx, manifest, steps)
	case domain.ExecutionRollback:
		return runner.runRollback(ctx, manifest, steps)
	case domain.ExecutionComplete, domain.ExecutionRolledBack:
		return manifest, nil
	case domain.ExecutionFailed:
		return manifest, errors.New("installation execution is failed")
	default:
		return manifest, fmt.Errorf(
			"unsupported execution phase %q",
			manifest.Execution.Phase,
		)
	}
}

func (runner *Runner) runForward(
	ctx context.Context,
	manifest domain.InstallationManifest,
	steps []Step,
) (domain.InstallationManifest, error) {
	for index := range steps {
		operation := manifest.Execution.Operations[index]
		switch operation.State {
		case domain.OperationApplied:
			continue
		case domain.OperationPending:
			var err error
			manifest, err = runner.markApplying(manifest, index)
			if err != nil {
				return manifest, err
			}
		case domain.OperationApplying:
			disposition, observation, err := inspect(
				ctx,
				steps[index],
				operation.Observation,
			)
			if err != nil {
				return manifest, fmt.Errorf(
					"inspect interrupted operation %s: %w",
					operation.ID,
					err,
				)
			}
			switch disposition {
			case DispositionApplied:
				manifest, err = runner.markApplied(
					manifest,
					index,
					observation,
				)
				if err != nil {
					return manifest, err
				}
				continue
			case DispositionPending, DispositionRolledBack:
				manifest, err = runner.markApplying(manifest, index)
				if err != nil {
					return manifest, err
				}
			case DispositionConflict:
				return runner.failConflict(
					manifest,
					index,
					"reconcile interrupted apply",
				)
			default:
				return manifest, fmt.Errorf(
					"inspect operation %s returned invalid disposition %q",
					operation.ID,
					disposition,
				)
			}
		default:
			return manifest, fmt.Errorf(
				"operation %s is %s during forward execution",
				operation.ID,
				operation.State,
			)
		}
		observation, applyErr := steps[index].Apply(ctx)
		if applyErr == nil {
			var err error
			manifest, err = runner.markApplied(manifest, index, observation)
			if err != nil {
				return manifest, err
			}
			continue
		}
		reconcileContext, cancel := context.WithTimeout(
			context.Background(),
			runner.rollbackTimeout,
		)
		disposition, inspected, inspectErr := inspect(
			reconcileContext,
			steps[index],
			nil,
		)
		cancel()
		if inspectErr != nil {
			return manifest, errors.Join(
				fmt.Errorf("apply operation %s: %w", operation.ID, applyErr),
				fmt.Errorf("inspect failed apply: %w", inspectErr),
			)
		}
		switch disposition {
		case DispositionApplied:
			var err error
			manifest, err = runner.markApplied(manifest, index, inspected)
			if err != nil {
				return manifest, errors.Join(applyErr, err)
			}
		case DispositionConflict:
			return runner.failConflict(
				manifest,
				index,
				fmt.Sprintf("apply returned an error: %v", applyErr),
			)
		case DispositionPending, DispositionRolledBack:
			// The write-ahead applying state is reconciled during rollback.
		default:
			return manifest, fmt.Errorf(
				"inspect operation %s returned invalid disposition %q",
				operation.ID,
				disposition,
			)
		}
		return runner.beginRollback(manifest, steps, fmt.Errorf(
			"apply operation %s: %w",
			operation.ID,
			applyErr,
		))
	}
	return runner.checkpoint(
		manifest,
		func(next *domain.InstallationManifest) {
			next.State = domain.InstallationInstalled
			next.Execution.Phase = domain.ExecutionComplete
		},
	)
}

func (runner *Runner) beginRollback(
	manifest domain.InstallationManifest,
	steps []Step,
	cause error,
) (domain.InstallationManifest, error) {
	rollingBack, err := runner.checkpoint(
		manifest,
		func(next *domain.InstallationManifest) {
			next.State = domain.InstallationRollingBack
			next.Execution.Phase = domain.ExecutionRollback
		},
	)
	if err != nil {
		return manifest, errors.Join(cause, fmt.Errorf(
			"begin installation rollback: %w",
			err,
		))
	}
	rollbackContext, cancel := context.WithTimeout(
		context.Background(),
		runner.rollbackTimeout,
	)
	defer cancel()
	rolledBack, rollbackErr := runner.runRollback(
		rollbackContext,
		rollingBack,
		steps,
	)
	if rollbackErr != nil {
		return rolledBack, errors.Join(cause, rollbackErr)
	}
	return rolledBack, cause
}

func (runner *Runner) runRollback(
	ctx context.Context,
	manifest domain.InstallationManifest,
	steps []Step,
) (domain.InstallationManifest, error) {
	for index := len(steps) - 1; index >= 0; index-- {
	reconcile:
		operation := manifest.Execution.Operations[index]
		switch operation.State {
		case domain.OperationPending, domain.OperationRolledBack:
			continue
		case domain.OperationApplying:
			disposition, observation, err := inspect(
				ctx,
				steps[index],
				nil,
			)
			if err != nil {
				return manifest, fmt.Errorf(
					"inspect interrupted apply %s: %w",
					operation.ID,
					err,
				)
			}
			switch disposition {
			case DispositionPending, DispositionRolledBack:
				manifest, err = runner.markPending(manifest, index)
				if err != nil {
					return manifest, err
				}
				continue
			case DispositionApplied:
				manifest, err = runner.markApplied(
					manifest,
					index,
					observation,
				)
				if err != nil {
					return manifest, err
				}
				goto reconcile
			case DispositionConflict:
				return runner.failConflict(
					manifest,
					index,
					"reconcile interrupted apply during rollback",
				)
			default:
				return manifest, fmt.Errorf(
					"inspect operation %s returned invalid disposition %q",
					operation.ID,
					disposition,
				)
			}
		case domain.OperationApplied:
			var err error
			manifest, err = runner.markRollingBack(manifest, index)
			if err != nil {
				return manifest, err
			}
		case domain.OperationRollingBack:
			disposition, _, err := inspect(
				ctx,
				steps[index],
				operation.Observation,
			)
			if err != nil {
				return manifest, fmt.Errorf(
					"inspect interrupted rollback %s: %w",
					operation.ID,
					err,
				)
			}
			switch disposition {
			case DispositionRolledBack, DispositionPending:
				manifest, err = runner.markRolledBack(manifest, index)
				if err != nil {
					return manifest, err
				}
				continue
			case DispositionApplied:
				manifest, err = runner.markRollingBack(manifest, index)
				if err != nil {
					return manifest, err
				}
			case DispositionConflict:
				return runner.failConflict(
					manifest,
					index,
					"reconcile interrupted rollback",
				)
			default:
				return manifest, fmt.Errorf(
					"inspect operation %s returned invalid disposition %q",
					operation.ID,
					disposition,
				)
			}
		default:
			return manifest, fmt.Errorf(
				"operation %s is %s during rollback",
				operation.ID,
				operation.State,
			)
		}
		operation = manifest.Execution.Operations[index]
		if err := steps[index].Rollback(ctx, *operation.Observation); err != nil {
			disposition, _, inspectErr := inspect(
				ctx,
				steps[index],
				operation.Observation,
			)
			if inspectErr != nil {
				return manifest, errors.Join(
					fmt.Errorf("rollback operation %s: %w", operation.ID, err),
					fmt.Errorf("inspect failed rollback: %w", inspectErr),
				)
			}
			switch disposition {
			case DispositionRolledBack, DispositionPending:
				var saveErr error
				manifest, saveErr = runner.markRolledBack(manifest, index)
				if saveErr != nil {
					return manifest, errors.Join(err, saveErr)
				}
				continue
			case DispositionConflict:
				return runner.failConflict(
					manifest,
					index,
					fmt.Sprintf("rollback returned an error: %v", err),
				)
			case DispositionApplied:
				return manifest, fmt.Errorf(
					"rollback operation %s: %w",
					operation.ID,
					err,
				)
			default:
				return manifest, fmt.Errorf(
					"inspect operation %s returned invalid disposition %q",
					operation.ID,
					disposition,
				)
			}
		}
		var err error
		manifest, err = runner.markRolledBack(manifest, index)
		if err != nil {
			return manifest, err
		}
	}
	return runner.checkpoint(
		manifest,
		func(next *domain.InstallationManifest) {
			next.State = domain.InstallationRolledBack
			next.Execution.Phase = domain.ExecutionRolledBack
			for index := range next.Resources {
				if next.Resources[index].Ownership == domain.ResourceManaged {
					next.Resources[index].State = domain.ResourceRolledBack
					next.Resources[index].Fingerprint = ""
					next.Resources[index].Backup = nil
				}
			}
		},
	)
}

func (runner *Runner) markApplying(
	manifest domain.InstallationManifest,
	index int,
) (domain.InstallationManifest, error) {
	return runner.checkpoint(
		manifest,
		func(next *domain.InstallationManifest) {
			operation := &next.Execution.Operations[index]
			operation.State = domain.OperationApplying
			operation.Attempt++
			operation.Observation = nil
			operation.ErrorMessage = ""
		},
	)
}

func (runner *Runner) markApplied(
	manifest domain.InstallationManifest,
	index int,
	observation domain.InstallationObservation,
) (domain.InstallationManifest, error) {
	return runner.checkpoint(
		manifest,
		func(next *domain.InstallationManifest) {
			operation := &next.Execution.Operations[index]
			operation.State = domain.OperationApplied
			operation.Observation = copyObservation(&observation)
			resource := findResource(next.Resources, operation.ResourceID)
			resource.State = domain.ResourceVerified
			if resource.Rollback == domain.RollbackRestore &&
				resource.Kind != domain.ResourceFile &&
				resource.Kind != domain.ResourceTrustAnchor &&
				observation.SnapshotFingerprint != "" {
				resource.Fingerprint = observation.SnapshotFingerprint
			} else {
				resource.Fingerprint = observation.Fingerprint
				if resource.Fingerprint == "" {
					resource.Fingerprint = observation.SnapshotFingerprint
				}
			}
			resource.Backup = copyBackup(observation.Backup)
		},
	)
}

func (runner *Runner) markPending(
	manifest domain.InstallationManifest,
	index int,
) (domain.InstallationManifest, error) {
	return runner.checkpoint(
		manifest,
		func(next *domain.InstallationManifest) {
			operation := &next.Execution.Operations[index]
			operation.State = domain.OperationPending
			operation.Observation = nil
			operation.ErrorMessage = ""
		},
	)
}

func (runner *Runner) markRollingBack(
	manifest domain.InstallationManifest,
	index int,
) (domain.InstallationManifest, error) {
	return runner.checkpoint(
		manifest,
		func(next *domain.InstallationManifest) {
			operation := &next.Execution.Operations[index]
			operation.State = domain.OperationRollingBack
			operation.Attempt++
		},
	)
}

func (runner *Runner) markRolledBack(
	manifest domain.InstallationManifest,
	index int,
) (domain.InstallationManifest, error) {
	return runner.checkpoint(
		manifest,
		func(next *domain.InstallationManifest) {
			operation := &next.Execution.Operations[index]
			operation.State = domain.OperationRolledBack
			resource := findResource(next.Resources, operation.ResourceID)
			resource.State = domain.ResourceRolledBack
			resource.Fingerprint = ""
			resource.Backup = nil
		},
	)
}

func (runner *Runner) failConflict(
	manifest domain.InstallationManifest,
	index int,
	detail string,
) (domain.InstallationManifest, error) {
	message := ErrObservationConflict.Error() + ": " + detail
	if len(message) > 4096 {
		message = message[:4096]
	}
	failed, err := runner.checkpoint(
		manifest,
		func(next *domain.InstallationManifest) {
			next.State = domain.InstallationFailed
			next.Execution.Phase = domain.ExecutionFailed
			operation := &next.Execution.Operations[index]
			operation.State = domain.OperationFailed
			if operation.Attempt == 0 {
				operation.Attempt = 1
			}
			operation.ErrorMessage = message
		},
	)
	if err != nil {
		return manifest, errors.Join(ErrObservationConflict, err)
	}
	return failed, fmt.Errorf("%w: %s", ErrObservationConflict, detail)
}

func (runner *Runner) checkpoint(
	current domain.InstallationManifest,
	change func(*domain.InstallationManifest),
) (domain.InstallationManifest, error) {
	next := cloneManifest(current)
	change(&next)
	next.Generation++
	now := runner.now().UTC()
	if !now.After(current.UpdatedAt) {
		now = current.UpdatedAt.Add(time.Nanosecond)
	}
	next.UpdatedAt = now
	if err := runner.store.Save(current.Generation, next); err != nil {
		return current, fmt.Errorf("save execution checkpoint: %w", err)
	}
	return next, nil
}

func newExecution(steps []Step) *domain.InstallationExecution {
	execution := &domain.InstallationExecution{
		SchemaVersion: domain.InstallationExecutionSchemaVersion,
		Phase:         domain.ExecutionForward,
		Operations: make(
			[]domain.InstallationExecutionOperation,
			len(steps),
		),
	}
	for index, step := range steps {
		execution.Operations[index] = domain.InstallationExecutionOperation{
			ID:                step.ID,
			ResourceID:        step.ResourceID,
			Target:            step.Target,
			Stage:             step.Stage,
			IntentFingerprint: step.IntentFingerprint,
			IntentMode:        step.IntentMode,
			State:             domain.OperationPending,
		}
	}
	return execution
}

func validateSteps(
	manifest domain.InstallationManifest,
	steps []Step,
) error {
	if len(steps) == 0 {
		return errors.New("installation workflow requires steps")
	}
	seenIDs := map[string]bool{}
	seenResources := map[string]bool{}
	managed := map[string]domain.InstallationResource{}
	for _, resource := range manifest.Resources {
		if resource.Ownership == domain.ResourceManaged {
			managed[resource.ID] = resource
		}
	}
	for index, step := range steps {
		if strings.TrimSpace(step.ID) == "" ||
			strings.TrimSpace(step.ResourceID) == "" ||
			strings.TrimSpace(step.Target) == "" {
			return fmt.Errorf("step %d identity is incomplete", index)
		}
		if step.Apply == nil || step.Inspect == nil || step.Rollback == nil {
			return fmt.Errorf("step %s handlers are incomplete", step.ID)
		}
		if seenIDs[step.ID] {
			return fmt.Errorf("duplicate step ID %q", step.ID)
		}
		seenIDs[step.ID] = true
		resource, exists := managed[step.ResourceID]
		if !exists {
			return fmt.Errorf(
				"step %s references unknown managed resource %q",
				step.ID,
				step.ResourceID,
			)
		}
		if seenResources[step.ResourceID] {
			return fmt.Errorf(
				"managed resource %q has multiple steps",
				step.ResourceID,
			)
		}
		seenResources[step.ResourceID] = true
		if resource.Target != step.Target {
			return fmt.Errorf(
				"step %s target does not match resource %q",
				step.ID,
				step.ResourceID,
			)
		}
	}
	if len(seenResources) != len(managed) {
		return fmt.Errorf(
			"workflow covers %d of %d managed resources",
			len(seenResources),
			len(managed),
		)
	}
	if manifest.MaterialCache != nil {
		if err := validateMaterialCacheSteps(
			*manifest.MaterialCache,
			steps,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateMaterialCacheSteps(
	cache domain.InstallationMaterialCache,
	steps []Step,
) error {
	fileSteps := map[string]Step{}
	for _, step := range steps {
		if step.Stage == domain.ExecutionFiles {
			fileSteps[step.ResourceID] = step
		}
	}
	if len(fileSteps) != len(cache.Entries) {
		return fmt.Errorf(
			"workflow covers %d of %d cached file entries",
			len(fileSteps),
			len(cache.Entries),
		)
	}
	for _, entry := range cache.Entries {
		step, exists := fileSteps[entry.ArtifactID]
		if !exists ||
			step.Target != entry.Target ||
			step.IntentFingerprint != entry.Fingerprint ||
			step.IntentMode != entry.Mode {
			return fmt.Errorf(
				"cached material %s does not match file workflow intent",
				entry.ArtifactID,
			)
		}
	}
	return nil
}

func verifyTopology(
	execution domain.InstallationExecution,
	steps []Step,
) error {
	if len(execution.Operations) != len(steps) {
		return errors.New("execution journal does not match workflow step count")
	}
	for index, operation := range execution.Operations {
		step := steps[index]
		if operation.ID != step.ID ||
			operation.ResourceID != step.ResourceID ||
			operation.Target != step.Target ||
			operation.Stage != step.Stage ||
			operation.IntentFingerprint != step.IntentFingerprint ||
			operation.IntentMode != step.IntentMode {
			return fmt.Errorf(
				"execution journal operation %d does not match workflow",
				index,
			)
		}
	}
	return nil
}

func inspect(
	ctx context.Context,
	step Step,
	observation *domain.InstallationObservation,
) (Disposition, domain.InstallationObservation, error) {
	disposition, inspected, err := step.Inspect(ctx, observation)
	if err != nil {
		return "", domain.InstallationObservation{}, err
	}
	switch disposition {
	case DispositionPending,
		DispositionApplied,
		DispositionRolledBack,
		DispositionConflict:
		return disposition, inspected, nil
	default:
		return "", domain.InstallationObservation{}, fmt.Errorf(
			"invalid disposition %q",
			disposition,
		)
	}
}

func findResource(
	resources []domain.InstallationResource,
	resourceID string,
) *domain.InstallationResource {
	for index := range resources {
		if resources[index].ID == resourceID {
			return &resources[index]
		}
	}
	panic("validated workflow resource disappeared")
}

func cloneManifest(
	manifest domain.InstallationManifest,
) domain.InstallationManifest {
	cloned := manifest
	cloned.Resources = append(
		[]domain.InstallationResource(nil),
		manifest.Resources...,
	)
	for index := range cloned.Resources {
		cloned.Resources[index].Backup = copyBackup(
			manifest.Resources[index].Backup,
		)
	}
	if manifest.Execution != nil {
		execution := *manifest.Execution
		execution.Operations = append(
			[]domain.InstallationExecutionOperation(nil),
			manifest.Execution.Operations...,
		)
		for index := range execution.Operations {
			execution.Operations[index].Observation = copyObservation(
				manifest.Execution.Operations[index].Observation,
			)
		}
		cloned.Execution = &execution
	}
	return cloned
}

func copyObservation(
	observation *domain.InstallationObservation,
) *domain.InstallationObservation {
	if observation == nil {
		return nil
	}
	copied := *observation
	copied.Backup = copyBackup(observation.Backup)
	return &copied
}

func copyBackup(backup *domain.ResourceBackup) *domain.ResourceBackup {
	if backup == nil {
		return nil
	}
	copied := *backup
	return &copied
}
