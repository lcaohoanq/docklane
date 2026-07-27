package traefik

import (
	"strings"
	"testing"
)

func TestValidateAcceptsGeneratedConfiguration(t *testing.T) {
	configuration := Configuration{
		HTTP: HTTPConfiguration{
			Routers: map[string]Router{
				"draw": {
					Rule:        "Host(`draw.docker.home.arpa`)",
					EntryPoints: []string{"websecure"},
					Service:     "draw",
					TLS:         TLS{},
				},
			},
			Services: map[string]Service{
				"draw": {
					LoadBalancer: LoadBalancer{
						Servers: []Server{{URL: "http://docklane-route-1:80"}},
					},
				},
			},
		},
	}
	if err := Validate(configuration); err != nil {
		t.Fatal(err)
	}
	encoded, fingerprint, err := EncodeValidated(configuration)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) == 0 || len(fingerprint) != 64 {
		t.Fatalf("encoded bytes = %d, fingerprint = %q", len(encoded), fingerprint)
	}
	if _, err := DecodeValidated(encoded); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeValidatedSnapshot(encoded, fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeValidatedSnapshot(encoded, strings.Repeat("0", 64)); err == nil ||
		!strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("snapshot validation error = %v", err)
	}
}

func TestValidateRejectsMissingService(t *testing.T) {
	configuration := Configuration{
		HTTP: HTTPConfiguration{
			Routers: map[string]Router{
				"draw": {
					Rule:        "Host(`draw.docker.home.arpa`)",
					EntryPoints: []string{"websecure"},
					Service:     "missing",
				},
			},
			Services: map[string]Service{},
		},
	}
	err := Validate(configuration)
	if err == nil || !strings.Contains(err.Error(), "missing service") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestValidateRejectsUnsafeServerURL(t *testing.T) {
	configuration := Configuration{
		HTTP: HTTPConfiguration{
			Routers: map[string]Router{
				"draw": {
					Rule:        "Host(`draw.docker.home.arpa`)",
					EntryPoints: []string{"websecure"},
					Service:     "draw",
				},
			},
			Services: map[string]Service{
				"draw": {
					LoadBalancer: LoadBalancer{
						Servers: []Server{{URL: "file:///etc/passwd"}},
					},
				},
			},
		},
	}
	err := Validate(configuration)
	if err == nil || !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("validation error = %v", err)
	}
}
