// Command telos is the Telos CLI and local runtime.
//
// Public commands:
//
//	telos plan SPEC.md [--json]
//	telos apply SPEC.md [--env ENV] [--json]
//	telos run SPEC.md [--workspace DIR] [--model MODEL] [--thinking EFFORT]
//	    [--max-rounds N] [--max-cost-usd USD] [--agent-timeout-sec SEC] [--json]
//	telos list [--env ENV] [--limit N] [--wide] [--environments] [--local] [--hosted] [--json]
//	telos describe SESSION [--env ENV] [--json]
//	telos logs [-f] SESSION [--env ENV]
//	telos stop SESSION [--env ENV] [--json]
//	telos login [--endpoint URL] [--token TOKEN] [--no-prompt]
//	telos version
package main

import (
	"fmt"
	"os"
)

// Version is set at build time.
var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "plan":
		cmdPlan(os.Args[2:])
	case "apply":
		cmdApply(os.Args[2:])
	case "run":
		cmdRun(os.Args[2:])
	case "list":
		cmdList(os.Args[2:])
	case "describe":
		cmdDescribe(os.Args[2:])
	case "logs":
		cmdLogs(os.Args[2:])
	case "stop":
		cmdStop(os.Args[2:])
	case "login":
		cmdLogin(os.Args[2:])
	case "version":
		fmt.Println("telos " + Version)
	case "serve":
		cmdServe()
	case "worker":
		cmdWorker(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: telos <command> [args]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  plan SPEC.md       Show compiled spec plan")
	fmt.Fprintln(os.Stderr, "  apply SPEC.md      Apply a desired-state spec")
	fmt.Fprintln(os.Stderr, "  run SPEC.md        Create and run a session")
	fmt.Fprintln(os.Stderr, "  list               List sessions")
	fmt.Fprintln(os.Stderr, "  describe SESSION   Show session details")
	fmt.Fprintln(os.Stderr, "  logs SESSION       Show session transcript")
	fmt.Fprintln(os.Stderr, "  stop SESSION       Stop a running session")
	fmt.Fprintln(os.Stderr, "  login              Configure hosted access")
	fmt.Fprintln(os.Stderr, "  version            Show version")
}
