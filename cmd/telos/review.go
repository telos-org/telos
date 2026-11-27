package main

import (
	"fmt"
)

func cmdReview(args []string) {
	fs := newCommandFlagSet("review", "telos review PROPOSAL --approved|--blocked|--none [flags]")
	approved := fs.Bool("approved", false, "Approve the latest update")
	blocked := fs.Bool("blocked", false, "Block the latest update")
	none := fs.Bool("none", false, "Take back your review")
	jsonOut := fs.Bool("json", false, "JSON output")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	contextOverride, err := cloudContextOverride(fs, *contextValue)
	if err != nil {
		exitUsage("%v", err)
	}
	requireArgCount(fs, 1, "one PROPOSAL")
	requireIDKind("review", fs.Arg(0), "a PROPOSAL", "proposal")
	chosen := ""
	switch {
	case *approved && !*blocked && !*none:
		chosen = "approved"
	case *blocked && !*approved && !*none:
		chosen = "blocked"
	case *none && !*approved && !*blocked:
		// --none takes the review back.
	default:
		exitUsage("use one of --approved, --blocked, or --none")
	}
	var verdict *string
	if chosen != "" {
		verdict = &chosen
	}

	control := cloudClient(contextOverride)
	proposal, err := control.GetProposal(fs.Arg(0))
	if err != nil {
		exitWithError(err)
	}
	reviewed, err := control.ReviewProposal(proposal.ID, proposal.UpdateNumber, verdict)
	if err != nil {
		exitWithError(ruleError(err, ""))
	}
	if *jsonOut {
		printJSON(reviewed)
		return
	}
	if verdict == nil {
		fmt.Printf("cleared your review of %q\n", reviewed.Title)
		return
	}
	fmt.Printf("%s %q\n", *verdict, reviewed.Title)
}
