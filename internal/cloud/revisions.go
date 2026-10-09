package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

type RevisionCapability struct {
	Allowed bool    `json:"allowed"`
	Reason  *string `json:"reason"`
}

type Revision struct {
	ID                     string  `json:"id"`
	Sequence               int     `json:"sequence"`
	Kind                   string  `json:"kind"`
	ParentRevisionID       *string `json:"parent_revision_id"`
	RestoredFromRevisionID *string `json:"restored_from_revision_id"`
	PackageRef             string  `json:"package_ref"`
	PackageDigest          string  `json:"package_digest"`
	Message                string  `json:"message"`
	CreatedBy              struct {
		Subject string  `json:"subject"`
		Name    *string `json:"name"`
	} `json:"created_by"`
	CommittedAt string `json:"committed_at"`
	Snapshot    struct {
		Status     string  `json:"status"`
		CapturedAt *string `json:"captured_at"`
	} `json:"snapshot"`
	RestoreCapability  RevisionCapability `json:"restore_capability"`
	RedeployCapability RevisionCapability `json:"redeploy_capability"`
}

type RevisionPage struct {
	CurrentRevisionID string     `json:"current_revision_id"`
	Revisions         []Revision `json:"revisions"`
	NextBefore        *int       `json:"next_before"`
}

type RevisionActionOptions struct {
	ExpectedCurrentRevisionID string `json:"expected_current_revision_id"`
	RevisionMessage           string `json:"revision_message,omitempty"`
}

type RevisionOperation struct {
	ID               string  `json:"id"`
	Kind             string  `json:"kind,omitempty"`
	Status           string  `json:"status"`
	Stage            *string `json:"stage,omitempty"`
	ResultRevisionID *string `json:"result_revision_id"`
	Error            *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func revisionPath(sessionID, revisionID string) string {
	return "/api/deployments/" + url.PathEscape(sessionID) + "/revisions/" + url.PathEscape(revisionID)
}

func (c *Client) ListSessionRevisions(ctx context.Context, sessionID string, limit, before int) (*RevisionPage, error) {
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if before > 0 {
		query.Set("before", strconv.Itoa(before))
	}
	page, err := getJSONWithRetry[RevisionPage](ctx, c,
		"/api/deployments/"+url.PathEscape(sessionID)+"/revisions?"+query.Encode())
	if err != nil {
		return nil, err
	}
	if page.CurrentRevisionID == "" || page.Revisions == nil {
		return nil, fmt.Errorf("Cloud returned invalid revision history")
	}
	previous := before
	for _, revision := range page.Revisions {
		if revision.ID == "" || revision.Sequence <= 0 || (previous > 0 && revision.Sequence >= previous) {
			return nil, fmt.Errorf("Cloud returned unordered or invalid revision history")
		}
		previous = revision.Sequence
	}
	if page.NextBefore != nil && (*page.NextBefore <= 0 || len(page.Revisions) == 0 || *page.NextBefore > previous) {
		return nil, fmt.Errorf("Cloud returned an invalid revision history cursor")
	}
	return page, nil
}

func (c *Client) GetSessionRevision(ctx context.Context, sessionID, revisionID string) (*Revision, error) {
	revision, err := getJSONWithRetry[Revision](ctx, c, revisionPath(sessionID, revisionID))
	if err != nil {
		return nil, err
	}
	if revision.ID != revisionID || revision.Sequence <= 0 {
		return nil, fmt.Errorf("Cloud returned a mismatched or invalid revision")
	}
	return revision, nil
}

func (c *Client) DownloadRevisionBundle(ctx context.Context, sessionID, revisionID string) ([]byte, error) {
	return c.downloadRevisionBundle(ctx, revisionPath(sessionID, revisionID)+"/bundle")
}

func (c *Client) DownloadRevisionSkillBundle(ctx context.Context, sessionID, revisionID, scope, name, version, digest string) ([]byte, error) {
	path := revisionPath(sessionID, revisionID) + "/skills/" + url.PathEscape(scope) + "/" + url.PathEscape(name) + "/" + url.PathEscape(version) + "/bundle"
	return c.downloadRevisionBundle(ctx, path+"?"+url.Values{"digest": {digest}}.Encode())
}

func (c *Client) downloadRevisionBundle(ctx context.Context, path string) ([]byte, error) {
	resp, err := c.doRawContext(ctx, http.MethodGet, path, nil, "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	return readBundleResponse(resp, "revision bundle")
}

func (c *Client) StartRevisionOperation(ctx context.Context, sessionID, revisionID, action string, opts RevisionActionOptions) (*RevisionOperation, error) {
	if (action != "restore" && action != "redeploy") || opts.ExpectedCurrentRevisionID == "" {
		return nil, fmt.Errorf("revision action requires restore or redeploy and an expected current revision")
	}
	body, err := json.Marshal(opts)
	if err != nil {
		return nil, err
	}
	// Mutations are sent once. A lost response does not establish rejection.
	resp, err := c.doRawContext(ctx, http.MethodPost, revisionPath(sessionID, revisionID)+"/"+action, body, "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var operation RevisionOperation
	if err := json.NewDecoder(resp.Body).Decode(&operation); err != nil {
		return nil, err
	}
	if err := validateRevisionOperation(&operation); err != nil {
		return nil, err
	}
	if operation.Kind != "" && operation.Kind != action {
		return nil, fmt.Errorf("Cloud returned a different operation kind")
	}
	return &operation, nil
}

func (c *Client) GetRevisionOperation(ctx context.Context, sessionID, operationID string) (*RevisionOperation, error) {
	operation, err := getJSONWithRetry[RevisionOperation](ctx, c,
		"/api/deployments/"+url.PathEscape(sessionID)+"/operations/"+url.PathEscape(operationID))
	if err != nil {
		return nil, err
	}
	if operation.ID != operationID {
		return nil, fmt.Errorf("Cloud returned a different operation")
	}
	return operation, validateRevisionOperation(operation)
}

func validateRevisionOperation(operation *RevisionOperation) error {
	if operation.ID == "" {
		return fmt.Errorf("Cloud returned no operation ID")
	}
	switch operation.Status {
	case "pending", "running", "succeeded", "failed":
		return nil
	default:
		return fmt.Errorf("Cloud returned unknown operation status %q", operation.Status)
	}
}
