package main

import (
	"strings"
	"testing"
)

func TestUninstallRequiresDryRunUntilApplyExists(t *testing.T) {
	err := uninstall(nil)
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("error = %v", err)
	}
}
