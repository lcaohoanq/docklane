package domain

import "time"

type ControllerHealth struct {
	Status              string         `json:"status"`
	BaseDomain          string         `json:"baseDomain"`
	ProxyNetwork        string         `json:"proxyNetwork,omitempty"`
	LastReconciledAt    time.Time      `json:"lastReconciledAt"`
	LastReconcileError  string         `json:"lastReconcileError"`
	ReconcileIntervalMS int64          `json:"reconcileIntervalMs"`
	Provider            ProviderStatus `json:"provider"`
}
