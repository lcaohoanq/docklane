package domain

import (
	"fmt"
	"path/filepath"
	"strings"
)

const InstallationSpecificationSchemaVersion = 1

type InstallationImages struct {
	Traefik  string `json:"traefik"`
	Docklane string `json:"docklane"`
}

type InstallationPKI struct {
	RootCommonName      string   `json:"rootCommonName"`
	RootValidityDays    int      `json:"rootValidityDays"`
	LeafValidityDays    int      `json:"leafValidityDays"`
	RotateBeforeDays    int      `json:"rotateBeforeDays"`
	RSAKeyBits          int      `json:"rsaKeyBits"`
	DNSNames            []string `json:"dnsNames"`
	RootCertificatePath string   `json:"rootCertificatePath"`
	RootPrivateKeyPath  string   `json:"rootPrivateKeyPath"`
	LeafCertificatePath string   `json:"leafCertificatePath"`
	LeafPrivateKeyPath  string   `json:"leafPrivateKeyPath"`
	TrustAnchorPath     string   `json:"trustAnchorPath"`
}

type InstallationPaths struct {
	StateDirectory       string `json:"stateDirectory"`
	DataDirectory        string `json:"dataDirectory"`
	TraefikDirectory     string `json:"traefikDirectory"`
	TraefikDynamicConfig string `json:"traefikDynamicConfig"`
	DashboardPassword    string `json:"dashboardPassword"`
	DashboardUsers       string `json:"dashboardUsers"`
	DnsmasqConfig        string `json:"dnsmasqConfig"`
	ResolverConfig       string `json:"resolverConfig"`
}

type InstallationHostIntegration struct {
	DNSService      string `json:"dnsService"`
	ResolverService string `json:"resolverService"`
	TrustProfile    string `json:"trustProfile"`
	ResolverProfile string `json:"resolverProfile"`
}

type InstallationPortBinding struct {
	ContainerPort uint16 `json:"containerPort"`
	HostIP        string `json:"hostIp,omitempty"`
	HostPort      uint16 `json:"hostPort"`
}

type InstallationMount struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"readOnly"`
	Volume      bool   `json:"volume"`
}

type InstallationContainer struct {
	Name                string                    `json:"name"`
	Role                string                    `json:"role"`
	Image               string                    `json:"image"`
	Command             []string                  `json:"command"`
	Networks            []string                  `json:"networks"`
	Mounts              []InstallationMount       `json:"mounts"`
	Ports               []InstallationPortBinding `json:"ports"`
	ReadOnlyRootFS      bool                      `json:"readOnlyRootFs"`
	NoNewPrivileges     bool                      `json:"noNewPrivileges"`
	DropAllCapabilities bool                      `json:"dropAllCapabilities"`
	RestartPolicy       string                    `json:"restartPolicy"`
}

type InstallationSpecification struct {
	SchemaVersion  int                         `json:"schemaVersion"`
	BaseDomain     string                      `json:"baseDomain"`
	ProxyNetwork   string                      `json:"proxyNetwork"`
	ControlNetwork string                      `json:"controlNetwork"`
	ProbeVolume    string                      `json:"probeVolume"`
	DockerSocket   string                      `json:"dockerSocket"`
	Images         InstallationImages          `json:"images"`
	Paths          InstallationPaths           `json:"paths"`
	Host           InstallationHostIntegration `json:"host"`
	PKI            InstallationPKI             `json:"pki"`
	Containers     []InstallationContainer     `json:"containers"`
}

func (spec InstallationSpecification) Validate() error {
	if spec.SchemaVersion != InstallationSpecificationSchemaVersion {
		return fmt.Errorf(
			"unsupported installation specification schema version %d",
			spec.SchemaVersion,
		)
	}
	settings := InstallationSettings{
		BaseDomain: spec.BaseDomain, ProxyNetwork: spec.ProxyNetwork,
	}
	if err := settings.Validate(); err != nil {
		return err
	}
	for label, name := range map[string]string{
		"control network": spec.ControlNetwork,
		"probe volume":    spec.ProbeVolume,
	} {
		if !dockerNamePattern.MatchString(name) {
			return fmt.Errorf("invalid %s %q", label, name)
		}
	}
	if spec.ProxyNetwork == spec.ControlNetwork {
		return fmt.Errorf("proxy and control networks must be distinct")
	}
	if strings.TrimSpace(spec.Images.Traefik) == "" ||
		strings.TrimSpace(spec.Images.Docklane) == "" {
		return fmt.Errorf("managed image references are required")
	}
	for label, path := range map[string]string{
		"Docker socket":          spec.DockerSocket,
		"state directory":        spec.Paths.StateDirectory,
		"data directory":         spec.Paths.DataDirectory,
		"Traefik directory":      spec.Paths.TraefikDirectory,
		"Traefik dynamic config": spec.Paths.TraefikDynamicConfig,
		"dashboard password":     spec.Paths.DashboardPassword,
		"dashboard users":        spec.Paths.DashboardUsers,
		"dnsmasq config":         spec.Paths.DnsmasqConfig,
		"resolver config":        spec.Paths.ResolverConfig,
		"root certificate":       spec.PKI.RootCertificatePath,
		"root private key":       spec.PKI.RootPrivateKeyPath,
		"leaf certificate":       spec.PKI.LeafCertificatePath,
		"leaf private key":       spec.PKI.LeafPrivateKeyPath,
		"trust anchor":           spec.PKI.TrustAnchorPath,
	} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("%s path must be absolute and canonical", label)
		}
	}
	if !dockerNamePattern.MatchString(spec.Host.DNSService) ||
		!dockerNamePattern.MatchString(spec.Host.ResolverService) ||
		spec.Host.DNSService == spec.Host.ResolverService ||
		spec.Host.TrustProfile != "p11-kit" ||
		spec.Host.ResolverProfile != "systemd-resolved" {
		return fmt.Errorf("invalid managed host integration profile")
	}
	if spec.Paths.StateDirectory == string(filepath.Separator) ||
		!pathWithin(spec.Paths.StateDirectory, spec.Paths.DataDirectory) ||
		!pathWithin(spec.Paths.StateDirectory, spec.Paths.TraefikDirectory) ||
		!pathWithin(spec.Paths.StateDirectory, spec.Paths.DashboardPassword) ||
		!pathWithin(spec.Paths.StateDirectory, spec.Paths.DashboardUsers) ||
		!pathWithin(spec.Paths.StateDirectory, spec.PKI.RootCertificatePath) ||
		!pathWithin(spec.Paths.StateDirectory, spec.PKI.RootPrivateKeyPath) ||
		!pathWithin(spec.Paths.StateDirectory, spec.PKI.LeafCertificatePath) ||
		!pathWithin(spec.Paths.StateDirectory, spec.PKI.LeafPrivateKeyPath) {
		return fmt.Errorf("managed state paths must stay below the dedicated state directory")
	}
	if spec.PKI.RootValidityDays < 3650 ||
		spec.PKI.LeafValidityDays < 31 ||
		spec.PKI.LeafValidityDays > 397 ||
		spec.PKI.RotateBeforeDays < 7 ||
		spec.PKI.RotateBeforeDays >= spec.PKI.LeafValidityDays ||
		spec.PKI.RSAKeyBits < 3072 {
		return fmt.Errorf("invalid managed PKI lifetime or key settings")
	}
	requiredNames := map[string]bool{
		spec.BaseDomain:        false,
		"*." + spec.BaseDomain: false,
	}
	for _, name := range spec.PKI.DNSNames {
		if _, required := requiredNames[name]; required {
			requiredNames[name] = true
		}
	}
	for name, found := range requiredNames {
		if !found {
			return fmt.Errorf("managed certificate SANs omit %s", name)
		}
	}
	if len(spec.Containers) != 3 {
		return fmt.Errorf("managed specification requires three containers")
	}
	roles := map[string]bool{}
	names := map[string]bool{}
	for index, container := range spec.Containers {
		if err := validateInstallationContainer(container); err != nil {
			return fmt.Errorf("container %d: %w", index, err)
		}
		if names[container.Name] || roles[container.Role] {
			return fmt.Errorf("managed container names and roles must be unique")
		}
		names[container.Name] = true
		roles[container.Role] = true
		switch container.Role {
		case "gateway":
			if container.Image != spec.Images.Traefik ||
				!hasPublishedPort(container.Ports, 80, "") ||
				!hasPublishedPort(container.Ports, 443, "") {
				return fmt.Errorf("managed gateway image and ports are inconsistent")
			}
		case "controller":
			if container.Image != spec.Images.Docklane ||
				!hasPublishedPort(container.Ports, 4646, "127.0.0.1") {
				return fmt.Errorf("managed controller image or loopback port is inconsistent")
			}
		case "probe":
			if container.Image != spec.Images.Docklane ||
				len(container.Ports) != 0 ||
				!container.DropAllCapabilities {
				return fmt.Errorf("managed probe isolation is incomplete")
			}
		}
	}
	for _, role := range []string{"gateway", "controller", "probe"} {
		if !roles[role] {
			return fmt.Errorf("managed specification omits %s container", role)
		}
	}
	return nil
}

func pathWithin(parent string, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil &&
		relative != "." &&
		relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func hasPublishedPort(
	ports []InstallationPortBinding,
	port uint16,
	hostIP string,
) bool {
	for _, binding := range ports {
		if binding.ContainerPort == port &&
			binding.HostPort == port &&
			binding.HostIP == hostIP {
			return true
		}
	}
	return false
}

func validateInstallationContainer(container InstallationContainer) error {
	if !dockerNamePattern.MatchString(container.Name) {
		return fmt.Errorf("invalid container name %q", container.Name)
	}
	if strings.TrimSpace(container.Image) == "" ||
		len(container.Command) == 0 ||
		len(container.Networks) == 0 {
		return fmt.Errorf("container image, command, and networks are required")
	}
	if !container.ReadOnlyRootFS ||
		!container.NoNewPrivileges ||
		container.RestartPolicy != "unless-stopped" {
		return fmt.Errorf("container security and restart policy are incomplete")
	}
	for _, mount := range container.Mounts {
		if mount.Source == "" || !filepath.IsAbs(mount.Destination) {
			return fmt.Errorf("mount source and absolute destination are required")
		}
		if !mount.Volume && !filepath.IsAbs(mount.Source) {
			return fmt.Errorf("bind mount source must be absolute")
		}
	}
	for _, port := range container.Ports {
		if port.ContainerPort == 0 || port.HostPort == 0 {
			return fmt.Errorf("published ports must be non-zero")
		}
	}
	return nil
}
