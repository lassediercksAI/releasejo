// Package forge is a minimal Forgejo/Gitea REST client covering exactly what
// release automation needs: read commits, read/write files on a branch, manage
// the release pull request, and cut releases. Stdlib-only (net/http).
//
// The Gitea/Forgejo API is REST (no GraphQL), so nothing here depends on the
// GitHub-specific machinery that makes upstream release-please GitHub-only.
package forge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to one Forgejo/Gitea instance.
type Client struct {
	base  string // e.g. https://code.exaptation.company
	token string
	owner string
	repo  string
	http  *http.Client
}

// New builds a client. apiBase is the instance root (with or without a trailing
// "/api/v1"); token is a user PAT with contents+PR write.
func New(apiBase, token, owner, repo string) *Client {
	base := strings.TrimSuffix(strings.TrimRight(apiBase, "/"), "/api/v1")
	return &Client{
		base:  base,
		token: token,
		owner: owner,
		repo:  repo,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) urlf(format string, a ...any) string {
	return c.base + "/api/v1" + fmt.Sprintf(format, a...)
}

// do issues a request, JSON-encoding body (if non-nil) and decoding into out
// (if non-nil). Non-2xx responses become errors carrying the status + payload.
func (c *Client) do(ctx context.Context, method, u string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Method: method, URL: u, Status: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("forge: decoding %s %s: %w", method, u, err)
		}
	}
	return nil
}

// APIError is a non-2xx response.
type APIError struct {
	Method, URL, Body string
	Status            int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("forge: %s %s -> %d: %s", e.Method, e.URL, e.Status, e.Body)
}

// NotFound reports whether err is a 404 from this client.
func NotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// ---- repo -----------------------------------------------------------------

type Repo struct {
	DefaultBranch string `json:"default_branch"`
}

func (c *Client) Repo(ctx context.Context) (*Repo, error) {
	var r Repo
	err := c.do(ctx, "GET", c.urlf("/repos/%s/%s", c.owner, c.repo), nil, &r)
	return &r, err
}

// ---- commits --------------------------------------------------------------

// Commit is the subset we need.
type Commit struct {
	SHA     string
	Message string
}

type giteaCommit struct {
	SHA         string `json:"sha"`
	CommitInner struct {
		Message string `json:"message"`
	} `json:"commit"`
}

// Commits lists commits reachable from `sha` (a branch or commit), optionally
// filtered to those touching `path`, newest first, following pagination up to
// `max` results.
func (c *Client) Commits(ctx context.Context, sha, path string, max int) ([]Commit, error) {
	var out []Commit
	page := 1
	for len(out) < max {
		q := url.Values{}
		q.Set("sha", sha)
		q.Set("limit", "50")
		q.Set("page", fmt.Sprint(page))
		if path != "" && path != "." {
			q.Set("path", path)
		}
		var batch []giteaCommit
		u := c.urlf("/repos/%s/%s/commits?%s", c.owner, c.repo, q.Encode())
		if err := c.do(ctx, "GET", u, nil, &batch); err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		for _, gc := range batch {
			out = append(out, Commit{SHA: gc.SHA, Message: gc.CommitInner.Message})
			if len(out) >= max {
				break
			}
		}
		if len(batch) < 50 {
			break
		}
		page++
	}
	return out, nil
}

// ---- file contents --------------------------------------------------------

// File is a repo file's decoded content plus its blob SHA (needed to update it).
type File struct {
	Content string
	SHA     string
}

type contentsResp struct {
	Content string `json:"content"` // base64
	SHA     string `json:"sha"`
}

// GetFile fetches a file at ref. Returns forge.NotFound(err)==true if absent.
func (c *Client) GetFile(ctx context.Context, path, ref string) (*File, error) {
	q := url.Values{}
	if ref != "" {
		q.Set("ref", ref)
	}
	var cr contentsResp
	u := c.urlf("/repos/%s/%s/contents/%s?%s", c.owner, c.repo, path, q.Encode())
	if err := c.do(ctx, "GET", u, nil, &cr); err != nil {
		return nil, err
	}
	dec, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(cr.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("forge: decoding %s: %w", path, err)
	}
	return &File{Content: string(dec), SHA: cr.SHA}, nil
}

// PutFile creates or updates a file on branch. Pass prevSHA to update an
// existing file (empty to create).
func (c *Client) PutFile(ctx context.Context, path, branch, content, prevSHA, message string) error {
	body := map[string]any{
		"branch":  branch,
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
		"message": message,
	}
	// Forgejo/Gitea splits create vs update: POST /contents creates a new file
	// (no sha), PUT /contents updates an existing one (sha REQUIRED — it 422s
	// with "[SHA]: Required" otherwise). Pick the verb by whether the file was
	// already present on the branch (prevSHA is its blob sha, "" when absent).
	method := "POST"
	if prevSHA != "" {
		method = "PUT"
		body["sha"] = prevSHA
	}
	return c.do(ctx, method, c.urlf("/repos/%s/%s/contents/%s", c.owner, c.repo, path), body, nil)
}

// ---- branches -------------------------------------------------------------

func (c *Client) BranchExists(ctx context.Context, name string) (bool, error) {
	err := c.do(ctx, "GET", c.urlf("/repos/%s/%s/branches/%s", c.owner, c.repo, name), nil, nil)
	if err == nil {
		return true, nil
	}
	if NotFound(err) {
		return false, nil
	}
	return false, err
}

// CreateBranch branches `name` off `from`.
func (c *Client) CreateBranch(ctx context.Context, name, from string) error {
	body := map[string]string{"new_branch_name": name, "old_branch_name": from}
	return c.do(ctx, "POST", c.urlf("/repos/%s/%s/branches", c.owner, c.repo), body, nil)
}

// DeleteBranch removes a branch (used to reset a stale release branch).
func (c *Client) DeleteBranch(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", c.urlf("/repos/%s/%s/branches/%s", c.owner, c.repo, name), nil, nil)
}

// ---- pull requests --------------------------------------------------------

type Pull struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	Head           struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Labels []Label `json:"labels"`
}

type Label struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Pulls lists PRs in the given state ("open","closed","all").
func (c *Client) Pulls(ctx context.Context, state string) ([]Pull, error) {
	var out []Pull
	q := url.Values{}
	q.Set("state", state)
	q.Set("limit", "50")
	err := c.do(ctx, "GET", c.urlf("/repos/%s/%s/pulls?%s", c.owner, c.repo, q.Encode()), nil, &out)
	return out, err
}

func (c *Client) CreatePull(ctx context.Context, head, base, title, body string) (*Pull, error) {
	req := map[string]any{"head": head, "base": base, "title": title, "body": body}
	var p Pull
	err := c.do(ctx, "POST", c.urlf("/repos/%s/%s/pulls", c.owner, c.repo), req, &p)
	return &p, err
}

func (c *Client) EditPull(ctx context.Context, number int, title, body string) error {
	req := map[string]any{"title": title, "body": body}
	return c.do(ctx, "PATCH", c.urlf("/repos/%s/%s/pulls/%d", c.owner, c.repo, number), req, nil)
}

// ---- labels ---------------------------------------------------------------

// EnsureLabel returns the id of a repo label, creating it if missing.
func (c *Client) EnsureLabel(ctx context.Context, name, color string) (int64, error) {
	var labels []Label
	if err := c.do(ctx, "GET", c.urlf("/repos/%s/%s/labels?limit=100", c.owner, c.repo), nil, &labels); err != nil {
		return 0, err
	}
	for _, l := range labels {
		if l.Name == name {
			return l.ID, nil
		}
	}
	var created Label
	body := map[string]string{"name": name, "color": color}
	if err := c.do(ctx, "POST", c.urlf("/repos/%s/%s/labels", c.owner, c.repo), body, &created); err != nil {
		return 0, err
	}
	return created.ID, nil
}

// SetIssueLabels replaces the labels on an issue/PR (PRs share the issue index).
func (c *Client) SetIssueLabels(ctx context.Context, number int, ids []int64) error {
	body := map[string]any{"labels": ids}
	return c.do(ctx, "PUT", c.urlf("/repos/%s/%s/issues/%d/labels", c.owner, c.repo, number), body, nil)
}

// ---- releases -------------------------------------------------------------

// TagCommit returns the commit SHA a tag points at, or "" if the tag doesn't
// exist. Used to bound "commits since the last release".
func (c *Client) TagCommit(ctx context.Context, tag string) (string, error) {
	var t struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	err := c.do(ctx, "GET", c.urlf("/repos/%s/%s/tags/%s", c.owner, c.repo, url.PathEscape(tag)), nil, &t)
	if NotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return t.Commit.SHA, nil
}

// CreateRelease cuts a release, creating the tag from target if it doesn't exist.
func (c *Client) CreateRelease(ctx context.Context, tag, target, name, body string, prerelease bool) error {
	req := map[string]any{
		"tag_name":         tag,
		"target_commitish": target,
		"name":             name,
		"body":             body,
		"draft":            false,
		"prerelease":       prerelease,
	}
	return c.do(ctx, "POST", c.urlf("/repos/%s/%s/releases", c.owner, c.repo), req, nil)
}
