package main

import (
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/telos-org/telos/internal/cloud"
)

type integrationMetadata struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Keys []string `json:"keys"`
}

func cmdIntegrations(args []string) {
	cmdWorkspaceCredentials(args, true)
}

func cmdCredentials(args []string) {
	cmdWorkspaceCredentials(args, false)
}

func cmdWorkspaceCredentials(args []string, legacy bool) {
	command := "credentials"
	if legacy {
		command = "integrations"
	}
	if len(args) == 0 || isHelpArg(args[0]) {
		fmt.Printf("usage: telos %s <list|add> [flags]\n", command)
		fmt.Println("  list   List reusable credentials in the selected workspace")
		fmt.Println("  add    Print the workspace's secure form for adding credentials")
		return
	}
	if args[0] != "list" && args[0] != "add" {
		fmt.Fprintf(os.Stderr, "error: unknown %s command %q\n", command, args[0])
		os.Exit(2)
	}
	fs := newCommandFlagSet(command+" "+args[0], "telos "+command+" "+args[0]+" [flags]")
	jsonOut := fs.Bool("json", false, "JSON output")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args[1:])
	requireArgCount(fs, 0, "no positional arguments")
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if args[0] == "add" {
		contextName := control.ContextName()
		link, err := integrationAddURL(control)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if *jsonOut {
			printJSON(map[string]string{"url": link, "org_id": control.OrgID, "context": contextName})
			return
		}
		fmt.Println(link)
		fmt.Println("Enter credentials in the secure web form, then reference the saved ID on the appropriate egress destination in your goal.")
		return
	}
	secrets, err := control.ListSecrets()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	integrations := make([]integrationMetadata, 0, len(secrets))
	for _, secret := range secrets {
		if secret.ManagedBy != nil {
			continue
		}
		keys := []string{}
		for _, credential := range secret.Credentials {
			if !slices.Contains(keys, credential.Key) {
				keys = append(keys, credential.Key)
			}
		}
		integrations = append(integrations, integrationMetadata{ID: secret.ID, Name: secret.Name, Keys: keys})
	}
	if *jsonOut {
		printJSON(map[string]interface{}{"context": control.ContextName(), command: integrations})
		return
	}
	if len(integrations) == 0 {
		fmt.Println("no credentials listed")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tKEYS")
	for _, integration := range integrations {
		fmt.Fprintf(w, "%s\t%s\t%s\n", integration.ID, integration.Name, strings.Join(integration.Keys, ", "))
	}
	_ = w.Flush()
}

func integrationAddURL(control *cloud.Client) (string, error) {
	if cloud.NormalizeEndpoint(control.Endpoint) != cloud.DefaultAPIEndpoint {
		return "", fmt.Errorf("credential setup links require the official https://api.usetelos.ai control plane")
	}
	if control.OrgID == "" {
		organization, err := control.ResolveContext("personal")
		if err != nil {
			return "", err
		}
		control.OrgID = organization.ID
	}
	return "https://usetelos.ai/integrations/new?" + url.Values{"org": {control.OrgID}}.Encode(), nil
}
