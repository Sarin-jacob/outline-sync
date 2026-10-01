package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeOutline serves just enough of the Outline API for a sync.
type fakeOutline struct {
	mu      sync.Mutex
	files   map[string]string // zip contents
	deleted []string
	admin   bool
}

func (f *fakeOutline) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(401)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	reply := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": v}) }
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/api/collections.list":
		if body["offset"].(float64) > 0 {
			reply([]any{})
			return
		}
		reply([]map[string]string{{"id": "c1", "name": "My Docs"}})
	case "/api/collections.export":
		reply(map[string]any{"fileOperation": map[string]string{"id": "op1", "state": "creating"}})
	case "/api/fileOperations.info":
		reply(map[string]string{"id": "op1", "state": "complete"})
	case "/api/fileOperations.redirect":
		http.Redirect(w, r, "/files/export.zip", http.StatusFound)
	case "/files/export.zip":
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for name, content := range f.files {
			fw, _ := zw.Create(name)
			fw.Write([]byte(content))
		}
		zw.Close()
		w.Write(buf.Bytes())
	case "/api/fileOperations.delete":
		if !f.admin {
			w.WriteHeader(403)
			json.NewEncoder(w).Encode(map[string]string{"error": "authorization_error"})
			return
		}
		f.deleted = append(f.deleted, body["id"].(string))
		reply(true)
	default:
		w.WriteHeader(404)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestSyncEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote.git")
	git(t, tmp, "init", "--bare", "--quiet", remote)

	fake := &fakeOutline{admin: true, files: map[string]string{
		"My Docs/Guide.md":                 "# Guide\n\nHello\n",
		"My Docs/Guide/Setup (v2).md":      "# Setup\n",
		"My Docs/Guide/Setup (v2)/Deep.md": "# Deep\n",
		"My Docs/Other.md":                 "# Other\n",
		"My Docs/uploads/img.png":          "png",
	}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	cfg := &Config{
		OutlineURL: srv.URL, OutlineToken: "tok", GitUser: "bot", GitEmail: "b@x",
		CheckInterval: 1, ExportTimeout: 10, PurgeLocalHistory: true,
		SyncTasks: []Task{{CollectionName: "My Docs", RepoURL: remote, Branch: "main"}},
	}
	repos := filepath.Join(tmp, "repos")
	ctx := context.Background()
	commits := func() string { return git(t, remote, "rev-list", "--count", "main") }

	// 1. First sync pushes to an empty remote.
	NewSyncer(cfg, repos).RunCycle(ctx)
	if c := commits(); c != "1" {
		t.Fatalf("want 1 commit, got %s", c)
	}
	if len(fake.deleted) != 1 {
		t.Fatalf("export not deleted: %v", fake.deleted)
	}
	local := filepath.Join(repos, "My_Docs")
	setup, _ := os.ReadFile(filepath.Join(local, "My Docs", "Guide", "Setup (v2).md"))
	for _, want := range []string{
		"[🏠 My Docs](../../README.md) › [Guide](../Guide.md) › **Setup (v2)**",
		"* [Deep](Setup%20%28v2%29/Deep.md)",
		"[⬆ Back to Guide](../Guide.md)",
	} {
		if !strings.Contains(string(setup), want) {
			t.Errorf("Setup (v2).md missing %q:\n%s", want, setup)
		}
	}
	readme, _ := os.ReadFile(filepath.Join(local, "README.md"))
	if !strings.Contains(string(readme), "    * [Deep](My%20Docs/Guide/Setup%20%28v2%29/Deep.md)") {
		t.Errorf("README tree wrong:\n%s", readme)
	}
	if strings.Contains(string(readme), "uploads") {
		t.Errorf("README lists uploads:\n%s", readme)
	}

	// 2. Unchanged content: no new commit.
	NewSyncer(cfg, repos).RunCycle(ctx)
	if c := commits(); c != "1" {
		t.Fatalf("unchanged sync committed: %s commits", c)
	}

	// 3. Deleted doc in Outline is deleted in git.
	delete(fake.files, "My Docs/Other.md")
	NewSyncer(cfg, repos).RunCycle(ctx)
	if c := commits(); c != "2" {
		t.Fatalf("want 2 commits, got %s", c)
	}
	if out := git(t, remote, "ls-tree", "-r", "--name-only", "main"); strings.Contains(out, "Other.md") {
		t.Fatalf("Other.md still in remote:\n%s", out)
	}

	// 4. Someone commits directly on the remote; we must still be able to push.
	clone := filepath.Join(tmp, "clone")
	git(t, tmp, "clone", "--quiet", remote, clone)
	os.WriteFile(filepath.Join(clone, "manual.txt"), []byte("x"), 0o644)
	git(t, clone, "add", ".")
	git(t, clone, "-c", "user.name=a", "-c", "user.email=a@b", "commit", "-qm", "manual")
	git(t, clone, "push", "-q", "origin", "HEAD:main")
	fake.files["My Docs/New.md"] = "# New\n"
	NewSyncer(cfg, repos).RunCycle(ctx)
	if c := commits(); c != "4" {
		t.Fatalf("want 4 commits after diverged remote, got %s", c)
	}

	// 5. Local volume lost: history continues instead of failing to push.
	os.RemoveAll(repos)
	fake.files["My Docs/New.md"] = "# New v2\n"
	NewSyncer(cfg, repos).RunCycle(ctx)
	if c := commits(); c != "5" {
		t.Fatalf("want 5 commits after volume loss, got %s", c)
	}

	// 6. Non-admin token: sync still works, delete disabled after first refusal.
	fake.admin = false
	fake.files["My Docs/New.md"] = "# New v3\n"
	s := NewSyncer(cfg, repos)
	s.RunCycle(ctx)
	if c := commits(); c != "6" {
		t.Fatalf("want 6 commits with non-admin token, got %s", c)
	}
	if s.canDelete {
		t.Fatal("canDelete should be false after 403")
	}
}

func TestExtractRejectsZipSlip(t *testing.T) {
	tmp := t.TempDir()
	zp := filepath.Join(tmp, "bad.zip")
	f, _ := os.Create(zp)
	zw := zip.NewWriter(f)
	w, _ := zw.Create("../evil.md")
	w.Write([]byte("x"))
	zw.Close()
	f.Close()
	dest := filepath.Join(tmp, "out")
	os.Mkdir(dest, 0o755)
	if err := Extract(zp, dest); err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestRedact(t *testing.T) {
	got := redact("fatal: unable to access 'https://user:ghp_secret@github.com/u/r.git/'")
	if strings.Contains(got, "ghp_secret") {
		t.Fatal(got)
	}
}

func TestQuotePathMatchesPython(t *testing.T) {
	// urllib.parse.quote("A b/C#1?&(x).md")
	if got := quotePath("A b/C#1?&(x).md"); got != "A%20b/C%231%3F%26%28x%29.md" {
		t.Fatal(got)
	}
}
