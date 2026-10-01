package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Repo wraps the git CLI for a single working tree.
type Repo struct {
	Dir string
}

// GitError carries the exit code so callers can tell "differences found"
// (exit 1 from diff --quiet) apart from real failures.
type GitError struct {
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *GitError) Error() string {
	return redact(fmt.Sprintf("git %s: exit %d: %s", strings.Join(e.Args, " "), e.ExitCode, e.Stderr))
}

var credentialsInURL = regexp.MustCompile(`(://)[^/@\s]+@`)

// redact hides user:token@ in any URL so PATs never reach the logs.
func redact(s string) string {
	return credentialsInURL.ReplaceAllString(s, "${1}***@")
}

// Git runs a git command in the repo and returns trimmed stdout.
//
// Commands deliberately do not take the cancellable app context: killing git
// halfway through a commit or push would leave lock files behind, and every
// command here finishes quickly.
func (r *Repo) Git(args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", // never hang waiting for credentials
		"LC_ALL=C",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		code := -1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", &GitError{Args: args, ExitCode: code, Stderr: msg}
	}
	return strings.TrimSpace(stdout.String()), nil
}

func exitCode(err error) int {
	if ge, ok := err.(*GitError); ok {
		return ge.ExitCode
	}
	return -1
}

// Init prepares the repository: creates it if missing, points origin at the
// configured URL and sets the commit identity. Safe to run on every sync.
func (r *Repo) Init(remoteURL, branch, user, email string) error {
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return err
	}
	// A previous run that was killed mid-command can leave this behind; we are
	// the only writer, so it is always stale.
	_ = os.Remove(filepath.Join(r.Dir, ".git", "index.lock"))

	if _, err := os.Stat(filepath.Join(r.Dir, ".git")); os.IsNotExist(err) {
		if _, err := r.Git("init", "--quiet"); err != nil {
			return err
		}
	}

	if _, err := r.Git("remote", "get-url", "origin"); err != nil {
		if _, err := r.Git("remote", "add", "origin", remoteURL); err != nil {
			return err
		}
	} else if _, err := r.Git("remote", "set-url", "origin", remoteURL); err != nil {
		return err
	}

	steps := [][]string{
		{"config", "user.name", user},
		{"config", "user.email", email},
		{"config", "core.quotePath", "false"},
		{"symbolic-ref", "HEAD", "refs/heads/" + branch},
	}
	for _, s := range steps {
		if _, err := r.Git(s...); err != nil {
			return err
		}
	}
	return nil
}

// FetchRemote fetches origin/<branch> if it exists. It returns false when the
// remote branch does not exist yet (e.g. a brand-new empty GitHub repo).
func (r *Repo) FetchRemote(branch string, shallow bool) (bool, error) {
	_, err := r.Git("ls-remote", "--exit-code", "--heads", "origin", branch)
	if exitCode(err) == 2 {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	args := []string{"fetch", "--quiet", "--no-tags"}
	if shallow {
		args = append(args, "--depth=1")
	}
	args = append(args, "origin", fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", branch, branch))
	if _, err := r.Git(args...); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Repo) RevParse(ref string) (string, bool) {
	out, err := r.Git("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return out, err == nil
}

// HasChanges reports whether the staged tree differs from HEAD, ignoring the
// generated README (its timestamp changes on every run).
func (r *Repo) HasChanges() (bool, error) {
	_, err := r.Git("diff", "--cached", "--quiet", "--", ".", ":(exclude)README.md")
	switch exitCode(err) {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, err
	}
}

// Compact drops everything not reachable from the current branch and repacks.
func (r *Repo) Compact() error {
	if _, err := r.Git("reflog", "expire", "--expire=now", "--all"); err != nil {
		return err
	}
	_, err := r.Git("gc", "--quiet", "--prune=now", "--aggressive")
	return err
}
