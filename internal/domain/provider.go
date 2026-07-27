package domain

import "time"

const (
	ProviderSourceAwaiting      = "awaiting-first-poll"
	ProviderSourceLive          = "live"
	ProviderSourceLastKnownGood = "last-known-good"
	ProviderSourceUnavailable   = "unavailable"
)

type ProviderSnapshot struct {
	Configuration []byte
	Fingerprint   string
	GeneratedAt   time.Time
}

type ProviderStatus struct {
	Source      string    `json:"source"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	GeneratedAt time.Time `json:"generatedAt,omitempty"`
	LastError   string    `json:"lastError,omitempty"`
}
