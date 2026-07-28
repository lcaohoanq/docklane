package domain

import "time"

type PreflightTarget struct {
	BaseDomain   string `json:"baseDomain"`
	ProxyNetwork string `json:"proxyNetwork"`
	DockerSocket string `json:"dockerSocket"`
	ManifestPath string `json:"manifestPath"`
}

type PreflightReport struct {
	Status      DiagnosticStatus  `json:"status"`
	GeneratedAt time.Time         `json:"generatedAt"`
	Target      PreflightTarget   `json:"target"`
	Checks      []DiagnosticCheck `json:"checks"`
}
