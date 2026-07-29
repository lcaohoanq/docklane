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
	Disposition       PreflightDisposition `json:"disposition"`
	Addresses         []string             `json:"addresses"`
	ServiceActive     bool                 `json:"serviceActive"`
	ServiceStateKnown bool                 `json:"serviceStateKnown"`
}

type PreflightManifest struct {
	Exists         bool              `json:"exists"`
	InstallationID string            `json:"installationId,omitempty"`
	State          InstallationState `json:"state,omitempty"`
	Generation     uint64            `json:"generation,omitempty"`
}

type PreflightTLS struct {
	Disposition            PreflightDisposition `json:"disposition"`
	CertificatePath        string               `json:"certificatePath,omitempty"`
	PrivateKeyPath         string               `json:"privateKeyPath,omitempty"`
	TrustAnchorPath        string               `json:"trustAnchorPath,omitempty"`
	CertificateFingerprint string               `json:"certificateFingerprint,omitempty"`
	PrivateKeyFingerprint  string               `json:"privateKeyFingerprint,omitempty"`
	TrustFingerprint       string               `json:"trustFingerprint,omitempty"`
	NotAfter               time.Time            `json:"notAfter,omitempty"`
	DNSNames               []string             `json:"dnsNames"`
}

type PreflightRuntimeContainer struct {
	ContainerID      string `json:"containerId,omitempty"`
	ContainerName    string `json:"containerName,omitempty"`
	Image            string `json:"image,omitempty"`
	ImageFingerprint string `json:"imageFingerprint,omitempty"`
	Health           string `json:"health,omitempty"`
}

type PreflightVolume struct {
	Disposition PreflightDisposition `json:"disposition"`
	Name        string               `json:"name"`
	Driver      string               `json:"driver,omitempty"`
}

type PreflightRuntime struct {
	Disposition     PreflightDisposition      `json:"disposition"`
	Controller      PreflightRuntimeContainer `json:"controller"`
	Probe           PreflightRuntimeContainer `json:"probe"`
	ControlNetwork  PreflightNetwork          `json:"controlNetwork"`
	ProbeVolume     PreflightVolume           `json:"probeVolume"`
	DataDisposition PreflightDisposition      `json:"dataDisposition"`
	DataPath        string                    `json:"dataPath"`
}

type PreflightInventory struct {
	Gateway  PreflightGateway  `json:"gateway"`
	Network  PreflightNetwork  `json:"network"`
	DNS      PreflightDNS      `json:"dns"`
	Resolver PreflightResolver `json:"resolver"`
	Manifest PreflightManifest `json:"manifest"`
	TLS      PreflightTLS      `json:"tls"`
	Runtime  PreflightRuntime  `json:"runtime"`
}

type PreflightReport struct {
	Status      DiagnosticStatus   `json:"status"`
	GeneratedAt time.Time          `json:"generatedAt"`
	Target      PreflightTarget    `json:"target"`
	Inventory   PreflightInventory `json:"inventory"`
	Checks      []DiagnosticCheck  `json:"checks"`
}
