package installcompose

import (
	"context"
	"strings"
	"testing"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installworkflow"
)

func TestBuildOrdersDependencyGroups(t *testing.T) {
	resources := []domain.InstallationResource{
		resource("state", domain.ResourceDirectory, "/state/app"),
		resource("config", domain.ResourceFile, "/state/app/config.yml"),
		resource("service", domain.ResourceSystemService, "dnsmasq"),
		resource("network", domain.ResourceDockerNetwork, "proxy"),
	}
	steps, err := Build(resources, Groups{
		Directories: []installworkflow.Step{
			step("state", "/state/app", domain.ExecutionDirectories),
		},
		Files: []installworkflow.Step{
			step("config", "/state/app/config.yml", domain.ExecutionFiles),
		},
		Host: []installworkflow.Step{
			step("service", "dnsmasq", domain.ExecutionHost),
		},
		Docker: []installworkflow.Step{
			step("network", "proxy", domain.ExecutionDocker),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, step := range steps {
		got = append(got, step.ResourceID)
	}
	if strings.Join(got, ",") != "state,config,service,network" {
		t.Fatalf("step order = %v", got)
	}
}

func TestBuildRejectsUnsafeDirectoryAndFileOrder(t *testing.T) {
	resources := []domain.InstallationResource{
		resource("parent", domain.ResourceDirectory, "/state/app"),
		resource("child", domain.ResourceDirectory, "/state/app/config"),
		resource("file", domain.ResourceFile, "/state/app/config/tls.yml"),
	}
	tests := []struct {
		name      string
		resources []domain.InstallationResource
		groups    Groups
		contains  string
	}{
		{
			name:      "child before parent",
			resources: resources,
			groups: Groups{
				Directories: []installworkflow.Step{
					step("child", "/state/app/config", domain.ExecutionDirectories),
					step("parent", "/state/app", domain.ExecutionDirectories),
				},
				Files: []installworkflow.Step{
					step("file", "/state/app/config/tls.yml", domain.ExecutionFiles),
				},
			},
			contains: "must precede",
		},
		{
			name: "missing direct file parent",
			resources: []domain.InstallationResource{
				resource("parent", domain.ResourceDirectory, "/state/app"),
				resource("file", domain.ResourceFile, "/state/app/config/tls.yml"),
			},
			groups: Groups{
				Directories: []installworkflow.Step{
					step("parent", "/state/app", domain.ExecutionDirectories),
				},
				Files: []installworkflow.Step{
					step("file", "/state/app/config/tls.yml", domain.ExecutionFiles),
				},
			},
			contains: "explicit parent",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Build(
				test.resources,
				test.groups,
			); err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %v, want containing %q", err, test.contains)
			}
		})
	}
}

func TestBuildRejectsMissingOrMisgroupedManagedStep(t *testing.T) {
	resources := []domain.InstallationResource{
		resource("config", domain.ResourceFile, "/etc/config"),
	}
	if _, err := Build(resources, Groups{}); err == nil ||
		!strings.Contains(err.Error(), "covers 0 of 1") {
		t.Fatalf("missing coverage error = %v", err)
	}
	if _, err := Build(resources, Groups{
		Files: []installworkflow.Step{
			step("config", "/etc/config", domain.ExecutionDocker),
		},
	}); err == nil || !strings.Contains(err.Error(), "with stage") {
		t.Fatalf("misgrouped error = %v", err)
	}
}

func resource(
	id string,
	kind domain.ResourceKind,
	target string,
) domain.InstallationResource {
	return domain.InstallationResource{
		ID:        id,
		Kind:      kind,
		Target:    target,
		Ownership: domain.ResourceManaged,
		State:     domain.ResourcePlanned,
		Rollback:  domain.RollbackRemove,
	}
}

func step(
	resourceID string,
	target string,
	stage domain.InstallationExecutionStage,
) installworkflow.Step {
	return installworkflow.Step{
		ID:         "step-" + resourceID,
		ResourceID: resourceID,
		Target:     target,
		Stage:      stage,
		Apply: func(
			context.Context,
		) (domain.InstallationObservation, error) {
			return domain.InstallationObservation{}, nil
		},
		Inspect: func(
			context.Context,
			*domain.InstallationObservation,
		) (
			installworkflow.Disposition,
			domain.InstallationObservation,
			error,
		) {
			return installworkflow.DispositionPending,
				domain.InstallationObservation{},
				nil
		},
		Rollback: func(
			context.Context,
			domain.InstallationObservation,
		) error {
			return nil
		},
	}
}
