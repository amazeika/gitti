package git

// Ordered final-boundary regression for phase 1.1.1 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md,
// finding code:R1 (final probe-to-Kill gap can misattribute independent
// SIGKILL).
//
// What the retained files leave open: the probe-kill attribution file
// self-SIGKILLs with reaping delayed from the start, and the final-boundary
// file cancels-then-kills without an acknowledged live verdict at the
// boundary. The tests below add that acknowledgement seam: the direct child
// publishes its PID, the test asserts positive life (AlreadyExited false
// and SettledDead false) immediately before the cancel-plus-independent-
// SIGKILL sequence, then asserts positive death evidence before judging
// the classification, with a silent holder descendant proving the direct
// exit precedes completion notification (drain EOF). The live-cancel
// control asserts the same live acknowledgement, then cancellation alone
// terminates the still-running child.
//
// Same-package helpers (gitCommitUnderTestWithFakeGit,
// pushRouteUnderTest, argvCarriesOperationArgs, waitForTestFile,
// waitForRegressionPushLines, releaseRegressionDescendantAndRequireExit)
// are reused from the retained files and are not redefined here.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPhase111OrderedFinalBoundarySigKillSurvivesCancel(t *testing.T) {
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
      echo "ordbound-stdout"
      echo "ordbound-stderr" >&2
      echo $$ > "$FAKE_GIT_CHILD_PID"
      touch "$FAKE_GIT_READY"
      ( while [ ! -e "$FAKE_GIT_RELEASE_DESCENDANT" ]; do :; done
        touch "$FAKE_GIT_DESCENDANT_DONE"
      ) &
      exec sleep 30
      ;;
  esac
done
exit 0
`)
	ready := filepath.Join(root, "ordbound-ready")
	childPidFile := filepath.Join(root, "ordbound-child-pid")
	releaseDescendant := filepath.Join(root, "ordbound-release-descendant")
	descendantDone := filepath.Join(root, "ordbound-descendant-done")
	t.Setenv("FAKE_GIT_READY", ready)
	t.Setenv("FAKE_GIT_CHILD_PID", childPidFile)
	t.Setenv("FAKE_GIT_RELEASE_DESCENDANT", releaseDescendant)
	t.Setenv("FAKE_GIT_DESCENDANT_DONE", descendantDone)
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
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "ordbound-stdout", "ordbound-stderr")
	waitForTestFile(t, childPidFile)
	pidRaw, err := os.ReadFile(childPidFile)
	if err != nil {
		t.Fatalf("reading the direct-child PID file: %v", err)
	}
	childPid, err := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if err != nil || childPid <= 0 {
		t.Fatalf("parsing the direct-child PID %q: %v", strings.TrimSpace(string(pidRaw)), err)
	}
	// Acknowledged live verdict at the boundary: the child is provably
	// alive (not already exited, not settled dead) at the moment the
	// cancellation-plus-independent-SIGKILL sequence starts.
	if pushDirectProcessAlreadyExited(childPid) {
		t.Fatalf("direct child PID %d already reads as exited before the boundary sequence; the live-verdict premise cannot be established", childPid)
	}
	if pushDirectProcessSettledDead(childPid) {
		t.Fatalf("direct child PID %d already reads as settled dead before the boundary sequence; the live-verdict premise cannot be established", childPid)
	}
	// The cancellation starts the handshake's final observation; the
	// test-side SIGKILL lands immediately after, inside the final
	// observation-to-Kill window as an unreaped zombie.
	cancel()
	proc, findErr := os.FindProcess(childPid)
	if findErr != nil {
		t.Fatalf("finding the direct child PID %d for the independent SIGKILL: %v", childPid, findErr)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("delivering the independent final-boundary SIGKILL to PID %d: %v", childPid, err)
	}
	// Acknowledged death before classification: without positive death
	// evidence the non-cancelled oracle is vacuous.
	deathDeadline := time.Now().Add(5 * time.Second)
	for !pushDirectProcessAlreadyExited(childPid) && time.Now().Before(deathDeadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !pushDirectProcessAlreadyExited(childPid) {
		t.Fatalf("independently SIGKILLed PID %d never showed death evidence; the classification oracle is vacuous", childPid)
	}
	// Exit before completion notification: the silent holder still keeps
	// the inherited write ends open, so the drain cannot have reached EOF.
	if _, err := os.Stat(descendantDone); err == nil {
		t.Fatalf("descendant exited before the classification, so the direct exit no longer precedes completion notification")
	} else if !os.IsNotExist(err) {
		t.Fatalf("checking the descendant hold-open marker: %v", err)
	}

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the signal-terminated process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("independent SIGKILL in the final observation-to-Kill window relabelled the signal-terminated process as cancelled")
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the direct process *exec.ExitError from the independent signal termination", result.Err())
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
		if !strings.Contains(string(result.Stdout()), "ordbound-stdout") || !strings.Contains(string(result.Stderr()), "ordbound-stderr") {
			t.Errorf("signal failure lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("signal failure push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the final-boundary SIGKILL drain")
	}
}

func TestPhase111OrderedLiveCancelTerminatesAndReportsCancelled(t *testing.T) {
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
      echo "ordlive-stdout"
      echo "ordlive-stderr" >&2
      echo $$ > "$FAKE_GIT_CHILD_PID"
      touch "$FAKE_GIT_READY"
      ( while [ ! -e "$FAKE_GIT_RELEASE_DESCENDANT" ]; do :; done
        touch "$FAKE_GIT_DESCENDANT_DONE"
      ) &
      exec sleep 30
      ;;
  esac
done
exit 0
`)
	ready := filepath.Join(root, "ordlive-ready")
	childPidFile := filepath.Join(root, "ordlive-child-pid")
	releaseDescendant := filepath.Join(root, "ordlive-release-descendant")
	descendantDone := filepath.Join(root, "ordlive-descendant-done")
	t.Setenv("FAKE_GIT_READY", ready)
	t.Setenv("FAKE_GIT_CHILD_PID", childPidFile)
	t.Setenv("FAKE_GIT_RELEASE_DESCENDANT", releaseDescendant)
	t.Setenv("FAKE_GIT_DESCENDANT_DONE", descendantDone)
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
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "ordlive-stdout", "ordlive-stderr")
	waitForTestFile(t, childPidFile)
	pidRaw, err := os.ReadFile(childPidFile)
	if err != nil {
		t.Fatalf("reading the direct-child PID file: %v", err)
	}
	childPid, err := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if err != nil || childPid <= 0 {
		t.Fatalf("parsing the direct-child PID %q: %v", strings.TrimSpace(string(pidRaw)), err)
	}
	// Acknowledged live target: the cancellation demonstrably terminates
	// a still-running direct push (no independent killer exists).
	if pushDirectProcessAlreadyExited(childPid) {
		t.Fatalf("direct child PID %d already reads as exited before the cancellation; the live-kill premise cannot be established", childPid)
	}
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the live process had started before the cancellation")
		}
		if !result.Cancelled() {
			t.Error("a cancellation that terminates the still-running direct push must report cancelled")
		}
		if result.Success() {
			t.Error("a cancelled push must not be reported as a success")
		}
		if !errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, want the context cancellation cause for the demonstrable live kill", result.Err())
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the reaped process *exec.ExitError alongside the context cause", result.Err())
		} else if result.ExitCode() != exitErr.ExitCode() {
			t.Errorf("ExitCode() = %d, want the reaped process status %d", result.ExitCode(), exitErr.ExitCode())
		}
		if !strings.Contains(string(result.Stdout()), "ordlive-stdout") || !strings.Contains(string(result.Stderr()), "ordlive-stderr") {
			t.Errorf("live-killed push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("cancelled push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after the demonstrable live kill although cancellation must bound the inherited-descriptor drain")
	}
}
