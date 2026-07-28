package installartifacts

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"time"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installfiles"
)

func MaterializeFiles(
	specification domain.InstallationSpecification,
	now time.Time,
	random io.Reader,
) ([]installfiles.File, error) {
	artifacts, err := Build(specification)
	if err != nil {
		return nil, err
	}
	pki, err := GeneratePKI(specification, now, random)
	if err != nil {
		return nil, err
	}
	credentials, err := GenerateDashboardCredentials(random)
	if err != nil {
		clearPKI(&pki)
		return nil, err
	}
	defer clear(credentials.Password)
	defer clear(credentials.UsersFile)

	contentByID := map[string][]byte{
		"pki-root-private-key":       pki.RootPrivateKey,
		"pki-root-certificate":       pki.RootCertificate,
		"pki-leaf-private-key":       pki.LeafPrivateKey,
		"pki-leaf-certificate":       pki.LeafCertificate,
		"pki-trust-anchor":           pki.RootCertificate,
		"traefik-dashboard-password": credentials.PasswordFile(),
		"traefik-dashboard-users":    credentials.UsersFile,
	}
	defer clearPKI(&pki)
	defer clear(contentByID["traefik-dashboard-password"])

	files := make([]installfiles.File, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Kind == domain.ArtifactContainerSpec {
			continue
		}
		content := []byte(artifact.Content)
		if artifact.GeneratedAtApply {
			var found bool
			content, found = contentByID[artifact.ID]
			if !found {
				clearMaterializedFiles(files)
				return nil, fmt.Errorf(
					"no materializer for generated artifact %s",
					artifact.ID,
				)
			}
		}
		files = append(files, installfiles.File{
			ID:        artifact.ID,
			Target:    artifact.Target,
			Mode:      fs.FileMode(artifact.Mode),
			Content:   bytes.Clone(content),
			Sensitive: artifact.Sensitive,
		})
	}
	return files, nil
}

func ClearMaterializedFiles(files []installfiles.File) {
	clearMaterializedFiles(files)
}

func clearMaterializedFiles(files []installfiles.File) {
	for index := range files {
		if files[index].Sensitive {
			clear(files[index].Content)
		}
	}
}

func clearPKI(bundle *PKIBundle) {
	clear(bundle.RootPrivateKey)
	clear(bundle.LeafPrivateKey)
}
