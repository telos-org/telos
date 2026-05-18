// Command telosd is the Telos Sessions API daemon and session worker runtime.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/telos-org/telos-go/internal/telosd"
)

// Version is set at build time.
var Version = "dev"

func main() {
	fs := flag.NewFlagSet("telosd", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to telosd config YAML")
	mode := fs.String("mode", "", "Runtime mode: local or cloud")
	root := fs.String("root", "", "State root")
	sessionDir := fs.String("session-dir", "", "Run one persisted session worker")
	once := fs.Bool("once", false, "Run one controller cycle")
	showVersion := fs.Bool("version", false, "Show version")
	fs.Parse(os.Args[1:])

	if *showVersion {
		fmt.Println("telosd " + Version)
		return
	}
	if *sessionDir != "" {
		code, err := telosd.RunSessionWorker(*sessionDir, *once)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(code)
	}

	cfg, err := configFromFlags(*configPath, *mode, *root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := telosd.Run(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func configFromFlags(configPath, mode, root string) (telosd.Config, error) {
	var cfg telosd.Config
	var err error
	if configPath != "" {
		cfg, err = telosd.LoadConfig(configPath)
		if err != nil {
			return telosd.Config{}, err
		}
	} else {
		cfg = telosd.DefaultConfig(telosd.ModeLocal)
	}
	if mode != "" {
		cfg.Mode = telosd.Mode(mode)
	}
	if root != "" {
		cfg.Root = root
	}
	return telosd.NormalizeConfig(cfg)
}
