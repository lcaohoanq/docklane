package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"docklane.local/docklane/internal/config"
	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/reconcile"
	"docklane.local/docklane/internal/store"
	"docklane.local/docklane/internal/traefik"
	"docklane.local/docklane/internal/webui"
)

type API struct {
	config     config.Config
	store      *store.Store
	discovery  docker.Discovery
	reconciler *reconcile.Reconciler
}

func New(
	cfg config.Config,
	repository *store.Store,
	discovery docker.Discovery,
	reconciler *reconcile.Reconciler,
) http.Handler {
	api := &API{
		config:     cfg,
		store:      repository,
		discovery:  discovery,
		reconciler: reconciler,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", api.health)
	mux.HandleFunc("GET /api/v1/containers", api.containers)
	mux.HandleFunc("GET /api/v1/routes", api.routes)
	mux.HandleFunc("POST /api/v1/routes", api.createRoute)
	mux.HandleFunc("GET /api/v1/routes/{id}", api.getRoute)
	mux.HandleFunc("PUT /api/v1/routes/{id}", api.updateRoute)
	mux.HandleFunc("DELETE /api/v1/routes/{id}", api.deleteRoute)
	mux.HandleFunc("GET /internal/traefik", api.traefik)
	mux.Handle("/", webui.Handler())
	return requestLog(mux)
}

func (a *API) health(response http.ResponseWriter, _ *http.Request) {
	lastRefresh, reconcileErr := a.reconciler.LastResult()
	status := "ok"
	lastError := ""
	if reconcileErr != nil {
		status = "degraded"
		lastError = reconcileErr.Error()
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"status":              status,
		"baseDomain":          a.config.BaseDomain,
		"lastReconciledAt":    lastRefresh,
		"lastReconcileError":  lastError,
		"reconcileIntervalMs": a.config.ReconcileEvery.Milliseconds(),
	})
}

func (a *API) containers(response http.ResponseWriter, request *http.Request) {
	containers, err := a.discovery.ListContainers(request.Context())
	if err != nil {
		writeError(response, http.StatusBadGateway, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"containers": containers})
}

func (a *API) routes(response http.ResponseWriter, request *http.Request) {
	routes, err := a.store.ListRoutes(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	routes = a.reconciler.Enrich(routes)
	writeJSON(response, http.StatusOK, map[string]any{
		"routes":              routes,
		"baseDomain":          a.config.BaseDomain,
		"reconcileIntervalMs": a.config.ReconcileEvery.Milliseconds(),
	})
}

type routeInput struct {
	Name     string                   `json:"name"`
	Selector domain.ContainerSelector `json:"selector"`
	Port     uint16                   `json:"port"`
	Scheme   string                   `json:"scheme"`
	Enabled  *bool                    `json:"enabled"`
}

func (a *API) createRoute(response http.ResponseWriter, request *http.Request) {
	input, err := decodeRouteInput(response, request)
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	scheme := input.Scheme
	if scheme == "" {
		scheme = "http"
	}
	route := domain.Route{
		Name:     input.Name,
		Selector: input.Selector,
		Port:     input.Port,
		Scheme:   scheme,
		Enabled:  enabled,
	}
	created, err := a.store.CreateRoute(request.Context(), route)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	_ = a.reconciler.Refresh(request.Context())
	created = a.reconciler.Enrich([]domain.Route{created})[0]
	writeJSON(response, http.StatusCreated, created)
}

func (a *API) getRoute(response http.ResponseWriter, request *http.Request) {
	id, err := routeID(request)
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	route, err := a.store.GetRoute(request.Context(), id)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	route = a.reconciler.Enrich([]domain.Route{route})[0]
	writeJSON(response, http.StatusOK, route)
}

func (a *API) updateRoute(response http.ResponseWriter, request *http.Request) {
	id, err := routeID(request)
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	existing, err := a.store.GetRoute(request.Context(), id)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	input, err := decodeRouteInput(response, request)
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	existing.Name = input.Name
	existing.Selector = input.Selector
	existing.Port = input.Port
	if input.Scheme != "" {
		existing.Scheme = input.Scheme
	}
	if input.Enabled != nil {
		existing.Enabled = *input.Enabled
	}
	updated, err := a.store.UpdateRoute(request.Context(), existing)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	_ = a.reconciler.Refresh(request.Context())
	updated = a.reconciler.Enrich([]domain.Route{updated})[0]
	writeJSON(response, http.StatusOK, updated)
}

func (a *API) deleteRoute(response http.ResponseWriter, request *http.Request) {
	id, err := routeID(request)
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	if err := a.store.DeleteRoute(request.Context(), id); err != nil {
		writeStoreError(response, err)
		return
	}
	_ = a.reconciler.Refresh(request.Context())
	response.WriteHeader(http.StatusNoContent)
}

func (a *API) traefik(response http.ResponseWriter, request *http.Request) {
	routes, err := a.store.ListRoutes(request.Context())
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	containers, err := a.discovery.ListContainers(request.Context())
	if err != nil {
		writeError(response, http.StatusBadGateway, err)
		return
	}
	writeJSON(response, http.StatusOK, traefik.Build(routes, containers, a.config.BaseDomain))
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeError(response http.ResponseWriter, status int, err error) {
	if err == nil {
		err = errors.New(http.StatusText(status))
	}
	writeJSON(response, status, map[string]string{"error": err.Error()})
}

func decodeRouteInput(response http.ResponseWriter, request *http.Request) (routeInput, error) {
	var input routeInput
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&input)
	return input, err
}

func routeID(request *http.Request) (int64, error) {
	id, err := strconv.ParseInt(request.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("route ID must be a positive integer")
	}
	return id, nil
}

func writeStoreError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(response, http.StatusNotFound, err)
	case errors.Is(err, store.ErrConflict):
		writeError(response, http.StatusConflict, err)
	default:
		writeError(response, http.StatusUnprocessableEntity, err)
	}
}

func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(response, request)
	})
}
