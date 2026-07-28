package domain

import (
	"strings"
	"testing"
	"time"
)

func validInstallationManifest() InstallationManifest {
	now := time.Date(2026, time.July, 28, 4, 0, 0, 0, time.UTC)
	return InstallationManifest{
		SchemaVersion:  InstallationManifestSchemaVersion,
		InstallationID: "018f5e52-4f22-4a6e-8ad8-d3e4450d1957",
		Generation:     1,
		ProductVersion: "dev",
		State:          InstallationPlanned,
		CreatedAt:      now,
		UpdatedAt:      now,
		Settings: InstallationSettings{
			BaseDomain:   "docker.home.arpa",
			ProxyNetwork: "proxy",
		},
		Resources: []InstallationResource{
			{
				ID:        "dnsmasq-config",
				Kind:      ResourceFile,
				Target:    "/etc/dnsmasq.d/docklane.conf",
				Ownership: ResourceManaged,
				State:     ResourcePlanned,
				Rollback:  RollbackRestore,
			},
			{
				ID:        "proxy-network",
				Kind:      ResourceDockerNetwork,
				Target:    "proxy",
				Ownership: ResourceAdopted,
				State:     ResourceVerified,
				Rollback:  RollbackPreserve,
			},
		},
	}
}

func TestInstallationManifestValidation(t *testing.T) {
	if err := validInstallationManifest().Validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		change   func(*InstallationManifest)
		contains string
	}{
		{
			name: "installed resource is not verified",
			change: func(manifest *InstallationManifest) {
				manifest.State = InstallationInstalled
			},
			contains: "must be verified",
		},
		{
			name: "missing resources array",
			change: func(manifest *InstallationManifest) {
				manifest.Resources = nil
			},
			contains: "must be an array",
		},
		{
			name: "future schema",
			change: func(manifest *InstallationManifest) {
				manifest.SchemaVersion = 2
			},
			contains: "unsupported",
		},
		{
			name: "duplicate target",
			change: func(manifest *InstallationManifest) {
				manifest.Resources = append(
					manifest.Resources,
					InstallationResource{
						ID:        "dnsmasq-copy",
						Kind:      ResourceFile,
						Target:    "/etc/dnsmasq.d/docklane.conf",
						Ownership: ResourceManaged,
						State:     ResourcePlanned,
						Rollback:  RollbackRemove,
					},
				)
			},
			contains: "duplicate file target",
		},
		{
			name: "adopted resource removal",
			change: func(manifest *InstallationManifest) {
				manifest.Resources[1].Rollback = RollbackRemove
			},
			contains: "adopted resource rollback must be preserve",
		},
		{
			name: "relative managed file",
			change: func(manifest *InstallationManifest) {
				manifest.Resources[0].Target = "docklane.conf"
			},
			contains: "must be absolute",
		},
		{
			name: "applied restore without backup",
			change: func(manifest *InstallationManifest) {
				manifest.Resources[0].State = ResourceApplied
			},
			contains: "requires a backup",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validInstallationManifest()
			test.change(&manifest)
			err := manifest.Validate()
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %v, want containing %q", err, test.contains)
			}
		})
	}
}

func TestInstallationResourceAcceptsRecordedRestoreBackup(t *testing.T) {
	manifest := validInstallationManifest()
	manifest.Resources[0].State = ResourceVerified
	manifest.Resources[0].Fingerprint = strings.Repeat("a", 64)
	manifest.Resources[0].Backup = &ResourceBackup{
		Path:        "/var/lib/docklane/backups/dnsmasq.conf",
		Fingerprint: strings.Repeat("b", 64),
	}
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestInstallationExecutionValidation(t *testing.T) {
	resources := []InstallationResource{{
		ID:        "proxy-network",
		Kind:      ResourceDockerNetwork,
		Target:    "proxy",
		Ownership: ResourceManaged,
		State:     ResourcePlanned,
		Rollback:  RollbackRemove,
	}}
	valid := InstallationExecution{
		SchemaVersion: InstallationExecutionSchemaVersion,
		Phase:         ExecutionForward,
		Operations: []InstallationExecutionOperation{{
			ID:         "create-proxy-network",
			ResourceID: "proxy-network",
			Target:     "proxy",
			Stage:      ExecutionDocker,
			State:      OperationPending,
		}},
	}
	if err := valid.Validate(resources); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		change   func(*InstallationExecution)
		contains string
	}{
		{
			name: "duplicate operation",
			change: func(execution *InstallationExecution) {
				execution.Operations = append(
					execution.Operations,
					execution.Operations[0],
				)
			},
			contains: "duplicate operation ID",
		},
		{
			name: "missing observation",
			change: func(execution *InstallationExecution) {
				execution.Operations[0].State = OperationApplied
				execution.Operations[0].Attempt = 1
			},
			contains: "invalid checkpoint data",
		},
		{
			name: "complete pending operation",
			change: func(execution *InstallationExecution) {
				execution.Phase = ExecutionComplete
			},
			contains: "requires applied operations",
		},
		{
			name: "rollback state in forward phase",
			change: func(execution *InstallationExecution) {
				execution.Operations[0].State = OperationRollingBack
				execution.Operations[0].Attempt = 1
				execution.Operations[0].Observation = &InstallationObservation{}
			},
			contains: "cannot be rolling_back",
		},
		{
			name: "oversized error",
			change: func(execution *InstallationExecution) {
				execution.Phase = ExecutionFailed
				execution.Operations[0].State = OperationFailed
				execution.Operations[0].Attempt = 1
				execution.Operations[0].ErrorMessage = strings.Repeat("x", 4097)
			},
			contains: "exceeds 4096",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			execution := valid
			execution.Operations = append(
				[]InstallationExecutionOperation(nil),
				valid.Operations...,
			)
			test.change(&execution)
			err := execution.Validate(resources)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %v, want containing %q", err, test.contains)
			}
		})
	}
}
