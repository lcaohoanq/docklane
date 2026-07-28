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

func preflight(args []string) error {
	flags := flag.NewFlagSet("preflight", flag.ContinueOnError)
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
	dockerSocket := flags.String(
		"docker-socket",
		"/var/run/docker.sock",
		"Docker Engine Unix socket",
	)
	manifestPath := flags.String(
		"manifest",
		defaultManifestPath(),
		"absolute installation manifest path",
	)
	dnsmasqConfig := flags.String(
		"dnsmasq-config",
		"/etc/dnsmasq.conf",
		"primary dnsmasq configuration",
	)
	dnsmasqDir := flags.String(
		"dnsmasq-dir",
		"/etc/dnsmasq.d",
		"dnsmasq include directory",
	)
	dnsmasqService := flags.String(
		"dnsmasq-service",
		"dnsmasq",
		"dnsmasq system service name",
	)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: docklane preflight [options]")
	}
	dockerClient := docker.NewClient(*dockerSocket)
	runner, err := preflightcheck.New(
		preflightcheck.Config{
			BaseDomain:     *baseDomain,
			ProxyNetwork:   *proxyNetwork,
			DockerSocket:   *dockerSocket,
			ManifestPath:   *manifestPath,
			DnsmasqConfig:  *dnsmasqConfig,
			DnsmasqDir:     *dnsmasqDir,
			DnsmasqService: *dnsmasqService,
		},
		dockerClient,
		preflightcheck.SystemInspector{},
	)
	if err != nil {
		return err
	}
	report := runner.Run(context.Background())
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
