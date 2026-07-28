package main

import (
	"strings"
	"testing"
)

func TestInstallRequiresDryRunUntilApplyExists(t *testing.T) {
	err := install(nil)
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("error = %v", err)
	}
}
