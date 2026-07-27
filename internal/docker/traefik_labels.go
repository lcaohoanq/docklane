package docker

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	traefikRouterRuleLabel = regexp.MustCompile(
		`^traefik\.http\.routers\.([^.]+)\.rule$`,
	)
	traefikHostMatcher = regexp.MustCompile(`\bHost\s*\(([^)]*)\)`)
	traefikHostValue   = regexp.MustCompile("[`\"]([^`\"]+)[`\"]")
)

type TraefikHostnameClaim struct {
	Hostname      string
	Router        string
	ContainerName string
}

func TraefikHostnameClaims(containers []Container) []TraefikHostnameClaim {
	var claims []TraefikHostnameClaim
	for _, container := range containers {
		if !strings.EqualFold(
			strings.TrimSpace(container.Labels["traefik.enable"]),
			"true",
		) {
			continue
		}
		for label, rule := range container.Labels {
			match := traefikRouterRuleLabel.FindStringSubmatch(
				strings.ToLower(label),
			)
			if len(match) != 2 {
				continue
			}
			for _, matcher := range traefikHostMatcher.FindAllStringSubmatch(
				rule,
				-1,
			) {
				for _, value := range traefikHostValue.FindAllStringSubmatch(
					matcher[1],
					-1,
				) {
					hostname := normalizeHostname(value[1])
					if hostname == "" {
						continue
					}
					claims = append(claims, TraefikHostnameClaim{
						Hostname:      hostname,
						Router:        match[1],
						ContainerName: container.Name,
					})
				}
			}
		}
	}
	sort.Slice(claims, func(i, j int) bool {
		if claims[i].Hostname != claims[j].Hostname {
			return claims[i].Hostname < claims[j].Hostname
		}
		if claims[i].ContainerName != claims[j].ContainerName {
			return claims[i].ContainerName < claims[j].ContainerName
		}
		return claims[i].Router < claims[j].Router
	})
	return claims
}

func FindTraefikHostnameClaim(
	hostname string,
	containers []Container,
) (TraefikHostnameClaim, bool) {
	hostname = normalizeHostname(hostname)
	for _, claim := range TraefikHostnameClaims(containers) {
		if claim.Hostname == hostname {
			return claim, true
		}
	}
	return TraefikHostnameClaim{}, false
}

func HostnameClaimError(hostname string, claim TraefikHostnameClaim) error {
	return fmt.Errorf(
		"hostname %s is already claimed by Docker-label router %s on container %s; "+
			"disable or rename that router before enabling this Docklane route",
		hostname,
		claim.Router,
		claim.ContainerName,
	)
}

func normalizeHostname(hostname string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
}
