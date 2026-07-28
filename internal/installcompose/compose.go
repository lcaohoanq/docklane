package installcompose

import (
	"fmt"
	"path/filepath"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installworkflow"
)

type Groups struct {
	Directories []installworkflow.Step
	Files       []installworkflow.Step
	Host        []installworkflow.Step
	Docker      []installworkflow.Step
	Verify      []installworkflow.Step
}

func Build(
	resources []domain.InstallationResource,
	groups Groups,
) ([]installworkflow.Step, error) {
	orderedGroups := []struct {
		stage domain.InstallationExecutionStage
		steps []installworkflow.Step
	}{
		{domain.ExecutionDirectories, groups.Directories},
		{domain.ExecutionFiles, groups.Files},
		{domain.ExecutionHost, groups.Host},
		{domain.ExecutionDocker, groups.Docker},
		{domain.ExecutionVerify, groups.Verify},
	}
	managed := map[string]domain.InstallationResource{}
	for _, resource := range resources {
		if resource.Ownership == domain.ResourceManaged {
			managed[resource.ID] = resource
		}
	}
	steps := make([]installworkflow.Step, 0, len(managed))
	seenIDs := map[string]bool{}
	seenResources := map[string]bool{}
	for _, group := range orderedGroups {
		for _, step := range group.steps {
			if step.Stage != group.stage {
				return nil, fmt.Errorf(
					"step %s is in the %s group with stage %s",
					step.ID,
					group.stage,
					step.Stage,
				)
			}
			if seenIDs[step.ID] {
				return nil, fmt.Errorf("duplicate step ID %q", step.ID)
			}
			seenIDs[step.ID] = true
			resource, exists := managed[step.ResourceID]
			if !exists {
				return nil, fmt.Errorf(
					"step %s references unknown managed resource %q",
					step.ID,
					step.ResourceID,
				)
			}
			if seenResources[step.ResourceID] {
				return nil, fmt.Errorf(
					"managed resource %q has multiple steps",
					step.ResourceID,
				)
			}
			seenResources[step.ResourceID] = true
			if resource.Target != step.Target {
				return nil, fmt.Errorf(
					"step %s target does not match resource %q",
					step.ID,
					step.ResourceID,
				)
			}
			steps = append(steps, step)
		}
	}
	if len(seenResources) != len(managed) {
		return nil, fmt.Errorf(
			"composition covers %d of %d managed resources",
			len(seenResources),
			len(managed),
		)
	}
	if err := validateDirectoryDependencies(groups.Directories); err != nil {
		return nil, err
	}
	if err := validateFileParents(
		groups.Directories,
		groups.Files,
	); err != nil {
		return nil, err
	}
	return steps, nil
}

func validateDirectoryDependencies(
	steps []installworkflow.Step,
) error {
	indexByTarget := map[string]int{}
	for index, step := range steps {
		indexByTarget[step.Target] = index
	}
	for index, step := range steps {
		parent := filepath.Dir(step.Target)
		if parentIndex, managed := indexByTarget[parent]; managed &&
			parentIndex >= index {
			return fmt.Errorf(
				"managed directory parent %s must precede %s",
				parent,
				step.Target,
			)
		}
	}
	return nil
}

func validateFileParents(
	directories []installworkflow.Step,
	files []installworkflow.Step,
) error {
	managedDirectories := map[string]bool{}
	for _, step := range directories {
		managedDirectories[step.Target] = true
	}
	for _, step := range files {
		parent := filepath.Dir(step.Target)
		for candidate := range managedDirectories {
			if pathWithin(candidate, step.Target) &&
				!managedDirectories[parent] {
				return fmt.Errorf(
					"managed file %s requires explicit parent directory %s",
					step.Target,
					parent,
				)
			}
		}
	}
	return nil
}

func pathWithin(parent string, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil &&
		relative != "." &&
		relative != ".." &&
		!filepath.IsAbs(relative) &&
		len(relative) > 0 &&
		relative[0] != '.'
}
