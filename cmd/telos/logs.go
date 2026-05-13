package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/telos-org/telos-go/internal/hosted"
)

// -- logs ---------------------------------------------------------------------

func cmdLogs(args []string) {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	follow := fs.Bool("f", false, "Follow transcript")
	env := fs.String("env", "", "Hosted environment")
	parseFlags(fs, args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: telos logs [-f] SESSION")
		os.Exit(1)
	}
	sessionID := fs.Arg(0)

	if *follow {
		followLogs(sessionID, *env)
		return
	}

	text, err := getTranscriptFromAnywhere(sessionID, *env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(text)
}

func followLogs(sessionID, envID string) {
	if ctx, ok := controllerSessionContext(); ok {
		client := hosted.NewClient(ctx.endpoint, ctx.token)
		streamHostedEvents(client, sessionID)
		return
	}
	if localSessionExists(sessionID) {
		followTranscript(sessionID, envID)
		return
	}
	client, err := hostedClientForSession(sessionID, envID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	streamHostedEvents(client, sessionID)
}

func streamHostedEvents(client *hosted.Client, sessionID string) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	err := client.StreamEvents(ctx, sessionID, func(event map[string]any) error {
		return enc.Encode(event)
	})
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

func followTranscript(sessionID, envID string) {
	var lastLen int
	for {
		text, err := getTranscriptFromAnywhere(sessionID, envID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if len(text) > lastLen {
			fmt.Print(text[lastLen:])
			lastLen = len(text)
		}
		// Check if session is terminal
		sess, err := getSessionFromAnywhere(sessionID, envID)
		if err == nil && sess.Status.IsTerminal() {
			break
		}
		time.Sleep(2 * time.Second)
	}
}
