package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/telos-org/telos-go/internal/config"
	"github.com/telos-org/telos-go/internal/hosted"
	"github.com/telos-org/telos-go/internal/sessionapi"
)

// -- list ---------------------------------------------------------------------

func cmdList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	env := fs.String("env", "", "Hosted environment")
	limit := fs.Int("limit", 0, "Limit results")
	wide := fs.Bool("wide", false, "Wide output")
	environments := fs.Bool("environments", false, "List hosted environments")
	localOnly := fs.Bool("local", false, "Local sessions only")
	hostedOnly := fs.Bool("hosted", false, "Hosted sessions only")
	jsonOut := fs.Bool("json", false, "JSON output")
	parseFlags(fs, args)

	if *environments {
		listEnvironments(*jsonOut)
		return
	}

	var sessions []sessionapi.Session

	// Local sessions
	if !*hostedOnly {
		s := store()
		local, err := s.List()
		if err == nil {
			sessions = append(sessions, local...)
		}
	}

	// Hosted sessions
	if !*localOnly && (*hostedOnly || *env != "" || config.IsConfigured()) {
		hostedSessions, err := listHostedSessions(*env, *limit)
		if err != nil && (*hostedOnly || *env != "") {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if err == nil {
			sessions = append(sessions, hostedSessions...)
		}
	}

	if *limit > 0 && len(sessions) > *limit {
		sessions = sessions[:*limit]
	}

	if *jsonOut {
		printJSON(sessionapi.SessionListResponse{Sessions: sessions})
		return
	}

	if len(sessions) == 0 {
		fmt.Println("no sessions")
		return
	}
	for _, sess := range sessions {
		name := ""
		if sess.SpecName != nil {
			name = *sess.SpecName
		}
		kind := ""
		if sess.SessionKind != nil {
			kind = string(*sess.SessionKind)
		}
		cost := ""
		if sess.TotalCostUSD != nil {
			cost = fmt.Sprintf("$%.2f", *sess.TotalCostUSD)
		}
		if *wide {
			parent := ""
			if sess.ParentSessionID != nil {
				parent = *sess.ParentSessionID
			}
			fmt.Printf("%-40s %-12s %-10s %-12s %-20s %-40s %s\n",
				sess.SessionID, sess.Status, sess.Runtime, kind, name, parent, cost)
			continue
		}
		fmt.Printf("%-40s %-12s %-20s %s\n", sess.SessionID, sess.Status, name, cost)
	}
}

func listEnvironments(jsonOut bool) {
	control, err := hosted.ControlClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	envs, err := control.ListEnvironments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if jsonOut {
		printJSON(map[string]any{"environments": envs})
		return
	}
	if len(envs) == 0 {
		fmt.Println("no environments")
		return
	}
	for _, env := range envs {
		access := "-"
		if _, ok := config.EnvironmentAccessByID(env.ID); ok {
			access = "local"
		} else if env.HasRecoverable {
			access = "recoverable"
		}
		fmt.Printf("%-16s %-14s %-36s %s\n", env.ID, env.State, env.Handle, access)
	}
}
