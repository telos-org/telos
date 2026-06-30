// Command telos is the public Telos CLI.
//
// Public commands:
//
//	telos plan SPEC.md [--json]
//	telos push SPEC.md [--json]
//	telos apply SPEC.md [--json]
//	telos run SPEC.md [--workspace DIR] [--model MODEL] [--thinking EFFORT]
//	    [--until N] [--max-cost-usd USD] [--agent-timeout-sec SEC|0] [--json]
//	telos list [--env ENV] [--limit N] [--wide] [--environments] [--local] [--cloud] [--json]
//	telos describe SESSION [--env ENV] [--json]
//	telos logs [-f] [--raw] SESSION [--env ENV]
//	telos stop SESSION|DEPLOYMENT [--env ENV] [--json]
//	telos delete DEPLOYMENT [--json]
//	telos login [--endpoint URL] [--token TOKEN] [--no-prompt]
//	telos version
//	telos --version
package main

import (
	"fmt"
	"io"
	"os"
)

// Version is set at build time.
var Version = "dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("telos " + Version)
		return
	}
	if len(os.Args) == 2 && isHelpArg(os.Args[1]) {
		usage(os.Stdout)
		return
	}
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(1)
	}
	switch os.Args[1] {
	case "version":
		fmt.Println("telos " + Version)
		return
	case "plan":
		cmdPlan(os.Args[2:])
	case "push":
		cmdPush(os.Args[2:])
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
	case "delete":
		cmdDelete(os.Args[2:])
	case "login":
		cmdLogin(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(1)
	}
}

func isHelpArg(arg string) bool {
	return arg == "-h" || arg == "--help" || arg == "help"
}

func usage(out io.Writer) {
	fmt.Fprintln(out, "usage: telos <command> [args]")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "commands:")
	fmt.Fprintln(out, "  plan SPEC.md       Show compiled spec plan")
	fmt.Fprintln(out, "  push SPEC.md       Push an immutable spec package")
	fmt.Fprintln(out, "  apply SPEC.md      Apply a desired-state spec")
	fmt.Fprintln(out, "  run SPEC.md        Run a local or delegated spec")
	fmt.Fprintln(out, "  list               List sessions")
	fmt.Fprintln(out, "  describe SESSION   Show session details")
	fmt.Fprintln(out, "  logs SESSION       Show session progress")
	fmt.Fprintln(out, "  stop SESSION|DEPLOYMENT  Stop a session or deployment")
	fmt.Fprintln(out, "  delete DEPLOYMENT  Delete a cloud deployment")
	fmt.Fprintln(out, "  login              Configure cloud access")
	fmt.Fprintln(out, "  version            Show version")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "global flags:")
	fmt.Fprintln(out, "  -h, --help         Show help")
	fmt.Fprintln(out, "  --version          Show version")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Use `telos <command> --help` for command-specific flags.")
}
