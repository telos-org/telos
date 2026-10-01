package cloud

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

type MergeFile struct {
	Content    *string `json:"content"`
	DataBase64 *string `json:"data_base64"`
	Mode       string  `json:"mode"`
}

type RequestMergeFile struct {
	Path        string     `json:"path"`
	Kind        string     `json:"kind,omitempty"`
	Base        *MergeFile `json:"base"`
	Current     *MergeFile `json:"current"`
	Proposed    *MergeFile `json:"proposed"`
	Merged      *MergeFile `json:"merged"`
	ConflictIDs []string   `json:"conflict_ids"`
}

type MergeConflict struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type RequestMerge struct {
	MergeID           string             `json:"merge_id"`
	RequestID         string             `json:"request_id"`
	UpdateNumber      int                `json:"update_number"`
	PreparedPlanID    string             `json:"prepared_plan_id"`
	BaseRevisionID    *string            `json:"base_revision_id"`
	CurrentRevisionID *string            `json:"current_revision_id"`
	Files             []RequestMergeFile `json:"files"`
	Conflicts         []MergeConflict    `json:"conflicts"`
}

type MergeResolution struct {
	ConflictID string  `json:"conflict_id"`
	Choice     string  `json:"choice"`
	Content    *string `json:"content"`
}

type MergeFileEdit struct {
	Path    string  `json:"path"`
	Content *string `json:"content"`
	Mode    string  `json:"mode,omitempty"`
}

type RequestMergeOptions struct {
	ExpectedUpdateNumber      int               `json:"expected_update_number"`
	ExpectedCurrentRevisionID *string           `json:"expected_current_revision_id"`
	MergeID                   string            `json:"merge_id"`
	Resolutions               []MergeResolution `json:"resolutions"`
	Files                     []MergeFileEdit   `json:"files,omitempty"`
	RevisionMessage           string            `json:"revision_message,omitempty"`
}

func (c *Client) PrepareRequestMerge(request ChangeRequestRecord) (*RequestMerge, error) {
	query := url.Values{"expected_update_number": {strconv.Itoa(request.UpdateNumber)}}
	if request.CurrentRevisionID != nil {
		query.Set("expected_current_revision_id", *request.CurrentRevisionID)
	}
	resp, err := c.do(http.MethodGet, changeRequestPath(request.DeploymentID, request.ID)+"/merge?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var merge RequestMerge
	if err := json.NewDecoder(resp.Body).Decode(&merge); err != nil {
		return nil, err
	}
	if merge.MergeID == "" || merge.RequestID != request.ID || merge.UpdateNumber != request.UpdateNumber {
		return nil, fmt.Errorf("Cloud returned a different request merge; nothing was saved")
	}
	return &merge, nil
}

func (c *Client) ResolveRequestMerge(request ChangeRequestRecord, options RequestMergeOptions) (*ChangeRequestRecord, error) {
	body, err := json.Marshal(options)
	if err != nil {
		return nil, err
	}
	return c.deploymentPlanRequest(http.MethodPost, changeRequestPath(request.DeploymentID, request.ID)+"/merge", body)
}
