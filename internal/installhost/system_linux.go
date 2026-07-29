//go:build linux

package installhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type SystemProfile struct {
	Name                 string
	TrustProfile         string
	DnsmasqBinary        string
	DnsmasqValidator     string
	DnsmasqValidatorArgs []string
	DnsmasqIncludeConfig string
	ManagedDnsmasqConfig string
	Systemctl            string
	Resolvectl           string
	UpdateCATrust        string
	UpdateCATrustArgs    []string
	TrustBundle          string
	ManagedTrustAnchor   string
	PreflightTrustAnchor string
}

func ArchSystemdProfile() SystemProfile {
	return SystemProfile{
		Name:                 HostProfileArchSystemd,
		TrustProfile:         TrustProfileP11Kit,
		DnsmasqBinary:        "/usr/bin/dnsmasq",
		DnsmasqValidator:     "/usr/bin/dnsmasq",
		DnsmasqValidatorArgs: []string{"--test"},
		DnsmasqIncludeConfig: "/etc/dnsmasq.conf",
		ManagedDnsmasqConfig: "/etc/dnsmasq.conf",
		Systemctl:            "/usr/bin/systemctl",
		Resolvectl:           "/usr/bin/resolvectl",
		UpdateCATrust:        "/usr/bin/update-ca-trust",
		UpdateCATrustArgs:    []string{"extract"},
		TrustBundle:          "/etc/ca-certificates/extracted/tls-ca-bundle.pem",
		ManagedTrustAnchor:   "/etc/ca-certificates/trust-source/anchors/docklane-local-root-ca.crt",
		PreflightTrustAnchor: "/etc/ca-certificates/trust-source/anchors/traefik-lab-root-ca.crt",
	}
}

func DebianSystemdProfile() SystemProfile {
	return SystemProfile{
		Name:                 HostProfileDebianSystemd,
		TrustProfile:         TrustProfileDebianCA,
		DnsmasqBinary:        "/usr/sbin/dnsmasq",
		DnsmasqValidator:     "/usr/share/dnsmasq/systemd-helper",
		DnsmasqValidatorArgs: []string{"checkconfig"},
		DnsmasqIncludeConfig: "/etc/default/dnsmasq",
		ManagedDnsmasqConfig: "/etc/dnsmasq.d/docklane.conf",
		Systemctl:            "/usr/bin/systemctl",
		Resolvectl:           "/usr/bin/resolvectl",
		UpdateCATrust:        "/usr/sbin/update-ca-certificates",
		TrustBundle:          "/etc/ssl/certs/ca-certificates.crt",
		ManagedTrustAnchor:   "/usr/local/share/ca-certificates/docklane-local-root-ca.crt",
		PreflightTrustAnchor: "/usr/local/share/ca-certificates/docklane-local-root-ca.crt",
	}
}

type SystemBackend struct {
	profile SystemProfile
}

func NewSystemBackend(profile SystemProfile) (*SystemBackend, error) {
	if profile.Name == "" || !SupportedTrustProfile(profile.TrustProfile) {
		return nil, errors.New("system host profile name and trust profile are required")
	}
	for label, path := range map[string]string{
		"dnsmasq":           profile.DnsmasqBinary,
		"dnsmasq validator": profile.DnsmasqValidator,
		"systemctl":         profile.Systemctl,
		"resolvectl":        profile.Resolvectl,
		"update-ca-trust":   profile.UpdateCATrust,
	} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, fmt.Errorf("%s executable path must be absolute and canonical", label)
		}
	}
	if !filepath.IsAbs(profile.TrustBundle) ||
		filepath.Clean(profile.TrustBundle) != profile.TrustBundle {
		return nil, errors.New("trust bundle path must be absolute and canonical")
	}
	if !filepath.IsAbs(profile.DnsmasqIncludeConfig) ||
		filepath.Clean(profile.DnsmasqIncludeConfig) !=
			profile.DnsmasqIncludeConfig {
		return nil, errors.New(
			"dnsmasq include config path must be absolute and canonical",
		)
	}
	if !filepath.IsAbs(profile.ManagedDnsmasqConfig) ||
		filepath.Clean(profile.ManagedDnsmasqConfig) !=
			profile.ManagedDnsmasqConfig {
		return nil, errors.New(
			"managed dnsmasq config path must be absolute and canonical",
		)
	}
	return &SystemBackend{profile: profile}, nil
}

func (backend *SystemBackend) ServiceState(
	ctx context.Context,
	service string,
) (ServiceState, error) {
	command := exec.CommandContext(
		ctx,
		backend.profile.Systemctl,
		"is-active",
		"--quiet",
		service,
	)
	err := command.Run()
	if err == nil {
		return ServiceState{Active: true}, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		switch exitError.ExitCode() {
		case 3, 4:
			return ServiceState{}, nil
		}
	}
	return ServiceState{}, err
}

func (backend *SystemBackend) ValidateDNSConfiguration(
	ctx context.Context,
) error {
	return backend.run(
		ctx,
		backend.profile.DnsmasqValidator,
		backend.profile.DnsmasqValidatorArgs...,
	)
}

func (backend *SystemBackend) RefreshTrustStore(
	ctx context.Context,
	profile string,
) error {
	if profile != backend.profile.TrustProfile {
		return fmt.Errorf("unsupported trust profile %q", profile)
	}
	return backend.run(
		ctx,
		backend.profile.UpdateCATrust,
		backend.profile.UpdateCATrustArgs...,
	)
}

func (backend *SystemBackend) StartService(
	ctx context.Context,
	service string,
) error {
	return backend.run(ctx, backend.profile.Systemctl, "start", service)
}

func (backend *SystemBackend) RestartService(
	ctx context.Context,
	service string,
) error {
	return backend.run(ctx, backend.profile.Systemctl, "restart", service)
}

func (backend *SystemBackend) StopService(
	ctx context.Context,
	service string,
) error {
	return backend.run(ctx, backend.profile.Systemctl, "stop", service)
}

func (backend *SystemBackend) FlushResolverCache(
	ctx context.Context,
	profile string,
) error {
	if profile != ResolverProfileSystemd {
		return fmt.Errorf("unsupported resolver profile %q", profile)
	}
	return backend.run(ctx, backend.profile.Resolvectl, "flush-caches")
}

func (backend *SystemBackend) LookupHost(
	ctx context.Context,
	hostname string,
) ([]string, error) {
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(
			ctx context.Context,
			network string,
			_ string,
		) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(
				ctx,
				network,
				"127.0.0.53:53",
			)
		},
	}
	return resolver.LookupHost(ctx, hostname)
}

func (backend *SystemBackend) VerifyTrustAnchor(
	_ context.Context,
	path string,
	expectedFingerprint string,
) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(content)
	if hex.EncodeToString(sum[:]) != expectedFingerprint {
		return errors.New("trust anchor file fingerprint changed")
	}
	block, rest := pem.Decode(content)
	if block == nil ||
		block.Type != "CERTIFICATE" ||
		len(strings.TrimSpace(string(rest))) != 0 {
		return errors.New("trust anchor is not one certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	bundle, err := os.ReadFile(backend.profile.TrustBundle)
	if err != nil {
		return err
	}
	for len(bundle) != 0 {
		var candidate *pem.Block
		candidate, bundle = pem.Decode(bundle)
		if candidate == nil {
			break
		}
		if candidate.Type == "CERTIFICATE" &&
			bytes.Equal(candidate.Bytes, certificate.Raw) {
			return nil
		}
	}
	return errors.New("trust anchor is absent from the refreshed p11-kit bundle")
}

func (backend *SystemBackend) run(
	ctx context.Context,
	executable string,
	arguments ...string,
) error {
	output, err := exec.CommandContext(ctx, executable, arguments...).CombinedOutput()
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if len(message) > 4096 {
		message = message[:4096]
	}
	if message == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, message)
}
