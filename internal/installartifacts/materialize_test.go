package installartifacts

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installfiles"
	"docklane.local/docklane/internal/installspec"
	"golang.org/x/crypto/bcrypt"
)

type noRandomReader struct{}

func (noRandomReader) Read([]byte) (int, error) {
	return 0, errors.New("randomness should not be used")
}

func TestMaterializeFilesStagesAndRollsBackCompleteBundle(t *testing.T) {
	root := t.TempDir()
	specification, err := installspec.Build(installspec.Config{
		BaseDomain:      "docker.home.arpa",
		ProxyNetwork:    "proxy",
		DockerSocket:    "/var/run/docker.sock",
		StateDirectory:  filepath.Join(root, "state"),
		DataDirectory:   filepath.Join(root, "state", "data"),
		DnsmasqConfig:   filepath.Join(root, "dnsmasq", "docklane.conf"),
		ResolverConfig:  filepath.Join(root, "resolved", "docklane.conf"),
		TrustAnchorPath: filepath.Join(root, "trust", "docklane-root.crt"),
		TraefikImage:    "traefik:v3.7",
		DocklaneImage:   "docklane:local",
	})
	if err != nil {
		t.Fatal(err)
	}
	files, err := MaterializeFiles(
		specification,
		time.Date(2026, 7, 28, 8, 30, 0, 0, time.UTC),
		rand.Reader,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ClearMaterializedFiles(files)
	if len(files) != 10 {
		t.Fatalf("materialized files = %d, want 10", len(files))
	}
	byID := map[string]int{}
	for index, file := range files {
		byID[file.ID] = index
	}
	for _, id := range []string{
		"dnsmasq-domain",
		"resolver-config",
		"traefik-dynamic-config",
		"pki-root-private-key",
		"pki-root-certificate",
		"pki-leaf-private-key",
		"pki-leaf-certificate",
		"pki-trust-anchor",
		"traefik-dashboard-password",
		"traefik-dashboard-users",
	} {
		if _, found := byID[id]; !found {
			t.Fatalf("materialized bundle omits %s", id)
		}
	}
	if files[byID["pki-root-private-key"]].Mode.Perm() != 0o600 ||
		files[byID["pki-leaf-private-key"]].Mode.Perm() != 0o600 ||
		files[byID["traefik-dashboard-password"]].Mode.Perm() != 0o600 {
		t.Fatal("private material permissions are not mode 0600")
	}
	if string(files[byID["pki-root-certificate"]].Content) !=
		string(files[byID["pki-trust-anchor"]].Content) {
		t.Fatal("trust anchor differs from generated root certificate")
	}
	password := bytes.TrimSpace(
		files[byID["traefik-dashboard-password"]].Content,
	)
	usersLine := bytes.TrimSpace(
		files[byID["traefik-dashboard-users"]].Content,
	)
	parts := bytes.SplitN(usersLine, []byte{':'}, 2)
	if len(parts) != 2 || string(parts[0]) != DashboardUsername {
		t.Fatalf("materialized users file = %q", usersLine)
	}
	if err := bcrypt.CompareHashAndPassword(parts[1], password); err != nil {
		t.Fatalf("materialized dashboard credentials disagree: %v", err)
	}

	transaction, err := installfiles.NewStager().Stage(
		files,
		filepath.Join(root, "state", "backups", "transaction-1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(transaction.Results) != len(files) {
		t.Fatalf("staged results = %d, want %d", len(transaction.Results), len(files))
	}
	for _, file := range files {
		info, err := os.Stat(file.Target)
		if err != nil {
			t.Fatalf("stat %s: %v", file.ID, err)
		}
		if info.Mode().Perm() != file.Mode.Perm() {
			t.Fatalf("%s mode = %o, want %o", file.ID, info.Mode().Perm(), file.Mode.Perm())
		}
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if _, err := os.Lstat(file.Target); !os.IsNotExist(err) {
			t.Fatalf("%s remains after rollback: %v", file.ID, err)
		}
	}
}

func TestMaterializeSelectedContainerArtifactsDoesNotGenerateFiles(t *testing.T) {
	specification := managedSpecification(t)
	artifacts, err := Build(specification)
	if err != nil {
		t.Fatal(err)
	}
	selected := []domain.InstallationArtifact{}
	for _, artifact := range artifacts {
		if artifact.ID == "container-controller" ||
			artifact.ID == "container-probe" {
			selected = append(selected, artifact)
		}
	}
	files, err := MaterializeSelectedFiles(
		specification,
		selected,
		time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC),
		noRandomReader{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("selected container artifacts produced files: %#v", files)
	}
	selected[0].Target = "other-controller"
	if _, err := MaterializeSelectedFiles(
		specification,
		selected,
		time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC),
		noRandomReader{},
	); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("changed selected artifact error = %v", err)
	}
}
