package domain

import "time"

type RouteReadinessState string

const (
	RouteReadinessReconciling RouteReadinessState = "reconciling"
	RouteReadinessPublishing  RouteReadinessState = "publishing"
	RouteReadinessVerifying   RouteReadinessState = "verifying"
	RouteReadinessReady       RouteReadinessState = "ready"
	RouteReadinessDisabled    RouteReadinessState = "disabled"
	RouteReadinessError       RouteReadinessState = "error"
)

type RouteReadiness struct {
	RouteID   int64                `json:"routeId"`
	Revision  uint64               `json:"revision"`
	State     RouteReadinessState  `json:"state"`
	Ready     bool                 `json:"ready"`
	Message   string               `json:"message"`
	CheckedAt time.Time            `json:"checkedAt"`
	Runtime   *TraefikRouteRuntime `json:"runtime,omitempty"`
}
