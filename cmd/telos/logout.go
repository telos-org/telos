package main

import (
	"fmt"
	"os"

	"github.com/telos-org/telos/internal/cloud"
	"github.com/telos-org/telos/internal/config"
)

// -- logout -------------------------------------------------------------------

func cmdLogout(args []string) {
	fs := newCommandFlagSet("logout", "telos logout")
	parseFlags(fs, args)
	requireArgCount(fs, 0, "no positional arguments")

	effective, err := config.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if effective.AuthToken == "" {
		fmt.Println("not logged in")
		return
	}
	if os.Getenv(config.AuthTokenEnv) != "" {
		fmt.Fprintf(os.Stderr, "warning: %s is set; unset it to fully log out\n", config.AuthTokenEnv)
	} else if tokenID := cloud.APITokenID(effective.AuthToken); tokenID != "" {
		endpoint := effective.APIEndpoint
		if endpoint == "" {
			endpoint = cloud.DefaultAPIEndpoint
		}
		// Best-effort server-side revoke so the credential dies with the login.
		if err := cloud.NewClient(endpoint, effective.AuthToken).RevokeAPIToken(tokenID); err != nil {
			fmt.Fprintf(os.Stderr, "warning: token not revoked server-side: %v\n", err)
		}
	}

	stored, err := config.LoadStoredConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	stored.AuthToken = ""
	if err := config.SaveConfig(stored); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("logged out")
}
