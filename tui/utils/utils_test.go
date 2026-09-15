package utils

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ------------------------------------
//
//	Build a scratch git repository and return its path
//
// ------------------------------------
func scratchGitRepo(t *testing.T, name string) string {
	t.Helper()

	repo := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("git", "init", "-q", "-b", "master", repo)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", name, err, output)
	}
	return repo
}

func TestBuildGitSigningExecCmdPinsTheWorkingDirectory(t *testing.T) {
	first := scratchGitRepo(t, "first")
	second := scratchGitRepo(t, "second")
	// git resolves the symlinked /var prefix to /private/var
	first, err := filepath.EvalSymlinks(first)
	if err != nil {
		t.Fatalf("resolving the scratch repo path: %v", err)
	}
	second, err = filepath.EvalSymlinks(second)
	if err != nil {
		t.Fatalf("resolving the scratch repo path: %v", err)
	}

	cmd := buildGitSigningExecCmd([]string{"rev-parse", "--show-toplevel"}, first)
	if cmd.Dir != first {
		t.Errorf("cmd.Dir = %q, want the active model repository path %q", cmd.Dir, first)
	}
	if got := strings.Join(cmd.Args, " "); got != "git rev-parse --show-toplevel" {
		t.Errorf("cmd.Args = %q, want the git operation unchanged", got)
	}

	// Executing the command must resolve the repository from the pinned
	// directory, not from the process launch directory.
	t.Chdir(t.TempDir())
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("the pinned signing command failed: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != first {
		t.Errorf("rev-parse in the pinned dir = %q, want %q", got, first)
	}

	// The same operation pinned to a different worktree resolves that
	// worktree, which is what a worktree switch must keep the signing route
	// tracking.
	other := buildGitSigningExecCmd([]string{"rev-parse", "--show-toplevel"}, second)
	output, err = other.Output()
	if err != nil {
		t.Fatalf("the other pinned signing command failed: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != second {
		t.Errorf("rev-parse in the other pinned dir = %q, want %q", got, second)
	}
}
