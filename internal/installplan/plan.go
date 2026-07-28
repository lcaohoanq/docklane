package installplan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	"docklane.local/docklane/internal/domain"
)

type Options struct {
	DnsmasqTarget  string
	DnsmasqService string
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
		Pending: []string{
			"docklane-runtime",
			"local-tls-certificate",
			"local-trust-anchor",
		},
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
	addManifestOperation(&plan)
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
	if fact.ServiceActive {
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
			},
			"Inactive dnsmasq service must be started and later restored.",
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
		addResource(
			plan,
			domain.InstallationResource{
				ID:        "resolver-domain",
				Kind:      domain.ResourceResolverRule,
				Target:    plan.Target.BaseDomain,
				Ownership: domain.ResourceManaged,
				State:     domain.ResourcePlanned,
				Rollback:  domain.RollbackRestore,
			},
			"Missing split-DNS behavior must be configured and verified.",
		)
	default:
		plan.Blockers = append(plan.Blockers, "resolver-domain-ownership")
	}
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
