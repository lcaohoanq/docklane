package domain

import "time"

type DiagnosticStatus string

const (
	DiagnosticPass DiagnosticStatus = "pass"
	DiagnosticWarn DiagnosticStatus = "warn"
	DiagnosticFail DiagnosticStatus = "fail"
)

type DiagnosticCheck struct {
	ID         string           `json:"id"`
	Layer      string           `json:"layer"`
	Status     DiagnosticStatus `json:"status"`
	Summary    string           `json:"summary"`
	Detail     string           `json:"detail,omitempty"`
	Suggestion string           `json:"suggestion,omitempty"`
}

type DiagnosticReport struct {
	Status      DiagnosticStatus  `json:"status"`
	Target      string            `json:"target,omitempty"`
	Hostname    string            `json:"hostname,omitempty"`
	GeneratedAt time.Time         `json:"generatedAt"`
	Checks      []DiagnosticCheck `json:"checks"`
}
