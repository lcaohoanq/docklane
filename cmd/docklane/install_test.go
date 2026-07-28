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
