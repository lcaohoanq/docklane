package main

import (
	"strings"
	"testing"
)

func TestUpgradeRequiresReviewedToken(t *testing.T) {
	err := upgrade(nil)
	if err == nil || !strings.Contains(err.Error(), "requires --token") {
		t.Fatalf("error = %v", err)
	}
}

func TestUpgradeRejectsTokenWithDryRun(t *testing.T) {
	err := upgrade([]string{
		"--dry-run",
		"--token", strings.Repeat("a", 64),
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("error = %v", err)
	}
}
