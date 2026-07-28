package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"docklane.local/docklane/internal/domain"
	"docklane.local/docklane/internal/installmanifest"
)

func manifest(args []string) error {
	if len(args) == 0 {
		return errors.New("manifest requires a subcommand: init, show, or validate")
	}
	switch args[0] {
	case "init":
		return manifestInit(args[1:])
	case "show":
		return manifestShow(args[1:])
	case "validate":
		return manifestValidate(args[1:])
	default:
		return fmt.Errorf("unknown manifest command %q", args[0])
	}
}

func manifestInit(args []string) error {
	flags := flag.NewFlagSet("manifest init", flag.ContinueOnError)
	path := flags.String(
		"path",
		defaultManifestPath(),
		"absolute installation manifest path",
	)
	baseDomain := flags.String(
		"base-domain",
		"docker.home.arpa",
		"managed local DNS suffix",
	)
	proxyNetwork := flags.String(
		"proxy-network",
		"proxy",
		"shared Docker proxy network",
	)
	asJSON := flags.Bool("json", false, "print the created manifest as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: docklane manifest init [options]")
	}
	installation, err := installmanifest.New(
		docklaneVersion,
		*baseDomain,
		*proxyNetwork,
		time.Now(),
	)
	if err != nil {
		return err
	}
	manifestStore, err := installmanifest.NewStore(*path)
	if err != nil {
		return err
	}
	if err := manifestStore.Create(installation); err != nil {
		return err
	}
	if *asJSON {
		return printJSON(installation)
	}
	fmt.Printf(
		"Created installation manifest schema v%d at %s\n",
		installation.SchemaVersion,
		manifestStore.Path(),
	)
	fmt.Println("No host resources were changed.")
	return nil
}

func manifestShow(args []string) error {
	flags := flag.NewFlagSet("manifest show", flag.ContinueOnError)
	path := flags.String(
		"path",
		defaultManifestPath(),
		"absolute installation manifest path",
	)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: docklane manifest show [options]")
	}
	manifestStore, err := installmanifest.NewStore(*path)
	if err != nil {
		return err
	}
	installation, err := manifestStore.Load()
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(installation)
	}
	printInstallationManifest(installation, manifestStore.Path())
	return nil
}

func manifestValidate(args []string) error {
	flags := flag.NewFlagSet("manifest validate", flag.ContinueOnError)
	path := flags.String(
		"path",
		defaultManifestPath(),
		"absolute installation manifest path",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: docklane manifest validate [options]")
	}
	manifestStore, err := installmanifest.NewStore(*path)
	if err != nil {
		return err
	}
	installation, err := manifestStore.Load()
	if err != nil {
		return err
	}
	fmt.Printf(
		"Valid installation manifest schema v%d, generation %d (%s)\n",
		installation.SchemaVersion,
		installation.Generation,
		manifestStore.Path(),
	)
	return nil
}

func printInstallationManifest(
	manifest domain.InstallationManifest,
	path string,
) {
	managed := 0
	adopted := 0
	for _, resource := range manifest.Resources {
		if resource.Ownership == domain.ResourceManaged {
			managed++
		} else if resource.Ownership == domain.ResourceAdopted {
			adopted++
		}
	}
	fmt.Printf("Manifest: %s\n", path)
	fmt.Printf(
		"Installation %s: %s, schema v%d, generation %d\n",
		manifest.InstallationID,
		manifest.State,
		manifest.SchemaVersion,
		manifest.Generation,
	)
	fmt.Printf(
		"Domain %s · network %s · resources %d managed / %d adopted\n",
		manifest.Settings.BaseDomain,
		manifest.Settings.ProxyNetwork,
		managed,
		adopted,
	)
	for _, resource := range manifest.Resources {
		fmt.Printf(
			"- %-16s %-18s %-8s %-11s %s\n",
			resource.ID,
			resource.Kind,
			resource.Ownership,
			resource.State,
			resource.Target,
		)
	}
}

func defaultManifestPath() string {
	if value := os.Getenv("DOCKLANE_MANIFEST"); value != "" {
		return value
	}
	return "/var/lib/docklane/install-manifest.json"
}
