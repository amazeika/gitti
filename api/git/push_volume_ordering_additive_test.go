package git

// Ordered sibling regression for finding R2
// (api/git/push_cancellation_causality_additive_test.go volume-drain test
// cancels before its asserted prerequisites).
//
// The frozen volume-drain fixture starts its descriptor-holding descendant
// only after the 500-line loop yet waits only for vol-stdout-1 and
// vol-stderr-1 before cancelling, then demands >1024 retained bytes on both
// streams plus the descendant-done marker. Cancellation can therefore kill
// the shell before the loop finishes (retained bytes below the asserted
// bound) or before the descendant launches (teardown waits for a marker
// that will never be created).
//
// This sibling does NOT repair that frozen fixture, which stays
// byte-for-byte unchanged: a green run here must never be reported as
// having fixed R2. The residual frozen-fixture race remains an unresolved
// gap requiring human policy follow-up. This file only proves the ordered
// shape a volume-drain cancellation must have: descendant readiness and
// sufficient two-stream progress are both signalled before cancellation,
// retained-byte assertions match the amount explicitly observed, the drain
// stays bounded despite held-open descriptors, and teardown releases only
// the descendant proven to exist. Explicit file/line ordering signals only;
// no wall-clock sleeps, no leaked descendant processes.
//
// Reuses same-package helpers (gitCommitUnderTestWithFakeGit,
// pushRouteUnderTest, argvCarriesOperationArgs, waitForTestFile,
// waitForRegressionPushLines, releaseRegressionDescendantAndRequireExit)
// and defines no new helpers.

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

func TestGitPushVolumeDrainOrdersDescendantAndProgressBeforeCancel(t *testing.T) {
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
      ( touch "$FAKE_GIT_DESCENDANT_STARTED"
        while [ ! -e "$FAKE_GIT_RELEASE_DESCENDANT" ]; do :; done
        touch "$FAKE_GIT_DESCENDANT_DONE"
      ) &
      i=1
      while [ "$i" -le 500 ]; do
        echo "vol-ordered-stdout-$i"
        echo "vol-ordered-stderr-$i" >&2
        i=$((i + 1))
      done
      touch "$FAKE_GIT_VOLUME_COMPLETE"
      exec sleep 30
      ;;
  esac
done
exit 0
`)
	descendantStarted := filepath.Join(root, "vol-ordered-descendant-started")
	volumeComplete := filepath.Join(root, "vol-ordered-volume-complete")
	releaseDescendant := filepath.Join(root, "vol-ordered-release-descendant")
	descendantDone := filepath.Join(root, "vol-ordered-descendant-done")
	t.Setenv("FAKE_GIT_DESCENDANT_STARTED", descendantStarted)
	t.Setenv("FAKE_GIT_VOLUME_COMPLETE", volumeComplete)
	t.Setenv("FAKE_GIT_RELEASE_DESCENDANT", releaseDescendant)
	t.Setenv("FAKE_GIT_DESCENDANT_DONE", descendantDone)
	// Best-effort teardown releases only a descendant proven to exist:
	// when cancellation precedes the descendant launch there is no
	// started marker, so nothing is released and no done marker is
	// awaited. Runs without Fatal so it cannot mask the test failure.
	defer func() {
		if _, err := os.Stat(descendantStarted); err != nil {
			return
		}
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

	// Ordering, each step an explicit signal rather than a sleep:
	// 1. the descendant touched its started marker, so it provably
	// exists and holds the inherited write ends (teardown release is
	// now safe);
	// 2. the shell finished the full 500-line loop before any
	// cancellation, so the volume prerequisite cannot be raced;
	// 3. both readers provably consumed the final lines of both
	// streams through the live progress buffers, so the retained-byte
	// assertions below match explicitly observed progress (>1024
	// bytes per stream: 500 lines at ~22 bytes each).
	waitForTestFile(t, descendantStarted)
	waitForTestFile(t, volumeComplete)
	waitForRegressionPushLines(t, gitCommit, "vol-ordered-stdout-500", "vol-ordered-stderr-500")
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the live process had started before the cancellation")
		}
		if !result.Cancelled() {
			t.Error("killing the still-running direct push must report cancelled even though a descendant holds the inherited descriptors")
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
		// Retained bytes match the progress observed before
		// cancellation: both the head and the final lines of the
		// 500-line volume on each stream, plus the >1024-byte bound
		// that volume implies.
		if !strings.Contains(string(result.Stdout()), "vol-ordered-stdout-1") || !strings.Contains(string(result.Stdout()), "vol-ordered-stdout-500") {
			t.Errorf("cancelled push lost ordered stdout volume: %d bytes retained", len(result.Stdout()))
		}
		if !strings.Contains(string(result.Stderr()), "vol-ordered-stderr-1") || !strings.Contains(string(result.Stderr()), "vol-ordered-stderr-500") {
			t.Errorf("cancelled push lost ordered stderr volume: %d bytes retained", len(result.Stderr()))
		}
		if len(result.Stdout()) <= 1024 || len(result.Stderr()) <= 1024 {
			t.Errorf("cancelled push retained only %d stdout and %d stderr bytes, want the >1024-byte volume observed before cancellation", len(result.Stdout()), len(result.Stderr()))
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("cancelled push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		// The descendant was proven to exist (started marker
		// observed above), so requiring its exit proves no leak.
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after cancellation bounded the inherited-descriptor volume drain")
	}
}
