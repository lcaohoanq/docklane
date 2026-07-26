package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
)

type RouteStore interface {
	ListRoutes(context.Context) ([]domain.Route, error)
}

type Reconciler struct {
	store     RouteStore
	discovery docker.Discovery
	interval  time.Duration

	mu           sync.RWMutex
	observations map[int64]domain.RouteObservation
	lastRefresh  time.Time
	lastError    error
}

func New(store RouteStore, discovery docker.Discovery, interval time.Duration) *Reconciler {
	return &Reconciler{
		store:        store,
		discovery:    discovery,
		interval:     interval,
		observations: map[int64]domain.RouteObservation{},
	}
}

func (r *Reconciler) Run(ctx context.Context) {
	if err := r.Refresh(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("Docklane reconciliation failed: %v", err)
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.Refresh(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("Docklane reconciliation failed: %v", err)
			}
		}
	}
}

func (r *Reconciler) Refresh(ctx context.Context) error {
	routes, err := r.store.ListRoutes(ctx)
	if err != nil {
		r.setError(err)
		return fmt.Errorf("list desired routes: %w", err)
	}
	containers, err := r.discovery.ListContainers(ctx)
	if err != nil {
		observations := make(map[int64]domain.RouteObservation, len(routes))
		checkedAt := time.Now().UTC()
		for _, route := range routes {
			observations[route.ID] = domain.RouteObservation{
				State:     domain.RouteStateError,
				Message:   err.Error(),
				CheckedAt: checkedAt,
			}
		}
		r.replace(observations, err)
		return fmt.Errorf("discover containers: %w", err)
	}

	checkedAt := time.Now().UTC()
	observations := make(map[int64]domain.RouteObservation, len(routes))
	for _, route := range routes {
		observations[route.ID] = observe(route, containers, checkedAt)
	}
	r.replace(observations, nil)
	return nil
}

func (r *Reconciler) Enrich(routes []domain.Route) []domain.Route {
	r.mu.RLock()
	defer r.mu.RUnlock()
	enriched := make([]domain.Route, len(routes))
	copy(enriched, routes)
	for index := range enriched {
		if observation, exists := r.observations[enriched[index].ID]; exists {
			enriched[index].Observed = observation
		} else {
			enriched[index].Observed = domain.RouteObservation{
				State:   domain.RouteStateError,
				Message: "route has not been reconciled yet",
			}
		}
	}
	return enriched
}

func (r *Reconciler) LastResult() (time.Time, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastRefresh, r.lastError
}

func (r *Reconciler) replace(observations map[int64]domain.RouteObservation, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observations = observations
	r.lastRefresh = time.Now().UTC()
	r.lastError = err
}

func (r *Reconciler) setError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastRefresh = time.Now().UTC()
	r.lastError = err
}

func observe(route domain.Route, containers []docker.Container, checkedAt time.Time) domain.RouteObservation {
	if !route.Enabled {
		return domain.RouteObservation{
			State:     domain.RouteStateDisabled,
			Message:   "route is disabled",
			CheckedAt: checkedAt,
		}
	}
	container, err := docker.ResolveContainer(route.Selector, containers)
	if err != nil {
		state := domain.RouteStateError
		switch {
		case errors.Is(err, docker.ErrNoMatch):
			state = domain.RouteStateUnresolved
		case errors.Is(err, docker.ErrAmbiguous):
			state = domain.RouteStateAmbiguous
		}
		return domain.RouteObservation{
			State:     state,
			Message:   err.Error(),
			CheckedAt: checkedAt,
		}
	}

	return domain.RouteObservation{
		State:         domain.RouteStateReady,
		Message:       "running workload resolved",
		ContainerID:   container.ID,
		ContainerName: container.Name,
		UpstreamURL:   fmt.Sprintf("%s://%s:%d", route.Scheme, container.Name, route.Port),
		CheckedAt:     checkedAt,
	}
}
