package domain

const InstallationPlanSchemaVersion = 1

type InstallationAction string

const (
	InstallationAdopt          InstallationAction = "adopt"
	InstallationCreate         InstallationAction = "create"
	InstallationConfigure      InstallationAction = "configure"
	InstallationCreateManifest InstallationAction = "create_manifest"
	InstallationPreserve       InstallationAction = "preserve"
	InstallationRemove         InstallationAction = "remove"
	InstallationRestore        InstallationAction = "restore"
)

type InstallationOperation struct {
	ID         string             `json:"id"`
	Action     InstallationAction `json:"action"`
	ResourceID string             `json:"resourceId,omitempty"`
	Kind       ResourceKind       `json:"kind"`
	Target     string             `json:"target"`
	Reason     string             `json:"reason"`
	Mutating   bool               `json:"mutating"`
	Backup     *ResourceBackup    `json:"backup,omitempty"`
}

type UninstallationPlan struct {
	SchemaVersion  int                     `json:"schemaVersion"`
	Token          string                  `json:"token"`
	Ready          bool                    `json:"ready"`
	Status         DiagnosticStatus        `json:"status"`
	ManifestPath   string                  `json:"manifestPath"`
	InstallationID string                  `json:"installationId"`
	Generation     uint64                  `json:"generation"`
	Operations     []InstallationOperation `json:"operations"`
	Blockers       []string                `json:"blockers"`
}

type InstallationPlan struct {
	SchemaVersion        int                        `json:"schemaVersion"`
	Token                string                     `json:"token"`
	Ready                bool                       `json:"ready"`
	Complete             bool                       `json:"complete"`
	Status               DiagnosticStatus           `json:"status"`
	Target               PreflightTarget            `json:"target"`
	Inventory            PreflightInventory         `json:"inventory"`
	ManagedSpecification *InstallationSpecification `json:"managedSpecification,omitempty"`
	Resources            []InstallationResource     `json:"resources"`
	Operations           []InstallationOperation    `json:"operations"`
	Blockers             []string                   `json:"blockers"`
	Pending              []string                   `json:"pending"`
}
