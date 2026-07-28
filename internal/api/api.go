package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"docklane.local/docklane/internal/client"
	"docklane.local/docklane/internal/config"
	"docklane.local/docklane/internal/diagnostics"
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
	upstream   UpstreamProber
	runtime    TraefikRuntimeInspector

	providerMu     sync.RWMutex
	providerStatus domain.ProviderStatus
}

type UpstreamProber interface {
	Probe(context.Context, string) (domain.UpstreamProbe, error)
}

type TraefikRuntimeInspector interface {
	InspectRoute(context.Context, string) (domain.TraefikRouteRuntime, error)
}

type Option func(*API)

func WithUpstreamProber(prober UpstreamProber) Option {
	return func(api *API) {
		api.upstream = prober
	}
}

func WithTraefikRuntimeInspector(inspector TraefikRuntimeInspector) Option {
	return func(api *API) {
		api.runtime = inspector
	}
}

func New(
	cfg config.Config,
	repository *store.Store,
	discovery docker.Discovery,
	reconciler *reconcile.Reconciler,
	options ...Option,
) http.Handler {
	api := &API{
		config:     cfg,
		store:      repository,
		discovery:  discovery,
		reconciler: reconciler,
		providerStatus: domain.ProviderStatus{
			Source: domain.ProviderSourceAwaiting,
		},
	}
	for _, option := range options {
		option(api)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", api.health)
	mux.HandleFunc("GET /api/v1/containers", api.containers)
	mux.HandleFunc("GET /api/v1/network/plan", api.networkPlan)
	mux.HandleFunc("POST /api/v1/network/apply", api.applyNetworkPlan)
	mux.HandleFunc("GET /api/v1/routes", api.routes)
	mux.HandleFunc("POST /api/v1/routes", api.createRoute)
	mux.HandleFunc("GET /api/v1/routes/{id}", api.getRoute)
	mux.HandleFunc("PUT /api/v1/routes/{id}", api.updateRoute)
	mux.HandleFunc("DELETE /api/v1/routes/{id}", api.deleteRoute)
	mux.HandleFunc("GET /api/v1/routes/{id}/upstream-probe", api.upstreamProbe)
	mux.HandleFunc("GET /api/v1/routes/{id}/traefik-runtime", api.traefikRuntime)
	mux.HandleFunc("GET /api/v1/diagnostics/routes/{id}", api.routeDiagnostics)
	mux.HandleFunc("GET /internal/traefik", api.traefik)
	mux.Handle("/", webui.Handler())
	return requestLog(mux)
}

type localDiagnosticsController struct {
	api *API
}

func (controller localDiagnosticsController) Health(
	context.Context,
) (domain.ControllerHealth, error) {
	return controller.api.controllerHealth(), nil
}

func (controller localDiagnosticsController) ListContainersWithNetworkAliases(
	ctx context.Context,
) ([]docker.Container, error) {
	return controller.api.listContainers(ctx, true)
}

func (controller localDiagnosticsController) ListRoutes(
	ctx context.Context,
) (client.Routes, error) {
	routes, err := controller.api.store.ListRoutes(ctx)
	if err != nil {
		return client.Routes{}, err
	}
	return client.Routes{
		Routes:     controller.api.reconciler.Enrich(routes),
		BaseDomain: controller.api.config.BaseDomain,
	}, nil
}

func (controller localDiagnosticsController) InspectTraefikRuntime(
	ctx context.Context,
	id int64,
) (domain.TraefikRouteRuntime, error) {
	route, err := controller.api.readyRoute(ctx, id)
	if err != nil {
		return domain.TraefikRouteRuntime{}, err
	}
	if controller.api.runtime == nil {
		return domain.TraefikRouteRuntime{}, errors.New(
			"Traefik runtime inspection is not configured",
		)
	}
	return controller.api.runtime.InspectRoute(ctx, route.Name)
}

func (controller localDiagnosticsController) ProbeUpstream(
	ctx context.Context,
	id int64,
) (domain.UpstreamProbe, error) {
	route, err := controller.api.readyRoute(ctx, id)
	if err != nil {
		return domain.UpstreamProbe{}, err
	}
	if controller.api.upstream == nil {
		return domain.UpstreamProbe{}, errors.New(
			"proxy-network upstream probe is not configured",
		)
	}
	return controller.api.upstream.Probe(ctx, route.Observed.UpstreamURL)
}

func (a *API) routeDiagnostics(
	response http.ResponseWriter,
	request *http.Request,
) {
	id, err := routeID(request)
	if err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	if _, err := a.store.GetRoute(request.Context(), id); err != nil {
		writeStoreError(response, err)
		return
	}
	report := diagnostics.RunController(
		request.Context(),
		localDiagnosticsController{api: a},
		strconv.FormatInt(id, 10),
	)
	writeJSON(response, http.StatusOK, report)
}

func (a *API) readyRoute(
	ctx context.Context,
	id int64,
) (domain.Route, error) {
	route, err := a.store.GetRoute(ctx, id)
	if err != nil {
		return domain.Route{}, err
	}
	route = a.reconciler.Enrich([]domain.Route{route})[0]
	if route.Observed.State != domain.RouteStateReady ||
		route.Observed.UpstreamURL == "" {
		return domain.Route{}, fmt.Errorf("route is not ready: %s", route.Observed.State)
	}
	return route, nil
}

func (a *API) traefikRuntime(response http.ResponseWriter, request *http.Request) {
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
	if route.Observed.State != domain.RouteStateReady {
		writeError(
			response,
			http.StatusConflict,
			fmt.Errorf("route is not ready for Traefik inspection: %s", route.Observed.State),
		)
		return
	}
	if a.runtime == nil {
		writeError(
			response,
			http.StatusServiceUnavailable,
			errors.New("Traefik runtime inspection is not configured"),
		)
		return
	}
	result, err := a.runtime.InspectRoute(request.Context(), route.Name)
	if err != nil {
		writeError(response, http.StatusBadGateway, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (a *API) upstreamProbe(response http.ResponseWriter, request *http.Request) {
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
	if route.Observed.State != domain.RouteStateReady ||
		route.Observed.UpstreamURL == "" {
		writeError(
			response,
			http.StatusConflict,
			fmt.Errorf("route is not ready for an upstream probe: %s", route.Observed.State),
		)
		return
	}
	if a.upstream == nil {
		writeError(
			response,
			http.StatusServiceUnavailable,
			errors.New("proxy-network upstream probe is not configured"),
		)
		return
	}
	result, err := a.upstream.Probe(
		request.Context(),
		route.Observed.UpstreamURL,
	)
	if err != nil {
		writeError(response, http.StatusBadGateway, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (a *API) networkPlan(response http.ResponseWriter, request *http.Request) {
	plan, err := a.reconciler.NetworkPlan(request.Context())
	if err != nil {
		writeError(response, http.StatusBadGateway, err)
		return
	}
	writeJSON(response, http.StatusOK, plan)
}

func (a *API) applyNetworkPlan(response http.ResponseWriter, request *http.Request) {
	var input struct {
		Token string `json:"token"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(response, http.StatusBadRequest, err)
		return
	}
	result, err := a.reconciler.ApplyNetworkPlan(request.Context(), input.Token)
	if err != nil {
		writeError(response, http.StatusConflict, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (a *API) health(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, a.controllerHealth())
}

func (a *API) controllerHealth() domain.ControllerHealth {
	lastRefresh, reconcileErr := a.reconciler.LastResult()
	providerStatus := a.getProviderStatus()
	status := "ok"
	lastError := ""
	if reconcileErr != nil {
		status = "degraded"
		lastError = reconcileErr.Error()
	}
	if providerStatus.Source == domain.ProviderSourceLastKnownGood ||
		providerStatus.Source == domain.ProviderSourceUnavailable ||
		providerStatus.LastError != "" {
		status = "degraded"
	}
	return domain.ControllerHealth{
		Status:              status,
		BaseDomain:          a.config.BaseDomain,
		ProxyNetwork:        a.config.ProxyNetwork,
		LastReconciledAt:    lastRefresh,
		LastReconcileError:  lastError,
		ReconcileIntervalMS: a.config.ReconcileEvery.Milliseconds(),
		Provider:            providerStatus,
	}
}

func (a *API) containers(response http.ResponseWriter, request *http.Request) {
	withAliases := request.URL.Query().Get("networkAliases") == "true"
	containers, err := a.listContainers(request.Context(), withAliases)
	if err != nil {
		writeError(response, http.StatusBadGateway, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"containers": containers})
}

func (a *API) listContainers(
	ctx context.Context,
	withAliases bool,
) ([]docker.Container, error) {
	containers, err := a.discovery.ListContainers(ctx)
	if err != nil {
		return nil, err
	}
	if aliases, ok := a.discovery.(docker.NetworkAliasDiscovery); ok &&
		withAliases &&
		a.config.ProxyNetwork != "" {
		for index := range containers {
			if !containers[index].HasNetwork(a.config.ProxyNetwork) {
				continue
			}
			networkAliases, err := aliases.NetworkAliases(
				ctx,
				containers[index].ID,
				a.config.ProxyNetwork,
			)
			if err != nil {
				return nil, err
			}
			if containers[index].NetworkAliases == nil {
				containers[index].NetworkAliases = map[string][]string{}
			}
			containers[index].NetworkAliases[a.config.ProxyNetwork] = networkAliases
		}
	}
	return containers, nil
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
	Revision *uint64                  `json:"revision"`
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
	if input.Revision != nil {
		writeError(response, http.StatusBadRequest, errors.New("revision is only valid when updating a route"))
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
	if err := a.validateApplicationTarget(request.Context(), route); err != nil {
		writeError(response, http.StatusUnprocessableEntity, err)
		return
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
	if input.Revision == nil || *input.Revision == 0 {
		writeError(response, http.StatusBadRequest, errors.New("revision is required when updating a route"))
		return
	}
	existing.Revision = *input.Revision
	existing.Name = input.Name
	existing.Selector = input.Selector
	existing.Port = input.Port
	if input.Scheme != "" {
		existing.Scheme = input.Scheme
	}
	if input.Enabled != nil {
		existing.Enabled = *input.Enabled
	}
	if err := a.validateApplicationTarget(request.Context(), existing); err != nil {
		writeError(response, http.StatusUnprocessableEntity, err)
		return
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

func (a *API) validateApplicationTarget(ctx context.Context, route domain.Route) error {
	if !route.Enabled {
		return nil
	}
	containers, err := a.discovery.ListContainers(ctx)
	if err != nil {
		return nil
	}
	if claim, exists := docker.FindTraefikHostnameClaim(
		route.Hostname(a.config.BaseDomain),
		containers,
	); exists {
		return docker.HostnameClaimError(
			route.Hostname(a.config.BaseDomain),
			claim,
		)
	}
	container, err := docker.ResolveContainer(route.Selector, containers)
	if err != nil {
		return nil
	}
	return docker.ValidateApplicationTarget(container)
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
	configuration, err := a.liveTraefikConfiguration(request.Context())
	if err == nil {
		writeJSON(response, http.StatusOK, configuration)
		return
	}
	liveErr := err
	snapshot, snapshotErr := a.store.GetProviderSnapshot(request.Context())
	if snapshotErr == nil {
		configuration, snapshotErr = traefik.DecodeValidatedSnapshot(
			snapshot.Configuration,
			snapshot.Fingerprint,
		)
	}
	if snapshotErr == nil {
		a.setProviderStatus(domain.ProviderStatus{
			Source:      domain.ProviderSourceLastKnownGood,
			Fingerprint: snapshot.Fingerprint,
			GeneratedAt: snapshot.GeneratedAt,
			LastError:   liveErr.Error(),
		})
		writeJSON(response, http.StatusOK, configuration)
		return
	}
	combined := fmt.Errorf(
		"generate live provider configuration: %w; load last-known-good: %v",
		liveErr,
		snapshotErr,
	)
	a.setProviderStatus(domain.ProviderStatus{
		Source:    domain.ProviderSourceUnavailable,
		LastError: combined.Error(),
	})
	writeError(response, http.StatusBadGateway, combined)
}

func (a *API) liveTraefikConfiguration(
	ctx context.Context,
) (traefik.Configuration, error) {
	routes, err := a.store.ListRoutes(ctx)
	if err != nil {
		return traefik.Configuration{}, err
	}
	routes = a.reconciler.Enrich(routes)
	containers, err := a.discovery.ListContainers(ctx)
	if err != nil {
		return traefik.Configuration{}, err
	}
	configuration := traefik.Build(
		routes,
		containers,
		a.config.BaseDomain,
		a.config.ProxyNetwork,
	)
	encoded, fingerprint, err := traefik.EncodeValidated(configuration)
	if err != nil {
		return traefik.Configuration{}, fmt.Errorf(
			"validate generated provider configuration: %w",
			err,
		)
	}
	generatedAt := time.Now().UTC()
	snapshot, snapshotErr := a.store.SaveProviderSnapshot(
		ctx,
		domain.ProviderSnapshot{
			Configuration: encoded,
			Fingerprint:   fingerprint,
			GeneratedAt:   generatedAt,
		},
	)
	status := domain.ProviderStatus{
		Source:      domain.ProviderSourceLive,
		Fingerprint: fingerprint,
		GeneratedAt: generatedAt,
	}
	if snapshotErr != nil {
		status.LastError = fmt.Sprintf(
			"persist last-known-good provider configuration: %v",
			snapshotErr,
		)
	} else {
		status.Fingerprint = snapshot.Fingerprint
		status.GeneratedAt = snapshot.GeneratedAt
	}
	a.setProviderStatus(status)
	return configuration, nil
}

func (a *API) setProviderStatus(status domain.ProviderStatus) {
	a.providerMu.Lock()
	defer a.providerMu.Unlock()
	a.providerStatus = status
}

func (a *API) getProviderStatus() domain.ProviderStatus {
	a.providerMu.RLock()
	defer a.providerMu.RUnlock()
	return a.providerStatus
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
	case errors.Is(err, store.ErrRevisionConflict):
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
