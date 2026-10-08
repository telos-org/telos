package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/telos-org/telos/internal/cloud"
)

const revisionPageSize = 50

var revisionIDPattern = regexp.MustCompile(`^rev_[A-Za-z0-9_-]+$`)

func cmdHistory(args []string) {
	fs := newCommandFlagSet("history", "telos history SESSION [flags]")
	selector := fs.String("revision", "", "Inspect a revision number or full revision ID")
	limit := fs.Int("limit", revisionPageSize, "Revisions per page (1–100)")
	before := fs.Int("before", 0, "List revisions before this revision number")
	all := fs.Bool("all", false, "Read all remaining history pages")
	jsonOut := fs.Bool("json", false, "JSON output")
	contextValue := cloudContextFlag(fs)
	parseFlags(fs, args)
	requireArgCount(fs, 1, "one SESSION")
	if *limit < 1 || *limit > 100 || *before < 0 || (flagNameSet(fs, "before") && *before == 0) {
		exitWithError(fmt.Errorf("--limit must be between 1 and 100; --before must be positive"))
	}
	if flagNameSet(fs, "revision") {
		if _, err := parseRevisionSelector(*selector); err != nil {
			exitWithError(err)
		}
		if flagNamesSet(fs, "limit", "before", "all") {
			exitWithError(fmt.Errorf("--revision cannot be combined with --limit, --before, or --all"))
		}
	}
	control, session, capabilities, err := revisionCommandTarget(fs, *contextValue)
	if err != nil {
		exitWithError(err)
	}
	ctx := context.Background()
	page, err := control.ListSessionRevisions(ctx, session.ID, *limit, *before)
	if err != nil {
		exitWithError(err)
	}
	if *selector != "" {
		revision, err := resolveRevision(ctx, control, session.ID, *selector, page)
		if err != nil {
			exitWithError(err)
		}
		if *jsonOut {
			printJSON(struct {
				SessionID         string              `json:"session_id"`
				Context           string              `json:"context"`
				CurrentRevisionID string              `json:"current_revision_id"`
				Capabilities      *cloud.Capabilities `json:"capabilities"`
				Revision          *cloud.Revision     `json:"revision"`
			}{session.ID, control.ContextName(), page.CurrentRevisionID, capabilities, revision})
			return
		}
		printRevisionDetail(os.Stdout, session, control.ContextName(), page, revision, capabilities)
		return
	}
	if *all {
		for page.NextBefore != nil {
			next, err := control.ListSessionRevisions(ctx, session.ID, *limit, *page.NextBefore)
			if err != nil {
				exitWithError(err)
			}
			if next.CurrentRevisionID != page.CurrentRevisionID {
				exitWithError(fmt.Errorf("the current revision changed while reading history; retry history"))
			}
			page.Revisions = append(page.Revisions, next.Revisions...)
			page.NextBefore = next.NextBefore
		}
	}
	if *jsonOut {
		printJSON(struct {
			SessionID    string              `json:"session_id"`
			Context      string              `json:"context"`
			Capabilities *cloud.Capabilities `json:"capabilities"`
			*cloud.RevisionPage
		}{session.ID, control.ContextName(), capabilities, page})
		return
	}
	printRevisionHistory(os.Stdout, session, control.ContextName(), page)
}

func revisionCommandTarget(fs *flag.FlagSet, contextValue string) (*cloud.Client, *cloud.SessionRecord, *cloud.Capabilities, error) {
	sessionID := strings.TrimSpace(fs.Arg(0))
	if !isCloudApplyID(sessionID) {
		return nil, nil, nil, fmt.Errorf("%s requires a Cloud session ID (sess_...)", fs.Name())
	}
	contextOverride, err := cloudContextOverride(fs, contextValue)
	if err != nil {
		return nil, nil, nil, err
	}
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		return nil, nil, nil, err
	}
	capabilities, err := control.RegistryCapabilities()
	if cloud.IsStatus(err, 404) || (err == nil && !capabilities.DeploymentRevisionHistory) {
		return nil, nil, nil, fmt.Errorf("this Cloud release does not support revision history")
	}
	if err != nil {
		return nil, nil, nil, err
	}
	session, err := control.GetSession(sessionID)
	if err != nil {
		return nil, nil, nil, err
	}
	if session.ID != sessionID {
		return nil, nil, nil, fmt.Errorf("Cloud returned a different session")
	}
	return control, session, capabilities, nil
}

func parseRevisionSelector(value string) (int, error) {
	if number, err := strconv.Atoi(value); err == nil && number > 0 {
		return number, nil
	}
	if revisionIDPattern.MatchString(value) {
		return 0, nil
	}
	return 0, fmt.Errorf("revision must be a positive number or full revision ID (rev_...); got %q", value)
}

func resolveRevision(ctx context.Context, control *cloud.Client, sessionID, selector string, first *cloud.RevisionPage) (*cloud.Revision, error) {
	number, err := parseRevisionSelector(selector)
	if err != nil {
		return nil, err
	}
	if number == 0 {
		return control.GetSessionRevision(ctx, sessionID, selector)
	}
	page := first
	for {
		for _, revision := range page.Revisions {
			if revision.Sequence == number {
				return &revision, nil
			}
		}
		if page.NextBefore == nil {
			return nil, fmt.Errorf("revision %d was not found in session %s", number, sessionID)
		}
		page, err = control.ListSessionRevisions(ctx, sessionID, revisionPageSize, *page.NextBefore)
		if err != nil {
			return nil, err
		}
		if page.CurrentRevisionID != first.CurrentRevisionID {
			return nil, fmt.Errorf("the current revision changed while selecting a revision; refresh history and retry")
		}
	}
}

func printRevisionHistory(out io.Writer, session *cloud.SessionRecord, contextName string, page *cloud.RevisionPage) {
	printRevisionField(out, "Goal", session.Name)
	printRevisionField(out, "Session", session.ID)
	printRevisionField(out, "Context", contextName)
	printRevisionField(out, "Current revision", revisionLabel(page.CurrentRevisionID, page))
	fmt.Fprintln(out)
	if len(page.Revisions) == 0 {
		fmt.Fprintln(out, "No revisions in this history page.")
		return
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "REV\tACTION\tDATE\tAUTHOR\tSNAPSHOT\tMESSAGE")
	for _, revision := range page.Revisions {
		label := strconv.Itoa(revision.Sequence)
		if revision.ID == page.CurrentRevisionID {
			label += "*"
		}
		date := revision.CommittedAt
		if len(date) > 10 {
			date = date[:10]
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", label, revisionText(revision.Kind), revisionText(date), revisionAuthor(&revision), revisionText(revision.Snapshot.Status), revisionText(revision.Message))
	}
	_ = w.Flush()
	fmt.Fprintln(out, "\n* Current revision")
	if page.NextBefore != nil {
		fmt.Fprintf(out, "\nOlder revisions: telos history %s --context %s --before %d\n", session.ID, contextName, *page.NextBefore)
	}
}

func printRevisionDetail(out io.Writer, session *cloud.SessionRecord, contextName string, page *cloud.RevisionPage, revision *cloud.Revision, capabilities *cloud.Capabilities) {
	printRevisionField(out, "Goal", session.Name)
	printRevisionField(out, "Session", session.ID)
	printRevisionField(out, "Context", contextName)
	printRevisionField(out, "Revision", strconv.Itoa(revision.Sequence))
	printRevisionField(out, "Revision ID", revision.ID)
	printRevisionField(out, "Current revision", revisionLabel(page.CurrentRevisionID, page))
	printRevisionField(out, "Parent revision", revisionLabel(revisionString(revision.ParentRevisionID), page))
	if revision.RestoredFromRevisionID != nil {
		printRevisionField(out, "Source revision", revisionLabel(*revision.RestoredFromRevisionID, page))
	}
	printRevisionField(out, "Action", revision.Kind)
	printRevisionField(out, "Author", revisionAuthor(revision))
	printRevisionField(out, "Created", revision.CommittedAt)
	printRevisionField(out, "Message", revision.Message)
	fmt.Fprintln(out)
	printRevisionField(out, "Package", revision.PackageRef)
	printRevisionField(out, "Digest", revision.PackageDigest)
	printRevisionField(out, "Snapshot", revision.Snapshot.Status)
	printRevisionField(out, "Captured", revisionString(revision.Snapshot.CapturedAt))
	fmt.Fprintln(out)
	printRevisionField(out, "Restore", revisionCapabilityText(revisionActionCapability("restore", revision, page.CurrentRevisionID, capabilities)))
	printRevisionField(out, "Redeploy", revisionCapabilityText(revisionActionCapability("redeploy", revision, page.CurrentRevisionID, capabilities)))
}

func revisionLabel(id string, page *cloud.RevisionPage) string {
	for _, revision := range page.Revisions {
		if revision.ID == id {
			return strconv.Itoa(revision.Sequence)
		}
	}
	return orDash(id)
}

func revisionString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func revisionAuthor(revision *cloud.Revision) string {
	if name := revisionString(revision.CreatedBy.Name); name != "" {
		return revisionText(name)
	}
	return revisionText(revision.CreatedBy.Subject)
}

func printRevisionField(out io.Writer, label, value string) {
	fmt.Fprintf(out, "%-18s %s\n", label, orDash(revisionText(value)))
}

func revisionText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, value)
}

func revisionCapabilityText(capability cloud.RevisionCapability) string {
	if capability.Allowed {
		return "available"
	}
	return "unavailable — " + revisionReason(revisionString(capability.Reason))
}

func revisionReason(reason string) string {
	reasons := map[string]string{
		"unsupported":            "not supported by this Cloud release",
		"current_revision":       "this revision is already current",
		"no_snapshot":            "no snapshot was captured",
		"snapshot_pending":       "a snapshot is still being captured",
		"snapshot_lost":          "the saved snapshot is no longer available",
		"snapshot_incompatible":  "the saved snapshot is incompatible with this deployment host",
		"operation_in_progress":  "another deployment operation is in progress",
		"deployment_not_ready":   "the deployment is not ready for this action",
		"runtime_unsupported":    "this deployment host does not support snapshot restore",
		"package_unavailable":    "the revision's package is no longer available",
		"runtime_unavailable":    "the revision's runtime release is no longer available",
		"stale_current_revision": "the current revision changed; refresh history before trying again",
	}
	if message, ok := reasons[reason]; ok {
		return message
	}
	if reason == "" {
		return "this action is not currently allowed"
	}
	return reason
}
