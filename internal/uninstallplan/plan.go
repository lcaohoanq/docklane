package uninstallplan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"

	"docklane.local/docklane/internal/domain"
)

const SchemaVersion = 1

func Build(
	manifest domain.InstallationManifest,
	manifestPath string,
) (domain.UninstallationPlan, error) {
	if err := manifest.Validate(); err != nil {
		return domain.UninstallationPlan{}, err
	}
	if !filepath.IsAbs(manifestPath) ||
		filepath.Clean(manifestPath) != manifestPath {
		return domain.UninstallationPlan{}, fmt.Errorf(
			"manifest path must be absolute and canonical",
		)
	}
	plan := domain.UninstallationPlan{
		SchemaVersion:  SchemaVersion,
		Status:         domain.DiagnosticPass,
		ManifestPath:   manifestPath,
		InstallationID: manifest.InstallationID,
		Generation:     manifest.Generation,
		Operations:     []domain.InstallationOperation{},
		Blockers:       []string{},
	}
	if manifest.State != domain.InstallationInstalled {
		plan.Status = domain.DiagnosticFail
		plan.Blockers = append(
			plan.Blockers,
			"installation-state-"+string(manifest.State),
		)
	}
	for index := len(manifest.Resources) - 1; index >= 0; index-- {
		resource := manifest.Resources[index]
		operation, err := reverseOperation(resource)
		if err != nil {
			plan.Status = domain.DiagnosticFail
			plan.Blockers = append(plan.Blockers, resource.ID+"-rollback")
			continue
		}
		plan.Operations = append(plan.Operations, operation)
	}
	plan.Ready = len(plan.Blockers) == 0
	token, err := fingerprint(plan)
	if err != nil {
		return domain.UninstallationPlan{}, err
	}
	plan.Token = token
	return plan, nil
}

func reverseOperation(
	resource domain.InstallationResource,
) (domain.InstallationOperation, error) {
	operation := domain.InstallationOperation{
		ResourceID: resource.ID,
		Kind:       resource.Kind,
		Target:     resource.Target,
	}
	switch {
	case resource.Ownership == domain.ResourceAdopted &&
		resource.Rollback == domain.RollbackPreserve:
		operation.ID = "preserve-" + resource.ID
		operation.Action = domain.InstallationPreserve
		operation.Reason = "Resource predates Docklane ownership and must be preserved."
		operation.Mutating = false
	case resource.Ownership == domain.ResourceManaged &&
		resource.Rollback == domain.RollbackRemove:
		operation.ID = "remove-" + resource.ID
		operation.Action = domain.InstallationRemove
		operation.Reason = "Docklane created this resource; uninstall may remove it."
		operation.Mutating = true
	case resource.Ownership == domain.ResourceManaged &&
		resource.Rollback == domain.RollbackRestore:
		operation.ID = "restore-" + resource.ID
		operation.Action = domain.InstallationRestore
		operation.Reason = "Restore the exact prior state recorded before installation."
		operation.Mutating = true
		if resource.Backup != nil {
			backup := *resource.Backup
			operation.Backup = &backup
		}
	default:
		return domain.InstallationOperation{}, fmt.Errorf(
			"resource %s has no executable rollback contract",
			resource.ID,
		)
	}
	return operation, nil
}

func fingerprint(plan domain.UninstallationPlan) (string, error) {
	plan.Token = ""
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
