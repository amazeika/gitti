package git

// Queued-output remediation coverage for phase 1.1.1 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md,
// finding code:R3 (cancellation can discard output queued before
// direct-process completion).
//
// What the retained files leave open: the frozen delayed-reader test
// releases its readers with no cancellation, and every cancellation
// test waits for stream consumption through the live progress buffers
// before cancelling. Neither orders readers paused before the first
// read, independently observed child exit, then immediate cancellation. Closing both read
// ends at cancellation then discards bytes already written by the
// completed child. The tests below order exactly that way — the done
// marker only proves the queued bytes were written, while a descendant
// observer records the direct push PID's zombie state (death) and its
// disappearance (reaped by the single reaper, completion buffered)
// before cancellation — and assert
// the queued bytes survive without first waiting for them to appear
// in the live progress buffers.
//
// Same-package helpers (gitCommitUnderTestWithFakeGit,
// pushRouteUnderTest, argvCarriesOperationArgs, waitForTestFile) are
// reused from the retained files and are not redefined here.

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

func TestPushDelayedReaderExitBeforeCancelRetainsQueuedStreams(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the direct-exit ordering fixture requires POSIX kill/ps observation")
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
      echo "queued-stdout-marker"
      echo "queued-stderr-marker" >&2
      parent=$$
      ( while kill -0 "$parent" 2>/dev/null; do
          state=$(ps -o stat= -p "$parent" 2>/dev/null)
          case "$state" in Z*) break ;; esac
        done
        touch "$FAKE_GIT_PARENT_DEAD"
        while kill -0 "$parent" 2>/dev/null; do :; done
        touch "$FAKE_GIT_PARENT_REAPED"
      ) &
      touch "$FAKE_GIT_DONE"
      exit 0
      ;;
  esac
done
exit 0
`)
	doneMarker := filepath.Join(root, "queued-done")
	t.Setenv("FAKE_GIT_DONE", doneMarker)
	parentDead := filepath.Join(root, "queued-parent-dead")
	parentReaped := filepath.Join(root, "queued-parent-reaped")
	t.Setenv("FAKE_GIT_PARENT_DEAD", parentDead)
	t.Setenv("FAKE_GIT_PARENT_REAPED", parentReaped)

	// Hold both stream readers before their first read so the child
	// writes and exits while the output still sits unread in the pipe
	// buffers. Cancel immediately after the exit without waiting for
	// the live progress buffers, then release the readers.
	release := make(chan struct{})
	gitCommit.gitPushDrainPreRead = func() { <-release }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	// The done marker only proves the child wrote its queued bytes before
	// exiting; it cannot prove the exit itself. The zombie observation
	// proves the direct push process exited, and the PID disappearance
	// proves the single reaper already returned (the completion is
	// buffered), so the cancellation below is ordered after direct exit
	// even though the completion notification may not have been consumed
	// yet. The asserted bytes are ordered before the exit, never behind
	// progress-buffer consumption.
	waitForTestFile(t, doneMarker)
	waitForTestFile(t, parentDead)
	waitForTestFile(t, parentReaped)
	cancel()
	close(release)

	select {
	case result := <-done:
		if !result.Started() {
			t.Error("the completed process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("late drain cancellation relabelled the completed zero-exit process as cancelled")
		}
		if result.ExitCode() != 0 {
			t.Errorf("ExitCode() = %d, want the direct Wait status 0", result.ExitCode())
		}
		if !result.Success() {
			t.Error("a zero exit before late cancellation must retain Success() == true for success-only reconciliation")
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not contain the late drain cancellation", result.Err())
		}
		if !strings.Contains(string(result.Stdout()), "queued-stdout-marker") {
			t.Errorf("retained stdout = %q, want the queued stdout the child wrote before exiting", string(result.Stdout()))
		}
		if !strings.Contains(string(result.Stderr()), "queued-stderr-marker") {
			t.Errorf("retained stderr = %q, want the queued stderr the child wrote before exiting", string(result.Stderr()))
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("queued push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after the delayed-reader late cancellation")
	}
}

func TestPushDelayedReaderNonzeroExitBeforeCancelNotRelabelled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the direct-exit ordering fixture requires POSIX kill/ps observation")
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
      echo "queued-fail-stdout"
      echo "queued-fail-stderr" >&2
      parent=$$
      ( while kill -0 "$parent" 2>/dev/null; do
          state=$(ps -o stat= -p "$parent" 2>/dev/null)
          case "$state" in Z*) break ;; esac
        done
        touch "$FAKE_GIT_PARENT_DEAD"
        while kill -0 "$parent" 2>/dev/null; do :; done
        touch "$FAKE_GIT_PARENT_REAPED"
      ) &
      touch "$FAKE_GIT_DONE"
      exit 3
      ;;
  esac
done
exit 0
`)
	doneMarker := filepath.Join(root, "queued-fail-done")
	t.Setenv("FAKE_GIT_DONE", doneMarker)
	parentDead := filepath.Join(root, "queued-fail-parent-dead")
	parentReaped := filepath.Join(root, "queued-fail-parent-reaped")
	t.Setenv("FAKE_GIT_PARENT_DEAD", parentDead)
	t.Setenv("FAKE_GIT_PARENT_REAPED", parentReaped)

	// Same delayed-reader ordering as the zero-exit case: the done marker
	// only proves the queued bytes were written, while the zombie and
	// reaped observations prove the nonzero direct exit precedes the
	// cancellation. The asserted bytes are never gated on
	// progress-buffer consumption.
	release := make(chan struct{})
	gitCommit.gitPushDrainPreRead = func() { <-release }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	waitForTestFile(t, doneMarker)
	waitForTestFile(t, parentDead)
	waitForTestFile(t, parentReaped)
	cancel()
	close(release)

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the failed process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("late drain cancellation relabelled the independently failed nonzero process as cancelled")
		}
		if result.ExitCode() != 3 {
			t.Errorf("ExitCode() = %d, want the direct Wait status 3", result.ExitCode())
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the direct process *exec.ExitError", result.Err())
		} else if result.ExitCode() != exitErr.ExitCode() {
			t.Errorf("ExitCode() = %d, want the direct Wait status %d", result.ExitCode(), exitErr.ExitCode())
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not contain the late drain cancellation", result.Err())
		}
		if result.Success() {
			t.Error("a nonzero exit must not be reported as a success")
		}
		if !strings.Contains(string(result.Stdout()), "queued-fail-stdout") {
			t.Errorf("retained stdout = %q, want the queued stdout the child wrote before exiting", string(result.Stdout()))
		}
		if !strings.Contains(string(result.Stderr()), "queued-fail-stderr") {
			t.Errorf("retained stderr = %q, want the queued stderr the child wrote before exiting", string(result.Stderr()))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after the delayed-reader late cancellation of the nonzero drain")
	}
}

func TestPushDelayedReaderInheritedHolderCancelStillBoundsDrain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the direct-exit ordering fixture requires POSIX kill/ps observation")
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
      echo "held-queued-stdout"
      echo "held-queued-stderr" >&2
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
      touch "$FAKE_GIT_DONE"
      exit 0
      ;;
  esac
done
exit 0
`)
	doneMarker := filepath.Join(root, "held-queued-done")
	releaseDescendant := filepath.Join(root, "held-queued-release-descendant")
	descendantDone := filepath.Join(root, "held-queued-descendant-done")
	parentDead := filepath.Join(root, "held-queued-parent-dead")
	parentReaped := filepath.Join(root, "held-queued-parent-reaped")
	t.Setenv("FAKE_GIT_DONE", doneMarker)
	t.Setenv("FAKE_GIT_PARENT_DEAD", parentDead)
	t.Setenv("FAKE_GIT_PARENT_REAPED", parentReaped)
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

	// Delayed readers plus a descendant holding the inherited write
	// ends: the zombie and reaped observations prove the direct child
	// exited and was reaped before the cancellation, while the descendant
	// keeps the drain from reaching EOF on its own. The cancellation must
	// still return boundedly while the already queued bytes survive.
	release := make(chan struct{})
	gitCommit.gitPushDrainPreRead = func() { <-release }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	waitForTestFile(t, doneMarker)
	waitForTestFile(t, parentDead)
	waitForTestFile(t, parentReaped)
	cancel()
	close(release)

	select {
	case result := <-done:
		if !result.Started() {
			t.Error("the completed process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("late drain cancellation relabelled the completed zero-exit process as cancelled")
		}
		if result.ExitCode() != 0 {
			t.Errorf("ExitCode() = %d, want the direct Wait status 0", result.ExitCode())
		}
		if !result.Success() {
			t.Error("a zero exit before late cancellation must retain Success() == true for success-only reconciliation")
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not contain the late drain cancellation", result.Err())
		}
		if !strings.Contains(string(result.Stdout()), "held-queued-stdout") {
			t.Errorf("retained stdout = %q, want the queued stdout the child wrote before exiting", string(result.Stdout()))
		}
		if !strings.Contains(string(result.Stderr()), "held-queued-stderr") {
			t.Errorf("retained stderr = %q, want the queued stderr the child wrote before exiting", string(result.Stderr()))
		}
		_ = os.WriteFile(releaseDescendant, []byte{}, 0o600)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(descendantDone); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if _, err := os.Stat(descendantDone); err != nil {
			t.Errorf("the descendant did not exit after its release, suggesting a leaked process still holding push descriptors (missing %q)", descendantDone)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after cancellation although an inherited writer must never prevent a bounded cancellation return")
	}
}
