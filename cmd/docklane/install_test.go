package main

import (
	"strings"
	"testing"
)

func TestInstallRequiresReviewedToken(t *testing.T) {
	err := install(nil)
	if err == nil || !strings.Contains(err.Error(), "requires --token") {
		t.Fatalf("error = %v", err)
	}
}

func TestInstallRejectsTokenWithDryRun(t *testing.T) {
	err := install([]string{"--dry-run", "--token", strings.Repeat("a", 64)})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("error = %v", err)
	}
}

func TestDefaultDocklaneImageTracksProductVersion(t *testing.T) {
	previous := docklaneVersion
	t.Cleanup(func() {
		docklaneVersion = previous
	})
	for _, test := range []struct {
		version string
		want    string
	}{
		{"", "docklane:local"},
		{"dev", "docklane:local"},
		{"v0.0.0-dev.0123456789ab", "docklane:local"},
		{"v0.1.0-alpha.2", "lcaohoanq/docklane:v0.1.0-alpha.2"},
	} {
		docklaneVersion = test.version
		if got := defaultDocklaneImage(); got != test.want {
			t.Fatalf(
				"defaultDocklaneImage() for %q = %q, want %q",
				test.version,
				got,
				test.want,
			)
		}
	}
}
