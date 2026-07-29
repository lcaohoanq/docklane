package installplan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installartifacts"
)

type Options struct {
	DnsmasqTarget        string
	DnsmasqService       string
	ManagedSpecification domain.InstallationSpecification
}

func Build(
	report domain.PreflightReport,
	options Options,
) (domain.InstallationPlan, error) {
	if !filepath.IsAbs(options.DnsmasqTarget) {
		return domain.InstallationPlan{}, fmt.Errorf(
			"dnsmasq target must be absolute",
		)
	}
	if options.DnsmasqService == "" {
		return domain.InstallationPlan{}, fmt.Errorf(
			"dnsmasq service is required",
		)
	}
	plan := domain.InstallationPlan{
		SchemaVersion: domain.InstallationPlanSchemaVersion,
		Status:        report.Status,
		Target:        report.Target,
		Inventory:     report.Inventory,
		Resources:     []domain.InstallationResource{},
		Operations:    []domain.InstallationOperation{},
		Blockers:      []string{},
		Pending:       []string{},
	}
	for _, check := range report.Checks {
		if check.Status == domain.DiagnosticFail {
			plan.Blockers = append(plan.Blockers, check.ID)
		}
	}
	if report.Inventory.Manifest.Exists {
		plan.Blockers = append(plan.Blockers, "existing-install-manifest")
	}
	addGateway(&plan)
	addNetwork(&plan)
	addDNS(&plan, options)
	addResolver(&plan)
	addTLS(&plan)
	addRuntime(&plan)
	addManifestOperation(&plan)
	if hasManagedResources(plan.Resources) {
		if err := options.ManagedSpecification.Validate(); err != nil {
			return domain.InstallationPlan{}, fmt.Errorf(
				"managed installation specification: %w",
				err,
			)
		}
		specification := options.ManagedSpecification
		plan.ManagedSpecification = &specification
		artifacts, err := installartifacts.Build(specification)
		if err != nil {
			return domain.InstallationPlan{}, fmt.Errorf(
				"managed installation artifacts: %w",
				err,
			)
		}
		plan.ManagedArtifacts = selectManagedArtifacts(&plan, artifacts)
		if err := domain.ValidateInstallationArtifacts(plan.ManagedArtifacts); err != nil {
			return domain.InstallationPlan{}, err
		}
		if err := addManagedDirectoryResources(
			&plan,
			specification,
			plan.ManagedArtifacts,
		); err != nil {
			return domain.InstallationPlan{}, err
		}
		if err := addManagedArtifactResources(
			&plan,
			plan.ManagedArtifacts,
		); err != nil {
			return domain.InstallationPlan{}, err
		}
	}
	sort.Strings(plan.Blockers)
	plan.Blockers = unique(plan.Blockers)
	plan.Ready = len(plan.Blockers) == 0
	plan.Complete = len(plan.Pending) == 0
	if !plan.Ready {
		plan.Status = domain.DiagnosticFail
	}
	for _, resource := range plan.Resources {
		if err := resource.Validate(); err != nil {
			return domain.InstallationPlan{}, fmt.Errorf(
				"planned resource %s: %w",
				resource.ID,
				err,
			)
		}
	}
	token, err := fingerprint(plan)
	if err != nil {
		return domain.InstallationPlan{}, err
	}
	plan.Token = token
	return plan, nil
}

func addManagedDirectoryResources(
	plan *domain.InstallationPlan,
	specification domain.InstallationSpecification,
	artifacts []domain.InstallationArtifact,
) error {
	if managedResourceExists(plan.Resources, "resolver-domain") {
		switch plan.Inventory.Resolver.ConfigDirectoryDisposition {
		case domain.PreflightCreate:
			addResource(
				plan,
				domain.InstallationResource{
					ID:        "resolver-config-directory",
					Kind:      domain.ResourceDirectory,
					Target:    filepath.Dir(specification.Paths.ResolverConfig),
					Ownership: domain.ResourceManaged,
					State:     domain.ResourcePlanned,
					Rollback:  domain.RollbackRemove,
				},
				"Missing resolver file parent must be created as a journaled host directory.",
			)
		case domain.PreflightAdopt:
		default:
			return errors.New(
				"managed resolver requires known configuration directory ownership",
			)
		}
	}
	hasFiles := false
	needed := map[string]bool{}
	state := specification.Paths.StateDirectory
	for _, artifact := range artifacts {
		if artifact.Kind == domain.ArtifactContainerSpec {
			continue
		}
		hasFiles = true
		parent := filepath.Dir(artifact.Target)
		for parent != state && pathBelow(state, parent) {
			needed[parent] = true
			parent = filepath.Dir(parent)
		}
	}
	if !hasFiles {
		return nil
	}
	needed[specification.Paths.BackupDirectory] = true
	candidates := []struct {
		id     string
		target string
	}{
		{"docklane-traefik-directory", specification.Paths.TraefikDirectory},
		{
			"docklane-traefik-dynamic-directory",
			filepath.Dir(specification.Paths.TraefikDynamicConfig),
		},
		{
			"docklane-traefik-certs-directory",
			filepath.Dir(specification.PKI.LeafCertificatePath),
		},
		{
			"docklane-pki-directory",
			filepath.Dir(specification.PKI.RootPrivateKeyPath),
		},
		{
			"docklane-secrets-directory",
			filepath.Dir(specification.Paths.DashboardPassword),
		},
		{"docklane-backup-directory", specification.Paths.BackupDirectory},
	}
	known := map[string]bool{}
	for _, candidate := range candidates {
		known[candidate.target] = true
		if !needed[candidate.target] {
			continue
		}
		if resourceTargetExists(
			plan.Resources,
			domain.ResourceDirectory,
			candidate.target,
		) {
			continue
		}
		addResource(
			plan,
			domain.InstallationResource{
				ID:        candidate.id,
				Kind:      domain.ResourceDirectory,
				Target:    candidate.target,
				Ownership: domain.ResourceManaged,
				State:     domain.ResourcePlanned,
				Rollback:  domain.RollbackRemove,
			},
			"Managed file parent must have explicit directory ownership.",
		)
	}
	for directory := range needed {
		if !known[directory] {
			return fmt.Errorf(
				"managed state directory %s has no ownership resource",
				directory,
			)
		}
	}
	return nil
}

func managedResourceExists(
	resources []domain.InstallationResource,
	id string,
) bool {
	for _, resource := range resources {
		if resource.ID == id &&
			resource.Ownership == domain.ResourceManaged {
			return true
		}
	}
	return false
}

func pathBelow(parent string, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil &&
		relative != "." &&
		relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func resourceTargetExists(
	resources []domain.InstallationResource,
	kind domain.ResourceKind,
	target string,
) bool {
	for _, resource := range resources {
		if resource.Kind == kind && resource.Target == target {
			return true
		}
	}
	return false
}

func selectManagedArtifacts(
	plan *domain.InstallationPlan,
	artifacts []domain.InstallationArtifact,
) []domain.InstallationArtifact {
	selected := make([]domain.InstallationArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		resourceID := artifact.ID
		switch artifact.ID {
		case "resolver-config":
			resourceID = "resolver-domain"
		case "traefik-dynamic-config",
			"traefik-dashboard-password",
			"traefik-dashboard-users":
			resourceID = "global-traefik"
		case "pki-root-private-key",
			"pki-root-certificate",
			"pki-leaf-private-key",
			"pki-leaf-certificate",
			"pki-trust-anchor":
			if plan.Inventory.TLS.Disposition == domain.PreflightCreate {
				selected = append(selected, artifact)
			}
			continue
		case "container-gateway":
			resourceID = "global-traefik"
		case "container-controller":
			resourceID = runtimeControllerResourceID
		case "container-probe":
			resourceID = runtimeProbeName
		}
		if resourceIsManaged(plan.Resources, resourceID) {
			selected = append(selected, artifact)
		}
	}
	return selected
}

func resourceIsManaged(
	resources []domain.InstallationResource,
	resourceID string,
) bool {
	for _, resource := range resources {
		if resource.ID == resourceID {
			return resource.Ownership == domain.ResourceManaged
		}
	}
	return false
}

func hasManagedResources(resources []domain.InstallationResource) bool {
	for _, resource := range resources {
		if resource.Ownership == domain.ResourceManaged {
			return true
		}
	}
	return false
}

func addGateway(plan *domain.InstallationPlan) {
	fact := plan.Inventory.Gateway
	switch fact.Disposition {
	case domain.PreflightAdopt:
		addResource(
			plan,
			domain.InstallationResource{
				ID:        "global-traefik",
				Kind:      domain.ResourceDockerContainer,
				Target:    fact.ContainerName,
				Ownership: domain.ResourceAdopted,
				State:     domain.ResourceVerified,
				Rollback:  domain.RollbackPreserve,
			},
			"Existing Traefik will be recorded and preserved.",
		)
	case domain.PreflightCreate:
		addResource(
			plan,
			domain.InstallationResource{
				ID:        "global-traefik",
				Kind:      domain.ResourceDockerContainer,
				Target:    "traefik",
				Ownership: domain.ResourceManaged,
				State:     domain.ResourcePlanned,
				Rollback:  domain.RollbackRemove,
			},
			"No global Traefik exists; Docklane must create one.",
		)
	default:
		plan.Blockers = append(plan.Blockers, "gateway-ownership")
	}
}

func addNetwork(plan *domain.InstallationPlan) {
	fact := plan.Inventory.Network
	switch fact.Disposition {
	case domain.PreflightAdopt:
		addResource(
			plan,
			domain.InstallationResource{
				ID:        "proxy-network",
				Kind:      domain.ResourceDockerNetwork,
				Target:    fact.Name,
				Ownership: domain.ResourceAdopted,
				State:     domain.ResourceVerified,
				Rollback:  domain.RollbackPreserve,
			},
			"Existing compatible proxy network will be recorded and preserved.",
		)
	case domain.PreflightCreate:
		addResource(
			plan,
			domain.InstallationResource{
				ID:        "proxy-network",
				Kind:      domain.ResourceDockerNetwork,
				Target:    fact.Name,
				Ownership: domain.ResourceManaged,
				State:     domain.ResourcePlanned,
				Rollback:  domain.RollbackRemove,
			},
			"Missing proxy network must be created.",
		)
	default:
		plan.Blockers = append(plan.Blockers, "proxy-network-ownership")
	}
}

func addDNS(plan *domain.InstallationPlan, options Options) {
	fact := plan.Inventory.DNS
	switch fact.Disposition {
	case domain.PreflightAdopt:
		if len(fact.MappingPaths) != 1 {
			plan.Blockers = append(plan.Blockers, "dnsmasq-mapping-ownership")
			break
		}
		addResource(
			plan,
			domain.InstallationResource{
				ID:        "dnsmasq-domain",
				Kind:      domain.ResourceFile,
				Target:    fact.MappingPaths[0],
				Ownership: domain.ResourceAdopted,
				State:     domain.ResourceVerified,
				Rollback:  domain.RollbackPreserve,
			},
			"Existing correct wildcard mapping will be recorded and preserved.",
		)
	case domain.PreflightCreate:
		rollback := domain.RollbackRemove
		for _, path := range fact.ConfigPaths {
			if path == options.DnsmasqTarget {
				rollback = domain.RollbackRestore
				break
			}
		}
		addResource(
			plan,
			domain.InstallationResource{
				ID:        "dnsmasq-domain",
				Kind:      domain.ResourceFile,
				Target:    options.DnsmasqTarget,
				Ownership: domain.ResourceManaged,
				State:     domain.ResourcePlanned,
				Rollback:  rollback,
			},
			"Missing wildcard mapping must be configured.",
		)
	default:
		plan.Blockers = append(plan.Blockers, "dnsmasq-mapping-ownership")
	}
	if fact.ServiceActive && fact.Disposition != domain.PreflightCreate {
		addResource(
			plan,
			domain.InstallationResource{
				ID:        "dnsmasq-service",
				Kind:      domain.ResourceSystemService,
				Target:    options.DnsmasqService,
				Ownership: domain.ResourceAdopted,
				State:     domain.ResourceVerified,
				Rollback:  domain.RollbackPreserve,
			},
			"Active dnsmasq service will be preserved.",
		)
	} else {
		addResource(
			plan,
			domain.InstallationResource{
				ID:        "dnsmasq-service",
				Kind:      domain.ResourceSystemService,
				Target:    options.DnsmasqService,
				Ownership: domain.ResourceManaged,
				State:     domain.ResourcePlanned,
				Rollback:  domain.RollbackRestore,
				Fingerprint: serviceStateFingerprint(
					fact.ServiceActive,
				),
			},
			"dnsmasq must reload managed configuration and return to its prior state on rollback.",
		)
	}
}

func addResolver(plan *domain.InstallationPlan) {
	fact := plan.Inventory.Resolver
	switch fact.Disposition {
	case domain.PreflightAdopt:
		addResource(
			plan,
			domain.InstallationResource{
				ID:        "resolver-domain",
				Kind:      domain.ResourceResolverRule,
				Target:    plan.Target.BaseDomain,
				Ownership: domain.ResourceAdopted,
				State:     domain.ResourceVerified,
				Rollback:  domain.RollbackPreserve,
			},
			"Working split-DNS behavior will be recorded and preserved.",
		)
	case domain.PreflightCreate:
		if !fact.ServiceStateKnown {
			plan.Blockers = append(plan.Blockers, "resolver-service-state")
			break
		}
		addResource(
			plan,
			domain.InstallationResource{
				ID:        "resolver-domain",
				Kind:      domain.ResourceResolverRule,
				Target:    plan.Target.BaseDomain,
				Ownership: domain.ResourceManaged,
				State:     domain.ResourcePlanned,
				Rollback:  domain.RollbackRestore,
				Fingerprint: serviceStateFingerprint(
					fact.ServiceActive,
				),
			},
			"Missing split-DNS behavior must be configured and verified.",
		)
	default:
		plan.Blockers = append(plan.Blockers, "resolver-domain-ownership")
	}
}

func serviceStateFingerprint(active bool) string {
	value := "inactive"
	if active {
		value = "active"
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func addTLS(plan *domain.InstallationPlan) {
	fact := plan.Inventory.TLS
	switch fact.Disposition {
	case domain.PreflightAdopt:
		for _, resource := range []domain.InstallationResource{
			{
				ID:          "tls-certificate",
				Kind:        domain.ResourceFile,
				Target:      fact.CertificatePath,
				Ownership:   domain.ResourceAdopted,
				State:       domain.ResourceVerified,
				Rollback:    domain.RollbackPreserve,
				Fingerprint: fact.CertificateFingerprint,
			},
			{
				ID:          "tls-private-key",
				Kind:        domain.ResourceFile,
				Target:      fact.PrivateKeyPath,
				Ownership:   domain.ResourceAdopted,
				State:       domain.ResourceVerified,
				Rollback:    domain.RollbackPreserve,
				Fingerprint: fact.PrivateKeyFingerprint,
			},
			{
				ID:          "tls-trust-anchor",
				Kind:        domain.ResourceTrustAnchor,
				Target:      fact.TrustAnchorPath,
				Ownership:   domain.ResourceAdopted,
				State:       domain.ResourceVerified,
				Rollback:    domain.RollbackPreserve,
				Fingerprint: fact.TrustFingerprint,
			},
		} {
			addResource(
				plan,
				resource,
				"Verified TLS ownership will be recorded and preserved.",
			)
		}
	case domain.PreflightCreate:
		// Generated PKI files and the trust anchor are added from the reviewed
		// managed artifact set after the specification is rendered.
	default:
		plan.Pending = append(
			plan.Pending,
			"local-tls-certificate",
			"local-trust-anchor",
		)
		plan.Blockers = append(plan.Blockers, "tls-ownership")
	}
}

func addManagedArtifactResources(
	plan *domain.InstallationPlan,
	artifacts []domain.InstallationArtifact,
) error {
	for _, artifact := range artifacts {
		if artifact.Kind == domain.ArtifactContainerSpec {
			continue
		}
		existing := -1
		for index, resource := range plan.Resources {
			if resource.Target == artifact.Target &&
				(resource.Kind == domain.ResourceFile ||
					resource.Kind == domain.ResourceTrustAnchor) {
				existing = index
				break
			}
		}
		if existing >= 0 {
			resource := plan.Resources[existing]
			if resource.ID != artifact.ID ||
				resource.Ownership != domain.ResourceManaged {
				return fmt.Errorf(
					"managed artifact %s target %s conflicts with resource %s",
					artifact.ID,
					artifact.Target,
					resource.ID,
				)
			}
			continue
		}
		for _, resource := range plan.Resources {
			if resource.ID == artifact.ID {
				return fmt.Errorf(
					"managed artifact %s conflicts with resource target %s",
					artifact.ID,
					resource.Target,
				)
			}
		}
		kind := domain.ResourceFile
		if artifact.ID == "pki-trust-anchor" {
			kind = domain.ResourceTrustAnchor
		}
		addResource(
			plan,
			domain.InstallationResource{
				ID:        artifact.ID,
				Kind:      kind,
				Target:    artifact.Target,
				Ownership: domain.ResourceManaged,
				State:     domain.ResourcePlanned,
				Rollback:  domain.RollbackRemove,
			},
			"Managed file artifact must be journaled before it is written.",
		)
	}
	return nil
}

func addRuntime(plan *domain.InstallationPlan) {
	fact := plan.Inventory.Runtime
	addRuntimeNetwork(plan, fact.ControlNetwork)
	addRuntimeVolume(plan, fact.ProbeVolume)
	addRuntimeData(plan, fact.DataDisposition, fact.DataPath)
	switch fact.Disposition {
	case domain.PreflightAdopt:
		for _, resource := range []domain.InstallationResource{
			{
				ID:          "docklane-probe",
				Kind:        domain.ResourceDockerContainer,
				Target:      fact.Probe.ContainerName,
				Ownership:   domain.ResourceAdopted,
				State:       domain.ResourceVerified,
				Rollback:    domain.RollbackPreserve,
				Fingerprint: fact.Probe.ImageFingerprint,
			},
			{
				ID:          "docklane-controller",
				Kind:        domain.ResourceDockerContainer,
				Target:      fact.Controller.ContainerName,
				Ownership:   domain.ResourceAdopted,
				State:       domain.ResourceVerified,
				Rollback:    domain.RollbackPreserve,
				Fingerprint: fact.Controller.ImageFingerprint,
			},
		} {
			addResource(
				plan,
				resource,
				"Verified Docklane runtime container will be recorded and preserved.",
			)
		}
	case domain.PreflightCreate:
		for _, container := range []struct {
			id     string
			target string
		}{
			{runtimeProbeName, runtimeProbeName},
			{runtimeControllerResourceID, runtimeControllerName},
		} {
			addResource(
				plan,
				domain.InstallationResource{
					ID:        container.id,
					Kind:      domain.ResourceDockerContainer,
					Target:    container.target,
					Ownership: domain.ResourceManaged,
					State:     domain.ResourcePlanned,
					Rollback:  domain.RollbackRemove,
				},
				"Missing Docklane runtime container must be created.",
			)
		}
	default:
		plan.Blockers = append(plan.Blockers, "docklane-runtime-ownership")
	}
}

const (
	runtimeControllerResourceID = "docklane-controller"
	runtimeControllerName       = "docklane"
	runtimeProbeName            = "docklane-probe"
)

func addRuntimeNetwork(
	plan *domain.InstallationPlan,
	fact domain.PreflightNetwork,
) {
	addRuntimeFoundationResource(
		plan,
		"docklane-control-network",
		domain.ResourceDockerNetwork,
		fact.Name,
		fact.Disposition,
	)
}

func addRuntimeVolume(
	plan *domain.InstallationPlan,
	fact domain.PreflightVolume,
) {
	addRuntimeFoundationResource(
		plan,
		"docklane-probe-volume",
		domain.ResourceDockerVolume,
		fact.Name,
		fact.Disposition,
	)
}

func addRuntimeData(
	plan *domain.InstallationPlan,
	disposition domain.PreflightDisposition,
	path string,
) {
	addRuntimeFoundationResource(
		plan,
		"docklane-data",
		domain.ResourceDirectory,
		path,
		disposition,
	)
}

func addRuntimeFoundationResource(
	plan *domain.InstallationPlan,
	id string,
	kind domain.ResourceKind,
	target string,
	disposition domain.PreflightDisposition,
) {
	resource := domain.InstallationResource{
		ID:     id,
		Kind:   kind,
		Target: target,
	}
	switch disposition {
	case domain.PreflightAdopt:
		resource.Ownership = domain.ResourceAdopted
		resource.State = domain.ResourceVerified
		resource.Rollback = domain.RollbackPreserve
	case domain.PreflightCreate:
		resource.Ownership = domain.ResourceManaged
		resource.State = domain.ResourcePlanned
		resource.Rollback = domain.RollbackRemove
	default:
		plan.Blockers = append(plan.Blockers, id+"-ownership")
		return
	}
	addResource(
		plan,
		resource,
		"Docklane runtime foundation ownership will be recorded explicitly.",
	)
}

func addManifestOperation(plan *domain.InstallationPlan) {
	if plan.Inventory.Manifest.Exists {
		return
	}
	plan.Operations = append(
		[]domain.InstallationOperation{{
			ID:       "create-install-manifest",
			Action:   domain.InstallationCreateManifest,
			Kind:     domain.ResourceFile,
			Target:   plan.Target.ManifestPath,
			Reason:   "Create the ownership journal before any host mutation.",
			Mutating: true,
		}},
		plan.Operations...,
	)
}

func addResource(
	plan *domain.InstallationPlan,
	resource domain.InstallationResource,
	reason string,
) {
	plan.Resources = append(plan.Resources, resource)
	action := domain.InstallationAdopt
	mutating := false
	if resource.Ownership == domain.ResourceManaged {
		mutating = true
		if resource.Rollback == domain.RollbackRemove {
			action = domain.InstallationCreate
		} else {
			action = domain.InstallationConfigure
		}
	}
	plan.Operations = append(plan.Operations, domain.InstallationOperation{
		ID:         string(action) + "-" + resource.ID,
		Action:     action,
		ResourceID: resource.ID,
		Kind:       resource.Kind,
		Target:     resource.Target,
		Reason:     reason,
		Mutating:   mutating,
	})
}

func fingerprint(plan domain.InstallationPlan) (string, error) {
	plan.Token = ""
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("encode installation plan: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func unique(values []string) []string {
	if len(values) == 0 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}
