package traefik

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func Validate(configuration Configuration) error {
	if configuration.HTTP.Routers == nil || configuration.HTTP.Services == nil {
		return fmt.Errorf("http routers and services must be present")
	}
	referenced := map[string]bool{}
	for name, router := range configuration.HTTP.Routers {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("router name is empty")
		}
		if !validHostRule(router.Rule) {
			return fmt.Errorf("router %s has invalid Host rule %q", name, router.Rule)
		}
		if len(router.EntryPoints) != 1 || router.EntryPoints[0] != "websecure" {
			return fmt.Errorf("router %s must use only the websecure entrypoint", name)
		}
		if router.Service == "" {
			return fmt.Errorf("router %s has no service", name)
		}
		if _, exists := configuration.HTTP.Services[router.Service]; !exists {
			return fmt.Errorf(
				"router %s references missing service %s",
				name,
				router.Service,
			)
		}
		referenced[router.Service] = true
	}
	for name, service := range configuration.HTTP.Services {
		if !referenced[name] {
			return fmt.Errorf("service %s is not referenced by a router", name)
		}
		if len(service.LoadBalancer.Servers) != 1 {
			return fmt.Errorf("service %s must contain exactly one server", name)
		}
		if err := validateServerURL(service.LoadBalancer.Servers[0].URL); err != nil {
			return fmt.Errorf("service %s: %w", name, err)
		}
	}
	return nil
}

func EncodeValidated(configuration Configuration) ([]byte, string, error) {
	if err := Validate(configuration); err != nil {
		return nil, "", err
	}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		return nil, "", err
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(encoded))
	return encoded, fingerprint, nil
}

func DecodeValidated(encoded []byte) (Configuration, error) {
	var configuration Configuration
	if err := json.Unmarshal(encoded, &configuration); err != nil {
		return Configuration{}, err
	}
	if err := Validate(configuration); err != nil {
		return Configuration{}, err
	}
	return configuration, nil
}

func DecodeValidatedSnapshot(
	encoded []byte,
	expectedFingerprint string,
) (Configuration, error) {
	actualFingerprint := fmt.Sprintf("%x", sha256.Sum256(encoded))
	if actualFingerprint != expectedFingerprint {
		return Configuration{}, fmt.Errorf(
			"provider snapshot fingerprint mismatch: got %s, want %s",
			actualFingerprint,
			expectedFingerprint,
		)
	}
	return DecodeValidated(encoded)
}

func validHostRule(rule string) bool {
	const prefix = "Host(`"
	const suffix = "`)"
	if !strings.HasPrefix(rule, prefix) || !strings.HasSuffix(rule, suffix) {
		return false
	}
	host := strings.TrimSuffix(strings.TrimPrefix(rule, prefix), suffix)
	return host != "" && !strings.ContainsAny(host, "`/ :")
}

func validateServerURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("invalid server URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("server URL scheme must be http or https")
	}
	if parsed.Hostname() == "" || parsed.User != nil {
		return fmt.Errorf("server URL must contain only a hostname and port")
	}
	if parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("server URL must not contain a path, query, or fragment")
	}
	port := parsed.Port()
	if port == "" {
		return fmt.Errorf("server URL must include an explicit port")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("server URL has invalid port %q", port)
	}
	return nil
}
