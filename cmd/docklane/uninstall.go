package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installmanifest"
	"docklane.local/docklane/internal/uninstallplan"
)

func uninstall(args []string) error {
	flags := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	manifestPath := flags.String(
		"manifest",
		defaultManifestPath(),
		"absolute installation manifest path",
	)
	dryRun := flags.Bool("dry-run", false, "render rollback without applying it")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: docklane uninstall --dry-run [options]")
	}
	if !*dryRun {
		return errors.New(
			"uninstall apply is not implemented yet; review with docklane uninstall --dry-run",
		)
	}
	store, err := installmanifest.NewStore(*manifestPath)
	if err != nil {
		return err
	}
	manifest, err := store.Load()
	if err != nil {
		return err
	}
	plan, err := uninstallplan.Build(manifest, store.Path())
	if err != nil {
		return err
	}
	if *asJSON {
		if err := printJSON(plan); err != nil {
			return err
		}
	} else {
		printUninstallationPlan(plan)
	}
	if !plan.Ready {
		return errors.New("uninstallation plan has blocking conflicts")
	}
	return nil
}

func printUninstallationPlan(plan domain.UninstallationPlan) {
	label := strings.ToUpper(string(plan.Status))
	if !plan.Ready {
		label = "BLOCKED"
	}
	fmt.Printf(
		"Uninstallation plan %s · schema v%d · token %s\n",
		label,
		plan.SchemaVersion,
		plan.Token,
	)
	fmt.Printf(
		"Installation %s · manifest generation %d\n",
		plan.InstallationID,
		plan.Generation,
	)
	for _, operation := range plan.Operations {
		marker := "keep"
		if operation.Mutating {
			marker = "change"
		}
		fmt.Printf(
			"[%s] %-10s %-18s %s\n",
			marker,
			operation.Action,
			operation.Kind,
			operation.Target,
		)
		fmt.Printf("       %s\n", operation.Reason)
		if operation.Backup != nil {
			fmt.Printf(
				"       Backup %s · sha256:%s\n",
				operation.Backup.Path,
				operation.Backup.Fingerprint,
			)
		}
	}
	for _, blocker := range plan.Blockers {
		fmt.Printf("[block] %s\n", blocker)
	}
	fmt.Println("Dry run only. No manifest or host resource was changed.")
}
