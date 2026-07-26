package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var routeNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

type ContainerSelector struct {
	ComposeProject string `json:"composeProject"`
	ComposeService string `json:"composeService"`
	ContainerID    string `json:"containerId,omitempty"`
}

type Route struct {
	ID        int64             `json:"id"`
	Revision  uint64            `json:"revision"`
	Name      string            `json:"name"`
	Selector  ContainerSelector `json:"selector"`
	Port      uint16            `json:"port"`
	Scheme    string            `json:"scheme"`
	Enabled   bool              `json:"enabled"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
	Observed  RouteObservation  `json:"observed"`
}

type RouteState string

const (
	RouteStateReady      RouteState = "ready"
	RouteStateDisabled   RouteState = "disabled"
	RouteStateUnresolved RouteState = "unresolved"
	RouteStateAmbiguous  RouteState = "ambiguous"
	RouteStateError      RouteState = "error"
)

type RouteObservation struct {
	State         RouteState `json:"state"`
	Message       string     `json:"message,omitempty"`
	ContainerID   string     `json:"containerId,omitempty"`
	ContainerName string     `json:"containerName,omitempty"`
	UpstreamURL   string     `json:"upstreamUrl,omitempty"`
	CheckedAt     time.Time  `json:"checkedAt"`
}

type NetworkAttachment struct {
	ContainerID string    `json:"containerId"`
	Network     string    `json:"network"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (r Route) Validate() error {
	if !routeNamePattern.MatchString(r.Name) {
		return fmt.Errorf("name must be a lowercase DNS label")
	}
	if r.Port == 0 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if r.Scheme != "http" && r.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https")
	}
	if strings.TrimSpace(r.Selector.ContainerID) == "" &&
		(strings.TrimSpace(r.Selector.ComposeProject) == "" ||
			strings.TrimSpace(r.Selector.ComposeService) == "") {
		return fmt.Errorf("container ID or Compose project/service selector is required")
	}
	return nil
}

func (r Route) Hostname(baseDomain string) string {
	return r.Name + "." + baseDomain
}
