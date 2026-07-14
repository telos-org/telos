package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
)

// -- login --------------------------------------------------------------------

func cmdLogin(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	endpoint := fs.String("endpoint", cloud.DefaultAPIEndpoint, "API endpoint")
	token := fs.String("token", "", "API token (skips the browser flow)")
	noPrompt := fs.Bool("no-prompt", false, "Never open a browser or prompt; require --token or TELOS_AUTH_TOKEN")
	parseFlags(fs, args)

	ep := cloud.NormalizeEndpoint(*endpoint)
	tok := *token
	if tok == "" {
		tok = os.Getenv("TELOS_AUTH_TOKEN")
	}
	if tok == "" && !*noPrompt {
		var err error
		tok, err = browserLogin(ep)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}
	if tok == "" {
		fmt.Fprintln(os.Stderr, "error: token required")
		os.Exit(1)
	}

	cfg := config.LoadConfig()
	cfg.APIEndpoint = ep
	cfg.AuthToken = tok
	if err := config.SaveConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if me, err := cloud.NewClient(ep, tok).Me(); err == nil {
		who := me.Subject
		if me.Email != nil && *me.Email != "" {
			who = *me.Email
		}
		fmt.Printf("logged in to %s as %s\n", ep, who)
		return
	}
	fmt.Printf("logged in to %s\n", ep)
}

// browserLogin runs the browser handshake: start a login request, send the
// user to the approval page, and poll until a token can be claimed.
func browserLogin(endpoint string) (string, error) {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown device"
	}
	start, err := cloud.StartCLIAuth(endpoint, hostname)
	if err != nil {
		if cloud.IsStatus(err, http.StatusNotFound) {
			// Control plane predates browser login; fall back to pasting.
			return promptForToken(), nil
		}
		return "", err
	}

	fmt.Println("Opening your browser to approve this login...")
	fmt.Printf("If it doesn't open, visit this link on any device: %s\n", start.VerificationURL)
	_ = openBrowser(start.VerificationURL)
	fmt.Print("Waiting for approval")

	interval := start.Interval
	if interval <= 0 {
		interval = 5
	}
	ttl := start.ExpiresIn
	if ttl <= 0 {
		ttl = 600
	}
	deadline := time.Now().Add(time.Duration(ttl) * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(time.Duration(interval) * time.Second)
		poll, err := cloud.PollCLIAuth(endpoint, start.RequestID, start.PollSecret)
		if err != nil {
			fmt.Println()
			return "", err
		}
		if poll.Interval > 0 {
			interval = poll.Interval
		}
		switch poll.Status {
		case "pending":
			fmt.Print(".")
		case "approved":
			fmt.Println()
			if poll.Token == "" {
				return "", errors.New("login was approved but no token was returned")
			}
			return poll.Token, nil
		case "denied":
			fmt.Println()
			return "", errors.New("login was denied in the browser")
		default: // expired, claimed
			fmt.Println()
			return "", fmt.Errorf("login request %s; run `telos login` again", poll.Status)
		}
	}
	fmt.Println()
	return "", errors.New("timed out waiting for approval; run `telos login` again")
}

func promptForToken() string {
	var tok string
	fmt.Print("Telos API token: ")
	fmt.Scanln(&tok)
	return tok
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
