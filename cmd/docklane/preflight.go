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
	resolverConfig  *string
	trustAnchorPath *string
	runtimeDataPath *string
	hostProfile     *string
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
		"",
		"dnsmasq include configuration (host-profile default when omitted)",
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
	options.resolverConfig = flags.String(
		"managed-resolver-config",
		"/etc/systemd/resolved.conf.d/docklane.conf",
		"systemd-resolved route-only domain target",
	)
	options.trustAnchorPath = flags.String(
		"trust-anchor",
		"",
		"local root CA trust-anchor path (host-profile default when omitted)",
	)
	options.hostProfile = flags.String(
		"host-profile",
		installhost.HostProfileAuto,
		"host integration profile: auto, arch-systemd, or debian-systemd",
	)
	options.runtimeDataPath = flags.String(
		"runtime-data",
		"/var/lib/docklane/data",
		"Docklane controller data directory for a clean installation",
	)
	return options
}

func (options preflightOptions) run(
	ctx context.Context,
) (domain.PreflightReport, error) {
	profile, err := installhost.ResolveSystemProfile(*options.hostProfile)
	if err != nil {
		return domain.PreflightReport{}, err
	}
	if *options.trustAnchorPath == "" {
		*options.trustAnchorPath = profile.PreflightTrustAnchor
	}
	if *options.dnsmasqConfig == "" {
		*options.dnsmasqConfig = profile.DnsmasqIncludeConfig
	}
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
			ResolverConfig:  *options.resolverConfig,
			TrustAnchorPath: *options.trustAnchorPath,
			RuntimeDataPath: *options.runtimeDataPath,
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
