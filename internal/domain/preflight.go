package domain

import "time"

type PreflightTarget struct {
	BaseDomain   string `json:"baseDomain"`
	ProxyNetwork string `json:"proxyNetwork"`
	DockerSocket string `json:"dockerSocket"`
	ManifestPath string `json:"manifestPath"`
}

type PreflightDisposition string

const (
	PreflightCreate   PreflightDisposition = "create"
	PreflightAdopt    PreflightDisposition = "adopt"
	PreflightConflict PreflightDisposition = "conflict"
	PreflightUnknown  PreflightDisposition = "unknown"
)

type PreflightGateway struct {
	Disposition   PreflightDisposition `json:"disposition"`
	ContainerID   string               `json:"containerId,omitempty"`
	ContainerName string               `json:"containerName,omitempty"`
	Image         string               `json:"image,omitempty"`
}

type PreflightNetwork struct {
	Disposition PreflightDisposition `json:"disposition"`
	Name        string               `json:"name"`
	ID          string               `json:"id,omitempty"`
}

type PreflightDNS struct {
	Disposition   PreflightDisposition `json:"disposition"`
	MappingPaths  []string             `json:"mappingPaths"`
	ConfigPaths   []string             `json:"configPaths"`
	ServiceActive bool                 `json:"serviceActive"`
}

type PreflightResolver struct {
	Disposition PreflightDisposition `json:"disposition"`
	Addresses   []string             `json:"addresses"`
}

type PreflightManifest struct {
	Exists         bool              `json:"exists"`
	InstallationID string            `json:"installationId,omitempty"`
	State          InstallationState `json:"state,omitempty"`
	Generation     uint64            `json:"generation,omitempty"`
}

type PreflightInventory struct {
	Gateway  PreflightGateway  `json:"gateway"`
	Network  PreflightNetwork  `json:"network"`
	DNS      PreflightDNS      `json:"dns"`
	Resolver PreflightResolver `json:"resolver"`
	Manifest PreflightManifest `json:"manifest"`
}

type PreflightReport struct {
	Status      DiagnosticStatus   `json:"status"`
	GeneratedAt time.Time          `json:"generatedAt"`
	Target      PreflightTarget    `json:"target"`
	Inventory   PreflightInventory `json:"inventory"`
	Checks      []DiagnosticCheck  `json:"checks"`
}
