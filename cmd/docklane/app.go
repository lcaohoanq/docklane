package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"docklane.local/docklane/internal/appflow"
	"docklane.local/docklane/internal/client"
	"docklane.local/docklane/internal/domain"
)

type appEnableResult struct {
	Application string                 `json:"application"`
	Hostname    string                 `json:"hostname"`
	Created     bool                   `json:"created"`
	Changed     bool                   `json:"changed"`
	Route       domain.Route           `json:"route"`
	Readiness   *domain.RouteReadiness `json:"readiness,omitempty"`
}

type appGuideResult struct {
	Application string `json:"application"`
	RouteName   string `json:"routeName"`
	Port        uint16 `json:"port"`
	Scheme      string `json:"scheme"`
	Guidance    string `json:"guidance"`
}

func app(args []string) error {
	if len(args) == 0 {
		return errors.New("app requires a subcommand: enable, disable, or guide")
	}
	switch args[0] {
	case "enable":
		return appEnable(args[1:])
	case "disable":
		return appDisable(args[1:])
	case "guide":
		return appGuide(args[1:])
	default:
		return fmt.Errorf("unknown app command %q", args[0])
	}
}

func appEnable(args []string) error {
	target, remaining, err := appTarget("enable", args)
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("app enable", flag.ContinueOnError)
	controllerURL := flags.String("url", defaultControllerURL(), "Docklane controller URL")
	name := flags.String("name", "", "local route name (defaults to the Compose service or container)")
	port := flags.Uint("port", 0, "internal container port (inferred when unambiguous)")
	scheme := flags.String("scheme", "", "upstream scheme (inferred from the selected port)")
	wait := flags.Duration("wait", 30*time.Second, "maximum time to wait for a reachable route (0 disables waiting)")
	dryRun := flags.Bool("dry-run", false, "validate and print without saving")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(remaining); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: docklane app enable TARGET [options]")
	}
	if *port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if *wait < 0 {
		return errors.New("wait duration must not be negative")
	}

	ctx := context.Background()
	apiClient := client.New(*controllerURL)
	containers, err := apiClient.ListContainers(ctx)
	if err != nil {
		return err
	}
	application, err := appflow.Resolve(target, containers)
	if err != nil {
		return err
	}
	routeName := application.Name
	if *name != "" {
		routeName = *name
	}
	selectedPort, err := appflow.SelectPort(application.Container, uint16(*port))
	if err != nil {
		return err
	}
	selectedScheme := strings.TrimSpace(*scheme)
	if selectedScheme == "" {
		selectedScheme = appflow.RecommendedScheme(selectedPort)
	}
	candidate := domain.Route{
		Name:     routeName,
		Selector: application.Selector,
		Port:     selectedPort,
		Scheme:   selectedScheme,
		Enabled:  true,
	}
	if err := candidate.Validate(); err != nil {
		return err
	}

	routes, err := apiClient.ListRoutes(ctx)
	if err != nil {
		return err
	}
	existing, exists := appflow.FindRouteByName(routes.Routes, routeName)
	if exists && (!appflow.SameSelector(existing.Selector, candidate.Selector) ||
		existing.Port != candidate.Port ||
		existing.Scheme != candidate.Scheme) {
		return fmt.Errorf(
			"local domain %s already belongs to route %d with a different target; "+
				"choose another --name or edit that route explicitly",
			existing.Hostname(routes.BaseDomain),
			existing.ID,
		)
	}
	if *dryRun {
		if exists {
			candidate = existing
			candidate.Enabled = true
		}
		return printJSON(appEnableResult{
			Application: application.Identity,
			Hostname:    candidate.Hostname(routes.BaseDomain),
			Created:     !exists,
			Changed:     !exists || !existing.Enabled,
			Route:       candidate,
		})
	}

	result := appEnableResult{
		Application: application.Identity,
		Created:     !exists,
		Changed:     !exists || !existing.Enabled,
	}
	switch {
	case !exists:
		result.Route, err = apiClient.CreateRoute(ctx, candidate)
	case existing.Enabled:
		result.Route = existing
	default:
		existing.Enabled = true
		result.Route, err = apiClient.UpdateRoute(ctx, existing)
	}
	if err != nil {
		return err
	}
	result.Hostname = result.Route.Hostname(routes.BaseDomain)
	if *wait > 0 {
		readiness, readyErr := waitForRoute(
			ctx,
			apiClient,
			result.Route.ID,
			*wait,
		)
		result.Readiness = &readiness
		if *asJSON {
			if err := printJSON(result); err != nil {
				return err
			}
		} else {
			printAppEnableResult(result)
		}
		return readyErr
	}
	if *asJSON {
		return printJSON(result)
	}
	printAppEnableResult(result)
	fmt.Println("Route was saved; readiness waiting was disabled.")
	return nil
}

func appDisable(args []string) error {
	target, remaining, err := appTarget("disable", args)
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("app disable", flag.ContinueOnError)
	controllerURL := flags.String("url", defaultControllerURL(), "Docklane controller URL")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(remaining); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: docklane app disable ROUTE [options]")
	}
	apiClient := client.New(*controllerURL)
	payload, err := apiClient.ListRoutes(context.Background())
	if err != nil {
		return err
	}
	route, found := findAppRoute(payload.Routes, target)
	if !found {
		return fmt.Errorf("no route named or numbered %q exists", target)
	}
	changed := route.Enabled
	if changed {
		route.Enabled = false
		route, err = apiClient.UpdateRoute(context.Background(), route)
		if err != nil {
			return err
		}
	}
	if *asJSON {
		return printJSON(struct {
			Changed  bool         `json:"changed"`
			Hostname string       `json:"hostname"`
			Route    domain.Route `json:"route"`
		}{
			Changed:  changed,
			Hostname: route.Hostname(payload.BaseDomain),
			Route:    route,
		})
	}
	if changed {
		fmt.Printf(
			"Disabled %s. Docklane will detach only an owned proxy endpoint when no other route needs it.\n",
			route.Hostname(payload.BaseDomain),
		)
	} else {
		fmt.Printf("%s is already disabled.\n", route.Hostname(payload.BaseDomain))
	}
	return nil
}

func appGuide(args []string) error {
	target, remaining, err := appTarget("guide", args)
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("app guide", flag.ContinueOnError)
	controllerURL := flags.String("url", defaultControllerURL(), "Docklane controller URL")
	name := flags.String("name", "", "local route name")
	port := flags.Uint("port", 0, "internal container port")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(remaining); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: docklane app guide TARGET [options]")
	}
	if *port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	containers, err := client.New(*controllerURL).ListContainers(context.Background())
	if err != nil {
		return err
	}
	application, err := appflow.Resolve(target, containers)
	if err != nil {
		return err
	}
	if *name != "" {
		application.Name = *name
	}
	selectedPort := uint16(*port)
	if selectedPort == 0 {
		selectedPort, err = appflow.SelectPort(application.Container, 0)
		if err != nil {
			return err
		}
	}
	candidate := domain.Route{
		Name:     application.Name,
		Selector: application.Selector,
		Port:     selectedPort,
		Scheme:   appflow.RecommendedScheme(selectedPort),
		Enabled:  true,
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	result := appGuideResult{
		Application: application.Identity,
		RouteName:   application.Name,
		Port:        selectedPort,
		Scheme:      candidate.Scheme,
		Guidance:    appflow.ComposeGuidance(application, selectedPort),
	}
	if *asJSON {
		return printJSON(result)
	}
	fmt.Print(result.Guidance)
	return nil
}

func appTarget(command string, args []string) (string, []string, error) {
	if len(args) == 0 || args[0] == "" || strings.HasPrefix(args[0], "-") {
		return "", nil, fmt.Errorf(
			"usage: docklane app %s TARGET [options]",
			command,
		)
	}
	return args[0], args[1:], nil
}

func findAppRoute(routes []domain.Route, target string) (domain.Route, bool) {
	if route, found := appflow.FindRouteByName(routes, target); found {
		return route, true
	}
	if id, err := strconv.ParseInt(target, 10, 64); err == nil && id > 0 {
		for _, route := range routes {
			if route.ID == id {
				return route, true
			}
		}
	}
	return domain.Route{}, false
}

func waitForRoute(
	ctx context.Context,
	apiClient *client.Client,
	routeID int64,
	timeout time.Duration,
) (domain.RouteReadiness, error) {
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var last domain.RouteReadiness
	for {
		readiness, err := apiClient.RouteReadiness(waitContext, routeID)
		if err != nil {
			if waitContext.Err() != nil {
				return last, fmt.Errorf(
					"route %d was saved but did not become reachable within %s",
					routeID,
					timeout,
				)
			}
			return last, fmt.Errorf("route %d was saved but readiness failed: %w", routeID, err)
		}
		last = readiness
		if readiness.Ready {
			return readiness, nil
		}
		if readiness.State == domain.RouteReadinessError {
			return readiness, fmt.Errorf(
				"route %d was saved but is not reachable: %s; run docklane doctor %d",
				routeID,
				readiness.Message,
				routeID,
			)
		}
		select {
		case <-waitContext.Done():
			return last, fmt.Errorf(
				"route %d was saved but did not become reachable within %s; "+
					"last state: %s; run docklane doctor %d",
				routeID,
				timeout,
				last.State,
				routeID,
			)
		case <-ticker.C:
		}
	}
}

func printAppEnableResult(result appEnableResult) {
	action := "Route already enabled"
	if result.Created {
		action = "Created"
	} else if result.Changed {
		action = "Enabled"
	}
	fmt.Printf("%s https://%s for %s.\n", action, result.Hostname, result.Application)
	if result.Readiness != nil && result.Readiness.Ready {
		fmt.Printf("Ready: %s\n", result.Readiness.Message)
	}
}
