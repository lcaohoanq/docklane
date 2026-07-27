package docker

import "testing"

func TestTraefikHostnameClaims(t *testing.T) {
	containers := []Container{
		{
			Name: "draw",
			Labels: map[string]string{
				"traefik.enable":                                      "true",
				"traefik.http.routers.draw.rule":                      "Host(`draw.docker.home.arpa`) && PathPrefix(`/`)",
				"traefik.http.routers.alternate.rule":                 "Host(`ALT.docker.home.arpa`, \"other.docker.home.arpa\")",
				"traefik.http.routers.pattern.rule":                   "HostRegexp(`{name:.+}.docker.home.arpa`)",
				"traefik.http.services.draw.loadbalancer.server.port": "80",
			},
		},
		{
			Name: "disabled",
			Labels: map[string]string{
				"traefik.enable":                 "false",
				"traefik.http.routers.draw.rule": "Host(`ignored.docker.home.arpa`)",
			},
		},
	}

	claims := TraefikHostnameClaims(containers)
	if len(claims) != 3 {
		t.Fatalf("claims = %#v", claims)
	}
	for _, hostname := range []string{
		"alt.docker.home.arpa",
		"draw.docker.home.arpa",
		"other.docker.home.arpa",
	} {
		claim, found := FindTraefikHostnameClaim(hostname, containers)
		if !found || claim.Hostname != hostname || claim.ContainerName != "draw" {
			t.Fatalf("claim for %s = %#v, found = %v", hostname, claim, found)
		}
	}
	if _, found := FindTraefikHostnameClaim(
		"ignored.docker.home.arpa",
		containers,
	); found {
		t.Fatal("disabled container label was treated as an active claim")
	}
}
