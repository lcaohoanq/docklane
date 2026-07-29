package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installmanifest"
	"docklane.local/docklane/internal/installupgrade"
)

func upgrade(args []string) error {
	flags := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	path := flags.String(
		"path",
		defaultManifestPath(),
		"absolute installation manifest path",
	)
	dryRun := flags.Bool(
		"dry-run",
		false,
		"render the manifest migration plan without applying it",
	)
	token := flags.String(
		"token",
		"",
		"exact token from the reviewed upgrade dry run",
	)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New(
			"usage: docklane upgrade (--dry-run | --token TOKEN) [options]",
		)
	}
	if *dryRun && *token != "" {
		return errors.New("--token cannot be combined with --dry-run")
	}
	if !*dryRun && *token == "" {
		return errors.New(
			"upgrade requires --token from a fresh docklane upgrade --dry-run",
		)
	}
	store, err := installmanifest.NewStore(*path)
	if err != nil {
		return err
	}
	runner, err := installupgrade.New(store)
	if err != nil {
		return err
	}
	if *dryRun {
		plan, err := runner.Plan(context.Background())
		if err != nil {
			return err
		}
		if *asJSON {
			if err := printJSON(plan); err != nil {
				return err
			}
		} else {
			printUpgradePlan(plan)
		}
		if !plan.Ready {
			return errors.New("upgrade plan has blocking conflicts")
		}
		return nil
	}
	manifest, err := runner.Apply(context.Background(), *token)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(manifest)
	}
	printUpgradeResult(manifest, store.Path())
	return nil
}

func printUpgradePlan(plan installupgrade.Plan) {
	fmt.Printf(
		"Installation manifest %s · generation %d · schema v%d\n",
		plan.State,
		plan.Generation,
		plan.FromSchemaVersion,
	)
	if plan.Current {
		fmt.Printf(
			"Already current at schema v%d. No upgrade operation is required.\n",
			plan.ToSchemaVersion,
		)
		return
	}
	for _, operation := range plan.Operations {
		fmt.Printf(
			"[change] migrate schema v%d → v%d\n",
			operation.FromSchemaVersion,
			operation.ToSchemaVersion,
		)
		fmt.Printf("         %s\n", operation.Reason)
		fmt.Printf("         exact backup: %s\n", operation.BackupPath)
	}
	for _, blocker := range plan.Blockers {
		fmt.Printf("[block]  %s\n", blocker)
	}
	fmt.Printf("Reviewed upgrade token: %s\n", plan.Token)
	fmt.Println("Dry run only. The manifest and host resources were not changed.")
}

func printUpgradeResult(
	manifest domain.InstallationManifest,
	path string,
) {
	record := manifest.UpgradeHistory[len(manifest.UpgradeHistory)-1]
	fmt.Printf(
		"Upgraded installation manifest to schema v%d, generation %d at %s\n",
		manifest.SchemaVersion,
		manifest.Generation,
		path,
	)
	fmt.Printf(
		"Preserved exact schema v%d source at %s\n",
		record.FromSchemaVersion,
		record.SourceBackup.Path,
	)
	fmt.Println("No Docker, DNS, TLS, service, or application resource was changed.")
}
