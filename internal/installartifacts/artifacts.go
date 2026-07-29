package installartifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"docklane.local/docklane/internal/domain"
)

func Build(
	specification domain.InstallationSpecification,
) ([]domain.InstallationArtifact, error) {
	if err := specification.Validate(); err != nil {
		return nil, err
	}
	dnsmasq := fmt.Sprintf(
		"# Managed by Docklane installation manifest\n"+
			"bind-interfaces\n"+
			"listen-address=127.0.0.1\n"+
			"address=/.%s/127.0.0.1\n",
		specification.BaseDomain,
	)
	dynamic := fmt.Sprintf(
		"http:\n"+
			"  routers:\n"+
			"    dashboard:\n"+
			"      rule: \"Host(`traefik.%s`)\"\n"+
			"      entryPoints: [websecure]\n"+
			"      service: api@internal\n"+
			"      middlewares: [dashboard-auth]\n"+
			"      tls: {}\n"+
			"  middlewares:\n"+
			"    dashboard-auth:\n"+
			"      basicAuth:\n"+
			"        usersFile: /run/secrets/traefik-dashboard-users\n"+
			"tls:\n"+
			"  certificates:\n"+
			"    - certFile: /certs/local.crt\n"+
			"      keyFile: /certs/local.key\n",
		specification.BaseDomain,
	)
	resolver := fmt.Sprintf(
		"# Managed by Docklane installation manifest\n"+
			"[Resolve]\n"+
			"DNS=127.0.0.1\n"+
			"Domains=~%s\n",
		specification.BaseDomain,
	)
	artifacts := []domain.InstallationArtifact{
		configArtifact(
			"dnsmasq-domain",
			specification.Paths.DnsmasqConfig,
			0o644,
			dnsmasq,
		),
		configArtifact(
			"resolver-config",
			specification.Paths.ResolverConfig,
			0o644,
			resolver,
		),
		configArtifact(
			"traefik-dynamic-config",
			specification.Paths.TraefikDynamicConfig,
			0o644,
			dynamic,
		),
		generatedArtifact(
			"pki-root-private-key",
			domain.ArtifactGeneratedPKI,
			specification.PKI.RootPrivateKeyPath,
			0o600,
			true,
		),
		generatedArtifact(
			"pki-root-certificate",
			domain.ArtifactGeneratedPKI,
			specification.PKI.RootCertificatePath,
			0o644,
			false,
		),
		generatedArtifact(
			"pki-leaf-private-key",
			domain.ArtifactGeneratedPKI,
			specification.PKI.LeafPrivateKeyPath,
			0o600,
			true,
		),
		generatedArtifact(
			"pki-leaf-certificate",
			domain.ArtifactGeneratedPKI,
			specification.PKI.LeafCertificatePath,
			0o644,
			false,
		),
		generatedArtifact(
			"pki-trust-anchor",
			domain.ArtifactGeneratedPKI,
			specification.PKI.TrustAnchorPath,
			0o644,
			false,
		),
		generatedArtifact(
			"traefik-dashboard-password",
			domain.ArtifactGeneratedSecret,
			specification.Paths.DashboardPassword,
			0o600,
			true,
		),
		generatedArtifact(
			"traefik-dashboard-users",
			domain.ArtifactGeneratedSecret,
			specification.Paths.DashboardUsers,
			0o600,
			true,
		),
	}
	for _, container := range specification.Containers {
		content, err := json.MarshalIndent(container, "", "  ")
		if err != nil {
			return nil, err
		}
		content = append(content, '\n')
		artifacts = append(artifacts, domain.InstallationArtifact{
			ID:          "container-" + container.Role,
			Kind:        domain.ArtifactContainerSpec,
			Target:      container.Name,
			Fingerprint: sha256Hex(content),
			Content:     string(content),
		})
	}
	return artifacts, nil
}

func configArtifact(
	id string,
	target string,
	mode uint32,
	content string,
) domain.InstallationArtifact {
	return domain.InstallationArtifact{
		ID:          id,
		Kind:        domain.ArtifactConfig,
		Target:      target,
		Mode:        mode,
		Fingerprint: sha256Hex([]byte(content)),
		Content:     content,
	}
}

func generatedArtifact(
	id string,
	kind domain.InstallationArtifactKind,
	target string,
	mode uint32,
	sensitive bool,
) domain.InstallationArtifact {
	return domain.InstallationArtifact{
		ID:               id,
		Kind:             kind,
		Target:           target,
		Mode:             mode,
		Sensitive:        sensitive,
		GeneratedAtApply: true,
	}
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
