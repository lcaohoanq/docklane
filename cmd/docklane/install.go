package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installapply"
	"docklane.local/docklane/internal/installmanifest"
	"docklane.local/docklane/internal/installplan"
	"docklane.local/docklane/internal/installspec"
)

func install(args []string) error {
	flags := flag.NewFlagSet("install", flag.ContinueOnError)
	options := bindPreflightFlags(flags)
	dnsmasqTarget := flags.String(
		"dnsmasq-target",
		"/etc/dnsmasq.d/docklane.conf",
		"managed dnsmasq wildcard configuration target",
	)
	managedStateDirectory := flags.String(
		"managed-state-dir",
		"/var/lib/docklane",
		"dedicated state directory for Docklane-managed resources",
	)
	managedTrustAnchor := flags.String(
		"managed-trust-anchor",
		"/etc/ca-certificates/trust-source/anchors/docklane-local-root-ca.crt",
		"clean-install system trust-anchor target",
	)
	managedResolverConfig := flags.String(
		"managed-resolver-config",
		"/etc/systemd/resolved.conf.d/docklane.conf",
		"clean-install systemd-resolved route-only domain target",
	)
	traefikImage := flags.String(
		"traefik-image",
		"traefik:v3.7",
		"clean-install Traefik image reference",
	)
	docklaneImage := flags.String(
		"docklane-image",
		"docklane:local",
		"clean-install Docklane image reference",
	)
	dryRun := flags.Bool("dry-run", false, "render the plan without applying it")
	token := flags.String(
		"token",
		"",
		"exact token from the reviewed dry-run plan",
	)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New(
			"usage: docklane install (--dry-run | --token TOKEN) [options]",
		)
	}
	if *dryRun && *token != "" {
		return errors.New(
			"--token cannot be combined with --dry-run",
		)
	}
	if !*dryRun && *token == "" {
		return errors.New(
			"installation requires --token from a fresh docklane install --dry-run",
		)
	}
	report, err := options.run(context.Background())
	if err != nil {
		return err
	}
	specification, err := installspec.Build(installspec.Config{
		BaseDomain:      *options.baseDomain,
		ProxyNetwork:    *options.proxyNetwork,
		DockerSocket:    *options.dockerSocket,
		StateDirectory:  *managedStateDirectory,
		DataDirectory:   *options.runtimeDataPath,
		DnsmasqConfig:   *dnsmasqTarget,
		ResolverConfig:  *managedResolverConfig,
		TrustAnchorPath: *managedTrustAnchor,
		TraefikImage:    *traefikImage,
		DocklaneImage:   *docklaneImage,
	})
	if err != nil {
		return err
	}
	plan, err := installplan.Build(report, installplan.Options{
		DnsmasqTarget:        *dnsmasqTarget,
		DnsmasqService:       *options.dnsmasqService,
		ManagedSpecification: specification,
	})
	if err != nil {
		return err
	}
	if *dryRun {
		if *asJSON {
			if err := printJSON(plan); err != nil {
				return err
			}
		} else {
			printInstallationPlan(plan)
		}
		if !plan.Ready {
			return errors.New("installation plan has blocking conflicts")
		}
		return nil
	}
	manifestStore, err := installmanifest.NewStore(*options.manifestPath)
	if err != nil {
		return err
	}
	runner, err := installapply.New(manifestStore, docklaneVersion)
	if err != nil {
		return err
	}
	installation, err := runner.Apply(context.Background(), plan, *token)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(installation)
	}
	fmt.Printf(
		"Installed Docklane ownership manifest generation %d at %s\n",
		installation.Generation,
		manifestStore.Path(),
	)
	fmt.Printf(
		"Recorded %d verified adopted resources; no running infrastructure was changed.\n",
		len(installation.Resources),
	)
	return nil
}

func printInstallationPlan(plan domain.InstallationPlan) {
	label := strings.ToUpper(string(plan.Status))
	if !plan.Ready {
		label = "BLOCKED"
	}
	fmt.Printf(
		"Installation foundation plan %s · schema v%d · token %s\n",
		label,
		plan.SchemaVersion,
		plan.Token,
	)
	managed := 0
	adopted := 0
	for _, resource := range plan.Resources {
		if resource.Ownership == domain.ResourceManaged {
			managed++
		} else {
			adopted++
		}
	}
	fmt.Printf(
		"Resources: %d managed, %d adopted and preserved\n",
		managed,
		adopted,
	)
	if plan.ManagedSpecification != nil {
		specification := plan.ManagedSpecification
		fmt.Printf(
			"Managed contract: state %s · images %s / %s\n",
			specification.Paths.StateDirectory,
			specification.Images.Traefik,
			specification.Images.Docklane,
		)
		fmt.Printf(
			"Managed PKI: %s and *.%s · root key %s\n",
			specification.BaseDomain,
			specification.BaseDomain,
			specification.PKI.RootPrivateKeyPath,
		)
		rendered := 0
		generated := 0
		containers := 0
		for _, artifact := range plan.ManagedArtifacts {
			switch {
			case artifact.GeneratedAtApply:
				generated++
			case artifact.Kind == domain.ArtifactContainerSpec:
				containers++
			default:
				rendered++
			}
		}
		fmt.Printf(
			"Managed artifacts: %d rendered · %d generated at apply · %d container specs\n",
			rendered,
			generated,
			containers,
		)
	}
	for _, operation := range plan.Operations {
		marker := "record"
		if operation.Mutating {
			marker = "change"
		}
		fmt.Printf(
			"[%s] %-16s %-18s %s\n",
			marker,
			operation.Action,
			operation.Kind,
			operation.Target,
		)
		fmt.Printf("         %s\n", operation.Reason)
	}
	for _, blocker := range plan.Blockers {
		fmt.Printf("[block]  %s\n", blocker)
	}
	for _, pending := range plan.Pending {
		fmt.Printf("[pending] %s\n", pending)
	}
	fmt.Println("Dry run only. No manifest or host resource was changed.")
}
