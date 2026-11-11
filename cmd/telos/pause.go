package main

import (
	"fmt"
	"os"

	"github.com/telos-org/telos/internal/cloud"
)

// -- pause / resume -----------------------------------------------------------

func cmdPause(args []string) {
	cmdHold("pause", "Paused", (*cloud.Client).PauseSession, args)
}

func cmdResume(args []string) {
	cmdHold("resume", "Resumed", (*cloud.Client).ResumeSession, args)
}

// cmdHold pauses or resumes a Cloud Goal's agents. Workspace admins only.
func cmdHold(
	name, done string,
	call func(*cloud.Client, string) (*cloud.SessionRecord, error),
	args []string,
) {
	fs := newCommandFlagSet(name, "telos "+name+" GOAL [flags]")
	jsonOut := fs.Bool("json", false, "JSON output")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	requireArgCount(fs, 1, "one GOAL")
	goalID := fs.Arg(0)
	if err := validateCloudSessionContext(goalID, contextOverride); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if isLocalApplyID(goalID) {
		fmt.Fprintf(os.Stderr, "error: %s works only for Cloud Goals\n", name)
		os.Exit(2)
	}
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	session, err := call(control, goalID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", goalNotFound(err, goalID, control.ContextName()))
		os.Exit(1)
	}
	if *jsonOut {
		printJSON(newCloudSessionJSON(session))
		return
	}
	fmt.Printf("%s %s\n", done, session.ID)
}
