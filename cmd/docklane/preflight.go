package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"docklane.local/docklane/internal/docker"
	"docklane.local/docklane/internal/domain"
	preflightcheck "docklane.local/docklane/internal/preflight"
)

type preflightOptions struct {
	baseDomain      *string
	proxyNetwork    *string
	dockerSocket    *string
	manifestPath    *string
	dnsmasqConfig   *string
	dnsmasqDir      *string
	dnsmasqService  *string
	trustAnchorPath *string
}

func bindPreflightFlags(flags *flag.FlagSet) preflightOptions {
	options := preflightOptions{}
	options.baseDomain = flags.String(
		"base-domain",
		"docker.home.arpa",
		"managed local DNS suffix",
	)
	options.proxyNetwork = flags.String(
		"proxy-network",
		"proxy",
		"shared Docker proxy network",
	)
	options.dockerSocket = flags.String(
		"docker-socket",
		"/var/run/docker.sock",
		"Docker Engine Unix socket",
	)
	options.manifestPath = flags.String(
		"manifest",
		defaultManifestPath(),
		"absolute installation manifest path",
	)
	options.dnsmasqConfig = flags.String(
		"dnsmasq-config",
		"/etc/dnsmasq.conf",
		"primary dnsmasq configuration",
	)
	options.dnsmasqDir = flags.String(
		"dnsmasq-dir",
		"/etc/dnsmasq.d",
		"dnsmasq include directory",
	)
	options.dnsmasqService = flags.String(
		"dnsmasq-service",
		"dnsmasq",
		"dnsmasq system service name",
	)
	options.trustAnchorPath = flags.String(
		"trust-anchor",
		"/etc/ca-certificates/trust-source/anchors/traefik-lab-root-ca.crt",
		"local root CA trust-anchor path",
	)
	return options
}

func (options preflightOptions) run(
	ctx context.Context,
) (domain.PreflightReport, error) {
	dockerClient := docker.NewClient(*options.dockerSocket)
	runner, err := preflightcheck.New(
		preflightcheck.Config{
			BaseDomain:      *options.baseDomain,
			ProxyNetwork:    *options.proxyNetwork,
			DockerSocket:    *options.dockerSocket,
			ManifestPath:    *options.manifestPath,
			DnsmasqConfig:   *options.dnsmasqConfig,
			DnsmasqDir:      *options.dnsmasqDir,
			DnsmasqService:  *options.dnsmasqService,
			TrustAnchorPath: *options.trustAnchorPath,
		},
		dockerClient,
		preflightcheck.SystemInspector{},
	)
	if err != nil {
		return domain.PreflightReport{}, err
	}
	return runner.Run(ctx), nil
}

func preflight(args []string) error {
	flags := flag.NewFlagSet("preflight", flag.ContinueOnError)
	options := bindPreflightFlags(flags)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: docklane preflight [options]")
	}
	report, err := options.run(context.Background())
	if err != nil {
		return err
	}
	if *asJSON {
		if err := printJSON(report); err != nil {
			return err
		}
	} else {
		printPreflightReport(report)
	}
	if report.Status == domain.DiagnosticFail {
		return errors.New("preflight found blocking conflicts")
	}
	return nil
}

func printPreflightReport(report domain.PreflightReport) {
	fmt.Printf(
		"Preflight %s for %s\n",
		strings.ToUpper(string(report.Status)),
		report.Target.BaseDomain,
	)
	for _, check := range report.Checks {
		fmt.Printf(
			"[%s] %-10s %s\n",
			strings.ToUpper(string(check.Status)),
			check.Layer,
			check.Summary,
		)
		if check.Detail != "" {
			fmt.Printf("       %s\n", check.Detail)
		}
		if check.Suggestion != "" {
			fmt.Printf("       Next: %s\n", check.Suggestion)
		}
	}
}
