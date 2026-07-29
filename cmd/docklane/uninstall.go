package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installhost"
	"docklane.local/docklane/internal/installmanifest"
	"docklane.local/docklane/internal/installuninstall"
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
	token := flags.String(
		"token",
		"",
		"exact token from the reviewed uninstall dry-run",
	)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New(
			"usage: docklane uninstall (--dry-run | --token TOKEN) [options]",
		)
	}
	if *dryRun && *token != "" {
		return errors.New(
			"--token cannot be combined with --dry-run",
		)
	}
	if !*dryRun && *token == "" {
		return errors.New(
			"uninstallation requires --token from docklane uninstall --dry-run",
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
	if !*dryRun &&
		(manifest.State == domain.InstallationRollingBack ||
			manifest.State == domain.InstallationRolledBack) {
		runner, err := newUninstallRunner(store, manifest)
		if err != nil {
			return err
		}
		result, err := runner.Resume(context.Background(), *token)
		if err != nil {
			return err
		}
		return printUninstalled(store, result, *asJSON)
	}
	plan, err := uninstallplan.Build(manifest, store.Path())
	if err != nil {
		return err
	}
	if *dryRun {
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
	runner, err := newUninstallRunner(store, manifest)
	if err != nil {
		return err
	}
	result, err := runner.Apply(
		context.Background(),
		manifest,
		plan,
		*token,
	)
	if err != nil {
		return err
	}
	return printUninstalled(store, result, *asJSON)
}

func newUninstallRunner(
	store *installmanifest.Store,
	manifest domain.InstallationManifest,
) (*installuninstall.Runner, error) {
	if manifest.ManagedSpecification == nil {
		return installuninstall.New(store, nil, nil)
	}
	hostBackend, err := installhost.NewSystemBackend(
		installhost.ArchSystemdProfile(),
	)
	if err != nil {
		return nil, err
	}
	return installuninstall.New(
		store,
		docker.NewClient(manifest.ManagedSpecification.DockerSocket),
		hostBackend,
	)
}

func printUninstalled(
	store *installmanifest.Store,
	manifest domain.InstallationManifest,
	asJSON bool,
) error {
	if asJSON {
		return printJSON(manifest)
	}
	fmt.Printf(
		"Rolled back Docklane installation %s at manifest generation %d.\n",
		manifest.InstallationID,
		manifest.Generation,
	)
	fmt.Printf(
		"Audit manifest retained at %s; adopted resources were preserved.\n",
		store.Path(),
	)
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
