package forge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient wires a Client to an httptest server and returns both.
func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL, "test-token", "own", "rep")
}

// wantAuth asserts the token header is present on every request.
func wantAuth(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "token test-token" {
		t.Errorf("%s %s: Authorization = %q, want %q", r.Method, r.URL.Path, got, "token test-token")
	}
}

func TestBaseURLNormalisation(t *testing.T) {
	// New must strip a trailing /api/v1 so callers can pass either form.
	for _, in := range []string{"https://code.example.com", "https://code.example.com/", "https://code.example.com/api/v1"} {
		c := New(in, "t", "o", "r")
		if got := c.urlf("/x"); got != "https://code.example.com/api/v1/x" {
			t.Errorf("New(%q).urlf = %q", in, got)
		}
	}
}

func TestRepoAndAuth(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantAuth(t, r)
		if r.URL.Path != "/api/v1/repos/own/rep" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]string{"default_branch": "main"})
	}))
	repo, err := c.Repo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if repo.DefaultBranch != "main" {
		t.Errorf("default_branch = %q", repo.DefaultBranch)
	}
}

func TestCommitsPaginate(t *testing.T) {
	// Two pages of 50 + a short final page; client should stop when a page < 50.
	page := 0
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantAuth(t, r)
		if got := r.URL.Query().Get("path"); got != "services/api" {
			t.Errorf("path filter = %q", got)
		}
		page++
		n := 50
		if page == 2 {
			n = 3 // short page -> loop ends
		}
		var out []map[string]any
		for i := 0; i < n; i++ {
			out = append(out, map[string]any{"sha": "s", "commit": map[string]string{"message": "feat: x"}})
		}
		json.NewEncoder(w).Encode(out)
	}))
	got, err := c.Commits(context.Background(), "main", "services/api", 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 53 {
		t.Errorf("expected 53 commits across 2 pages, got %d", len(got))
	}
	if got[0].Message != "feat: x" {
		t.Errorf("message not parsed: %q", got[0].Message)
	}
}

func TestGetFileDecodeAndNotFound(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "missing") {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		enc := base64.StdEncoding.EncodeToString([]byte("hello\nworld"))
		json.NewEncoder(w).Encode(map[string]string{"content": enc, "sha": "blob1"})
	}))
	f, err := c.GetFile(context.Background(), "README.md", "main")
	if err != nil {
		t.Fatal(err)
	}
	if f.Content != "hello\nworld" || f.SHA != "blob1" {
		t.Errorf("decoded file = %q / %q", f.Content, f.SHA)
	}
	_, err = c.GetFile(context.Background(), "missing.txt", "main")
	if !NotFound(err) {
		t.Errorf("expected NotFound, got %v", err)
	}
}

func TestPutFileCreateVsUpdate(t *testing.T) {
	var bodies []map[string]any
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantAuth(t, r)
		if r.Method != "PUT" {
			t.Errorf("method = %s", r.Method)
		}
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		w.WriteHeader(http.StatusOK)
	}))
	// create: no prevSHA -> no "sha" key
	if err := c.PutFile(context.Background(), "a.txt", "br", "content", "", "msg"); err != nil {
		t.Fatal(err)
	}
	// update: prevSHA present -> "sha" key set
	if err := c.PutFile(context.Background(), "a.txt", "br", "content2", "oldsha", "msg"); err != nil {
		t.Fatal(err)
	}
	if _, ok := bodies[0]["sha"]; ok {
		t.Error("create should not send sha")
	}
	if bodies[1]["sha"] != "oldsha" {
		t.Errorf("update should send prev sha, got %v", bodies[1]["sha"])
	}
	// content must be base64 of the input
	if bodies[0]["content"] != base64.StdEncoding.EncodeToString([]byte("content")) {
		t.Error("content not base64-encoded")
	}
}

func TestBranchExists(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/gone") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	if ok, err := c.BranchExists(context.Background(), "here"); err != nil || !ok {
		t.Errorf("here: ok=%v err=%v", ok, err)
	}
	if ok, err := c.BranchExists(context.Background(), "gone"); err != nil || ok {
		t.Errorf("gone: ok=%v err=%v (want false,nil)", ok, err)
	}
}

func TestTagCommit(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "v9.9.9") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": "deadbeef"}})
	}))
	sha, err := c.TagCommit(context.Background(), "v1.0.0")
	if err != nil || sha != "deadbeef" {
		t.Errorf("existing tag: sha=%q err=%v", sha, err)
	}
	sha, err = c.TagCommit(context.Background(), "v9.9.9")
	if err != nil || sha != "" {
		t.Errorf("missing tag should be empty,nil: sha=%q err=%v", sha, err)
	}
}

func TestPullsAndCreate(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pulls"):
			if r.URL.Query().Get("state") != "open" {
				t.Errorf("state = %q", r.URL.Query().Get("state"))
			}
			w.Write([]byte(`[{"number":7,"state":"open","head":{"ref":"releasejo--branches--main"},"labels":[{"id":1,"name":"autorelease: pending"}]}]`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pulls"):
			w.Write([]byte(`{"number":8,"state":"open"}`))
		}
	}))
	ps, err := c.Pulls(context.Background(), "open")
	if err != nil || len(ps) != 1 || ps[0].Number != 7 || ps[0].Head.Ref != "releasejo--branches--main" {
		t.Fatalf("pulls = %+v err=%v", ps, err)
	}
	if ps[0].Labels[0].Name != "autorelease: pending" {
		t.Errorf("label not parsed: %+v", ps[0].Labels)
	}
	pr, err := c.CreatePull(context.Background(), "h", "b", "t", "body")
	if err != nil || pr.Number != 8 {
		t.Errorf("create pull = %+v err=%v", pr, err)
	}
}

func TestEnsureLabelFindsThenCreates(t *testing.T) {
	existing := true
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			if existing {
				w.Write([]byte(`[{"id":42,"name":"autorelease: pending"}]`))
			} else {
				w.Write([]byte(`[]`))
			}
			return
		}
		// POST create
		w.Write([]byte(`{"id":99,"name":"autorelease: pending"}`))
	}))
	id, err := c.EnsureLabel(context.Background(), "autorelease: pending", "ededed")
	if err != nil || id != 42 {
		t.Errorf("existing label id = %d err=%v (want 42)", id, err)
	}
	existing = false
	id, err = c.EnsureLabel(context.Background(), "autorelease: pending", "ededed")
	if err != nil || id != 99 {
		t.Errorf("created label id = %d err=%v (want 99)", id, err)
	}
}

func TestCreateReleasePayload(t *testing.T) {
	var body map[string]any
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantAuth(t, r)
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
	}))
	if err := c.CreateRelease(context.Background(), "v1.2.3", "main", "v1.2.3", "notes", false); err != nil {
		t.Fatal(err)
	}
	if body["tag_name"] != "v1.2.3" || body["target_commitish"] != "main" || body["prerelease"] != false {
		t.Errorf("release payload = %+v", body)
	}
}

func TestAPIErrorSurfacesStatusAndBody(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"token missing scope"}`))
	}))
	err := c.CreateRelease(context.Background(), "v1", "main", "v1", "", false)
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "token missing scope") {
		t.Errorf("expected 403 + body in error, got %v", err)
	}
}
