package git

// Revision regression coverage for phase 1.1 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md.
//
// The frozen fixtures pin the ordered independent SIGTERM failure (including
// exit before completion notification is consumed) and the still-live kill.
// The genuinely missing ordering is an independently SIGKILL-terminated
// direct push: SIGKILL is the same signal the cancellation handshake itself
// sends, so only explicit exit-before-cancellation ordering proves the
// handshake does not relabel that independent failure as cancelled. The
// live-kill test below keeps the revision file self-contained for the
// complementary ordering (cancellation that demonstrably terminates a
// still-running direct push). Same-package helpers
// (gitCommitUnderTestWithFakeGit, pushRouteUnderTest,
// argvCarriesOperationArgs, waitForTestFile, waitForRegressionPushLines,
// releaseRegressionDescendantAndRequireExit) are reused from the frozen
// files and are not redefined here.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGitPushIndependentSigKillSurvivesLateCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell signals")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
    push)
      echo "sigkill-stdout"
      echo "sigkill-stderr" >&2
      parent=$$
      ( while kill -0 "$parent" 2>/dev/null; do
          state=$(ps -o stat= -p "$parent" 2>/dev/null)
          case "$state" in Z*) break ;; esac
        done
        touch "$FAKE_GIT_PARENT_DEAD"
        while [ ! -e "$FAKE_GIT_RELEASE_DESCENDANT" ]; do :; done
        touch "$FAKE_GIT_DESCENDANT_DONE"
      ) &
      kill -KILL "$parent"
      ;;
  esac
done
exit 0
`)
	parentDead := filepath.Join(root, "sigkill-parent-dead")
	releaseDescendant := filepath.Join(root, "sigkill-release-descendant")
	descendantDone := filepath.Join(root, "sigkill-descendant-done")
	t.Setenv("FAKE_GIT_PARENT_DEAD", parentDead)
	t.Setenv("FAKE_GIT_RELEASE_DESCENDANT", releaseDescendant)
	t.Setenv("FAKE_GIT_DESCENDANT_DONE", descendantDone)
	// Best-effort descendant release: runs even when the test times out or
	// fails, without a Fatal that could mask the original failure.
	defer func() {
		_ = os.WriteFile(releaseDescendant, []byte{}, 0o600)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(descendantDone); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	// The direct child died by its own SIGKILL while the descendant keeps
	// both inherited write ends open, so the drain cannot reach EOF
	// (completion notification) on its own. Both readers consume the
	// pre-exit bytes first so the retained-stream oracle is not raced by
	// the cancel-time pipe close; only then does the late cancellation
	// bound the still-open drain.
	waitForTestFile(t, parentDead)
	waitForRegressionPushLines(t, gitCommit, "sigkill-stdout", "sigkill-stderr")
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the signal-terminated process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("late drain cancellation relabelled the independently signal-terminated process as cancelled")
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the direct process *exec.ExitError from the signal termination", result.Err())
		} else {
			if result.ExitCode() != exitErr.ExitCode() {
				t.Errorf("ExitCode() = %d, want the reaped signal status %d", result.ExitCode(), exitErr.ExitCode())
			}
			if result.ExitCode() == 0 {
				t.Error("ExitCode() = 0, want the nonzero signal status, not a clean exit")
			}
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not contain the late context cancellation", result.Err())
		}
		if result.Success() {
			t.Error("a signal-terminated push must not be reported as a success")
		}
		if !strings.Contains(string(result.Stdout()), "sigkill-stdout") || !strings.Contains(string(result.Stderr()), "sigkill-stderr") {
			t.Errorf("signal failure lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("signal failure push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the SIGKILL-terminated drain")
	}
}

func TestGitPushLiveCancellationTerminatesTheStillRunningPush(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
    push)
      echo "revision-cancel-stdout"
      echo "revision-cancel-stderr" >&2
      touch "$FAKE_GIT_READY"
      exec sleep 30
      ;;
  esac
done
exit 0
`)
	ready := filepath.Join(root, "revision-live-ready")
	t.Setenv("FAKE_GIT_READY", ready)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	// The direct child is provably running (it printed both pre-exec lines
	// before replacing itself with sleep), so the cancellation demonstrably
	// terminates a still-running direct push.
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "revision-cancel-stdout", "revision-cancel-stderr")
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the live process had started before the cancellation")
		}
		if !result.Cancelled() {
			t.Error("killing the still-running direct push must report cancelled")
		}
		if result.Success() {
			t.Error("a cancelled push must not be reported as a success")
		}
		if !errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, want the context cancellation cause for the live kill", result.Err())
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the reaped process *exec.ExitError alongside the context cause", result.Err())
		} else if result.ExitCode() != exitErr.ExitCode() {
			t.Errorf("ExitCode() = %d, want the reaped process status %d", result.ExitCode(), exitErr.ExitCode())
		}
		if !strings.Contains(string(result.Stdout()), "revision-cancel-stdout") || !strings.Contains(string(result.Stderr()), "revision-cancel-stderr") {
			t.Errorf("live-killed push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("cancelled push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after terminating the still-running process")
	}
}
