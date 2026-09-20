// Package gitx wraps the git and gh operations the build loop needs.
//
// Work happens in the main working tree on a branch per unit, not in a
// worktree: the loop is sequential, and keeping one path means the dev and
// checks tabs never have to follow the build somewhere else.
package gitx

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// Repo is a git working tree.
type Repo struct {
	Dir string
	run func(ctx context.Context, name string, args ...string) (string, string, error)
}

// New returns a Repo rooted at dir.
func New(dir string) *Repo {
	r := &Repo{Dir: dir}
	r.run = r.execute
	return r
}

func (r *Repo) execute(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = r.Dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func (r *Repo) git(ctx context.Context, args ...string) (string, error) {
	stdout, stderr, err := r.run(ctx, "git", args...)
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = strings.TrimSpace(stdout)
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(stdout), nil
}

// CurrentBranch returns the checked-out branch.
func (r *Repo) CurrentBranch(ctx context.Context) (string, error) {
	return r.git(ctx, "rev-parse", "--abbrev-ref", "HEAD")
}

// DefaultBranch returns the remote's default branch, falling back to main.
func (r *Repo) DefaultBranch(ctx context.Context) (string, error) {
	out, err := r.git(ctx, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err != nil {
		return "main", nil
	}
	if i := strings.LastIndex(out, "/"); i >= 0 {
		return out[i+1:], nil
	}
	return "main", nil
}

// IsClean reports whether the working tree has no uncommitted changes.
//
// The loop refuses to start on a dirty tree: it commits everything it finds
// after a builder runs, and sweeping up unrelated work-in-progress into a
// unit's PR would be both wrong and hard to notice.
func (r *Repo) IsClean(ctx context.Context) (bool, error) {
	out, err := r.git(ctx, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out == "", nil
}

// HasChanges reports whether anything is staged or unstaged.
func (r *Repo) HasChanges(ctx context.Context) (bool, error) {
	clean, err := r.IsClean(ctx)
	return !clean, err
}

// Checkout creates and switches to a branch, or switches to it if it exists.
func (r *Repo) Checkout(ctx context.Context, branch string) error {
	if _, err := r.git(ctx, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		_, err := r.git(ctx, "checkout", branch)
		return err
	}
	_, err := r.git(ctx, "checkout", "-b", branch)
	return err
}

// CommitAll stages everything and commits.
func (r *Repo) CommitAll(ctx context.Context, message string) error {
	if _, err := r.git(ctx, "add", "-A"); err != nil {
		return err
	}
	_, err := r.git(ctx, "commit", "-m", message)
	return err
}

// Push publishes a branch and sets upstream.
func (r *Repo) Push(ctx context.Context, branch string) error {
	_, err := r.git(ctx, "push", "-u", "origin", branch)
	return err
}

// Diff returns the diff against a base branch, for a reviewer to read.
func (r *Repo) Diff(ctx context.Context, base string) (string, error) {
	return r.git(ctx, "diff", base+"...HEAD")
}

// ChangedFiles lists the paths this branch changes relative to base, for the
// autonomy guardrails to inspect.
func (r *Repo) ChangedFiles(ctx context.Context, base string) ([]string, error) {
	out, err := r.git(ctx, "diff", "--name-only", base+"...HEAD")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// MergePR merges a pull request and deletes its branch.
func (r *Repo) MergePR(ctx context.Context, url string) error {
	_, stderr, err := r.run(ctx, "gh", "pr", "merge", url, "--squash", "--delete-branch")
	if err != nil {
		return fmt.Errorf("gh pr merge: %s", strings.TrimSpace(stderr))
	}
	return nil
}

// CreatePR opens a pull request and returns its URL.
func (r *Repo) CreatePR(ctx context.Context, title, body, base string) (string, error) {
	stdout, stderr, err := r.run(ctx, "gh", "pr", "create",
		"--title", title, "--body", body, "--base", base)
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = strings.TrimSpace(stdout)
		}
		return "", fmt.Errorf("gh pr create: %s", msg)
	}
	return strings.TrimSpace(stdout), nil
}

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// BranchName builds a stable branch name for a unit.
func BranchName(number int, title string) string {
	slug := slugUnsafe.ReplaceAllString(strings.ToLower(title), "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	if slug == "" {
		return fmt.Sprintf("unit/%d", number)
	}
	return fmt.Sprintf("unit/%d-%s", number, slug)
}
