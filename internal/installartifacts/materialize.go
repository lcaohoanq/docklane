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
	return MaterializeSelectedFiles(specification, artifacts, now, random)
}

func MaterializeSelectedFiles(
	specification domain.InstallationSpecification,
	artifacts []domain.InstallationArtifact,
	now time.Time,
	random io.Reader,
) ([]installfiles.File, error) {
	expected, err := Build(specification)
	if err != nil {
		return nil, err
	}
	if err := domain.ValidateInstallationArtifacts(artifacts); err != nil {
		return nil, err
	}
	expectedByID := map[string]domain.InstallationArtifact{}
	for _, artifact := range expected {
		expectedByID[artifact.ID] = artifact
	}
	needsPKI := false
	needsCredentials := false
	for index, artifact := range artifacts {
		expectedArtifact, exists := expectedByID[artifact.ID]
		if !exists || artifact != expectedArtifact {
			return nil, fmt.Errorf(
				"selected artifact %d does not match the managed specification",
				index,
			)
		}
		switch artifact.ID {
		case "pki-root-private-key",
			"pki-root-certificate",
			"pki-leaf-private-key",
			"pki-leaf-certificate",
			"pki-trust-anchor":
			needsPKI = true
		case "traefik-dashboard-password", "traefik-dashboard-users":
			needsCredentials = true
		}
	}

	contentByID := map[string][]byte{}
	var pki PKIBundle
	if needsPKI {
		pki, err = GeneratePKI(specification, now, random)
		if err != nil {
			return nil, err
		}
		defer clearPKI(&pki)
		contentByID["pki-root-private-key"] = pki.RootPrivateKey
		contentByID["pki-root-certificate"] = pki.RootCertificate
		contentByID["pki-leaf-private-key"] = pki.LeafPrivateKey
		contentByID["pki-leaf-certificate"] = pki.LeafCertificate
		contentByID["pki-trust-anchor"] = pki.RootCertificate
	}
	var credentials DashboardCredentials
	if needsCredentials {
		credentials, err = GenerateDashboardCredentials(random)
		if err != nil {
			return nil, err
		}
		defer clear(credentials.Password)
		defer clear(credentials.UsersFile)
		contentByID["traefik-dashboard-password"] = credentials.PasswordFile()
		contentByID["traefik-dashboard-users"] = credentials.UsersFile
		defer clear(contentByID["traefik-dashboard-password"])
	}

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
