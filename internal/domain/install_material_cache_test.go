package domain

import (
	"path/filepath"
	"strings"
	"testing"
)

func validMaterialCache(t *testing.T) InstallationMaterialCache {
	t.Helper()
	directory := filepath.Join(
		"/var/lib/docklane",
		".material-cache",
		"018f5e52-4f22-4a6e-8ad8-d3e4450d1957",
	)
	cache := InstallationMaterialCache{
		SchemaVersion: InstallationMaterialCacheSchemaVersion,
		State:         MaterialCacheReady,
		Directory:     directory,
		Entries: []InstallationMaterialCacheEntry{{
			ArtifactID:  "private-key",
			Target:      "/var/lib/docklane/pki/root.key",
			CachePath:   filepath.Join(directory, "000-private-key.material"),
			Mode:        0o600,
			Fingerprint: strings.Repeat("a", 64),
			Sensitive:   true,
		}},
	}
	var err error
	cache.InventoryFingerprint, err = MaterialCacheInventoryFingerprint(
		cache.Entries,
	)
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

func TestInstallationMaterialCacheValidation(t *testing.T) {
	cache := validMaterialCache(t)
	if err := cache.Validate(
		"018f5e52-4f22-4a6e-8ad8-d3e4450d1957",
		"/var/lib/docklane",
	); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		change   func(*InstallationMaterialCache)
		contains string
	}{
		{
			name: "wrong installation directory",
			change: func(candidate *InstallationMaterialCache) {
				candidate.Directory = "/var/lib/docklane/.material-cache/other"
			},
			contains: "installation-bound",
		},
		{
			name: "cache path escapes",
			change: func(candidate *InstallationMaterialCache) {
				candidate.Entries[0].CachePath = "/tmp/private-key"
			},
			contains: "escapes",
		},
		{
			name: "broad sensitive target",
			change: func(candidate *InstallationMaterialCache) {
				candidate.Entries[0].Mode = 0o644
			},
			contains: "broad sensitive",
		},
		{
			name: "changed inventory",
			change: func(candidate *InstallationMaterialCache) {
				candidate.Entries[0].Fingerprint = strings.Repeat("b", 64)
			},
			contains: "inventory fingerprint",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cache
			candidate.Entries = append(
				[]InstallationMaterialCacheEntry(nil),
				cache.Entries...,
			)
			test.change(&candidate)
			err := candidate.Validate(
				"018f5e52-4f22-4a6e-8ad8-d3e4450d1957",
				"/var/lib/docklane",
			)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %v, want containing %q", err, test.contains)
			}
		})
	}
}
