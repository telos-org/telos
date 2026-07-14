package cloud

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Browser-login handshake against /api/cli/auth/*. Start and Poll run before
// the CLI has a credential, so they send no Authorization header. The token
// can only be claimed with the poll secret, which never leaves this process.

type CLIAuthStart struct {
	RequestID       string `json:"request_id"`
	UserCode        string `json:"user_code"`
	PollSecret      string `json:"poll_secret"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type CLIAuthPoll struct {
	Status   string `json:"status"`
	Token    string `json:"token"`
	Interval int    `json:"interval"`
}

// preauthClient makes requests on a fresh connection each time: polls are
// spaced at about the server's keep-alive idle timeout, so a reused
// connection races the server-side close and surfaces as a reset.
func preauthClient(endpoint string) *Client {
	return &Client{
		Endpoint: NormalizeEndpoint(endpoint),
		HTTP: &http.Client{
			Timeout:   DefaultTimeout,
			Transport: &http.Transport{DisableKeepAlives: true},
		},
	}
}

func StartCLIAuth(endpoint, clientName string) (*CLIAuthStart, error) {
	body, err := json.Marshal(map[string]string{"client_name": clientName})
	if err != nil {
		return nil, err
	}
	resp, err := preauthClient(endpoint).do("POST", "/api/cli/auth/start", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var start CLIAuthStart
	if err := json.NewDecoder(resp.Body).Decode(&start); err != nil {
		return nil, err
	}
	return &start, nil
}

func PollCLIAuth(endpoint, requestID, pollSecret string) (*CLIAuthPoll, error) {
	body, err := json.Marshal(map[string]string{
		"request_id":  requestID,
		"poll_secret": pollSecret,
	})
	if err != nil {
		return nil, err
	}
	resp, err := preauthClient(endpoint).do("POST", "/api/cli/auth/poll", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var poll CLIAuthPoll
	if err := json.NewDecoder(resp.Body).Decode(&poll); err != nil {
		return nil, err
	}
	return &poll, nil
}

type MeRecord struct {
	Subject string  `json:"subject"`
	Email   *string `json:"email,omitempty"`
	Name    *string `json:"name,omitempty"`
}

func (c *Client) Me() (*MeRecord, error) {
	resp, err := c.do("GET", "/api/me", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var me MeRecord
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		return nil, err
	}
	return &me, nil
}

// APITokenID extracts the token id embedded in a telos_pat_<id>.<secret>
// token, or "" if the token has another shape (legacy shared secrets).
func APITokenID(token string) string {
	const prefix = "telos_pat_"
	if !strings.HasPrefix(token, prefix) {
		return ""
	}
	id, _, found := strings.Cut(strings.TrimPrefix(token, prefix), ".")
	if !found || !strings.HasPrefix(id, "tok_") {
		return ""
	}
	return id
}

func (c *Client) RevokeAPIToken(tokenID string) error {
	resp, err := c.do("DELETE", "/api/api-tokens/"+tokenID, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readError(resp)
	}
	return nil
}
