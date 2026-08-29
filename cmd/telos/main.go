// Command telos is the public Telos CLI. Run telos --help for its command
// surface and telos COMMAND --help for current flags.
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
	case "get":
		cmdGet(os.Args[2:])
	case "describe":
		cmdDescribe(os.Args[2:])
	case "logs":
		cmdLogs(os.Args[2:])
	case "delete":
		cmdDelete(os.Args[2:])
	case "pull":
		cmdPull(os.Args[2:])
	case "login":
		cmdLogin(os.Args[2:])
	case "logout":
		cmdLogout(os.Args[2:])
	case "config":
		cmdConfig(os.Args[2:])
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
	fmt.Fprintln(out, "core commands:")
	fmt.Fprintln(out, "  login              Log in to Telos Cloud via the browser")
	fmt.Fprintln(out, "  plan SPEC.md       Preview a spec without running it")
	fmt.Fprintln(out, "  apply SPEC.md      Create or update a durable session from a spec")
	fmt.Fprintln(out, "  list               List sessions")
	fmt.Fprintln(out, "  describe SESSION   Show session details")
	fmt.Fprintln(out, "  logs SESSION       Show recent activity")
	fmt.Fprintln(out, "  delete SESSION     Delete a session")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "other commands:")
	fmt.Fprintln(out, "  run SPEC.md        Run a spec as a bounded task")
	fmt.Fprintln(out, "  push SPEC.md       Publish a versioned spec or skill for reuse")
	fmt.Fprintln(out, "  pull PACKAGE       Download a package; use `pull skill REF` for a skill")
	fmt.Fprintln(out, "  get SESSION        Download a session's package")
	fmt.Fprintln(out, "  logout             Log out and revoke this device's token")
	fmt.Fprintln(out, "  config             Show or update CLI configuration")
	fmt.Fprintln(out, "  version            Show version")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "global flags:")
	fmt.Fprintln(out, "  -h, --help         Show help")
	fmt.Fprintln(out, "  --version          Show version")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Use `telos <command> --help` for command-specific flags.")
}
