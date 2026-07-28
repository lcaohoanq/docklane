package installspec

import (
	"testing"
)

func testConfig() Config {
	return Config{
		BaseDomain:      "docker.home.arpa",
		ProxyNetwork:    "proxy",
		DockerSocket:    "/var/run/docker.sock",
		StateDirectory:  "/var/lib/docklane",
		DataDirectory:   "/var/lib/docklane/data",
		DnsmasqConfig:   "/etc/dnsmasq.d/docklane.conf",
		TrustAnchorPath: "/etc/ca-certificates/trust-source/anchors/docklane-local-root-ca.crt",
		TraefikImage:    "traefik:v3.7",
		DocklaneImage:   "docklane:local",
	}
}

func TestBuildCreatesSelfContainedManagedSpecification(t *testing.T) {
	specification, err := Build(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	if specification.PKI.RootPrivateKeyPath !=
		"/var/lib/docklane/pki/root-ca.key" ||
		specification.PKI.LeafCertificatePath !=
			"/var/lib/docklane/traefik/certs/local.crt" ||
		specification.Paths.TraefikDynamicConfig !=
			"/var/lib/docklane/traefik/dynamic/tls.yml" {
		t.Fatalf("managed paths = %#v", specification)
	}
	if len(specification.Containers) != 3 ||
		specification.Containers[0].Role != "gateway" ||
		specification.Containers[1].Role != "probe" ||
		specification.Containers[2].Role != "controller" {
		t.Fatalf("containers = %#v", specification.Containers)
	}
}

func TestBuildRejectsStatePathsOutsideDedicatedDirectory(t *testing.T) {
	config := testConfig()
	config.DataDirectory = "/home/example/docklane-data"
	if _, err := Build(config); err == nil {
		t.Fatal("expected external data path to be rejected")
	}
}

func TestBuildRejectsRootStateDirectory(t *testing.T) {
	config := testConfig()
	config.StateDirectory = "/"
	if _, err := Build(config); err == nil {
		t.Fatal("expected root state directory to be rejected")
	}
}

func TestBuildRejectsSharedProxyAndControlNetwork(t *testing.T) {
	config := testConfig()
	config.ProxyNetwork = "docklane-control"
	if _, err := Build(config); err == nil {
		t.Fatal("expected shared proxy and control network to be rejected")
	}
}
