package installhost

import "testing"

func TestDetectSystemProfile(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"arch", "NAME=Arch Linux\nID=arch\n", HostProfileArchSystemd},
		{"debian", "PRETTY_NAME=\"Debian GNU/Linux 12\"\nID=debian\n", HostProfileDebianSystemd},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile, err := DetectSystemProfile([]byte(test.content))
			if err != nil {
				t.Fatal(err)
			}
			if profile.Name != test.want {
				t.Fatalf("profile = %q, want %q", profile.Name, test.want)
			}
		})
	}
}

func TestDebianSystemdProfileUsesDebianTrustStore(t *testing.T) {
	profile := DebianSystemdProfile()
	if profile.TrustProfile != TrustProfileDebianCA ||
		profile.DnsmasqBinary != "/usr/sbin/dnsmasq" ||
		profile.DnsmasqValidator != "/usr/share/dnsmasq/systemd-helper" ||
		profile.DnsmasqIncludeConfig != "/etc/default/dnsmasq" ||
		profile.UpdateCATrust != "/usr/sbin/update-ca-certificates" ||
		profile.TrustBundle != "/etc/ssl/certs/ca-certificates.crt" ||
		profile.ManagedTrustAnchor !=
			"/usr/local/share/ca-certificates/docklane-local-root-ca.crt" {
		t.Fatalf("unexpected Debian profile: %#v", profile)
	}
}

func TestProfileForSpecificationRejectsMismatch(t *testing.T) {
	if _, err := ProfileForSpecification(
		HostProfileDebianSystemd,
		TrustProfileP11Kit,
	); err == nil {
		t.Fatal("expected platform/trust mismatch to fail")
	}
}
