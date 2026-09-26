package git

// Additive regression coverage for phase 1.1.1 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md,
// finding code:R3 (custom cancellation cause loses standard cancellation
// identity). No retained test exercises WithCancelCause: the live-kill
// branch joins context.Cause(ctx) but omits ctx.Err(), so a non-sentinel
// custom cause drops errors.Is(Err(), context.Canceled). The first test
// below cancels a provably live direct push via WithCancelCause and
// requires both the standard sentinel and the custom cause to remain
// discoverable alongside the reaped process status and the captured
// streams. The second test is the plain-WithCancel live-kill control that
// pins the same oracle without a custom cause. Same-package helpers
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
	"testing"
	"time"
)

const pushCancelCauseScript = `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
    push)
      echo "cancelcause-stdout"
      echo "cancelcause-stderr" >&2
      touch "$FAKE_GIT_READY"
      ( while [ ! -e "$FAKE_GIT_RELEASE_DESCENDANT" ]; do :; done
        touch "$FAKE_GIT_DESCENDANT_DONE"
      ) &
      exec sleep 30
      ;;
  esac
done
exit 0
`

func TestGitPushLiveCancelWithCancelCauseKeepsStandardIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, pushCancelCauseScript)
	ready := filepath.Join(root, "cancelcause-ready")
	releaseDescendant := filepath.Join(root, "cancelcause-release-descendant")
	descendantDone := filepath.Join(root, "cancelcause-descendant-done")
	t.Setenv("FAKE_GIT_READY", ready)
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

	customCause := errors.New("push custom cancel cause")
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	// Ordering: the direct child is provably running (ready file plus
	// both pre-exec lines consumed through the live progress buffers)
	// while a descendant already holds the inherited write ends, so the
	// cancellation demonstrably terminates the still-running direct
	// push rather than racing an independent exit.
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "cancelcause-stdout", "cancelcause-stderr")
	cancel(customCause)

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the live process had started before the cancellation")
		}
		if !result.Cancelled() {
			t.Error("cancelling the still-running direct push must report cancelled")
		}
		if result.Success() {
			t.Error("a cancelled push must not be reported as a success")
		}
		if !errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, want the standard context.Canceled identity to remain discoverable alongside the custom cause", result.Err())
		}
		if !errors.Is(result.Err(), customCause) {
			t.Errorf("Err() = %v, want the supplied WithCancelCause cause to remain discoverable", result.Err())
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the reaped process *exec.ExitError alongside the cancellation causes", result.Err())
		} else if result.ExitCode() != exitErr.ExitCode() {
			t.Errorf("ExitCode() = %d, want the reaped process status %d", result.ExitCode(), exitErr.ExitCode())
		}
		if !strings.Contains(string(result.Stdout()), "cancelcause-stdout") || !strings.Contains(string(result.Stderr()), "cancelcause-stderr") {
			t.Errorf("live-killed push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("cancelled push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after the WithCancelCause live kill although cancellation must bound the inherited-descriptor drain")
	}
}

func TestGitPushLiveCancelWithPlainContextKeepsStatusAndStreams(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, pushCancelCauseScript)
	ready := filepath.Join(root, "plaincancel-ready")
	releaseDescendant := filepath.Join(root, "plaincancel-release-descendant")
	descendantDone := filepath.Join(root, "plaincancel-descendant-done")
	t.Setenv("FAKE_GIT_READY", ready)
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
	// Ordering: identical provably-live gate as the custom-cause test,
	// so this control pins the genuine live-cancellation oracle without
	// a custom cause in play.
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "cancelcause-stdout", "cancelcause-stderr")
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the live process had started before the cancellation")
		}
		if !result.Cancelled() {
			t.Error("cancelling the still-running direct push must report cancelled")
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
		if !strings.Contains(string(result.Stdout()), "cancelcause-stdout") || !strings.Contains(string(result.Stderr()), "cancelcause-stderr") {
			t.Errorf("live-killed push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("cancelled push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after the plain live kill although cancellation must bound the inherited-descriptor drain")
	}
}
