package installmanaged

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
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
	"docklane.local/docklane/internal/installlinks"
	"docklane.local/docklane/internal/installmanifest"
	"docklane.local/docklane/internal/installmaterial"
	"docklane.local/docklane/internal/installworkflow"
)

type ManifestStore interface {
	Path() string
	Load() (domain.InstallationManifest, error)
	Create(domain.InstallationManifest) error
	Save(uint64, domain.InstallationManifest) error
}

type Runner struct {
	store          ManifestStore
	productVersion string
	docker         docker.ManagedLifecycle
	host           installhost.Backend
	now            func() time.Time
}

func New(
	store ManifestStore,
	productVersion string,
	dockerBackend docker.ManagedLifecycle,
	hostBackend installhost.Backend,
) (*Runner, error) {
	if store == nil {
		return nil, errors.New("installation manifest store is required")
	}
	if productVersion == "" {
		return nil, errors.New("product version is required")
	}
	if dockerBackend == nil {
		return nil, errors.New("managed Docker lifecycle is required")
	}
	if hostBackend == nil {
		return nil, errors.New("host integration backend is required")
	}
	return &Runner{
		store:          store,
		productVersion: productVersion,
		docker:         dockerBackend,
		host:           hostBackend,
		now:            time.Now,
	}, nil
}

func (runner *Runner) Apply(
	ctx context.Context,
	plan domain.InstallationPlan,
	expectedToken string,
) (domain.InstallationManifest, error) {
	if err := validatePlan(
		runner.store.Path(),
		plan,
		expectedToken,
	); err != nil {
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
	manifest.ReviewedToken = plan.Token
	manifest.Resources = append(
		[]domain.InstallationResource(nil),
		plan.Resources...,
	)
	specification := *plan.ManagedSpecification
	manifest.ManagedSpecification = &specification
	manifest.ManagedArtifacts = append(
		[]domain.InstallationArtifact(nil),
		plan.ManagedArtifacts...,
	)
	if err := manifest.Validate(); err != nil {
		return domain.InstallationManifest{}, fmt.Errorf(
			"construct managed installation manifest: %w",
			err,
		)
	}
	if err := runner.store.Create(manifest); err != nil {
		return domain.InstallationManifest{}, fmt.Errorf(
			"create managed installation manifest: %w",
			err,
		)
	}
	return runner.execute(ctx, manifest)
}

func (runner *Runner) Resume(
	ctx context.Context,
	expectedToken string,
) (domain.InstallationManifest, error) {
	manifest, err := runner.store.Load()
	if err != nil {
		return domain.InstallationManifest{}, err
	}
	if !tokenMatches(manifest.ReviewedToken, expectedToken) {
		return manifest, errors.New(
			"resume token does not match the reviewed installation token",
		)
	}
	if manifest.ManagedSpecification == nil {
		return manifest, errors.New(
			"installation manifest has no managed specification",
		)
	}
	switch manifest.State {
	case domain.InstallationInstalled:
		return manifest, nil
	case domain.InstallationPlanned,
		domain.InstallationApplying,
		domain.InstallationRollingBack:
		return runner.execute(ctx, manifest)
	case domain.InstallationRolledBack:
		return manifest, errors.New("managed installation was rolled back")
	case domain.InstallationFailed:
		return manifest, errors.New(
			"managed installation is failed and requires drift repair",
		)
	default:
		return manifest, fmt.Errorf(
			"cannot resume installation in state %s",
			manifest.State,
		)
	}
}

func (runner *Runner) execute(
	ctx context.Context,
	manifest domain.InstallationManifest,
) (domain.InstallationManifest, error) {
	materials, err := installmaterial.New(runner.store)
	if err != nil {
		return manifest, err
	}
	prepared, files, err := materials.PrepareArtifacts(
		ctx,
		manifest,
		runner.now(),
		nil,
	)
	if err != nil {
		return manifest, err
	}
	defer installmaterial.ClearFiles(files)
	specification := *prepared.ManagedSpecification
	directories, err := installdirs.NewManagedWorkflowAdapter(
		prepared.InstallationID,
		specification,
		prepared.Resources,
	)
	if err != nil {
		return prepared, err
	}
	fileAdapter, err := installfiles.NewWorkflowAdapter(
		files,
		prepared.Resources,
		specification.Paths.BackupDirectory,
	)
	if err != nil {
		return prepared, err
	}
	linkAdapter, err := installlinks.NewWorkflowAdapter(prepared.Resources)
	if err != nil {
		return prepared, err
	}
	defer fileAdapter.Clear()
	fileRestorer, err := installhost.NewManagedFileRestorer(
		runner.store,
		fileAdapter.Steps,
	)
	if err != nil {
		return prepared, err
	}
	trustFingerprint, err := managedTrustFingerprint(prepared, files)
	if err != nil {
		return prepared, err
	}
	hostContract, err := installhost.BuildContract(
		specification,
		trustFingerprint,
	)
	if err != nil {
		return prepared, err
	}
	hostAdapter, err := installhost.NewWorkflowAdapter(
		runner.host,
		fileRestorer,
		hostContract,
		prepared.Resources,
	)
	if err != nil {
		return prepared, err
	}
	dockerAdapter, err := installdocker.NewWorkflowAdapter(
		runner.docker,
		specification,
		prepared.InstallationID,
		prepared.Resources,
	)
	if err != nil {
		return prepared, err
	}
	steps, err := installcompose.Build(
		prepared.Resources,
		installcompose.Groups{
			Directories: directories.Steps,
			Files:       fileAdapter.Steps,
			Host: append(
				append([]installworkflow.Step(nil), linkAdapter.Steps...),
				hostAdapter.Steps...,
			),
			Docker: dockerAdapter.Steps,
		},
	)
	if err != nil {
		return prepared, err
	}
	workflow, err := installworkflow.New(runner.store)
	if err != nil {
		return prepared, err
	}
	result, runErr := workflow.Run(ctx, prepared, steps)
	if !terminal(result) {
		return result, runErr
	}
	cleared, clearErr := materials.Clear(context.Background(), result)
	if clearErr != nil {
		return result, errors.Join(runErr, fmt.Errorf(
			"clear terminal material cache: %w",
			clearErr,
		))
	}
	return cleared, runErr
}

func validatePlan(
	manifestPath string,
	plan domain.InstallationPlan,
	expectedToken string,
) error {
	if !tokenMatches(plan.Token, expectedToken) {
		return fmt.Errorf(
			"installation plan token does not match current machine state "+
				"(reviewed %s, current %s); rerun install --dry-run",
			expectedToken,
			plan.Token,
		)
	}
	if !plan.Ready {
		return errors.New("installation plan has blocking conflicts")
	}
	if !plan.Complete {
		return errors.New("installation plan has pending resource coverage")
	}
	if manifestPath != plan.Target.ManifestPath {
		return fmt.Errorf(
			"manifest store path %s does not match reviewed target %s",
			manifestPath,
			plan.Target.ManifestPath,
		)
	}
	if plan.ManagedSpecification == nil {
		return errors.New("managed installation specification is required")
	}
	managed := false
	for _, resource := range plan.Resources {
		if resource.Ownership == domain.ResourceManaged {
			managed = true
			break
		}
	}
	if !managed {
		return errors.New("managed installation plan has no managed resources")
	}
	return nil
}

func managedTrustFingerprint(
	manifest domain.InstallationManifest,
	files []installfiles.File,
) (string, error) {
	if manifest.ManagedSpecification == nil {
		return "", errors.New("managed specification is required")
	}
	target := manifest.ManagedSpecification.PKI.TrustAnchorPath
	for _, file := range files {
		if file.Target == target {
			sum := sha256.Sum256(file.Content)
			return hex.EncodeToString(sum[:]), nil
		}
	}
	for _, resource := range manifest.Resources {
		if resource.Target == target &&
			resource.Fingerprint != "" {
			return resource.Fingerprint, nil
		}
	}
	return "", errors.New("trust anchor fingerprint is unavailable")
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

func terminal(manifest domain.InstallationManifest) bool {
	if manifest.Execution == nil {
		return false
	}
	switch manifest.Execution.Phase {
	case domain.ExecutionComplete,
		domain.ExecutionRolledBack,
		domain.ExecutionFailed:
		return true
	default:
		return false
	}
}
