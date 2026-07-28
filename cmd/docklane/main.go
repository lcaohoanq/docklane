package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"docklane.local/docklane/internal/api"
	"docklane.local/docklane/internal/client"
	"docklane.local/docklane/internal/config"
	"docklane.local/docklane/internal/diagnostics"
	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/reconcile"
	"docklane.local/docklane/internal/store"
	"docklane.local/docklane/internal/traefikruntime"
	"docklane.local/docklane/internal/upstreamprobe"
)

const docklaneVersion = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "docklane:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}

	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "discover":
		return discover(args[1:])
	case "doctor":
		return doctor(args[1:])
	case "probe":
		return probe(args[1:])
	case "network":
		return network(args[1:])
	case "manifest":
		return manifest(args[1:])
	case "preflight":
		return preflight(args[1:])
	case "route":
		return route(args[1:])
	case "version":
		fmt.Println("docklane " + docklaneVersion)
		return nil
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func probe(args []string) error {
	if len(args) == 0 {
		return errors.New("probe requires a subcommand: serve or check")
	}
	flags := flag.NewFlagSet("probe "+args[0], flag.ContinueOnError)
	socketPath := flags.String("socket", "", "Unix socket path")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "serve":
		ctx, stop := signal.NotifyContext(
			context.Background(),
			syscall.SIGINT,
			syscall.SIGTERM,
		)
		defer stop()
		return upstreamprobe.Serve(ctx, *socketPath)
	case "check":
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return upstreamprobe.NewClient(*socketPath).Check(ctx)
	default:
		return fmt.Errorf("unknown probe command %q", args[0])
	}
}

func doctor(args []string) error {
	target := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	controllerURL := flags.String("url", defaultControllerURL(), "Docklane controller URL")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return fmt.Errorf("usage: docklane doctor [--json] [ROUTE]")
	}
	if flags.NArg() == 1 {
		if target != "" {
			return fmt.Errorf("usage: docklane doctor [--json] [ROUTE]")
		}
		target = flags.Arg(0)
	}
	report := diagnostics.Run(
		context.Background(),
		client.New(*controllerURL),
		diagnostics.SystemProber{},
		target,
	)
	if *asJSON {
		return printJSON(report)
	}
	printDiagnosticReport(report)
	return nil
}

func printDiagnosticReport(report domain.DiagnosticReport) {
	title := "Docklane"
	if report.Hostname != "" {
		title = report.Hostname
	} else if report.Target != "" {
		title = report.Target
	}
	fmt.Printf("Doctor %s: %s\n", title, strings.ToUpper(string(report.Status)))
	for _, check := range report.Checks {
		fmt.Printf(
			"[%s] %-13s %s\n",
			strings.ToUpper(string(check.Status)),
			check.Layer,
			check.Summary,
		)
		if check.Detail != "" {
			fmt.Printf("       %s\n", check.Detail)
		}
		if check.Suggestion != "" {
			fmt.Printf("       Repair: %s\n", check.Suggestion)
		}
	}
}

func network(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("network requires a subcommand: plan or apply")
	}
	switch args[0] {
	case "plan":
		return networkPlan(args[1:])
	case "apply":
		return networkApply(args[1:])
	default:
		return fmt.Errorf("unknown network command %q", args[0])
	}
}

func networkPlan(args []string) error {
	flags := flag.NewFlagSet("network plan", flag.ContinueOnError)
	controllerURL := flags.String("url", defaultControllerURL(), "Docklane controller URL")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	plan, err := client.New(*controllerURL).NetworkPlan(context.Background())
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(plan)
	}
	printNetworkPlan(plan)
	return nil
}

func networkApply(args []string) error {
	flags := flag.NewFlagSet("network apply", flag.ContinueOnError)
	controllerURL := flags.String("url", defaultControllerURL(), "Docklane controller URL")
	asJSON := flags.Bool("json", false, "print JSON")
	yes := flags.Bool("yes", false, "confirm destructive disconnect operations")
	if err := flags.Parse(args); err != nil {
		return err
	}
	apiClient := client.New(*controllerURL)
	plan, err := apiClient.NetworkPlan(context.Background())
	if err != nil {
		return err
	}
	if hasDestructiveNetworkOperation(plan) && !*yes {
		if !*asJSON {
			printNetworkPlan(plan)
		}
		return errors.New(
			"plan contains destructive disconnect operations; review it and rerun with --yes",
		)
	}
	result, err := apiClient.ApplyNetworkPlan(context.Background(), plan.Token)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(result)
	}
	fmt.Printf(
		"Applied %d operation(s); %d operation(s) remain.\n",
		len(result.Applied.Operations),
		len(result.Remaining.Operations),
	)
	for _, warning := range result.Remaining.Warnings {
		fmt.Printf("Warning: %s\n", warning)
	}
	return nil
}

func printNetworkPlan(plan domain.NetworkPlan) {
	details := "not present"
	if plan.Network.Driver != "" || plan.Network.Scope != "" {
		details = plan.Network.Driver + "/" + plan.Network.Scope
	}
	fmt.Printf(
		"Network %s: %s (%s), compatible=%t\n",
		plan.Network.Name,
		plan.Network.Ownership,
		details,
		plan.Network.Compatible,
	)
	if len(plan.Operations) == 0 {
		fmt.Println("No network operations required.")
	}
	for _, operation := range plan.Operations {
		target := operation.ContainerName
		if target == "" {
			target = plan.Network.Name
		}
		fmt.Printf(
			"- %-10s %-24s %s",
			operation.Action,
			target,
			operation.Reason,
		)
		if len(operation.Aliases) > 0 {
			fmt.Printf(" (aliases: %s)", strings.Join(operation.Aliases, ", "))
		}
		if operation.Destructive {
			fmt.Print(" [destructive]")
		}
		fmt.Println()
	}
	for _, warning := range plan.Warnings {
		fmt.Printf("Warning: %s\n", warning)
	}
}

func hasDestructiveNetworkOperation(plan domain.NetworkPlan) bool {
	for _, operation := range plan.Operations {
		if operation.Destructive {
			return true
		}
	}
	return false
}

func discover(args []string) error {
	flags := flag.NewFlagSet("discover", flag.ContinueOnError)
	controllerURL := flags.String("url", defaultControllerURL(), "Docklane controller URL")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	containers, err := client.New(*controllerURL).ListContainers(context.Background())
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(containers)
	}
	if len(containers) == 0 {
		fmt.Println("No running containers found.")
		return nil
	}
	for _, container := range containers {
		identity := container.Name
		if container.ComposeProject != "" && container.ComposeService != "" {
			identity = container.ComposeProject + "/" + container.ComposeService
		}
		fmt.Printf("%-32s %-24s %v\n", identity, container.Image, container.ExposedPorts)
	}
	return nil
}

func route(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("route requires a subcommand: list, add, edit, enable, disable, or delete")
	}
	switch args[0] {
	case "list":
		return routeList(args[1:])
	case "add":
		return routeAdd(args[1:])
	case "edit":
		return routeEdit(args[1:])
	case "enable":
		return routeSetEnabled(args[1:], true)
	case "disable":
		return routeSetEnabled(args[1:], false)
	case "delete":
		return routeDelete(args[1:])
	default:
		return fmt.Errorf("unknown route command %q", args[0])
	}
}

func routeList(args []string) error {
	flags := flag.NewFlagSet("route list", flag.ContinueOnError)
	controllerURL := flags.String("url", defaultControllerURL(), "Docklane controller URL")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	payload, err := client.New(*controllerURL).ListRoutes(context.Background())
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(payload)
	}
	if len(payload.Routes) == 0 {
		fmt.Println("No routes configured.")
		return nil
	}
	for _, route := range payload.Routes {
		state := route.Observed.State
		if state == "" {
			state = "pending"
		}
		fmt.Printf("%-4d %-11s https://%-38s -> %s://:%d\n",
			route.ID, state, route.Hostname(payload.BaseDomain), route.Scheme, route.Port)
	}
	return nil
}

func routeEdit(args []string) error {
	id, remaining, err := commandRouteID("edit", args)
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("route edit", flag.ContinueOnError)
	controllerURL := flags.String("url", defaultControllerURL(), "Docklane controller URL")
	name := flags.String("name", "", "new local route name")
	project := flags.String("project", "", "Compose project")
	service := flags.String("service", "", "Compose service")
	containerID := flags.String("container", "", "container ID or prefix")
	port := flags.Uint("port", 0, "internal container port")
	scheme := flags.String("scheme", "", "upstream scheme")
	dryRun := flags.Bool("dry-run", false, "validate and print without saving")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(remaining); err != nil {
		return err
	}
	changed := map[string]bool{}
	flags.Visit(func(item *flag.Flag) { changed[item.Name] = true })
	if !changed["name"] && !changed["project"] && !changed["service"] &&
		!changed["container"] && !changed["port"] && !changed["scheme"] {
		return fmt.Errorf("route edit requires at least one field to change")
	}

	apiClient := client.New(*controllerURL)
	candidate, err := apiClient.GetRoute(context.Background(), id)
	if err != nil {
		return err
	}
	if changed["name"] {
		candidate.Name = *name
	}
	if changed["project"] {
		candidate.Selector.ComposeProject = *project
		candidate.Selector.ContainerID = ""
	}
	if changed["service"] {
		candidate.Selector.ComposeService = *service
		candidate.Selector.ContainerID = ""
	}
	if changed["container"] {
		candidate.Selector = domain.ContainerSelector{ContainerID: *containerID}
	}
	if changed["port"] {
		if *port > 65535 {
			return fmt.Errorf("port must be between 1 and 65535")
		}
		candidate.Port = uint16(*port)
	}
	if changed["scheme"] {
		candidate.Scheme = *scheme
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	if *dryRun {
		return printJSON(candidate)
	}
	updated, err := apiClient.UpdateRoute(context.Background(), candidate)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(updated)
	}
	fmt.Printf("Updated route %d (%s).\n", updated.ID, updated.Name)
	return nil
}

func routeSetEnabled(args []string, enabled bool) error {
	command := "disable"
	if enabled {
		command = "enable"
	}
	id, remaining, err := commandRouteID(command, args)
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("route "+command, flag.ContinueOnError)
	controllerURL := flags.String("url", defaultControllerURL(), "Docklane controller URL")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(remaining); err != nil {
		return err
	}
	apiClient := client.New(*controllerURL)
	candidate, err := apiClient.GetRoute(context.Background(), id)
	if err != nil {
		return err
	}
	candidate.Enabled = enabled
	updated, err := apiClient.UpdateRoute(context.Background(), candidate)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(updated)
	}
	fmt.Printf("%s route %d (%s).\n", map[bool]string{true: "Enabled", false: "Disabled"}[enabled], id, updated.Name)
	return nil
}

func routeDelete(args []string) error {
	id, remaining, err := commandRouteID("delete", args)
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("route delete", flag.ContinueOnError)
	controllerURL := flags.String("url", defaultControllerURL(), "Docklane controller URL")
	if err := flags.Parse(remaining); err != nil {
		return err
	}
	if err := client.New(*controllerURL).DeleteRoute(context.Background(), id); err != nil {
		return err
	}
	fmt.Printf("Deleted route %d.\n", id)
	return nil
}

func commandRouteID(command string, args []string) (int64, []string, error) {
	if len(args) == 0 {
		return 0, nil, fmt.Errorf("usage: docklane route %s ID [options]", command)
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, nil, fmt.Errorf("route ID must be a positive integer")
	}
	return id, args[1:], nil
}

func routeAdd(args []string) error {
	if len(args) == 0 || args[0] == "" || args[0][0] == '-' {
		return fmt.Errorf("usage: docklane route add NAME [options]")
	}
	name := args[0]
	flags := flag.NewFlagSet("route add", flag.ContinueOnError)
	controllerURL := flags.String("url", defaultControllerURL(), "Docklane controller URL")
	project := flags.String("project", "", "Compose project")
	service := flags.String("service", "", "Compose service")
	containerID := flags.String("container", "", "container ID or prefix")
	port := flags.Uint("port", 0, "internal container port")
	scheme := flags.String("scheme", "http", "upstream scheme")
	dryRun := flags.Bool("dry-run", false, "validate and print without saving")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	candidate := domain.Route{
		Name: name,
		Selector: domain.ContainerSelector{
			ComposeProject: *project,
			ComposeService: *service,
			ContainerID:    *containerID,
		},
		Port:    uint16(*port),
		Scheme:  *scheme,
		Enabled: true,
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	if *dryRun {
		return printJSON(candidate)
	}
	created, err := client.New(*controllerURL).CreateRoute(context.Background(), candidate)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(created)
	}
	payload, err := client.New(*controllerURL).ListRoutes(context.Background())
	if err != nil {
		fmt.Printf("Created route %q.\n", created.Name)
		return nil
	}
	fmt.Printf("Created https://%s\n", created.Hostname(payload.BaseDomain))
	return nil
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := flags.String("listen", "127.0.0.1:4646", "controller listen address")
	database := flags.String("database", "./data/docklane.db", "state database path")
	baseDomain := flags.String("base-domain", "docker.home.arpa", "local route base domain")
	dockerSocket := flags.String("docker-socket", "/var/run/docker.sock", "Docker Engine Unix socket")
	proxyNetwork := flags.String("proxy-network", "", "required shared Traefik network")
	probeSocket := flags.String(
		"probe-socket",
		"",
		"proxy-network probe Unix socket",
	)
	traefikAPIURL := flags.String(
		"traefik-api-url",
		"",
		"authenticated Traefik dashboard API URL",
	)
	traefikAPIAddr := flags.String(
		"traefik-api-address",
		"",
		"private Traefik API dial address",
	)
	traefikAPIUser := flags.String(
		"traefik-api-username",
		"",
		"Traefik API basic-auth username",
	)
	traefikAPIPass := flags.String(
		"traefik-api-password-file",
		"",
		"Traefik API basic-auth password file",
	)
	traefikAPICA := flags.String(
		"traefik-api-ca-file",
		"",
		"Traefik API trusted CA file",
	)
	manageNetworks := flags.Bool(
		"manage-network-attachments",
		false,
		"connect routed containers to the proxy network",
	)
	reconcileEvery := flags.Duration("reconcile-interval", 5*time.Second, "Docker reconciliation interval")
	historyEvery := flags.Duration(
		"health-history-interval",
		5*time.Minute,
		"periodic route health snapshot interval",
	)
	historyLimit := flags.Int(
		"health-history-limit",
		288,
		"maximum health snapshots retained per route",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}

	cfg := config.Config{
		ListenAddress:  *listen,
		DatabasePath:   *database,
		BaseDomain:     *baseDomain,
		DockerSocket:   *dockerSocket,
		ProxyNetwork:   *proxyNetwork,
		ProbeSocket:    *probeSocket,
		TraefikAPIURL:  *traefikAPIURL,
		TraefikAPIAddr: *traefikAPIAddr,
		TraefikAPIUser: *traefikAPIUser,
		TraefikAPIPass: *traefikAPIPass,
		TraefikAPICA:   *traefikAPICA,
		HistoryEvery:   *historyEvery,
		HistoryLimit:   *historyLimit,
		ManageNetworks: *manageNetworks,
		ReconcileEvery: *reconcileEvery,
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	repository, err := store.Open(cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("open state: %w", err)
	}
	defer repository.Close()

	dockerClient := docker.NewClient(cfg.DockerSocket)
	reconcileOptions := []reconcile.Option{
		reconcile.WithBaseDomain(cfg.BaseDomain),
	}
	if cfg.ProxyNetwork != "" {
		var manager docker.NetworkManager
		if cfg.ManageNetworks {
			manager = dockerClient
		}
		reconcileOptions = append(
			reconcileOptions,
			reconcile.WithNetworkAttachments(cfg.ProxyNetwork, manager),
		)
	}
	reconciler := reconcile.New(
		repository,
		dockerClient,
		cfg.ReconcileEvery,
		reconcileOptions...,
	)
	apiOptions := []api.Option{}
	if cfg.ProbeSocket != "" {
		apiOptions = append(
			apiOptions,
			api.WithUpstreamProber(upstreamprobe.NewClient(cfg.ProbeSocket)),
		)
	}
	if cfg.TraefikAPIURL != "" {
		inspector, err := traefikruntime.New(traefikruntime.Config{
			BaseURL:      cfg.TraefikAPIURL,
			DialAddress:  cfg.TraefikAPIAddr,
			Username:     cfg.TraefikAPIUser,
			PasswordFile: cfg.TraefikAPIPass,
			CAFile:       cfg.TraefikAPICA,
		})
		if err != nil {
			return fmt.Errorf("configure Traefik runtime inspection: %w", err)
		}
		apiOptions = append(apiOptions, api.WithTraefikRuntimeInspector(inspector))
	}
	handler := api.New(
		cfg,
		repository,
		dockerClient,
		reconciler,
		apiOptions...,
	)
	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go reconciler.Run(ctx)
	go handler.RunHealthHistory(ctx)

	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("Docklane listening on http://%s", cfg.ListenAddress)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func printUsage() {
	fmt.Print(`Docklane local container gateway

Usage:
  docklane serve [options]   Start the controller
  docklane discover          List running Docker containers
  docklane doctor [ROUTE]    Diagnose controller or route layers
  docklane network plan      Preview network operations
  docklane network apply     Apply the reviewed network plan
  docklane manifest init     Create an empty ownership manifest
  docklane manifest show     Inspect the ownership manifest
  docklane manifest validate Validate the ownership manifest
  docklane preflight         Inspect host installation compatibility
  docklane route list        List saved local routes
  docklane route add NAME    Create a route
  docklane route edit ID     Edit a route
  docklane route enable ID   Enable a route
  docklane route disable ID  Disable a route
  docklane route delete ID   Delete a route
  docklane version           Print the version
  docklane help              Show this help

Environment:
  DOCKLANE_URL               Controller URL (default http://127.0.0.1:4646)
  DOCKLANE_MANIFEST          Manifest path (default /var/lib/docklane/install-manifest.json)
`)
}

func defaultControllerURL() string {
	if value := os.Getenv("DOCKLANE_URL"); value != "" {
		return value
	}
	return "http://127.0.0.1:4646"
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
