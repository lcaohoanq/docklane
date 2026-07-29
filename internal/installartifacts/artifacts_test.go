package installartifacts

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"reflect"
	"strings"
	"testing"
	"time"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installspec"
)

func managedSpecification(t *testing.T) domain.InstallationSpecification {
	t.Helper()
	specification, err := installspec.Build(installspec.Config{
		BaseDomain:      "docker.home.arpa",
		ProxyNetwork:    "proxy",
		DockerSocket:    "/var/run/docker.sock",
		StateDirectory:  "/var/lib/docklane",
		DataDirectory:   "/var/lib/docklane/data",
		DnsmasqConfig:   "/etc/dnsmasq.d/docklane.conf",
		TrustAnchorPath: "/etc/ca-certificates/trust-source/anchors/docklane-local-root-ca.crt",
		TraefikImage:    "traefik:v3.7",
		DocklaneImage:   "docklane:local",
	})
	if err != nil {
		t.Fatal(err)
	}
	return specification
}

func TestBuildRendersDeterministicSafeArtifacts(t *testing.T) {
	specification := managedSpecification(t)
	first, err := Build(specification)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(specification)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("artifact rendering is not deterministic")
	}
	if len(first) != 13 {
		t.Fatalf("artifact count = %d, want 13", len(first))
	}

	byID := map[string]domain.InstallationArtifact{}
	for _, artifact := range first {
		if err := artifact.Validate(); err != nil {
			t.Fatalf("%s: %v", artifact.ID, err)
		}
		byID[artifact.ID] = artifact
		if artifact.Sensitive && artifact.Content != "" {
			t.Fatalf("sensitive artifact %s exposed content", artifact.ID)
		}
	}
	if got := byID["dnsmasq-domain"].Content; got !=
		"# Managed by Docklane installation manifest\n"+
			"address=/.docker.home.arpa/127.0.0.1\n" {
		t.Fatalf("dnsmasq content = %q", got)
	}
	dynamic := byID["traefik-dynamic-config"].Content
	for _, required := range []string{
		"Host(`traefik.docker.home.arpa`)",
		"usersFile: /run/secrets/traefik-dashboard-users",
		"certFile: /certs/local.crt",
		"keyFile: /certs/local.key",
	} {
		if !strings.Contains(dynamic, required) {
			t.Fatalf("Traefik config omits %q:\n%s", required, dynamic)
		}
	}
	if got := byID["resolver-config"].Content; got !=
		"# Managed by Docklane installation manifest\n"+
			"[Resolve]\n"+
			"DNS=127.0.0.1\n"+
			"Domains=~docker.home.arpa\n" {
		t.Fatalf("resolver content = %q", got)
	}
	if !byID["pki-root-private-key"].GeneratedAtApply ||
		!byID["traefik-dashboard-password"].GeneratedAtApply {
		t.Fatal("private material was not deferred until apply")
	}
}

func TestBuildRendersDebianDnsmasqLoopbackBinding(t *testing.T) {
	specification := managedSpecification(t)
	specification.Host.PlatformProfile = "debian-systemd"
	specification.Host.TrustProfile = "debian-ca-certificates"
	specification.PKI.TrustAnchorPath =
		"/usr/local/share/ca-certificates/docklane-local-root-ca.crt"
	artifacts, err := Build(specification)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range artifacts {
		if artifact.ID != "dnsmasq-domain" {
			continue
		}
		want := "# Managed by Docklane installation manifest\n" +
			"bind-interfaces\n" +
			"listen-address=127.0.0.1\n" +
			"address=/.docker.home.arpa/127.0.0.1\n"
		if artifact.Content != want {
			t.Fatalf("dnsmasq content = %q, want %q", artifact.Content, want)
		}
		return
	}
	t.Fatal("dnsmasq artifact is missing")
}

func TestGeneratePKICoversApexAndWildcardSANs(t *testing.T) {
	specification := managedSpecification(t)
	now := time.Date(2026, 7, 28, 8, 30, 0, 0, time.UTC)
	bundle, err := GeneratePKI(specification, now, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	root := parseCertificate(t, bundle.RootCertificate)
	leaf := parseCertificate(t, bundle.LeafCertificate)
	if !root.IsCA || root.Subject.CommonName != specification.PKI.RootCommonName {
		t.Fatalf("root certificate = %#v", root)
	}
	if !reflect.DeepEqual(leaf.DNSNames, specification.PKI.DNSNames) {
		t.Fatalf("leaf SANs = %v, want %v", leaf.DNSNames, specification.PKI.DNSNames)
	}
	for _, hostname := range []string{
		"docker.home.arpa",
		"excalidraw.docker.home.arpa",
	} {
		if err := leaf.VerifyHostname(hostname); err != nil {
			t.Fatalf("verify hostname %s: %v", hostname, err)
		}
	}
	if err := leaf.CheckSignatureFrom(root); err != nil {
		t.Fatalf("verify leaf signature: %v", err)
	}
	if got, want := root.NotAfter, now.AddDate(0, 0, specification.PKI.RootValidityDays); !got.Equal(want) {
		t.Fatalf("root expiry = %s, want %s", got, want)
	}
	if got, want := leaf.NotAfter, now.AddDate(0, 0, specification.PKI.LeafValidityDays); !got.Equal(want) {
		t.Fatalf("leaf expiry = %s, want %s", got, want)
	}

	rootKey := parsePrivateKey(t, bundle.RootPrivateKey)
	leafKey := parsePrivateKey(t, bundle.LeafPrivateKey)
	if root.PublicKey.(*rsa.PublicKey).N.Cmp(rootKey.N) != 0 {
		t.Fatal("root private key does not match certificate")
	}
	if leaf.PublicKey.(*rsa.PublicKey).N.Cmp(leafKey.N) != 0 {
		t.Fatal("leaf private key does not match certificate")
	}
}

func parseCertificate(t *testing.T, content []byte) *x509.Certificate {
	t.Helper()
	block, rest := pem.Decode(content)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		t.Fatal("invalid certificate PEM")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

func parsePrivateKey(t *testing.T, content []byte) *rsa.PrivateKey {
	t.Helper()
	block, rest := pem.Decode(content)
	if block == nil || block.Type != "RSA PRIVATE KEY" || len(rest) != 0 {
		t.Fatal("invalid RSA private key PEM")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
