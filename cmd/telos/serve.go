package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/telos-org/telos-go/internal/local"
)

// -- serve / telosd (internal) ------------------------------------------------

// cmdServe runs telosd: the local Sessions API daemon for one workspace.
func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	workspaceRoot := fs.String("workspace-root", "", "Workspace root directory")
	idleSeconds := fs.Int("idle-seconds", defaultIdleSeconds(), "Idle shutdown timeout in seconds")
	parseFlags(fs, args)

	root := *workspaceRoot
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		root = cwd
	}

	if err := local.RunDaemon(root, *idleSeconds); err != nil {
		fmt.Fprintf(os.Stderr, "telosd: %v\n", err)
		os.Exit(1)
	}
}

func defaultIdleSeconds() int {
	if v := os.Getenv("TELOSD_IDLE_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return local.DefaultIdleSeconds
}
