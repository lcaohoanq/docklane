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
)

func install(args []string) error {
	flags := flag.NewFlagSet("install", flag.ContinueOnError)
	options := bindPreflightFlags(flags)
	dnsmasqTarget := flags.String(
		"dnsmasq-target",
		"/etc/dnsmasq.d/docklane.conf",
		"managed dnsmasq wildcard configuration target",
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
	plan, err := installplan.Build(report, installplan.Options{
		DnsmasqTarget:  *dnsmasqTarget,
		DnsmasqService: *options.dnsmasqService,
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
