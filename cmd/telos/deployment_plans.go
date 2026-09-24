package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	internaldiff "github.com/rogpeppe/go-internal/diff"
	"github.com/telos-org/telos/internal/cloud"
	"golang.org/x/term"
)

const savedPlanVersion = 1

// A bookmark identifies a server-owned proposal, never credentials or executable
// inputs. The configured endpoint/context must match before it can be used.
type savedDeploymentPlan struct {
	Version         int    `json:"version"`
	ChangeRequestID string `json:"change_request_id"`
	DeploymentID    string `json:"deployment_id"`
	Context         string `json:"context"`
	OrgID           string `json:"org_id"`
	APIEndpoint     string `json:"api_endpoint"`
}

type cloudPlanInput struct {
	specArg             string
	sessionID           string
	runtimeConfig       sessionRuntimeConfig
	force               bool
	requireConfirmation *bool
	contextOverride     string
	mode                string
	autoConfirm         bool
}

func checkFreshApplyConfirmation(yes, jsonOut, stdinTTY, promptTTY bool) error {
	if yes {
		return nil
	}
	if jsonOut || !stdinTTY || !promptTTY {
		return fmt.Errorf("interactive confirmation is unavailable; use `telos apply SPEC.md --yes` to confirm automatically, or `telos plan SPEC.md --out=change.plan` to submit a Change Request for review")
	}
	return nil
}

func cloudPlanPreflight(control *cloud.Client, sessionID, mode string) error {
	capabilities, err := control.DeploymentCapabilities()
	if err != nil {
		return err
	}
	if !capabilities.DeploymentPlans {
		return fmt.Errorf("this Cloud server does not support deployment plans; update Cloud before using plan or apply")
	}
	access, err := control.DeploymentPlanAccess(sessionID)
	if err != nil {
		return err
	}
	if mode == "apply" && !access.CanApply {
		return fmt.Errorf("Apply permission is required; use `telos plan SPEC.md --out=change.plan` to submit a Change Request for an owner or admin to confirm")
	}
	if !access.CanPlan {
		return fmt.Errorf("you do not have permission to plan changes for this deployment or context")
	}
	return nil
}

func createCloudPlan(control *cloud.Client, input cloudPlanInput) (*cloud.ChangeRequestRecord, *cloud.PackageVersionRecord, error) {
	if err := cloudPlanPreflight(control, input.sessionID, input.mode); err != nil {
		return nil, nil, err
	}
	options := cloud.DeploymentPlanOptions{
		Mode: input.mode, DeploymentID: input.sessionID, AutoConfirm: input.autoConfirm,
	}
	if input.sessionID != "" {
		current, err := control.GetSession(input.sessionID)
		if err != nil {
			return nil, nil, err
		}
		options.Update = &cloud.SessionUpdateOptions{
			Force: input.force, ExpectedCurrentRevisionID: current.CurrentRevisionID,
		}
	} else {
		inference, err := resolveCloudInference(control, input.runtimeConfig.Model)
		if err != nil {
			return nil, nil, err
		}
		options.Create = &cloud.SessionCreateOptions{
			AgentThinking: input.runtimeConfig.Thinking, Inference: inference,
			RequireConfirmation: input.requireConfirmation,
		}
	}
	var record *cloud.PackageVersionRecord
	var name string
	if strings.HasPrefix(strings.TrimSpace(input.specArg), "@") {
		reference, err := parsePackageReference(input.specArg)
		if err != nil {
			return nil, nil, err
		}
		name = reference.name
		record, err = registryPackageForApply(control, reference)
		if err != nil {
			return nil, nil, err
		}
	} else {
		path := resolveSpecPath(input.specArg)
		err := withPlanRegistrySkills(path, input.contextOverride, func() error {
			pkg, err := packageSpec(path, input.contextOverride)
			if err != nil {
				return err
			}
			name = pkg.name
			record, err = pushSpecPackage(control.PlanArtifactClient(), pkg, "")
			return err
		})
		if err != nil {
			return nil, nil, err
		}
	}
	if options.Create != nil {
		options.Create.Name = name
		options.Create.PackageRef = record.Ref
	} else {
		options.Update.PackageRef = record.Ref
	}
	request, err := control.CreateDeploymentPlan(options)
	if err != nil {
		return nil, nil, actionableDeploymentUpdateError(err, input.force)
	}
	if request.Mode != input.mode {
		return nil, nil, fmt.Errorf("Cloud returned a %q request for a %q plan", request.Mode, input.mode)
	}
	if input.mode != "apply" && request.Preview == nil {
		return nil, nil, fmt.Errorf("Cloud returned a plan without a preview; request %s", request.ID)
	}
	return request, record, nil
}

func cloudPlanOrgID(control *cloud.Client) (string, error) {
	if control.OrgID != "" {
		return control.OrgID, nil
	}
	account, err := control.AccountBootstrap()
	if err != nil {
		return "", err
	}
	if account.PersonalOrgID == "" {
		return "", fmt.Errorf("Cloud did not identify the personal organization")
	}
	return account.PersonalOrgID, nil
}

func newSavedDeploymentPlan(control *cloud.Client, request *cloud.ChangeRequestRecord, orgID string) savedDeploymentPlan {
	return savedDeploymentPlan{
		Version: savedPlanVersion, ChangeRequestID: request.ID, DeploymentID: request.DeploymentID,
		Context: control.ContextName(), OrgID: orgID, APIEndpoint: control.Endpoint,
	}
}

// A temporary sibling and an atomic link provide an all-or-nothing write that
// refuses to replace a file, including a symlink or a concurrently saved plan.
type savedPlanWriter struct {
	file *os.File
	path string
}

func prepareSavedPlanWriter(path string) (*savedPlanWriter, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("--out requires a filename")
	}
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("refusing to overwrite %s; choose another --out filename", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".telos-plan-*")
	if err != nil {
		return nil, fmt.Errorf("prepare saved plan file: %w", err)
	}
	return &savedPlanWriter{file: f, path: path}, nil
}

func (w *savedPlanWriter) close() {
	_ = w.file.Close()
	_ = os.Remove(w.file.Name())
}

func (w *savedPlanWriter) save(bookmark savedDeploymentPlan) error {
	encoder := json.NewEncoder(w.file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(bookmark); err != nil {
		return err
	}
	if err := w.file.Sync(); err != nil {
		return err
	}
	if err := w.file.Close(); err != nil {
		return err
	}
	return os.Link(w.file.Name(), w.path)
}

func readSavedDeploymentPlan(path string) (*savedDeploymentPlan, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// SPEC.md starts with YAML frontmatter. Any JSON-looking file is treated as
	// a bookmark and can never fall through to a new deployment on parse error.
	r := bufio.NewReader(f)
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, nil
		}
		if bytes.ContainsRune([]byte(" \t\r\n"), rune(b)) {
			continue
		}
		if b != '{' && b != '[' {
			return nil, nil
		}
		_ = r.UnreadByte()
		break
	}
	data, err := io.ReadAll(io.LimitReader(r, 64*1024+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 64*1024 {
		return nil, fmt.Errorf("saved plan file is too large")
	}
	var bookmark savedDeploymentPlan
	if err := json.Unmarshal(data, &bookmark); err != nil {
		return nil, fmt.Errorf("invalid saved plan file: %w", err)
	}
	if bookmark.Version != savedPlanVersion {
		return nil, fmt.Errorf("unsupported saved plan format %d; update the Telos CLI or create a new saved plan", bookmark.Version)
	}
	if bookmark.ChangeRequestID == "" || bookmark.DeploymentID == "" || bookmark.Context == "" || bookmark.OrgID == "" || bookmark.APIEndpoint == "" {
		return nil, fmt.Errorf("invalid saved plan file: request, deployment, context, organization, and API endpoint are required")
	}
	return &bookmark, nil
}

func validateSavedDeploymentPlan(control *cloud.Client, bookmark *savedDeploymentPlan) error {
	if cloud.NormalizeEndpoint(bookmark.APIEndpoint) != control.Endpoint {
		return fmt.Errorf("saved plan belongs to a different API endpoint; select its configured endpoint before applying")
	}
	if bookmark.Context != control.ContextName() {
		return fmt.Errorf("saved plan belongs to context %s, but the selected context is %s; use --context %s", bookmark.Context, control.ContextName(), bookmark.Context)
	}
	orgID, err := cloudPlanOrgID(control)
	if err != nil {
		return err
	}
	if bookmark.OrgID != orgID {
		return fmt.Errorf("saved plan belongs to a different organization or personal account")
	}
	return nil
}

func runCloudPlan(input cloudPlanInput, output string, jsonOut bool) error {
	var writer *savedPlanWriter
	var err error
	if input.mode == "saved" {
		writer, err = prepareSavedPlanWriter(output)
		if err != nil {
			return err
		}
		defer writer.close()
	}
	control, err := cloud.ControlClientForContext(input.contextOverride)
	if err != nil {
		return err
	}
	var orgID string
	if writer != nil {
		orgID, err = cloudPlanOrgID(control)
		if err != nil {
			return err
		}
	}
	request, pkg, err := createCloudPlan(control, input)
	if err != nil {
		return err
	}
	if writer != nil {
		if err := writer.save(newSavedDeploymentPlan(control, request, orgID)); err != nil {
			return fmt.Errorf("Change Request %s was saved, but its local reference could not be written: %w; review it at %s", request.ID, err, cloudRequestReviewURL(control, *request))
		}
	}
	if jsonOut {
		printDeploymentPlanJSON(control, request, pkg, output)
		return nil
	}
	printDeploymentPlan(os.Stdout, control, request)
	if output != "" {
		printSummaryField(os.Stdout, "Saved", output)
	}
	return nil
}

func runCloudApply(input cloudPlanInput, jsonOut bool) error {
	if err := checkFreshApplyConfirmation(input.autoConfirm, jsonOut,
		term.IsTerminal(int(os.Stdin.Fd())), term.IsTerminal(int(os.Stderr.Fd()))); err != nil {
		return err
	}
	control, err := cloud.ControlClientForContext(input.contextOverride)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	request, pkg, err := createCloudPlan(control.WithContext(ctx), input)
	if err != nil {
		return err
	}
	if !jsonOut {
		printDeploymentPlan(os.Stdout, control, request)
	}
	request, err = awaitCloudApply(ctx, control, request, input.autoConfirm, os.Stdin,
		planOutputWriter(jsonOut), os.Stderr, time.Second)
	if err != nil {
		return err
	}
	if jsonOut {
		printDeploymentPlanJSON(control, request, pkg, "")
	} else {
		printDeploymentPlanResult(os.Stdout, control, request)
	}
	return nil
}

func runSavedCloudApply(bookmark *savedDeploymentPlan, contextOverride string, jsonOut bool) error {
	control, err := cloud.ControlClientForContext(contextOverride)
	if err != nil {
		return err
	}
	if err := validateSavedDeploymentPlan(control, bookmark); err != nil {
		return err
	}
	if err := cloudPlanPreflight(control, bookmark.DeploymentID, "apply"); err != nil {
		return err
	}
	request, err := control.GetChangeRequest(bookmark.DeploymentID, bookmark.ChangeRequestID)
	if err != nil {
		return err
	}
	if request.Mode != "saved" {
		return fmt.Errorf("this file does not identify a saved Change Request; preview-only plans cannot be applied")
	}
	if request.ID != bookmark.ChangeRequestID || request.DeploymentID != bookmark.DeploymentID {
		return fmt.Errorf("Cloud returned a different Change Request than the saved file")
	}
	if !jsonOut {
		printDeploymentPlan(os.Stdout, control, request)
	}
	if request.Status != "applied" && request.Status != "applying" && request.Status != "confirmed" {
		if request.Status != "awaiting_confirmation" {
			return requestStoppedError(request)
		}
		request, err = confirmCloudRequest(control, request)
		if err != nil {
			return err
		}
	}
	// Explicit saved-plan application is itself confirmation. Once confirmed,
	// a disconnected terminal cannot revoke that authorization.
	if jsonOut {
		printDeploymentPlanJSON(control, request, nil, "")
	} else {
		printDeploymentPlanResult(os.Stdout, control, request)
	}
	return nil
}

func planOutputWriter(jsonOut bool) io.Writer {
	if jsonOut {
		return io.Discard
	}
	return os.Stdout
}

func requestStarted(request *cloud.ChangeRequestRecord) bool {
	return request.Status == "confirmed" || request.Status == "applying" || request.Status == "applied"
}

func requestStoppedError(request *cloud.ChangeRequestRecord) error {
	reason := ""
	if request.Error != nil {
		reason = ": " + *request.Error
	}
	return fmt.Errorf("Change Request %s is %s%s; inspect its dashboard page before submitting a new plan", request.ID, changeRequestStatus(request.Status), reason)
}

func confirmCloudRequest(control *cloud.Client, request *cloud.ChangeRequestRecord) (*cloud.ChangeRequestRecord, error) {
	confirmed, err := control.ConfirmChangeRequest(*request)
	if err == nil {
		if !requestStarted(confirmed) {
			return nil, requestStoppedError(confirmed)
		}
		return confirmed, nil
	}
	// A dashboard confirmation or a lost acknowledgement may have won the race.
	current, readErr := control.GetChangeRequest(request.DeploymentID, request.ID)
	if readErr == nil && requestStarted(current) {
		return current, nil
	}
	return nil, err
}

func discardUnstartedCloudRequest(control *cloud.Client, request *cloud.ChangeRequestRecord) (*cloud.ChangeRequestRecord, error) {
	discarded, err := control.DiscardChangeRequest(request.DeploymentID, request.ID)
	if err != nil {
		current, readErr := control.GetChangeRequest(request.DeploymentID, request.ID)
		if readErr == nil && requestStarted(current) {
			return current, nil
		}
		return nil, fmt.Errorf("could not discard Change Request %s: %w; inspect or discard it at %s", request.ID, err, cloudRequestReviewURL(control, *request))
	}
	if requestStarted(discarded) {
		return discarded, nil
	}
	return nil, fmt.Errorf("Change Request %s was discarded; no changes were applied", request.ID)
}

func awaitCloudApply(ctx context.Context, control *cloud.Client, request *cloud.ChangeRequestRecord,
	autoConfirm bool, input io.Reader, previewOut, promptOut io.Writer, pollInterval time.Duration,
) (*cloud.ChangeRequestRecord, error) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	var answer <-chan bool
	printedPreview := request.Preview != nil
	for {
		if request.Preview != nil && !printedPreview {
			printDeploymentPlan(previewOut, control, request)
			printedPreview = true
		}
		if requestStarted(request) {
			return request, nil
		}
		if request.Status != "queued" && request.Status != "awaiting_confirmation" {
			return nil, requestStoppedError(request)
		}
		if request.Status == "awaiting_confirmation" && !autoConfirm && answer == nil {
			if request.Preview == nil {
				return nil, fmt.Errorf("Cloud did not provide a reviewable plan for request %s", request.ID)
			}
			fmt.Fprint(promptOut, "Apply these changes? Type yes to confirm: ")
			response := make(chan bool, 1)
			answer = response
			go func() {
				line, err := bufio.NewReader(input).ReadString('\n')
				// EOF, including a partial line, never supplies confirmation.
				response <- err == nil && strings.TrimSpace(line) == "yes"
			}()
		}
		select {
		case <-ctx.Done():
			return discardUnstartedCloudRequest(control, request)
		case yes := <-answer:
			if !yes {
				return discardUnstartedCloudRequest(control, request)
			}
			return confirmCloudRequest(control, request)
		case <-ticker.C:
			current, err := control.GetChangeRequest(request.DeploymentID, request.ID)
			if err != nil {
				return nil, fmt.Errorf("could not observe Change Request %s: %w; inspect it at %s", request.ID, err, cloudRequestReviewURL(control, *request))
			}
			request = current
		}
	}
}

func deploymentPlanState(markdown string, skills []cloud.DeploymentPlanSkill) planSpecState {
	state := planSpecState{Skills: []planSkillLock{}}
	if strings.TrimSpace(markdown) != "" {
		if parsed, err := planSpecStateFromMarkdown([]byte(markdown), nil); err == nil {
			state = parsed
		}
	}
	for _, skill := range skills {
		state.Skills = append(state.Skills, planSkillLock{Name: skill.Name, Ref: skill.Ref, Digest: skill.Digest, Starred: skill.Starred})
	}
	slices.SortFunc(state.Skills, func(a, b planSkillLock) int { return strings.Compare(a.Name, b.Name) })
	return state
}

func printDeploymentPlan(out io.Writer, control *cloud.Client, request *cloud.ChangeRequestRecord) {
	printSummaryField(out, "Request", request.ID)
	printSummaryField(out, "Mode", request.Mode)
	printSummaryField(out, "Status", changeRequestStatus(request.Status))
	if request.Error != nil {
		printSummaryField(out, "Reason", *request.Error)
	}
	printSummaryField(out, "Context", control.ContextName())
	printSummaryField(out, "Session", request.DeploymentID)
	printSummaryField(out, "Review", cloudRequestReviewURL(control, *request))
	if creation := request.Creation; creation != nil {
		printSummaryField(out, "Name", creation.Name)
		if creation.AgentModel != nil {
			printSummaryField(out, "Model", *creation.AgentModel)
		}
		if creation.AgentThinking != nil {
			printSummaryField(out, "Thinking", *creation.AgentThinking)
		}
		if creation.Inference != nil {
			selection, _ := json.Marshal(creation.Inference)
			printSummaryField(out, "Inference", string(selection))
		}
		printSummaryField(out, "Secrets", firstNonEmpty(strings.Join(creation.SecretIDs, ", "), "none"))
	}
	if request.Force {
		printSummaryField(out, "Snapshot", "bypass allowed (--force)")
		fmt.Fprintln(out, "The current revision may not have a restore point when this change applies.")
	}
	if request.ExpiresAt != nil {
		printSummaryField(out, "Expires", *request.ExpiresAt)
	}
	if request.QueuePosition != nil {
		printSummaryField(out, "Queue", fmt.Sprint(*request.QueuePosition))
	}
	if request.Preview == nil {
		fmt.Fprintln(out, "Waiting for this request's turn before preparing its plan.")
		return
	}
	if request.BaseRevisionID != nil {
		printSummaryField(out, "Base", *request.BaseRevisionID)
	}
	preview := request.Preview
	printPlanStateDelta(out, deploymentPlanState(preview.BaseSpec, preview.BaseSkills), deploymentPlanState(preview.ProposedSpec, preview.ProposedSkills))
	diff := internaldiff.Diff("deployed/SPEC.md", []byte(preview.BaseSpec), "proposed/SPEC.md", []byte(preview.ProposedSpec))
	if len(diff) > 0 {
		fmt.Fprintln(out)
		fmt.Fprint(out, string(diff))
	} else {
		fmt.Fprintln(out, "No spec changes.")
	}
	if request.Mode == "preview" {
		fmt.Fprintln(out, "Preview only. Use --out=FILE to save a Change Request that can be applied.")
	}
}

func printDeploymentPlanResult(out io.Writer, control *cloud.Client, request *cloud.ChangeRequestRecord) {
	fmt.Fprintln(out)
	printSummaryField(out, "Request", request.ID)
	printSummaryField(out, "Status", changeRequestStatus(request.Status))
	if request.Error != nil {
		printSummaryField(out, "Reason", *request.Error)
	}
	if request.ResultRevisionID != nil {
		printSummaryField(out, "Revision", *request.ResultRevisionID)
	}
	printSummaryField(out, "Describe", "telos describe "+request.DeploymentID+" --context "+control.ContextName())
}

func printDeploymentPlanJSON(control *cloud.Client, request *cloud.ChangeRequestRecord, pkg *cloud.PackageVersionRecord, output string) {
	operation := request.Status
	if request.Mode == "preview" {
		operation = "preview"
	} else if !requestStarted(request) {
		operation = "requested"
	}
	receipt := map[string]any{
		"operation": operation, "context": control.ContextName(), "change_request": request,
		"review_url": cloudRequestReviewURL(control, *request), "session_id": request.DeploymentID,
	}
	if pkg != nil {
		receipt["package"] = pkg
	}
	if output != "" {
		receipt["plan_file"] = output
	}
	printJSON(receipt)
}
