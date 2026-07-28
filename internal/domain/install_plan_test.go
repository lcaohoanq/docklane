package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestInstallationArtifactValidation(t *testing.T) {
	content := "address=/.docker.home.arpa/127.0.0.1\n"
	sum := sha256.Sum256([]byte(content))
	valid := InstallationArtifact{
		ID:          "dnsmasq-domain",
		Kind:        ArtifactConfig,
		Target:      "/etc/dnsmasq.d/docklane.conf",
		Mode:        0o644,
		Fingerprint: hex.EncodeToString(sum[:]),
		Content:     content,
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		change func(*InstallationArtifact)
	}{
		{
			name: "fingerprint mismatch",
			change: func(artifact *InstallationArtifact) {
				artifact.Fingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			},
		},
		{
			name: "relative target",
			change: func(artifact *InstallationArtifact) {
				artifact.Target = "docklane.conf"
			},
		},
		{
			name: "sensitive broad permissions",
			change: func(artifact *InstallationArtifact) {
				artifact.Sensitive = true
			},
		},
		{
			name: "deferred content",
			change: func(artifact *InstallationArtifact) {
				artifact.Kind = ArtifactGeneratedSecret
				artifact.Mode = 0o600
				artifact.Sensitive = true
				artifact.GeneratedAtApply = true
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			artifact := valid
			test.change(&artifact)
			if err := artifact.Validate(); err == nil {
				t.Fatalf("artifact unexpectedly valid: %#v", artifact)
			}
		})
	}

	generated := InstallationArtifact{
		ID:               "pki-leaf-private-key",
		Kind:             ArtifactGeneratedPKI,
		Target:           "/var/lib/docklane/traefik/certs/local.key",
		Mode:             0o600,
		Sensitive:        true,
		GeneratedAtApply: true,
	}
	if err := generated.Validate(); err != nil {
		t.Fatal(err)
	}

	duplicateTarget := valid
	duplicateTarget.ID = "dnsmasq-secret"
	duplicateTarget.Kind = ArtifactGeneratedSecret
	duplicateTarget.Mode = 0o600
	duplicateTarget.Fingerprint = ""
	duplicateTarget.Content = ""
	duplicateTarget.Sensitive = true
	duplicateTarget.GeneratedAtApply = true
	if err := ValidateInstallationArtifacts(
		[]InstallationArtifact{valid, duplicateTarget},
	); err == nil {
		t.Fatal("duplicate file artifact target was accepted")
	}
}
