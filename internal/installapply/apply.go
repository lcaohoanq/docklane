package installapply

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installmanifest"
)

var ErrManagedOperationsUnsupported = errors.New(
	"managed installation operations are not implemented",
)

type ManifestStore interface {
	Path() string
	Create(domain.InstallationManifest) error
	Save(uint64, domain.InstallationManifest) error
}

type Runner struct {
	store          ManifestStore
	productVersion string
	now            func() time.Time
}

func New(
	store ManifestStore,
	productVersion string,
) (*Runner, error) {
	if store == nil {
		return nil, errors.New("installation manifest store is required")
	}
	if productVersion == "" {
		return nil, errors.New("product version is required")
	}
	return &Runner{
		store:          store,
		productVersion: productVersion,
		now:            time.Now,
	}, nil
}

func (runner *Runner) Apply(
	ctx context.Context,
	plan domain.InstallationPlan,
	expectedToken string,
) (domain.InstallationManifest, error) {
	if err := ctx.Err(); err != nil {
		return domain.InstallationManifest{}, err
	}
	if !tokenMatches(plan.Token, expectedToken) {
		return domain.InstallationManifest{}, fmt.Errorf(
			"installation plan token does not match current machine state "+
				"(reviewed %s, current %s); rerun install --dry-run",
			expectedToken,
			plan.Token,
		)
	}
	if !plan.Ready {
		return domain.InstallationManifest{}, errors.New(
			"installation plan has blocking conflicts",
		)
	}
	if !plan.Complete {
		return domain.InstallationManifest{}, errors.New(
			"installation plan has pending resource coverage",
		)
	}
	if runner.store.Path() != plan.Target.ManifestPath {
		return domain.InstallationManifest{}, fmt.Errorf(
			"manifest store path %s does not match reviewed target %s",
			runner.store.Path(),
			plan.Target.ManifestPath,
		)
	}
	if err := adoptionOnly(plan); err != nil {
		return domain.InstallationManifest{}, err
	}
	manifest, err := installmanifest.New(
		runner.productVersion,
		plan.Target.BaseDomain,
		plan.Target.ProxyNetwork,
		runner.now(),
	)
	if err != nil {
		return domain.InstallationManifest{}, err
	}
	manifest.Resources = append(
		[]domain.InstallationResource(nil),
		plan.Resources...,
	)
	if err := manifest.Validate(); err != nil {
		return domain.InstallationManifest{}, fmt.Errorf(
			"construct planned installation manifest: %w",
			err,
		)
	}
	if err := runner.store.Create(manifest); err != nil {
		return domain.InstallationManifest{}, fmt.Errorf(
			"create planned installation manifest: %w",
			err,
		)
	}
	applying := runner.advance(manifest, domain.InstallationApplying)
	if err := runner.store.Save(manifest.Generation, applying); err != nil {
		return manifest, fmt.Errorf(
			"mark installation applying: %w",
			err,
		)
	}
	if err := ctx.Err(); err != nil {
		return runner.fail(applying, err)
	}
	installed := runner.advance(applying, domain.InstallationInstalled)
	if err := runner.store.Save(applying.Generation, installed); err != nil {
		return runner.fail(
			applying,
			fmt.Errorf("finalize installed manifest: %w", err),
		)
	}
	return installed, nil
}

func adoptionOnly(plan domain.InstallationPlan) error {
	for _, resource := range plan.Resources {
		if resource.Ownership != domain.ResourceAdopted ||
			resource.State != domain.ResourceVerified ||
			resource.Rollback != domain.RollbackPreserve {
			return fmt.Errorf(
				"%w: resource %s requires %s/%s",
				ErrManagedOperationsUnsupported,
				resource.ID,
				resource.Ownership,
				resource.Rollback,
			)
		}
	}
	for _, operation := range plan.Operations {
		if operation.Action == domain.InstallationCreateManifest {
			continue
		}
		if operation.Mutating || operation.Action != domain.InstallationAdopt {
			return fmt.Errorf(
				"%w: operation %s requires %s",
				ErrManagedOperationsUnsupported,
				operation.ID,
				operation.Action,
			)
		}
	}
	return nil
}

func tokenMatches(actual string, expected string) bool {
	if len(actual) != 64 || len(expected) != len(actual) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}

func (runner *Runner) advance(
	manifest domain.InstallationManifest,
	state domain.InstallationState,
) domain.InstallationManifest {
	manifest.Generation++
	manifest.State = state
	now := runner.now().UTC()
	if !now.After(manifest.UpdatedAt) {
		now = manifest.UpdatedAt.Add(time.Nanosecond)
	}
	manifest.UpdatedAt = now
	return manifest
}

func (runner *Runner) fail(
	current domain.InstallationManifest,
	cause error,
) (domain.InstallationManifest, error) {
	failed := runner.advance(current, domain.InstallationFailed)
	if saveErr := runner.store.Save(current.Generation, failed); saveErr != nil {
		return current, errors.Join(cause, fmt.Errorf(
			"record failed installation state: %w",
			saveErr,
		))
	}
	return failed, cause
}
