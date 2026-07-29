package main

import (
	"strings"
	"testing"
)

func TestUninstallRequiresReviewedToken(t *testing.T) {
	err := uninstall(nil)
	if err == nil || !strings.Contains(err.Error(), "requires --token") {
		t.Fatalf("error = %v", err)
	}
}

func TestUninstallRejectsTokenWithDryRun(t *testing.T) {
	err := uninstall([]string{"--dry-run", "--token", strings.Repeat("a", 64)})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("error = %v", err)
	}
}
