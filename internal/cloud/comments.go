package cloud

import (
	"context"
	"net/http"
	"net/url"
)

// CommentAnchor locates a comment: a quoted line in a file, or a spec
// frontmatter field, whose value Cloud quotes. Side is "old" for text a
// proposal removes.
type CommentAnchor struct {
	Path   string `json:"path"`
	Field  string `json:"field,omitempty"`
	Quote  string `json:"quote,omitempty"`
	Prefix string `json:"prefix,omitempty"`
	Suffix string `json:"suffix,omitempty"`
	Side   string `json:"side,omitempty"`
}

// Comment is a comment on a Goal or a proposal, with its replies. A nil
// Anchor is a comment on the whole proposal.
type Comment struct {
	ID           string         `json:"id"`
	Goal         string         `json:"goal"`
	Proposal     *string        `json:"proposal"`
	Author       Actor          `json:"author"`
	Body         string         `json:"body"`
	Anchor       *CommentAnchor `json:"anchor"`
	Resolved     bool           `json:"resolved"`
	Outdated     bool           `json:"outdated"`
	Deleted      bool           `json:"deleted"`
	UpdateNumber *int           `json:"update_number"`
	CreatedAt    string         `json:"created_at"`
	EditedAt     *string        `json:"edited_at"`
	Replies      []Reply        `json:"replies"`
}

// Reply is a reply in a comment's thread.
type Reply struct {
	ID        string  `json:"id"`
	Author    Actor   `json:"author"`
	Body      string  `json:"body"`
	CreatedAt string  `json:"created_at"`
	EditedAt  *string `json:"edited_at"`
}

// CommentCreate comments on a Goal or on a proposal's update.
type CommentCreate struct {
	Goal         string         `json:"goal,omitempty"`
	Proposal     string         `json:"proposal,omitempty"`
	Body         string         `json:"body"`
	Anchor       *CommentAnchor `json:"anchor"`
	UpdateNumber int            `json:"update_number,omitempty"`
}

// CommentEdit changes one thing about a comment: its words, or whether its
// thread is resolved.
type CommentEdit struct {
	Body     *string `json:"body,omitempty"`
	Resolved *bool   `json:"resolved,omitempty"`
}

type commentList struct {
	Comments []Comment `json:"comments"`
}

func commentPath(id string) string {
	return "/api/comments/" + url.PathEscape(id)
}

// ListComments lists comments matching every filter: goal or proposal, and
// optionally status and author.
func (c *Client) ListComments(filter url.Values) ([]Comment, error) {
	list, err := getJSONWithRetry[commentList](context.Background(), c, withQuery("/api/comments", filter))
	if err != nil {
		return nil, err
	}
	return list.Comments, nil
}

func (c *Client) CreateComment(body CommentCreate) (*Comment, error) {
	return sendJSON[Comment](c, http.MethodPost, "/api/comments", body)
}

// EditComment edits a comment or a reply.
func (c *Client) EditComment(id string, body CommentEdit) error {
	_, err := sendJSON[struct{}](c, http.MethodPatch, commentPath(id), body)
	return err
}

// DeleteComment deletes a comment or a reply.
func (c *Client) DeleteComment(id string) error {
	_, err := sendJSON[struct{}](c, http.MethodDelete, commentPath(id), nil)
	return err
}

// ReplyToComment adds a reply to the thread of a comment or reply.
func (c *Client) ReplyToComment(id, body string) error {
	_, err := sendJSON[struct{}](c, http.MethodPost, commentPath(id)+"/replies", map[string]string{"body": body})
	return err
}
