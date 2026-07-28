package domain

const InstallationPlanSchemaVersion = 1

type InstallationAction string

const (
	InstallationAdopt          InstallationAction = "adopt"
	InstallationCreate         InstallationAction = "create"
	InstallationConfigure      InstallationAction = "configure"
	InstallationCreateManifest InstallationAction = "create_manifest"
)

type InstallationOperation struct {
	ID         string             `json:"id"`
	Action     InstallationAction `json:"action"`
	ResourceID string             `json:"resourceId,omitempty"`
	Kind       ResourceKind       `json:"kind"`
	Target     string             `json:"target"`
	Reason     string             `json:"reason"`
	Mutating   bool               `json:"mutating"`
}

type InstallationPlan struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Token         string                  `json:"token"`
	Ready         bool                    `json:"ready"`
	Complete      bool                    `json:"complete"`
	Status        DiagnosticStatus        `json:"status"`
	Target        PreflightTarget         `json:"target"`
	Inventory     PreflightInventory      `json:"inventory"`
	Resources     []InstallationResource  `json:"resources"`
	Operations    []InstallationOperation `json:"operations"`
	Blockers      []string                `json:"blockers"`
	Pending       []string                `json:"pending"`
}
