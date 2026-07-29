package installhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installworkflow"
)

const (
	dnsServiceResourceID = "dnsmasq-service"
	resolverResourceID   = "resolver-domain"
)

type ManifestLoader interface {
	Load() (domain.InstallationManifest, error)
}

type ManagedFileRestorer struct {
	store ManifestLoader
	steps map[string]installworkflow.Step
}

func NewManagedFileRestorer(
	store ManifestLoader,
	steps []installworkflow.Step,
) (*ManagedFileRestorer, error) {
	if store == nil {
		return nil, errors.New("installation manifest loader is required")
	}
	restorer := &ManagedFileRestorer{
		store: store,
		steps: map[string]installworkflow.Step{},
	}
	for _, step := range steps {
		if step.Stage != domain.ExecutionFiles {
			return nil, fmt.Errorf(
				"managed file restorer received %s step %s",
				step.Stage,
				step.ID,
			)
		}
		if restorer.steps[step.ResourceID].ID != "" {
			return nil, fmt.Errorf(
				"duplicate managed file step for %s",
				step.ResourceID,
			)
		}
		restorer.steps[step.ResourceID] = step
	}
	return restorer, nil
}

func (restorer *ManagedFileRestorer) Rollback(ctx context.Context) error {
	manifest, err := restorer.store.Load()
	if err != nil {
		return err
	}
	if manifest.Execution == nil {
		return errors.New("managed file rollback requires execution journal")
	}
	var rollbackErrors []error
	for index := len(manifest.Execution.Operations) - 1; index >= 0; index-- {
		operation := manifest.Execution.Operations[index]
		if operation.Stage != domain.ExecutionFiles {
			continue
		}
		step, exists := restorer.steps[operation.ResourceID]
		if !exists {
			rollbackErrors = append(rollbackErrors, fmt.Errorf(
				"file operation %s has no rollback step",
				operation.ID,
			))
			continue
		}
		switch operation.State {
		case domain.OperationPending, domain.OperationRolledBack:
			continue
		case domain.OperationApplied,
			domain.OperationRollingBack:
			if operation.Observation == nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf(
					"file operation %s has no observation",
					operation.ID,
				))
				continue
			}
			disposition, _, err := step.Inspect(
				ctx,
				operation.Observation,
			)
			if err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf(
					"inspect file operation %s: %w",
					operation.ID,
					err,
				))
				continue
			}
			switch disposition {
			case installworkflow.DispositionRolledBack,
				installworkflow.DispositionPending:
				continue
			case installworkflow.DispositionApplied:
			case installworkflow.DispositionConflict:
				rollbackErrors = append(rollbackErrors, fmt.Errorf(
					"file operation %s changed before host rollback",
					operation.ID,
				))
				continue
			default:
				rollbackErrors = append(rollbackErrors, fmt.Errorf(
					"file operation %s returned disposition %s",
					operation.ID,
					disposition,
				))
				continue
			}
			if err := step.Rollback(
				ctx,
				*operation.Observation,
			); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf(
					"restore file operation %s: %w",
					operation.ID,
					err,
				))
			}
		default:
			rollbackErrors = append(rollbackErrors, fmt.Errorf(
				"file operation %s is %s during host rollback",
				operation.ID,
				operation.State,
			))
		}
	}
	return errors.Join(rollbackErrors...)
}

type WorkflowAdapter struct {
	Steps []installworkflow.Step
}

func NewWorkflowAdapter(
	backend Backend,
	files *ManagedFileRestorer,
	contract Contract,
	resources []domain.InstallationResource,
) (*WorkflowAdapter, error) {
	if backend == nil {
		return nil, errors.New("host integration backend is required")
	}
	if files == nil {
		return nil, errors.New("managed file restorer is required")
	}
	if err := contract.Validate(); err != nil {
		return nil, err
	}
	adapter := &WorkflowAdapter{Steps: []installworkflow.Step{}}
	byID := map[string]domain.InstallationResource{}
	for _, resource := range resources {
		byID[resource.ID] = resource
	}
	dns, dnsManaged, err := managedHostResource(
		byID,
		dnsServiceResourceID,
		domain.ResourceSystemService,
		contract.DNSService,
	)
	if err != nil {
		return nil, err
	}
	if dnsManaged {
		dnsPrior, err := priorServiceState(dns)
		if err != nil {
			return nil, err
		}
		adapter.Steps = append(
			adapter.Steps,
			dnsServiceStep(backend, files, contract, dns, dnsPrior),
		)
	}
	resolver, resolverManaged, err := managedHostResource(
		byID,
		resolverResourceID,
		domain.ResourceResolverRule,
		contract.BaseDomain,
	)
	if err != nil {
		return nil, err
	}
	if resolverManaged {
		resolverPrior, err := priorServiceState(resolver)
		if err != nil {
			return nil, err
		}
		adapter.Steps = append(
			adapter.Steps,
			resolverStep(
				backend,
				files,
				contract,
				resolver,
				resolverPrior,
			),
		)
	}
	for _, resource := range resources {
		if resource.Ownership != domain.ResourceManaged {
			continue
		}
		if (resource.Kind == domain.ResourceSystemService ||
			resource.Kind == domain.ResourceResolverRule) &&
			resource.ID != dnsServiceResourceID &&
			resource.ID != resolverResourceID {
			return nil, fmt.Errorf(
				"managed host resource %s has no workflow contract",
				resource.ID,
			)
		}
	}
	return adapter, nil
}

func dnsServiceStep(
	backend Backend,
	files *ManagedFileRestorer,
	contract Contract,
	resource domain.InstallationResource,
	prior ServiceState,
) installworkflow.Step {
	return installworkflow.Step{
		ID:         "activate-" + resource.ID,
		ResourceID: resource.ID,
		Target:     resource.Target,
		Stage:      domain.ExecutionHost,
		Apply: func(ctx context.Context) (
			domain.InstallationObservation,
			error,
		) {
			if err := backend.ValidateDNSConfiguration(ctx); err != nil {
				return domain.InstallationObservation{}, fmt.Errorf(
					"validate managed dnsmasq configuration: %w",
					err,
				)
			}
			observation, err := activateManagedService(
				ctx,
				backend,
				contract.DNSService,
				prior,
			)
			if err == nil {
				return observation, nil
			}
			return domain.InstallationObservation{}, errors.Join(
				err,
				compensateDNSFailure(
					ctx,
					backend,
					files,
					contract,
					serviceObservation(contract.DNSService, prior),
				),
			)
		},
		Inspect: func(
			ctx context.Context,
			recorded *domain.InstallationObservation,
		) (installworkflow.Disposition, domain.InstallationObservation, error) {
			return inspectManagedService(
				ctx,
				backend,
				contract.DNSService,
				prior,
				recorded,
			)
		},
		Rollback: func(
			ctx context.Context,
			observation domain.InstallationObservation,
		) error {
			if err := files.Rollback(ctx); err != nil {
				return fmt.Errorf("restore managed host files: %w", err)
			}
			if err := backend.RefreshTrustStore(
				ctx,
				contract.TrustProfile,
			); err != nil {
				return fmt.Errorf("refresh restored trust store: %w", err)
			}
			if err := backend.ValidateDNSConfiguration(ctx); err != nil {
				return fmt.Errorf(
					"validate restored dnsmasq configuration: %w",
					err,
				)
			}
			return restoreManagedService(
				ctx,
				backend,
				contract.DNSService,
				observation,
			)
		},
	}
}

func resolverStep(
	backend Backend,
	files *ManagedFileRestorer,
	contract Contract,
	resource domain.InstallationResource,
	prior ServiceState,
) installworkflow.Step {
	return installworkflow.Step{
		ID:         "activate-" + resource.ID,
		ResourceID: resource.ID,
		Target:     resource.Target,
		Stage:      domain.ExecutionHost,
		Apply: func(ctx context.Context) (
			domain.InstallationObservation,
			error,
		) {
			if err := backend.RefreshTrustStore(
				ctx,
				contract.TrustProfile,
			); err != nil {
				return domain.InstallationObservation{}, errors.Join(
					fmt.Errorf("refresh managed trust store: %w", err),
					compensateResolverFailure(
						ctx,
						backend,
						files,
						contract,
						serviceObservation(
							contract.ResolverService,
							prior,
						),
					),
				)
			}
			if err := backend.VerifyTrustAnchor(
				ctx,
				contract.TrustAnchorPath,
				contract.TrustAnchorFingerprint,
			); err != nil {
				return domain.InstallationObservation{}, errors.Join(
					fmt.Errorf("verify managed trust anchor: %w", err),
					compensateResolverFailure(
						ctx,
						backend,
						files,
						contract,
						serviceObservation(
							contract.ResolverService,
							prior,
						),
					),
				)
			}
			observation, err := activateManagedService(
				ctx,
				backend,
				contract.ResolverService,
				prior,
			)
			if err != nil {
				return domain.InstallationObservation{}, errors.Join(
					err,
					compensateResolverFailure(
						ctx,
						backend,
						files,
						contract,
						serviceObservation(
							contract.ResolverService,
							prior,
						),
					),
				)
			}
			if err := backend.FlushResolverCache(
				ctx,
				contract.ResolverProfile,
			); err != nil {
				return domain.InstallationObservation{}, errors.Join(
					fmt.Errorf("flush managed resolver cache: %w", err),
					compensateResolverFailure(
						ctx,
						backend,
						files,
						contract,
						observation,
					),
				)
			}
			if err := verifyDNS(ctx, backend, contract.BaseDomain); err != nil {
				return domain.InstallationObservation{}, errors.Join(
					err,
					compensateResolverFailure(
						ctx,
						backend,
						files,
						contract,
						observation,
					),
				)
			}
			return observation, nil
		},
		Inspect: func(
			ctx context.Context,
			recorded *domain.InstallationObservation,
		) (installworkflow.Disposition, domain.InstallationObservation, error) {
			return inspectManagedService(
				ctx,
				backend,
				contract.ResolverService,
				prior,
				recorded,
			)
		},
		Rollback: func(
			ctx context.Context,
			observation domain.InstallationObservation,
		) error {
			if err := files.Rollback(ctx); err != nil {
				return fmt.Errorf("restore managed host files: %w", err)
			}
			if err := backend.RefreshTrustStore(
				ctx,
				contract.TrustProfile,
			); err != nil {
				return fmt.Errorf("refresh restored trust store: %w", err)
			}
			if err := restoreManagedService(
				ctx,
				backend,
				contract.ResolverService,
				observation,
			); err != nil {
				return err
			}
			if !observation.Created {
				if err := backend.FlushResolverCache(
					ctx,
					contract.ResolverProfile,
				); err != nil {
					return fmt.Errorf(
						"flush restored resolver cache: %w",
						err,
					)
				}
			}
			return nil
		},
	}
}

func activateManagedService(
	ctx context.Context,
	backend Backend,
	name string,
	prior ServiceState,
) (domain.InstallationObservation, error) {
	current, err := backend.ServiceState(ctx, name)
	if err != nil {
		return domain.InstallationObservation{}, err
	}
	if current != prior {
		if !prior.Active && current.Active {
			return serviceObservation(name, prior), nil
		}
		return domain.InstallationObservation{}, fmt.Errorf(
			"service %s changed from its reviewed prior state",
			name,
		)
	}
	if prior.Active {
		err = backend.RestartService(ctx, name)
	} else {
		err = backend.StartService(ctx, name)
	}
	if err != nil {
		return domain.InstallationObservation{}, fmt.Errorf(
			"activate service %s: %w",
			name,
			err,
		)
	}
	current, err = backend.ServiceState(ctx, name)
	if err != nil {
		return domain.InstallationObservation{}, err
	}
	if !current.Active {
		return domain.InstallationObservation{}, fmt.Errorf(
			"service %s is inactive after activation",
			name,
		)
	}
	return serviceObservation(name, prior), nil
}

func compensateDNSFailure(
	ctx context.Context,
	backend Backend,
	files *ManagedFileRestorer,
	contract Contract,
	observation domain.InstallationObservation,
) error {
	var compensationErrors []error
	if err := files.Rollback(ctx); err != nil {
		compensationErrors = append(
			compensationErrors,
			fmt.Errorf("restore managed host files: %w", err),
		)
	}
	if err := backend.RefreshTrustStore(
		ctx,
		contract.TrustProfile,
	); err != nil {
		compensationErrors = append(
			compensationErrors,
			fmt.Errorf("refresh restored trust store: %w", err),
		)
	}
	if err := backend.ValidateDNSConfiguration(ctx); err != nil {
		compensationErrors = append(
			compensationErrors,
			fmt.Errorf("validate restored dnsmasq configuration: %w", err),
		)
	}
	if err := restoreManagedService(
		ctx,
		backend,
		contract.DNSService,
		observation,
	); err != nil {
		compensationErrors = append(compensationErrors, err)
	}
	return errors.Join(compensationErrors...)
}

func compensateResolverFailure(
	ctx context.Context,
	backend Backend,
	files *ManagedFileRestorer,
	contract Contract,
	observation domain.InstallationObservation,
) error {
	var compensationErrors []error
	if err := files.Rollback(ctx); err != nil {
		compensationErrors = append(
			compensationErrors,
			fmt.Errorf("restore managed host files: %w", err),
		)
	}
	if err := backend.RefreshTrustStore(
		ctx,
		contract.TrustProfile,
	); err != nil {
		compensationErrors = append(
			compensationErrors,
			fmt.Errorf("refresh restored trust store: %w", err),
		)
	}
	if err := restoreManagedService(
		ctx,
		backend,
		contract.ResolverService,
		observation,
	); err != nil {
		compensationErrors = append(compensationErrors, err)
	}
	if !observation.Created {
		if err := backend.FlushResolverCache(
			ctx,
			contract.ResolverProfile,
		); err != nil {
			compensationErrors = append(
				compensationErrors,
				fmt.Errorf("flush restored resolver cache: %w", err),
			)
		}
	}
	return errors.Join(compensationErrors...)
}

func inspectManagedService(
	ctx context.Context,
	backend Backend,
	name string,
	prior ServiceState,
	recorded *domain.InstallationObservation,
) (installworkflow.Disposition, domain.InstallationObservation, error) {
	current, err := backend.ServiceState(ctx, name)
	if err != nil {
		return "", domain.InstallationObservation{}, err
	}
	if recorded == nil {
		if current == prior {
			return installworkflow.DispositionPending,
				domain.InstallationObservation{},
				nil
		}
		if !prior.Active && current.Active {
			return installworkflow.DispositionApplied,
				serviceObservation(name, prior),
				nil
		}
		return installworkflow.DispositionConflict,
			domain.InstallationObservation{},
			nil
	}
	if !validServiceObservation(name, prior, *recorded) {
		return installworkflow.DispositionConflict,
			domain.InstallationObservation{},
			nil
	}
	if current.Active {
		return installworkflow.DispositionApplied, *recorded, nil
	}
	if recorded.Created {
		return installworkflow.DispositionRolledBack, *recorded, nil
	}
	return installworkflow.DispositionConflict,
		domain.InstallationObservation{},
		nil
}

func restoreManagedService(
	ctx context.Context,
	backend Backend,
	name string,
	observation domain.InstallationObservation,
) error {
	prior := ServiceState{Active: !observation.Created}
	if !validServiceObservation(name, prior, observation) {
		return fmt.Errorf("service %s observation changed", name)
	}
	current, err := backend.ServiceState(ctx, name)
	if err != nil {
		return err
	}
	if !current.Active {
		if observation.Created {
			return nil
		}
		return fmt.Errorf("service %s changed state before rollback", name)
	}
	if observation.Created {
		err = backend.StopService(ctx, name)
	} else {
		err = backend.RestartService(ctx, name)
	}
	if err != nil {
		return fmt.Errorf("restore service %s: %w", name, err)
	}
	restored, err := backend.ServiceState(ctx, name)
	if err != nil {
		return err
	}
	if restored.Active == observation.Created {
		return fmt.Errorf("service %s did not return to its prior state", name)
	}
	return nil
}

func serviceObservation(
	name string,
	prior ServiceState,
) domain.InstallationObservation {
	return domain.InstallationObservation{
		ExternalID:          name,
		Fingerprint:         serviceStateFingerprint(ServiceState{Active: true}),
		SnapshotFingerprint: serviceStateFingerprint(prior),
		Created:             !prior.Active,
	}
}

func validServiceObservation(
	name string,
	prior ServiceState,
	observation domain.InstallationObservation,
) bool {
	return observation.ExternalID == name &&
		observation.Fingerprint ==
			serviceStateFingerprint(ServiceState{Active: true}) &&
		observation.SnapshotFingerprint == serviceStateFingerprint(prior) &&
		observation.Backup == nil
}

func priorServiceState(
	resource domain.InstallationResource,
) (ServiceState, error) {
	switch resource.Fingerprint {
	case serviceStateFingerprint(ServiceState{}):
		return ServiceState{}, nil
	case serviceStateFingerprint(ServiceState{Active: true}):
		return ServiceState{Active: true}, nil
	default:
		return ServiceState{}, fmt.Errorf(
			"managed host resource %s has no reviewed service state",
			resource.ID,
		)
	}
}

func serviceStateFingerprint(state ServiceState) string {
	value := "inactive"
	if state.Active {
		value = "active"
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func managedHostResource(
	resources map[string]domain.InstallationResource,
	id string,
	kind domain.ResourceKind,
	target string,
) (domain.InstallationResource, bool, error) {
	resource, exists := resources[id]
	if !exists || resource.Ownership != domain.ResourceManaged {
		return domain.InstallationResource{}, false, nil
	}
	if err := resource.Validate(); err != nil {
		return domain.InstallationResource{}, false, err
	}
	if resource.Kind != kind ||
		resource.Target != target ||
		resource.Rollback != domain.RollbackRestore {
		return domain.InstallationResource{}, false, fmt.Errorf(
			"managed host resource %s does not match its contract",
			id,
		)
	}
	return resource, true, nil
}
