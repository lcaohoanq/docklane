package domain

const (
	NetworkOwnershipMissing  = "missing"
	NetworkOwnershipManaged  = "managed"
	NetworkOwnershipExternal = "external"
	NetworkOwnershipConflict = "conflict"

	NetworkActionCreate     = "create"
	NetworkActionConnect    = "connect"
	NetworkActionDisconnect = "disconnect"
)

type NetworkState struct {
	Name       string            `json:"name"`
	ID         string            `json:"id,omitempty"`
	Driver     string            `json:"driver,omitempty"`
	Scope      string            `json:"scope,omitempty"`
	Ownership  string            `json:"ownership"`
	Compatible bool              `json:"compatible"`
	Labels     map[string]string `json:"labels,omitempty"`
}

type NetworkOperation struct {
	Action        string   `json:"action"`
	ContainerID   string   `json:"containerId,omitempty"`
	ContainerName string   `json:"containerName,omitempty"`
	Aliases       []string `json:"aliases,omitempty"`
	Reason        string   `json:"reason"`
	Destructive   bool     `json:"destructive"`
}

type NetworkPlan struct {
	Token      string             `json:"token"`
	Network    NetworkState       `json:"network"`
	Operations []NetworkOperation `json:"operations"`
	Warnings   []string           `json:"warnings,omitempty"`
}

type NetworkApplyResult struct {
	Applied   NetworkPlan `json:"applied"`
	Remaining NetworkPlan `json:"remaining"`
}
