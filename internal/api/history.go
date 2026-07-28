package api

import (
	"context"
	"errors"
	"log"
	"strconv"
	"time"

	"docklane.local/docklane/internal/diagnostics"
	"docklane.local/docklane/internal/domain"
)

const healthSampleTimeout = 15 * time.Second

func (a *API) RunHealthHistory(ctx context.Context) {
	ticker := time.NewTicker(a.config.HistoryEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.SampleHealthHistory(ctx); err != nil &&
				!errors.Is(err, context.Canceled) {
				log.Printf("Sample route health history: %v", err)
			}
		}
	}
}

func (a *API) SampleHealthHistory(ctx context.Context) error {
	controller := localDiagnosticsController{api: a}
	routes, err := controller.ListRoutes(ctx)
	if err != nil {
		return err
	}
	var sampleErrors []error
	for _, route := range routes.Routes {
		if !route.Enabled {
			continue
		}
		sampleCtx, cancel := context.WithTimeout(ctx, healthSampleTimeout)
		report := diagnostics.RunController(
			sampleCtx,
			controller,
			strconv.FormatInt(route.ID, 10),
		)
		_, saveErr := a.store.SaveHealthSnapshot(
			sampleCtx,
			domain.HealthSnapshot{
				RouteID:    route.ID,
				RecordedAt: report.GeneratedAt,
				Report:     report,
			},
			a.config.HistoryLimit,
		)
		cancel()
		if saveErr != nil {
			sampleErrors = append(sampleErrors, saveErr)
		}
	}
	return errors.Join(sampleErrors...)
}
