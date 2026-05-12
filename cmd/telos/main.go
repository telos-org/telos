// Command telos is the Telos CLI and local Sessions API server.
//
// Public commands:
//
//	telos run SPEC.md
//	telos list
//	telos logs SESSION
//	telos describe SESSION
//	telos stop SESSION
//
// An embedded HTTP server (telosd) runs the Sessions API on localhost.
package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/telos-org/telos-go/internal/sessionapi"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		cmdRun(os.Args[2:])
	case "list":
		cmdList()
	case "logs":
		cmdLogs(os.Args[2:])
	case "describe":
		cmdDescribe(os.Args[2:])
	case "stop":
		cmdStop(os.Args[2:])
	case "serve":
		// Hidden internal role: run the Sessions API server.
		cmdServe()
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
	fmt.Fprintln(os.Stderr, "  run SPEC.md    Create and start a session")
	fmt.Fprintln(os.Stderr, "  list           List sessions")
	fmt.Fprintln(os.Stderr, "  logs SESSION   Show session transcript")
	fmt.Fprintln(os.Stderr, "  describe ID    Show session details")
	fmt.Fprintln(os.Stderr, "  stop ID        Stop a running session")
}

func store() *sessionapi.FileStore {
	root := os.Getenv("TELOS_SESSION_DIR")
	if root == "" {
		root = filepath.Join(".telos", "sessions")
	}
	return sessionapi.NewFileStore(root)
}

func cmdRun(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos run SPEC.md")
		os.Exit(1)
	}
	specPath := args[0]

	s := store()
	session, err := s.Create(sessionapi.SessionCreateRequest{
		SpecPath: &specPath,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("created session %s (status: %s)\n", session.SessionID, session.Status)
}

func cmdList() {
	s := store()
	sessions, err := s.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
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
		fmt.Printf("%-40s %-12s %s\n", sess.SessionID, sess.Status, name)
	}
}

func cmdLogs(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos logs SESSION")
		os.Exit(1)
	}
	s := store()
	text, err := s.Transcript(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(text)
}

func cmdDescribe(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos describe SESSION")
		os.Exit(1)
	}
	s := store()
	session, err := s.Get(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Session:  %s\n", session.SessionID)
	fmt.Printf("Status:   %s\n", session.Status)
	fmt.Printf("Runtime:  %s\n", session.Runtime)
	if session.SpecName != nil {
		fmt.Printf("Spec:     %s\n", *session.SpecName)
	}
	if session.CreatedAt != nil {
		fmt.Printf("Created:  %s\n", *session.CreatedAt)
	}
	if session.Result != nil {
		fmt.Printf("Result:   %s\n", *session.Result)
	}
	if session.Error != nil {
		fmt.Printf("Error:    %s\n", *session.Error)
	}
}

func cmdStop(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos stop SESSION")
		os.Exit(1)
	}
	s := store()
	session, err := s.Stop(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("session %s: %s\n", session.SessionID, session.Status)
}

func cmdServe() {
	addr := os.Getenv("TELOS_LISTEN_ADDR")
	if addr == "" {
		addr = "127.0.0.1:0"
	}

	s := store()
	mux := http.NewServeMux()
	sessionapi.RegisterRoutes(mux, s)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "telos sessions api listening on %s\n", ln.Addr())
	if err := http.Serve(ln, mux); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		os.Exit(1)
	}
}
