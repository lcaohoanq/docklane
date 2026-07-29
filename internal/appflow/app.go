package appflow

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
)

var (
	ErrTargetNotFound  = errors.New("no running application matches the target")
	ErrTargetAmbiguous = errors.New(
		"application target matches multiple running containers",
	)
)

type Application struct {
	Container docker.Container
	Identity  string
	Selector  domain.ContainerSelector
	Name      string
}

func Resolve(target string, containers []docker.Container) (Application, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return Application{}, errors.New("application target is required")
	}
	matches := make([]docker.Container, 0, 1)
	seen := map[string]bool{}
	for _, container := range containers {
		if !matchesTarget(target, container) || seen[container.ID] {
			continue
		}
		seen[container.ID] = true
		matches = append(matches, container)
	}
	if len(matches) == 0 {
		return Application{}, fmt.Errorf("%w %q", ErrTargetNotFound, target)
	}
	if len(matches) > 1 {
		identities := make([]string, 0, len(matches))
		for _, container := range matches {
			identities = append(identities, Identity(container))
		}
		sort.Strings(identities)
		return Application{}, fmt.Errorf(
			"%w %q: %s",
			ErrTargetAmbiguous,
			target,
			strings.Join(identities, ", "),
		)
	}
	container := matches[0]
	if err := docker.ValidateApplicationTarget(container); err != nil {
		return Application{}, err
	}
	selector := domain.ContainerSelector{ContainerID: container.ID}
	if container.ComposeProject != "" && container.ComposeService != "" {
		selector = domain.ContainerSelector{
			ComposeProject: container.ComposeProject,
			ComposeService: container.ComposeService,
		}
	}
	name := container.ComposeService
	if name == "" {
		name = container.Name
	}
	name = RouteName(name)
	if name == "" {
		return Application{}, fmt.Errorf(
			"cannot derive a local domain name from container %q; supply --name",
			container.Name,
		)
	}
	return Application{
		Container: container,
		Identity:  Identity(container),
		Selector:  selector,
		Name:      name,
	}, nil
}

func Identity(container docker.Container) string {
	if container.ComposeProject != "" && container.ComposeService != "" {
		return container.ComposeProject + "/" + container.ComposeService
	}
	return container.Name
}

func SelectPort(container docker.Container, requested uint16) (uint16, error) {
	if requested != 0 {
		if err := docker.ValidateTCPPort(container, requested); err != nil {
			return 0, err
		}
		return requested, nil
	}
	ports := uniquePorts(container.ExposedPorts)
	switch len(ports) {
	case 0:
		return 0, fmt.Errorf(
			"%s declares no TCP port; add Compose `expose` and supply --port",
			Identity(container),
		)
	case 1:
		return ports[0], nil
	default:
		return 0, fmt.Errorf(
			"%s declares multiple TCP ports %v; choose one with --port",
			Identity(container),
			ports,
		)
	}
}

func RouteName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var output strings.Builder
	dash := false
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') {
			if dash && output.Len() > 0 {
				output.WriteByte('-')
			}
			output.WriteRune(character)
			dash = false
			continue
		}
		dash = output.Len() > 0
	}
	name := strings.Trim(output.String(), "-")
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}

func SameSelector(left, right domain.ContainerSelector) bool {
	return left.ContainerID == right.ContainerID &&
		left.ComposeProject == right.ComposeProject &&
		left.ComposeService == right.ComposeService
}

func FindRouteByName(routes []domain.Route, name string) (domain.Route, bool) {
	for _, route := range routes {
		if route.Name == name {
			return route, true
		}
	}
	return domain.Route{}, false
}

func ComposeGuidance(application Application, port uint16) string {
	var output strings.Builder
	fmt.Fprintf(
		&output,
		"# Docklane attaches %s to the proxy network at runtime.\n",
		application.Identity,
	)
	output.WriteString("# No host port or Traefik labels are required.\n")
	if application.Container.ComposeService != "" &&
		!containsPort(application.Container.ExposedPorts, port) {
		fmt.Fprintf(
			&output,
			"services:\n  %s:\n    expose:\n      - %q\n\n",
			application.Container.ComposeService,
			fmt.Sprintf("%d", port),
		)
	}
	fmt.Fprintf(
		&output,
		"docklane app enable %s --name %s --port %d\n",
		application.Identity,
		application.Name,
		port,
	)
	return output.String()
}

func matchesTarget(target string, container docker.Container) bool {
	if target == container.Name ||
		target == Identity(container) ||
		target == container.ID ||
		strings.HasPrefix(container.ID, target) {
		return true
	}
	return container.ComposeService != "" && target == container.ComposeService
}

func uniquePorts(ports []uint16) []uint16 {
	seen := map[uint16]bool{}
	result := make([]uint16, 0, len(ports))
	for _, port := range ports {
		if port == 0 || seen[port] {
			continue
		}
		seen[port] = true
		result = append(result, port)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left] < result[right]
	})
	return result
}

func containsPort(ports []uint16, target uint16) bool {
	for _, port := range ports {
		if port == target {
			return true
		}
	}
	return false
}
