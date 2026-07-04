package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
)

func cmdOrg(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: telos org list|use")
		os.Exit(1)
	}
	switch args[0] {
	case "list":
		cmdOrgList(args[1:])
	case "use":
		cmdOrgUse(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown org command: %s\n", args[0])
		os.Exit(1)
	}
}

func cmdOrgList(args []string) {
	fs := flag.NewFlagSet("org list", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	client, err := cloud.NewControlClientFromConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	me, err := client.Me()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if *jsonOut {
		printJSON(me)
		return
	}
	if len(me.Organizations) == 0 {
		fmt.Println("no organizations")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "CURRENT\tID\tNAME\tROLE")
	for _, org := range me.Organizations {
		current := ""
		if org.ID == me.OrgID {
			current = "*"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", current, org.ID, org.DisplayName, org.Role)
	}
	_ = w.Flush()
}

func cmdOrgUse(args []string) {
	fs := flag.NewFlagSet("org use", flag.ExitOnError)
	parseFlags(fs, args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: telos org use ORG")
		os.Exit(1)
	}
	target := strings.TrimSpace(fs.Arg(0))
	if target == "" {
		fmt.Fprintln(os.Stderr, "error: organization is required")
		os.Exit(1)
	}
	client, err := cloud.NewControlClientFromConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	me, err := client.Me()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	orgID := resolveOrgID(target, me.Organizations)
	if orgID == "" {
		fmt.Fprintf(os.Stderr, "error: organization %q not found\n", target)
		os.Exit(1)
	}
	client.SetOrgID(orgID)
	selected, err := client.Me()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	cfg := config.LoadConfig()
	cfg.OrgID = selected.OrgID
	if err := config.SaveConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(selected.OrgID)
}

func resolveOrgID(target string, orgs []cloud.OrganizationRecord) string {
	for _, org := range orgs {
		if org.ID == target || strings.EqualFold(org.DisplayName, target) {
			return org.ID
		}
	}
	return ""
}

type orgClient interface {
	SetOrgID(string)
}

func applyOrgOverride(client orgClient, orgID string) {
	if client == nil {
		return
	}
	if orgID = strings.TrimSpace(orgID); orgID != "" {
		client.SetOrgID(orgID)
	}
}
