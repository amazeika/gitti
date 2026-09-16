package utils

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gohyuhan/gitti/logging"
	"github.com/gohyuhan/gitti/tui/types"
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

// ------------------------------------
//
//	TestSigningRouteGuardRefusalNeverStartsTheSigningProcess proves the
//	route guard runs immediately before the suspended launch: a refusal
//	publishes the completion message with the guard's error, logs the
//	not-started refusal, and never runs the prepared git command
//
// ------------------------------------
func TestSigningRouteGuardRefusalNeverStartsTheSigningProcess(t *testing.T) {
	repo := scratchGitRepo(t, "signing")
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolving the scratch repo path: %v", err)
	}

	m := &types.GittiModel{GittiLogger: logging.InitGittiLogging(64, make(chan string, 256), 3)}
	guardErr := errors.New("the worktree generation changed after the signing push was confirmed")

	// the git command writes a marker into the repository config: if the
	// suspended process ever ran, the marker would be present
	_, suspended := SuspendGittiUIForGitOperationRequireSigningWithWorkdir(m,
		[]string{"config", "user.name", "gitti-guard-marker"},
		repo, func() error { return guardErr }, logging.GIT_PUSH_WITH_SIGNING_OPS)

	msg, ok := suspended().(types.GitOperationRequiredSigningFinishedMsg)
	if !ok {
		t.Fatalf("the suspended command returned %T, want the signing completion message", suspended())
	}
	if !errors.Is(msg.Err, guardErr) {
		t.Errorf("completion message Err() = %v, want the guard's error", msg.Err)
	}
	config, err := os.ReadFile(filepath.Join(repo, ".git", "config"))
	if err != nil {
		t.Fatalf("reading the scratch repository config: %v", err)
	}
	if strings.Contains(string(config), "gitti-guard-marker") {
		t.Error("a guard refusal started the signing process")
	}

	var notStartedLogged bool
	for _, entry := range m.GittiLogger.GetFullLogs() {
		if entry.OpsSeverityLevel == logging.WARN && strings.Contains(entry.OpsDescription, "NOT STARTED") {
			notStartedLogged = true
		}
	}
	if !notStartedLogged {
		t.Error("a guard refusal left no NOT STARTED log entry")
	}
}
