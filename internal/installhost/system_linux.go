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
	DnsmasqBinary string
	Systemctl     string
	Resolvectl    string
	UpdateCATrust string
	TrustBundle   string
}

func ArchSystemdProfile() SystemProfile {
	return SystemProfile{
		DnsmasqBinary: "/usr/bin/dnsmasq",
		Systemctl:     "/usr/bin/systemctl",
		Resolvectl:    "/usr/bin/resolvectl",
		UpdateCATrust: "/usr/bin/update-ca-trust",
		TrustBundle:   "/etc/ca-certificates/extracted/tls-ca-bundle.pem",
	}
}

type SystemBackend struct {
	profile SystemProfile
}

func NewSystemBackend(profile SystemProfile) (*SystemBackend, error) {
	for label, path := range map[string]string{
		"dnsmasq":         profile.DnsmasqBinary,
		"systemctl":       profile.Systemctl,
		"resolvectl":      profile.Resolvectl,
		"update-ca-trust": profile.UpdateCATrust,
	} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, fmt.Errorf("%s executable path must be absolute and canonical", label)
		}
	}
	if !filepath.IsAbs(profile.TrustBundle) ||
		filepath.Clean(profile.TrustBundle) != profile.TrustBundle {
		return nil, errors.New("trust bundle path must be absolute and canonical")
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
	return backend.run(ctx, backend.profile.DnsmasqBinary, "--test")
}

func (backend *SystemBackend) RefreshTrustStore(
	ctx context.Context,
	profile string,
) error {
	if profile != TrustProfileP11Kit {
		return fmt.Errorf("unsupported trust profile %q", profile)
	}
	return backend.run(ctx, backend.profile.UpdateCATrust, "extract")
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
	return net.DefaultResolver.LookupHost(ctx, hostname)
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
