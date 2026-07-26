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
	if strings.TrimSpace(c.BaseDomain) == "" || strings.ContainsAny(c.BaseDomain, "/: ") {
		return fmt.Errorf("invalid base domain %q", c.BaseDomain)
	}
	if c.ReconcileEvery <= 0 {
		return fmt.Errorf("reconcile interval must be greater than zero")
	}
	return nil
}
