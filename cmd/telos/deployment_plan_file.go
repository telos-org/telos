package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/telos-org/telos/internal/cloud"
)

const savedPlanVersion = 2

// A bookmark identifies a server-owned proposal, never credentials or executable
// inputs. The configured endpoint/context must match before it can be used.
type savedDeploymentPlan struct {
	UpdateNumber    int    `json:"update_number,omitempty"`
	PreparedPlanID  string `json:"prepared_plan_id,omitempty"`
	Version         int    `json:"version"`
	ChangeRequestID string `json:"change_request_id,omitempty"`
	PlanID          string `json:"plan_id,omitempty"`
	DeploymentID    string `json:"deployment_id"`
	Context         string `json:"context"`
	OrgID           string `json:"org_id"`
	APIEndpoint     string `json:"api_endpoint"`
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
	bookmark := savedDeploymentPlan{
		Version: savedPlanVersion, DeploymentID: request.DeploymentID,
		Context: control.ContextName(), OrgID: orgID, APIEndpoint: control.Endpoint,
	}
	if request.Kind == "plan" {
		bookmark.PlanID = request.ID
	} else {
		bookmark.ChangeRequestID = request.ID
		bookmark.UpdateNumber = request.UpdateNumber
		bookmark.PreparedPlanID = request.PreparedPlanID
		// Older servers have immutable requests and only understand v1 files.
		if request.PreparedPlanID == "" {
			bookmark.Version = 1
		}
	}
	return bookmark
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
	if bookmark.Version != 1 && bookmark.Version != savedPlanVersion {
		return nil, fmt.Errorf("unsupported saved plan format %d; update the Telos CLI or create a new saved plan", bookmark.Version)
	}
	if (bookmark.ChangeRequestID == "") == (bookmark.PlanID == "") || bookmark.DeploymentID == "" || bookmark.Context == "" || bookmark.OrgID == "" || bookmark.APIEndpoint == "" {
		return nil, fmt.Errorf("invalid saved plan file: one plan or request ID, deployment, context, organization, and API endpoint are required")
	}
	if bookmark.Version >= 2 && bookmark.ChangeRequestID != "" && (bookmark.PreparedPlanID == "" || bookmark.UpdateNumber < 1) {
		return nil, fmt.Errorf("invalid saved plan file: an exact prepared plan and update number are required")
	}
	return &bookmark, nil
}

func validateSavedRequest(bookmark *savedDeploymentPlan, request *cloud.ChangeRequestRecord) error {
	if bookmark.ChangeRequestID != "" {
		if bookmark.Version == 1 {
			if request.LegacyConfirmationValid != nil && !*request.LegacyConfirmationValid {
				return fmt.Errorf("this saved file refers to the original plan of %s, which was replaced; review the request and save a new plan", request.ID)
			}
		} else if bookmark.PreparedPlanID != request.PreparedPlanID || bookmark.UpdateNumber != request.UpdateNumber {
			return fmt.Errorf("%s Update %d's saved plan was replaced; review Update %d and save a new plan", request.ID, bookmark.UpdateNumber, request.UpdateNumber)
		}
	}
	if request.PlanStale {
		if request.Kind == "plan" {
			return fmt.Errorf("the deployment changed after this plan was prepared; nothing was applied; prepare a fresh plan against Goal %s", request.DeploymentID)
		}
		return fmt.Errorf("the deployment changed after this plan was prepared; nothing was applied; review and reconcile request %s in the dashboard before confirming its new plan", request.ID)
	}
	return nil
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
