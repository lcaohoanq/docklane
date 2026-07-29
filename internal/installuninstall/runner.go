package installuninstall

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installcompose"
	"docklane.local/docklane/internal/installdirs"
	"docklane.local/docklane/internal/installdocker"
	"docklane.local/docklane/internal/installfiles"
	"docklane.local/docklane/internal/installhost"
	"docklane.local/docklane/internal/installworkflow"
)

type ManifestStore interface {
	Path() string
	Load() (domain.InstallationManifest, error)
	Save(uint64, domain.InstallationManifest) error
}

type Runner struct {
	store  ManifestStore
	docker docker.ManagedLifecycle
	host   installhost.Backend
	now    func() time.Time
}

func New(
	store ManifestStore,
	dockerBackend docker.ManagedLifecycle,
	hostBackend installhost.Backend,
) (*Runner, error) {
	if store == nil {
		return nil, errors.New("installation manifest store is required")
	}
	return &Runner{
		store:  store,
		docker: dockerBackend,
		host:   hostBackend,
		now:    time.Now,
	}, nil
}

func (runner *Runner) Apply(
	ctx context.Context,
	manifest domain.InstallationManifest,
	plan domain.UninstallationPlan,
	expectedToken string,
) (domain.InstallationManifest, error) {
	if err := validatePlan(
		runner.store.Path(),
		manifest,
		plan,
		expectedToken,
	); err != nil {
		return manifest, err
	}
	if hasManagedResources(manifest.Resources) {
		return runner.rollbackManaged(ctx, manifest, plan.Token)
	}
	return runner.rollbackAdoption(ctx, manifest, plan.Token)
}

func (runner *Runner) Resume(
	ctx context.Context,
	expectedToken string,
) (domain.InstallationManifest, error) {
	manifest, err := runner.store.Load()
	if err != nil {
		return domain.InstallationManifest{}, err
	}
	if !tokenMatches(manifest.RollbackToken, expectedToken) {
		return manifest, errors.New(
			"resume token does not match the reviewed uninstallation token",
		)
	}
	switch manifest.State {
	case domain.InstallationRollingBack:
		if hasManagedResources(manifest.Resources) {
			return runner.rollbackManaged(
				ctx,
				manifest,
				manifest.RollbackToken,
			)
		}
		return runner.finishAdoptionRollback(manifest)
	case domain.InstallationRolledBack:
		return manifest, nil
	default:
		return manifest, fmt.Errorf(
			"cannot resume uninstallation in state %s",
			manifest.State,
		)
	}
}

func (runner *Runner) rollbackManaged(
	ctx context.Context,
	manifest domain.InstallationManifest,
	token string,
) (domain.InstallationManifest, error) {
	if runner.docker == nil {
		return manifest, errors.New("managed Docker lifecycle is required")
	}
	if runner.host == nil {
		return manifest, errors.New("host integration backend is required")
	}
	if manifest.ManagedSpecification == nil {
		return manifest, errors.New(
			"managed uninstall requires installation specification",
		)
	}
	specification := *manifest.ManagedSpecification
	directories, err := installdirs.NewUninstallWorkflowAdapter(manifest)
	if err != nil {
		return manifest, err
	}
	files, err := installfiles.NewRollbackWorkflowAdapter(manifest)
	if err != nil {
		return manifest, err
	}
	fileRestorer, err := installhost.NewManagedFileRestorer(
		runner.store,
		files.Steps,
	)
	if err != nil {
		return manifest, err
	}
	trustFingerprint, err := trustIntentFingerprint(manifest)
	if err != nil {
		return manifest, err
	}
	contract, err := installhost.BuildContract(
		specification,
		trustFingerprint,
	)
	if err != nil {
		return manifest, err
	}
	host, err := installhost.NewRollbackWorkflowAdapter(
		runner.host,
		fileRestorer,
		contract,
		manifest,
	)
	if err != nil {
		return manifest, err
	}
	dockerAdapter, err := installdocker.NewWorkflowAdapter(
		runner.docker,
		specification,
		manifest.InstallationID,
		manifest.Resources,
	)
	if err != nil {
		return manifest, err
	}
	steps, err := installcompose.Build(
		manifest.Resources,
		installcompose.Groups{
			Directories: directories.Steps,
			Files:       files.Steps,
			Host:        host.Steps,
			Docker:      dockerAdapter.Steps,
		},
	)
	if err != nil {
		return manifest, err
	}
	workflow, err := installworkflow.New(runner.store)
	if err != nil {
		return manifest, err
	}
	return workflow.Rollback(ctx, manifest, steps, token)
}

func (runner *Runner) rollbackAdoption(
	ctx context.Context,
	manifest domain.InstallationManifest,
	token string,
) (domain.InstallationManifest, error) {
	if err := ctx.Err(); err != nil {
		return manifest, err
	}
	rollingBack := cloneManifest(manifest)
	rollingBack.Generation++
	rollingBack.State = domain.InstallationRollingBack
	rollingBack.RollbackToken = token
	rollingBack.UpdatedAt = nextTime(runner.now(), manifest.UpdatedAt)
	if err := runner.store.Save(
		manifest.Generation,
		rollingBack,
	); err != nil {
		return manifest, fmt.Errorf(
			"begin adoption-only uninstallation: %w",
			err,
		)
	}
	return runner.finishAdoptionRollback(rollingBack)
}

func (runner *Runner) finishAdoptionRollback(
	manifest domain.InstallationManifest,
) (domain.InstallationManifest, error) {
	rolledBack := cloneManifest(manifest)
	rolledBack.Generation++
	rolledBack.State = domain.InstallationRolledBack
	rolledBack.UpdatedAt = nextTime(runner.now(), manifest.UpdatedAt)
	if err := runner.store.Save(
		manifest.Generation,
		rolledBack,
	); err != nil {
		return manifest, fmt.Errorf(
			"finish adoption-only uninstallation: %w",
			err,
		)
	}
	return rolledBack, nil
}

func validatePlan(
	manifestPath string,
	manifest domain.InstallationManifest,
	plan domain.UninstallationPlan,
	expectedToken string,
) error {
	if !tokenMatches(plan.Token, expectedToken) {
		return errors.New(
			"uninstallation token does not match the reviewed manifest state",
		)
	}
	if !plan.Ready {
		return errors.New("uninstallation plan has blocking conflicts")
	}
	if plan.ManifestPath != manifestPath ||
		plan.InstallationID != manifest.InstallationID ||
		plan.Generation != manifest.Generation {
		return errors.New(
			"uninstallation plan does not match the current manifest",
		)
	}
	if manifest.State != domain.InstallationInstalled {
		return fmt.Errorf(
			"installation state is %s, not installed",
			manifest.State,
		)
	}
	return nil
}

func trustIntentFingerprint(
	manifest domain.InstallationManifest,
) (string, error) {
	target := manifest.ManagedSpecification.PKI.TrustAnchorPath
	if manifest.Execution != nil {
		for _, operation := range manifest.Execution.Operations {
			if operation.Target == target &&
				operation.Stage == domain.ExecutionFiles &&
				operation.IntentFingerprint != "" {
				return operation.IntentFingerprint, nil
			}
		}
	}
	for _, resource := range manifest.Resources {
		if resource.Target == target && resource.Fingerprint != "" {
			return resource.Fingerprint, nil
		}
	}
	return "", errors.New("trust anchor fingerprint is unavailable")
}

func hasManagedResources(
	resources []domain.InstallationResource,
) bool {
	for _, resource := range resources {
		if resource.Ownership == domain.ResourceManaged {
			return true
		}
	}
	return false
}

func tokenMatches(actual string, expected string) bool {
	if len(actual) != 64 || len(expected) != len(actual) {
		return false
	}
	return subtle.ConstantTimeCompare(
		[]byte(actual),
		[]byte(expected),
	) == 1
}

func nextTime(now time.Time, previous time.Time) time.Time {
	now = now.UTC()
	if !now.After(previous) {
		return previous.Add(time.Nanosecond)
	}
	return now
}

func cloneManifest(
	manifest domain.InstallationManifest,
) domain.InstallationManifest {
	encodedResources := append(
		[]domain.InstallationResource(nil),
		manifest.Resources...,
	)
	manifest.Resources = encodedResources
	return manifest
}
