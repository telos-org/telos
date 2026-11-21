package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
)

// -- pause / resume -----------------------------------------------------------

func cmdPause(args []string) {
	cmdHold("pause", "Paused", "Why you are pausing, for the Goal's agents to read (required)", (*cloud.Client).PauseSession, args)
}

func cmdResume(args []string) {
	cmdHold("resume", "Resumed", "What the agents should know when they wake, such as what you changed (required)", (*cloud.Client).ResumeSession, args)
}

// cmdHold pauses or resumes a Cloud Goal's agents and leaves them a message.
// Workspace admins only.
func cmdHold(
	name, done, messageUsage string,
	call func(*cloud.Client, string, string) (*cloud.SessionRecord, error),
	args []string,
) {
	fs := newCommandFlagSet(name, "telos "+name+" GOAL -m MESSAGE [flags]")
	message := fs.String("m", "", messageUsage)
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
	if strings.TrimSpace(*message) == "" {
		fmt.Fprintf(os.Stderr, "error: %s requires -m MESSAGE for the Goal's agents\n", name)
		os.Exit(2)
	}
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	session, err := call(control, goalID, strings.TrimSpace(*message))
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
