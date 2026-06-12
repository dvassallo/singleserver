package singleserver

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"strings"
)

func cliDomains(args []string, w io.Writer, logger *log.Logger) error {
	mode, args, err := commandModeFromArgs(args, noFlagValues)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: singleserver domains <add|remove|list|verify> ...")
	}
	return withCLIMode(mode, func() error {
		switch args[0] {
		case "add":
			return cliDomainChange(args[1:], true, w, logger)
		case "remove":
			return cliDomainChange(args[1:], false, w, logger)
		case "list":
			if len(args) > 2 {
				return errors.New("usage: singleserver domains list [app]")
			}
			return listDomains(args[1:], w)
		case "verify":
			if len(args) > 2 {
				return errors.New("usage: singleserver domains verify [app]")
			}
			return verifyDomains(args[1:], w)
		default:
			return fmt.Errorf("unknown domains command %q", args[0])
		}
	})
}

func cliDomainChange(args []string, add bool, w io.Writer, logger *log.Logger) error {
	command := "add"
	if !add {
		command = "remove"
	}
	mode, args, err := commandModeFromArgs(args, noFlagValues)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("domains "+command, flag.ContinueOnError)
	fs.SetOutput(w)
	noDeploy := fs.Bool("no-deploy", false, "update config and DNS without deploying")
	if err := fs.Parse(normalizeFlagArgs(args, noFlagValues)); err != nil {
		return err
	}
	prompting := cliCanPrompt(mode)
	p := interactivePrompter(w)
	changeArgs := fs.Args()
	if len(changeArgs) == 0 && prompting {
		appName, err := promptConfiguredAppName(p, "App")
		if err != nil {
			return err
		}
		changeArgs = append(changeArgs, appName)
	}
	if len(changeArgs) == 1 && prompting {
		label := "Domain to add"
		if !add {
			label = "Domain to remove"
		}
		host, err := p.askRequired(label)
		if err != nil {
			return err
		}
		changeArgs = append(changeArgs, host)
	}
	if len(changeArgs) != 2 {
		return fmt.Errorf("usage: singleserver domains %s <app> <domain> [--no-deploy]", command)
	}
	if prompting && !*noDeploy {
		deploy, err := p.askYesNo("Deploy now?", true)
		if err != nil {
			return err
		}
		*noDeploy = !deploy
	}

	app, err := updateDomain(changeArgs[0], changeArgs[1], add, w)
	if err != nil {
		return err
	}
	if *noDeploy {
		writeCheck(w, app.Name, "next", "pending", "deploy with `singleserver deploy "+app.Repo+"`")
		return nil
	}
	writeCheck(w, app.Name, "deploy", "start", "applying domain change")
	if err := cliDeploy([]string{app.Repo}, w, logger); err != nil {
		return err
	}
	return cliDoctor([]string{app.Name}, w)
}

func updateDomain(appName string, host string, add bool, w io.Writer) (*AppConfig, error) {
	host = strings.TrimSpace(host)
	if host == "" || strings.Contains(host, "://") || strings.Contains(host, "/") {
		return nil, fmt.Errorf("invalid domain: %q", host)
	}
	configPath := envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml")
	config, err := LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	appIndex := -1
	for i := range config.Apps {
		if appMatches(config.Apps[i], appName) {
			appIndex = i
			break
		}
	}
	if appIndex < 0 {
		return nil, fmt.Errorf("%s is not configured", appName)
	}

	if !add && !containsFold(config.Apps[appIndex].Hosts, host) {
		return nil, fmt.Errorf("%s is not configured for %s", host, config.Apps[appIndex].Name)
	}

	app := &config.Apps[appIndex]
	if add {
		if !containsFold(app.Hosts, host) {
			app.Hosts = append(app.Hosts, host)
		}
	} else {
		app.Hosts = removeFold(app.Hosts, host)
		if healthcheckBelongsToHost(app.Healthcheck, host, app.HealthcheckPath) {
			app.Healthcheck = ""
		}
	}
	if err := config.Normalize(); err != nil {
		return nil, err
	}
	app = &config.Apps[appIndex]

	if err := syncCloudflareAppDomainFunc(host, add, w); err != nil {
		return nil, err
	}
	if err := writeConfig(configPath, config); err != nil {
		if rollbackErr := syncCloudflareAppDomainFunc(host, !add, io.Discard); rollbackErr != nil {
			return nil, fmt.Errorf("%w; rollback cloudflare domain failed: %v", err, rollbackErr)
		}
		return nil, err
	}
	if add {
		writeCheck(w, app.Name, "domain", "ok", host, "added")
	} else {
		writeCheck(w, app.Name, "domain", "ok", host, "removed")
	}
	return app, nil
}

func healthcheckBelongsToHost(healthcheck, host, path string) bool {
	if strings.TrimSpace(healthcheck) == "" {
		return false
	}
	for _, candidatePath := range []string{path, "/up"} {
		if candidatePath == "" {
			continue
		}
		if !strings.HasPrefix(candidatePath, "/") {
			candidatePath = "/" + candidatePath
		}
		if strings.EqualFold(healthcheck, "https://"+host+candidatePath) {
			return true
		}
	}
	return false
}

func listDomains(args []string, w io.Writer) error {
	config, err := LoadConfig(envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml"))
	if err != nil {
		return err
	}
	for _, app := range config.Apps {
		if len(args) == 1 && !appMatches(app, args[0]) {
			continue
		}
		if len(app.Hosts) == 0 {
			fmt.Fprintf(w, "%s\t-\n", app.Name)
			continue
		}
		for _, host := range app.Hosts {
			fmt.Fprintf(w, "%s\t%s\n", app.Name, host)
		}
	}
	return nil
}

func verifyDomains(args []string, w io.Writer) error {
	config, err := LoadConfig(envDefault("SINGLESERVER_CONFIG", "/etc/singleserver/apps.yml"))
	if err != nil {
		return err
	}
	apps := config.Apps
	if len(args) == 1 {
		apps = nil
		for _, app := range config.Apps {
			if appMatches(app, args[0]) {
				apps = []AppConfig{app}
				break
			}
		}
		if len(apps) == 0 {
			return fmt.Errorf("%s is not configured", args[0])
		}
	}

	state, err := loadCloudflareState()
	if err != nil {
		return err
	}
	var cloudflareClient *CloudflareClient
	failed := false
	if state.TunnelID != "" {
		if token := cloudflareTokenFromEnvOrState(state); token != "" {
			client, err := newCloudflareClient(token)
			if err != nil {
				writeCheck(w, "cloudflare", "dns_api", "failed", err.Error())
				failed = true
			} else {
				cloudflareClient = client
			}
		}
	} else if appsHaveHosts(apps) {
		writeCheck(w, "cloudflare", "setup", "skipped", "-", "connect Cloudflare with `singleserver connect cloudflare` to verify DNS and tunnel routes")
	}

	verifyResolverDNS := cloudflareClient == nil
	for _, app := range apps {
		for _, host := range app.Hosts {
			if verifyResolverDNS && !doctorHostResolves(w, app.Name, "dns", host) {
				failed = true
			}
			if cloudflareClient != nil {
				target, err := verifyCloudflareDNSRecordFunc(host, state, cloudflareClient)
				if err != nil {
					writeCheck(w, app.Name, "cloudflare_dns", "failed", host, err.Error())
					failed = true
				} else {
					writeCheck(w, app.Name, "cloudflare_dns", "ok", host, "target="+target)
				}
			}
		}
	}
	if failed {
		return errors.New("domain verification failed")
	}
	return nil
}

var verifyCloudflareDNSRecordFunc = verifyCloudflareDNSRecord

func verifyCloudflareDNSRecord(host string, state *CloudflareState, client *CloudflareClient) (string, error) {
	if strings.TrimSpace(state.TunnelID) == "" {
		return "", errors.New("no Cloudflare DNS target configured; run `singleserver connect cloudflare`")
	}
	target := state.TunnelID + ".cfargotunnel.com"
	zone, err := client.zoneForHostname(host)
	if err != nil {
		return target, err
	}
	records, err := client.dnsRecords(zone.ID, host, "CNAME")
	if err != nil {
		return target, err
	}
	for _, record := range records {
		if dnsRecordContentMatches(record.Content, target) {
			return target, nil
		}
	}
	if len(records) == 0 {
		return target, fmt.Errorf("missing CNAME to %s", target)
	}
	contents := make([]string, 0, len(records))
	for _, record := range records {
		contents = append(contents, record.Content)
	}
	return target, fmt.Errorf("CNAME points to %s, expected %s", strings.Join(contents, ","), target)
}

func containsFold(values []string, needle string) bool {
	for _, value := range values {
		if strings.EqualFold(value, needle) {
			return true
		}
	}
	return false
}

func removeFold(values []string, needle string) []string {
	filtered := values[:0]
	for _, value := range values {
		if !strings.EqualFold(value, needle) {
			filtered = append(filtered, value)
		}
	}
	return filtered
}
