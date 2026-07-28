package config

import (
	"fmt"
	"net"
	"strings"
	"time"
)

type Config struct {
	ListenAddress  string
	DatabasePath   string
	BaseDomain     string
	DockerSocket   string
	ProxyNetwork   string
	ProbeSocket    string
	TraefikAPIURL  string
	TraefikAPIAddr string
	TraefikAPIUser string
	TraefikAPIPass string
	TraefikAPICA   string
	HistoryEvery   time.Duration
	HistoryLimit   int
	ManageNetworks bool
	ReconcileEvery time.Duration
}

func (c Config) Validate() error {
	if _, _, err := net.SplitHostPort(c.ListenAddress); err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	if strings.TrimSpace(c.DatabasePath) == "" {
		return fmt.Errorf("database path is required")
	}
	if strings.TrimSpace(c.DockerSocket) == "" {
		return fmt.Errorf("Docker socket path is required")
	}
	if c.ManageNetworks && strings.TrimSpace(c.ProxyNetwork) == "" {
		return fmt.Errorf("proxy network is required when network attachment is enabled")
	}
	traefikRuntimeValues := []string{
		c.TraefikAPIURL,
		c.TraefikAPIAddr,
		c.TraefikAPIUser,
		c.TraefikAPIPass,
		c.TraefikAPICA,
	}
	configured := 0
	for _, value := range traefikRuntimeValues {
		if strings.TrimSpace(value) != "" {
			configured++
		}
	}
	if configured != 0 && configured != len(traefikRuntimeValues) {
		return fmt.Errorf("all Traefik runtime API settings must be configured together")
	}
	if strings.TrimSpace(c.BaseDomain) == "" || strings.ContainsAny(c.BaseDomain, "/: ") {
		return fmt.Errorf("invalid base domain %q", c.BaseDomain)
	}
	if c.ReconcileEvery <= 0 {
		return fmt.Errorf("reconcile interval must be greater than zero")
	}
	if c.HistoryEvery <= 0 {
		return fmt.Errorf("health history interval must be greater than zero")
	}
	if c.HistoryLimit <= 0 || c.HistoryLimit > 10000 {
		return fmt.Errorf("health history limit must be between 1 and 10000")
	}
	return nil
}
