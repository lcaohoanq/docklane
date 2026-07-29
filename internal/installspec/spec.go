package installspec

import (
	"fmt"
	"path/filepath"

	"docklane.local/docklane/internal/domain"
)

type Config struct {
	BaseDomain      string
	ProxyNetwork    string
	DockerSocket    string
	StateDirectory  string
	DataDirectory   string
	DnsmasqConfig   string
	ResolverConfig  string
	TrustAnchorPath string
	TraefikImage    string
	DocklaneImage   string
	PlatformProfile string
	TrustProfile    string
}

func Build(config Config) (domain.InstallationSpecification, error) {
	state := filepath.Clean(config.StateDirectory)
	data := filepath.Clean(config.DataDirectory)
	traefikDirectory := filepath.Join(state, "traefik")
	pkiDirectory := filepath.Join(state, "pki")
	resolverConfig := config.ResolverConfig
	if resolverConfig == "" {
		resolverConfig = "/etc/systemd/resolved.conf.d/docklane.conf"
	}
	platformProfile := config.PlatformProfile
	trustProfile := config.TrustProfile
	if platformProfile == "" {
		platformProfile = "arch-systemd"
	}
	if trustProfile == "" {
		trustProfile = "p11-kit"
	}
	spec := domain.InstallationSpecification{
		SchemaVersion:  domain.InstallationSpecificationSchemaVersion,
		BaseDomain:     config.BaseDomain,
		ProxyNetwork:   config.ProxyNetwork,
		ControlNetwork: "docklane-control",
		ProbeVolume:    "docklane-probe-run",
		DockerSocket:   filepath.Clean(config.DockerSocket),
		Images: domain.InstallationImages{
			Traefik:  config.TraefikImage,
			Docklane: config.DocklaneImage,
		},
		Paths: domain.InstallationPaths{
			StateDirectory:       state,
			DataDirectory:        data,
			TraefikDirectory:     traefikDirectory,
			BackupDirectory:      filepath.Join(state, "backups"),
			TraefikDynamicConfig: filepath.Join(traefikDirectory, "dynamic", "tls.yml"),
			DashboardPassword:    filepath.Join(state, "secrets", "traefik-dashboard-password"),
			DashboardUsers:       filepath.Join(state, "secrets", "traefik-dashboard-users"),
			DnsmasqConfig:        filepath.Clean(config.DnsmasqConfig),
			ResolverConfig:       filepath.Clean(resolverConfig),
		},
		Host: domain.InstallationHostIntegration{
			PlatformProfile: platformProfile,
			DNSService:      "dnsmasq",
			ResolverService: "systemd-resolved",
			TrustProfile:    trustProfile,
			ResolverProfile: "systemd-resolved",
		},
		PKI: domain.InstallationPKI{
			RootCommonName:      "Docklane Local Root CA",
			RootValidityDays:    3650,
			LeafValidityDays:    365,
			RotateBeforeDays:    30,
			RSAKeyBits:          3072,
			DNSNames:            []string{config.BaseDomain, "*." + config.BaseDomain},
			RootCertificatePath: filepath.Join(pkiDirectory, "root-ca.crt"),
			RootPrivateKeyPath:  filepath.Join(pkiDirectory, "root-ca.key"),
			LeafCertificatePath: filepath.Join(traefikDirectory, "certs", "local.crt"),
			LeafPrivateKeyPath:  filepath.Join(traefikDirectory, "certs", "local.key"),
			TrustAnchorPath:     filepath.Clean(config.TrustAnchorPath),
		},
	}
	spec.Containers = []domain.InstallationContainer{
		gateway(spec),
		probe(spec),
		controller(spec),
	}
	if err := spec.Validate(); err != nil {
		return domain.InstallationSpecification{}, fmt.Errorf(
			"managed installation specification: %w",
			err,
		)
	}
	return spec, nil
}

func gateway(spec domain.InstallationSpecification) domain.InstallationContainer {
	return domain.InstallationContainer{
		Name:  "traefik",
		Role:  "gateway",
		Image: spec.Images.Traefik,
		Command: []string{
			"--entrypoints.web.address=:80",
			"--entrypoints.web.http.redirections.entrypoint.to=websecure",
			"--entrypoints.web.http.redirections.entrypoint.scheme=https",
			"--entrypoints.web.http.redirections.entrypoint.permanent=true",
			"--entrypoints.websecure.address=:443",
			"--entrypoints.websecure.http.tls=true",
			"--providers.docker=true",
			"--providers.docker.exposedbydefault=false",
			"--providers.docker.network=" + spec.ProxyNetwork,
			"--providers.file.filename=/dynamic/tls.yml",
			"--providers.http.endpoint=http://docklane:4646/internal/traefik",
			"--providers.http.pollinterval=2s",
			"--providers.http.polltimeout=2s",
			"--api.dashboard=true",
			"--api.insecure=false",
		},
		Networks: []string{spec.ProxyNetwork, spec.ControlNetwork},
		Mounts: []domain.InstallationMount{
			{Source: spec.DockerSocket, Destination: "/var/run/docker.sock", ReadOnly: true},
			{Source: filepath.Dir(spec.Paths.TraefikDynamicConfig), Destination: "/dynamic", ReadOnly: true},
			{Source: filepath.Dir(spec.PKI.LeafCertificatePath), Destination: "/certs", ReadOnly: true},
			{Source: spec.Paths.DashboardUsers, Destination: "/run/secrets/traefik-dashboard-users", ReadOnly: true},
		},
		Ports: []domain.InstallationPortBinding{
			{ContainerPort: 80, HostPort: 80},
			{ContainerPort: 443, HostPort: 443},
		},
		ReadOnlyRootFS:  true,
		NoNewPrivileges: true,
		RestartPolicy:   "unless-stopped",
	}
}

func probe(spec domain.InstallationSpecification) domain.InstallationContainer {
	return domain.InstallationContainer{
		Name:     "docklane-probe",
		Role:     "probe",
		Image:    spec.Images.Docklane,
		Command:  []string{"probe", "serve", "--socket=/run/docklane-probe/probe.sock"},
		Networks: []string{spec.ProxyNetwork},
		Mounts: []domain.InstallationMount{{
			Source: spec.ProbeVolume, Destination: "/run/docklane-probe", Volume: true,
		}},
		Ports:               []domain.InstallationPortBinding{},
		ReadOnlyRootFS:      true,
		NoNewPrivileges:     true,
		DropAllCapabilities: true,
		RestartPolicy:       "unless-stopped",
	}
}

func controller(spec domain.InstallationSpecification) domain.InstallationContainer {
	return domain.InstallationContainer{
		Name:  "docklane",
		Role:  "controller",
		Image: spec.Images.Docklane,
		Command: []string{
			"serve",
			"--listen=0.0.0.0:4646",
			"--database=/data/docklane.db",
			"--base-domain=" + spec.BaseDomain,
			"--docker-socket=/var/run/docker.sock",
			"--proxy-network=" + spec.ProxyNetwork,
			"--probe-socket=/run/docklane-probe/probe.sock",
			"--traefik-api-url=https://traefik." + spec.BaseDomain,
			"--traefik-api-address=traefik:443",
			"--traefik-api-username=admin",
			"--traefik-api-password-file=/run/secrets/traefik-dashboard-password",
			"--traefik-api-ca-file=/run/secrets/docklane-root-ca.crt",
			"--manage-network-attachments",
		},
		Networks: []string{spec.ControlNetwork},
		Mounts: []domain.InstallationMount{
			{Source: spec.DockerSocket, Destination: "/var/run/docker.sock", ReadOnly: true},
			{Source: spec.Paths.DataDirectory, Destination: "/data"},
			{Source: spec.ProbeVolume, Destination: "/run/docklane-probe", Volume: true},
			{Source: spec.Paths.DashboardPassword, Destination: "/run/secrets/traefik-dashboard-password", ReadOnly: true},
			{Source: spec.PKI.RootCertificatePath, Destination: "/run/secrets/docklane-root-ca.crt", ReadOnly: true},
		},
		Ports: []domain.InstallationPortBinding{{
			ContainerPort: 4646, HostIP: "127.0.0.1", HostPort: 4646,
		}},
		ReadOnlyRootFS:  true,
		NoNewPrivileges: true,
		RestartPolicy:   "unless-stopped",
	}
}
