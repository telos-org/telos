package cloud

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/telos-org/telos/internal/activity"
)

type ActivityPositions struct {
	Runtime  *int64  `json:"rt"`
	Control  *int64  `json:"cp"`
	Session  *string `json:"session"`
	Snapshot bool    `json:"snapshot"`
}
type ActivityCoverage struct {
	Runtime int64 `json:"rt"`
	Control int64 `json:"cp"`
}

type ActivityPage struct {
	Entries         []activity.Entry  `json:"entries"`
	RuntimeSnapshot []activity.Entry  `json:"runtime_snapshot"`
	NextCursor      *string           `json:"next_cursor"`
	History         string            `json:"history"`
	Cursors         ActivityPositions `json:"cursors"`
	Coverage        ActivityCoverage  `json:"coverage"`
}

type ActivityPageOptions struct {
	Limit        int
	Before       string
	AfterRuntime *int64
	AfterControl *int64
	Session      string
}

func (c *Client) ActivityPage(ctx context.Context, id string, options ActivityPageOptions) (*ActivityPage, error) {
	if options.Before != "" && (options.AfterRuntime != nil || options.AfterControl != nil || options.Session != "") {
		return nil, fmt.Errorf("activity continuation cannot be combined with recovery bounds")
	}
	query := url.Values{"limit": {strconv.Itoa(options.Limit)}}
	if options.Session != "" {
		query.Set("runtime_session", options.Session)
	}
	if options.Before != "" {
		query.Set("before", options.Before)
	}
	if options.AfterRuntime != nil {
		query.Set("after_rt", strconv.FormatInt(*options.AfterRuntime, 10))
	}
	if options.AfterControl != nil {
		query.Set("after_cp", strconv.FormatInt(*options.AfterControl, 10))
	}
	// Legacy adapters can scan several bounded source pages.
	client := *c
	transport := *c.HTTP
	transport.Timeout = 135 * time.Second
	client.HTTP = &transport
	page, err := getJSONWithRetry[ActivityPage](ctx, &client, "/api/deployments/"+url.PathEscape(id)+"/activity?"+query.Encode())
	if err == nil && page.History != "paged" && page.History != "recent" {
		return nil, fmt.Errorf("invalid activity response")
	}
	return page, err
}

type ActivityFrame struct {
	Type         string           `json:"type"`
	Session      string           `json:"session"`
	Sequence     int64            `json:"sequence"`
	CoveredAfter int64            `json:"covered_after"`
	Events       []activity.Entry `json:"events"`
	Snapshot     bool             `json:"snapshot"`
	Initializing bool             `json:"initializing"`
	Topics       []string         `json:"topics"`
	Allowed      bool             `json:"allowed"`
}

// FollowActivity shares the goal live transport with web; the caller owns
// reconnection and recovery so a dropped stream never advances its bookmark.
func (c *Client) FollowActivity(ctx context.Context, id, session string, after *int64, accept func(ActivityFrame) error) error {
	query := url.Values{"deployment_id": {id}, "view": {"activity"}}
	if session != "" {
		query.Set("runtime_session", session)
	}
	if after != nil {
		query.Set("after_rt", strconv.FormatInt(*after, 10))
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.Endpoint+"/api/live?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", UserAgent)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.OrgID != "" {
		req.Header.Set("X-Telos-Org-Id", c.OrgID)
	}
	client := *c.HTTP
	client.Timeout = 0
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return readError(response)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 5*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var frame ActivityFrame
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &frame); err != nil {
			return fmt.Errorf("decode goal activity: %w", err)
		}
		if err := accept(frame); err != nil {
			return err
		}
	}
	return scanner.Err()
}
