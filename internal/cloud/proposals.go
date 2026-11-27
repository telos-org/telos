package cloud

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// Actor is a person who wrote, reviewed, or blocked something.
type Actor struct {
	Subject string `json:"subject"`
	Name    string `json:"name,omitempty"`
	Email   string `json:"email,omitempty"`
}

// Label names the actor the way the CLI shows people: email, then name.
func (a Actor) Label() string {
	return cmp.Or(a.Email, a.Name, a.Subject)
}

// Proposal is a proposed change to a Goal.
type Proposal struct {
	ID           string  `json:"id"`
	Goal         string  `json:"goal"`
	GoalName     string  `json:"goal_name"`
	Status       string  `json:"status"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	Author       Actor   `json:"author"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	UpdateNumber int     `json:"update_number"`
	ReviewState  *string `json:"review_state"`
	MyReview     *string `json:"my_review"`
	BlockedBy    []Actor `json:"blocked_by"`
	// Error and ErrorCode say why the last apply didn't finish.
	Error     string           `json:"error"`
	ErrorCode string           `json:"error_code"`
	URL       string           `json:"url"`
	Preview   *ProposalPreview `json:"preview,omitempty"`
	// Action is "update", or "redeploy" or "restore" of SourceRevisionID.
	Action           string `json:"action"`
	SourceRevisionID string `json:"source_revision_id"`

	raw json.RawMessage
}

// UnmarshalJSON keeps Cloud's whole record, so --json prints every field.
func (p *Proposal) UnmarshalJSON(data []byte) error {
	type fields Proposal
	if err := json.Unmarshal(data, (*fields)(p)); err != nil {
		return err
	}
	p.raw = append(json.RawMessage(nil), data...)
	return nil
}

// MarshalJSON writes the record as Cloud sent it.
func (p Proposal) MarshalJSON() ([]byte, error) {
	if p.raw != nil {
		return p.raw, nil
	}
	type fields Proposal
	return json.Marshal(fields(p))
}

// ProposalPreview holds the text a proposal changes: the spec, and each
// changed skill file.
type ProposalPreview struct {
	BaseSpec      *string        `json:"base_spec"`
	ProposedSpec  string         `json:"proposed_spec"`
	Files         []ProposalFile `json:"files"`
	SkillWarnings []struct {
		Name   string `json:"name"`
		Reason string `json:"reason"`
	} `json:"skill_warnings"`
}

// ProposalFile is one changed skill file. Text is nil on the side where the
// file doesn't exist or wasn't shown.
type ProposalFile struct {
	Path         string  `json:"path"`
	BaseText     *string `json:"base_text"`
	ProposedText *string `json:"proposed_text"`
	// OmittedReason is "binary" or "too_large" for a file shown without text.
	OmittedReason string `json:"omitted_reason"`
}

// ProposalReview is one reviewer's verdict on one update; a nil verdict takes
// a review back.
type ProposalReview struct {
	Reviewer     Actor   `json:"reviewer"`
	UpdateNumber int     `json:"update_number"`
	Verdict      *string `json:"verdict"`
	CreatedAt    string  `json:"created_at"`
}

// ProposalHistory is what happened to a proposal, oldest first.
type ProposalHistory struct {
	Reviews []ProposalReview `json:"reviews"`
}

// ProposalCreate opens a proposal on a Goal with a pushed package.
type ProposalCreate struct {
	Goal        string `json:"goal"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	PackageRef  string `json:"package_ref"`
}

// ProposalEdit renames a proposal or changes its description.
type ProposalEdit struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
}

// ProposalUpdate adds an update, and may rename the proposal or change its
// description; ExpectedUpdateNumber guards against a concurrent update.
type ProposalUpdate struct {
	ExpectedUpdateNumber int     `json:"expected_update_number"`
	Message              string  `json:"message"`
	PackageRef           string  `json:"package_ref"`
	Title                *string `json:"title,omitempty"`
	Description          *string `json:"description,omitempty"`
}

type proposalList struct {
	Proposals  []Proposal `json:"proposals"`
	NextCursor *int       `json:"next_cursor"`
}

func proposalPath(id string, rest ...string) string {
	path := "/api/proposals/" + url.PathEscape(id)
	for _, part := range rest {
		path += "/" + part
	}
	return path
}

// maxProposalPage is the most proposals Cloud returns in one page.
const maxProposalPage = 200

// ListProposals lists proposals matching every filter (goal, status, author),
// newest first: limit of them, or all when limit is 0.
func (c *Client) ListProposals(filter url.Values, limit int) ([]Proposal, error) {
	query := url.Values{}
	for key, values := range filter {
		query[key] = values
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(min(limit, maxProposalPage)))
	}
	var proposals []Proposal
	for {
		page, err := getJSONWithRetry[proposalList](context.Background(), c, withQuery("/api/proposals", query))
		if err != nil {
			return nil, err
		}
		proposals = append(proposals, page.Proposals...)
		if page.NextCursor == nil || (limit > 0 && len(proposals) >= limit) {
			return proposals, nil
		}
		query.Set("cursor", strconv.Itoa(*page.NextCursor))
	}
}

func (c *Client) GetProposal(id string) (*Proposal, error) {
	return getJSONWithRetry[Proposal](context.Background(), c, proposalPath(id))
}

func (c *Client) CreateProposal(body ProposalCreate) (*Proposal, error) {
	return sendJSON[Proposal](c, http.MethodPost, "/api/proposals", body)
}

func (c *Client) EditProposal(id string, body ProposalEdit) (*Proposal, error) {
	return sendJSON[Proposal](c, http.MethodPatch, proposalPath(id), body)
}

func (c *Client) UpdateProposal(id string, body ProposalUpdate) (*Proposal, error) {
	return sendJSON[Proposal](c, http.MethodPost, proposalPath(id, "updates"), body)
}

// CloseProposal closes a proposal without deploying it.
func (c *Client) CloseProposal(id string) (*Proposal, error) {
	return sendJSON[Proposal](c, http.MethodDelete, proposalPath(id), nil)
}

// ReviewProposal records the caller's verdict on an update: "approved",
// "blocked", or nil to take a review back.
func (c *Client) ReviewProposal(id string, updateNumber int, verdict *string) (*Proposal, error) {
	body := map[string]any{"update_number": updateNumber, "verdict": verdict}
	return sendJSON[Proposal](c, http.MethodPut, proposalPath(id, "review"), body)
}

// ApplyProposal deploys a proposal's latest update; force skips waiting for a
// snapshot of the Goal's workspace.
func (c *Client) ApplyProposal(id string, force bool) (*Proposal, error) {
	return sendJSON[Proposal](c, http.MethodPost, proposalPath(id, "apply"), map[string]bool{"force": force})
}

func (c *Client) ProposalHistory(id string) (*ProposalHistory, error) {
	return getJSONWithRetry[ProposalHistory](context.Background(), c, proposalPath(id, "history"))
}

// DownloadProposalBundle returns the apply package of the Goal as it would be
// with the proposal applied, and the package digest Cloud recorded for it.
func (c *Client) DownloadProposalBundle(id string) ([]byte, string, error) {
	resp, err := c.downloadBundle(proposalPath(id, "bundle"))
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	data, err := readBundleResponse(resp, "proposal bundle")
	return data, resp.Header.Get("X-Telos-Package-Digest"), err
}

// DownloadProposalSkillBundle returns a skill bundle locked by a proposal's
// package, including skills that exist only in the proposal.
func (c *Client) DownloadProposalSkillBundle(id, scope, name, version, digest string) ([]byte, error) {
	path := proposalPath(id, "skills", url.PathEscape(scope), url.PathEscape(name), url.PathEscape(version), "bundle")
	resp, err := c.downloadBundle(withQuery(path, url.Values{"digest": {digest}}))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return readBundleResponse(resp, "skill bundle")
}

// StagePackage stores an apply package privately for a proposal, at a version
// Cloud derives from its content, without publishing a Registry version.
func (c *Client) StagePackage(name string, data []byte) (*PackageVersionRecord, error) {
	body := map[string]string{"name": name, "data_base64": base64.StdEncoding.EncodeToString(data)}
	return sendJSON[PackageVersionRecord](c, http.MethodPost, "/api/staging/packages", body)
}

// StageSkill stores a skill privately for a proposal, as StagePackage does.
func (c *Client) StageSkill(name string, files map[string]SkillFile) (*SkillRecord, error) {
	body := map[string]any{"name": name, "files": skillFilesRequest(files)}
	return sendJSON[SkillRecord](c, http.MethodPost, "/api/staging/skills", body)
}
