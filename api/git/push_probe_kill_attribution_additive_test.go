package git

// Additive regression coverage for phase 1.1.1 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md,
// finding code:R1 (probe-to-Kill race can misattribute independent SIGKILL).
//
// The frozen round-1 test injects independent death inside the ps
// diagnostic, before the handshake's final recheck. The tests below cover
// the sibling orderings the frozen file leaves open: an independently
// SIGKILLed direct push (self-SIGKILL, so the death is causally
// independent of any handshake Kill) whose reaping is delayed by a
// descendant holding the inherited descriptors, with the cancellation
// arriving only after the reaper already returned; and a unit-level
// demonstration that Kill success plus a SIGKILL reaped status cannot by
// themselves prove causation. Same-package helpers
// (gitCommitUnderTestWithFakeGit, pushRouteUnderTest,
// argvCarriesOperationArgs, waitForTestFile,
// waitForRegressionPushLines, releaseRegressionDescendantAndRequireExit)
// are reused from the retained files and are not redefined here.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGitPushIndependentSelfSigKillSurvivesLateCancel(t *testing.T) {
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
      echo "killattr-stdout"
      echo "killattr-stderr" >&2
      parent=$$
      ( while kill -0 "$parent" 2>/dev/null; do
          state=$(ps -o stat= -p "$parent" 2>/dev/null)
          case "$state" in Z*) break ;; esac
        done
        touch "$FAKE_GIT_PARENT_DEAD"
        while kill -0 "$parent" 2>/dev/null; do :; done
        touch "$FAKE_GIT_PARENT_REAPED"
        while [ ! -e "$FAKE_GIT_RELEASE_DESCENDANT" ]; do :; done
        touch "$FAKE_GIT_DESCENDANT_DONE"
      ) &
      kill -KILL "$parent"
      ;;
  esac
done
exit 0
`)
	parentDead := filepath.Join(root, "killattr-parent-dead")
	parentReaped := filepath.Join(root, "killattr-parent-reaped")
	releaseDescendant := filepath.Join(root, "killattr-release-descendant")
	descendantDone := filepath.Join(root, "killattr-descendant-done")
	t.Setenv("FAKE_GIT_PARENT_DEAD", parentDead)
	t.Setenv("FAKE_GIT_PARENT_REAPED", parentReaped)
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
	// Ordering: the direct child SIGKILLs itself independently (no
	// handshake Kill is involved in the death), the monitor observes the
	// zombie state, then PID disappearance proves the single reaper
	// already returned while the descendant still holds the inherited
	// write ends so the drain cannot reach EOF (completion notification).
	// Both readers consume the pre-exit bytes first so the retained-stream
	// oracle is not raced by the cancel-time pipe close. Only then does
	// the late cancellation arrive.
	waitForTestFile(t, parentDead)
	waitForTestFile(t, parentReaped)
	waitForRegressionPushLines(t, gitCommit, "killattr-stdout", "killattr-stderr")
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the signal-terminated process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("independent self-SIGKILL relabelled the signal-terminated process as cancelled")
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
		if !strings.Contains(string(result.Stdout()), "killattr-stdout") || !strings.Contains(string(result.Stderr()), "killattr-stderr") {
			t.Errorf("signal failure lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("signal failure push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the independently SIGKILLed drain")
	}
}

func TestPushKillOnUnreapedSigKillProvesNothingByItself(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("zombie Kill semantics are POSIX-specific")
	}
	// An independently SIGKILLed, not-yet-reaped child is the ambiguous
	// core of code:R1: a later Kill succeeds against the zombie and the
	// reaped status reads SIGKILL, so neither fact alone can attribute
	// the death to the later Kill. The handshake must therefore carry
	// positive liveness evidence, not Kill success or status alone.
	victim := exec.Command("sleep", "30")
	if err := victim.Start(); err != nil {
		t.Fatalf("starting the probe victim: %v", err)
	}
	pid := victim.Process.Pid
	defer func() { _ = victim.Wait() }()
	if err := victim.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("delivering the independent SIGKILL: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !pushDirectProcessAlreadyExited(pid) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !pushDirectProcessAlreadyExited(pid) {
		t.Fatalf("an independently SIGKILLed unreaped PID %d still reads as live; the probe cannot supply the liveness evidence the handshake needs", pid)
	}
	// The handshake's Kill landing now would succeed on the zombie
	// without having caused anything: Kill success alone proves nothing.
	if err := victim.Process.Kill(); err != nil {
		t.Errorf("Kill against the unreaped SIGKILLed zombie = %v, want nil success so the test proves Kill-alone cannot attribute causation", err)
	}
	waitErr := victim.Wait()
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("reaped wait error = %v, want the *exec.ExitError carrying the signal status", waitErr)
	}
	if !pushProcessKilledBySigKill(victim.ProcessState) {
		t.Error("reaped SIGKILL status does not read as SIGKILL, want the status filter to agree so the test isolates the ambiguity to causation rather than status")
	}
}
