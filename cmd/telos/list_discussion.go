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

// listFilters are the --filter keys each kind of listing takes.
var listFilters = map[string][]string{
	"goals":     {"status"},
	"proposals": {"goal", "status", "author"},
	"comments":  {"goal", "proposal", "status", "author"},
}

// parseListFilter reads --filter KEY=VALUE,... for a kind of listing.
func parseListFilter(kind, raw string) (url.Values, error) {
	keys, ok := listFilters[kind]
	if !ok {
		return nil, fmt.Errorf("unknown listing %q; use goals, proposals, or comments", kind)
	}
	filters := url.Values{}
	if strings.TrimSpace(raw) == "" {
		return filters, nil
	}
	for _, pair := range strings.Split(raw, ",") {
		key, value, _ := strings.Cut(pair, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !slices.Contains(keys, key) {
			return nil, fmt.Errorf("%s can't be filtered by %q; use %s", kind, key, strings.Join(keys, ", "))
		}
		if value == "" {
			return nil, fmt.Errorf("--filter %s needs a value, as in %s=VALUE", key, key)
		}
		switch {
		case key == "status":
			// telos list prints statuses capitalized, such as Pending; a
			// repeated status matches any of them.
			filters.Set(key, strings.Join(append(filters[key], strings.ToLower(value)), ","))
		case filters.Has(key):
			return nil, fmt.Errorf("--filter takes %s once", key)
		default:
			filters.Set(key, value)
		}
	}
	return filters, nil
}

func listProposals(control *cloud.Client, filters url.Values, limit int, jsonOut bool) {
	proposals, err := control.ListProposals(filters, limit)
	if err != nil {
		exitWithError(err)
	}
	proposals = limited(proposals, limit)
	if jsonOut {
		printJSON(map[string]any{"context": control.ContextName(), "proposals": proposals})
		return
	}
	if len(proposals) == 0 {
		fmt.Println("no proposals")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TITLE\tSTATUS\tUPDATED\tID")
	for _, proposal := range proposals {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", proposal.Title, proposalStatus(proposal.Status), orDash(ago(proposal.UpdatedAt)), proposal.ID)
	}
	_ = w.Flush()
}

func listComments(control *cloud.Client, filters url.Values, limit int, jsonOut bool) {
	proposalID, goalID := filters.Get("proposal"), filters.Get("goal")
	if proposalID == "" && goalID == "" {
		exitUsage("telos list comments requires --filter goal=GOAL or --filter proposal=PROPOSAL")
	}
	comments, err := control.ListComments(filters)
	if err != nil {
		exitWithError(err)
	}
	comments = limited(comments, limit)
	if jsonOut {
		printJSON(map[string]any{"context": control.ContextName(), "comments": comments})
		return
	}
	if len(comments) == 0 {
		fmt.Println("no comments")
		return
	}
	texts, err := commentTexts(control, proposalID, goalID, comments)
	if err != nil {
		exitWithError(err)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tAUTHOR\tON\tREPLIES")
	for _, comment := range comments {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", comment.ID, comment.Author.Label(), orDash(commentLocation(comment.Anchor, texts)), len(comment.Replies))
	}
	_ = w.Flush()
}

// commentTexts returns the files that comments on a proposal, or else on a
// Goal, quote.
func commentTexts(control *cloud.Client, proposalID, goalID string, comments []cloud.Comment) (fileTexts, error) {
	if proposalID == "" {
		return goalTexts(control, goalID, commentPaths(comments))
	}
	proposal, err := control.GetProposal(proposalID)
	if err != nil {
		return fileTexts{}, err
	}
	return proposalCommentTexts(control, proposal, commentPaths(comments))
}
