package git

// Ordered close-cutoff regression for phase 1.1.1 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md,
// finding code:R2 (queued-byte wording over-promises past the timed close).
//
// What the retained files leave open: the delayed-reader remediation holds
// both readers on a gate before their first read, cancels after the child
// exits, then releases the gate immediately. The readers therefore consume
// the queued bytes inside the 500ms grace window, so the test cannot tell
// the advertised "all queued bytes retained" promise apart from the
// narrower R4 contract ("bytes read before closure retained, drain still
// bounded"). A longer grace period would keep that test green without
// proving anything about consumption.
//
// The tests below order past the actual cutoff instead. Both hold at least
// one reader ungated until the timed close must have fired, anchoring the
// hold on pushQueuedDrainGracePeriod itself (grace plus a scheduling
// margin) rather than on a magic sleep, and prove the cutoff was crossed
// by the resulting closed-read-end stream errors. They pin the narrow R4
// contract only: pre-close reads are retained, the bounded shutdown still
// returns, ordinary Success() survives a late capture failure, and a live
// kill still reports cancelled with cause. Neither test asserts that
// unread queued bytes past the cutoff survive; such an assertion would
// reimpose the over-broad promise this finding removes.
//
// Same-package helpers (gitCommitUnderTestWithFakeGit,
// pushRouteUnderTest, argvCarriesOperationArgs, waitForTestFile,
// releaseRegressionDescendantAndRequireExit) are reused from the retained
// files and are not redefined here.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// cutoffHoldPastGrace is the anchored hold that guarantees the timed close
// has fired while a reader is still gated: the implementation's own grace
// period plus a scheduling margin. It is a lower-bound guarantee, not a
// retention proof; lengthening the grace period keeps the cutoff exercised
// instead of silently voiding it.
func cutoffHoldPastGrace() time.Duration {
	return pushQueuedDrainGracePeriod + time.Second
}

// TestPhase111CloseCutoffTruncatesUnreadButBoundsDrain holds both readers
// past the timed close after a zero-exit child queued its output and a
// descendant kept the pipes open. The push must still return boundedly
// with Success() intact (a late capture failure never relabels a zero
// exit), and the closed-read-end stream errors must be present to prove
// the cutoff path was taken. It deliberately asserts nothing about the
// queued markers: unread bytes past the cutoff are outside the R4 promise.
func TestPhase111CloseCutoffTruncatesUnreadButBoundsDrain(t *testing.T) {
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
      echo "cutoff-unread-stdout"
      echo "cutoff-unread-stderr" >&2
      ( touch "$FAKE_GIT_HOLDER_STARTED"
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
	doneMarker := filepath.Join(root, "cutoff-unread-done")
	holderStarted := filepath.Join(root, "cutoff-unread-holder")
	releaseDescendant := filepath.Join(root, "cutoff-unread-release-descendant")
	descendantDone := filepath.Join(root, "cutoff-unread-descendant-done")
	t.Setenv("FAKE_GIT_DONE", doneMarker)
	t.Setenv("FAKE_GIT_HOLDER_STARTED", holderStarted)
	t.Setenv("FAKE_GIT_RELEASE_DESCENDANT", releaseDescendant)
	t.Setenv("FAKE_GIT_DESCENDANT_DONE", descendantDone)
	defer func() {
		_ = os.WriteFile(releaseDescendant, []byte{}, 0o600)
	}()

	// Hold both readers before their first read: the child provably exits
	// with its bytes still queued and unread in the kernel pipe buffers.
	release := make(chan struct{})
	gitCommit.gitPushDrainPreRead = func() { <-release }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	// Ordered past the cutoff: the exit and the held-open holder are
	// observed first, then the cancellation starts the grace timer while
	// the readers stay gated well beyond its expiry.
	waitForTestFile(t, doneMarker)
	waitForTestFile(t, holderStarted)
	cancel()
	time.Sleep(cutoffHoldPastGrace())
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
			t.Error("a zero exit before late cancellation must retain Success() == true even when the cutoff truncated the drain")
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not contain the late drain cancellation", result.Err())
		}
		if result.Err() == nil || !strings.Contains(result.Err().Error(), "stream") {
			t.Errorf("Err() = %v, want the closed-read-end stream errors proving the readers hit the timed cutoff", result.Err())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("cutoff push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after the past-cutoff reader release although cancellation must bound the inherited-descriptor drain")
	}
}

// TestPhase111CloseCutoffRetainsPreCloseReads lets exactly one stream
// reader proceed while the other stays gated past the timed close, with a
// live direct child holding the drain open. Whichever stream the scheduler
// lets through is observed in the live progress buffers before the cancel,
// proving its bytes were read before closure; those bytes must survive in
// the result alongside the genuine cancellation (cause, reaped status,
// bounded return). The gated stream may be truncated by the cutoff, so no
// full-retention assertion covers it.
func TestPhase111CloseCutoffRetainsPreCloseReads(t *testing.T) {
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
      echo "cutpre-stdout-a"
      echo "cutpre-stdout-b"
      echo "cutpre-stderr-a" >&2
      echo "cutpre-stderr-b" >&2
      touch "$FAKE_GIT_READY"
      ( touch "$FAKE_GIT_HOLDER_STARTED"
        while [ ! -e "$FAKE_GIT_RELEASE_DESCENDANT" ]; do :; done
        touch "$FAKE_GIT_DESCENDANT_DONE"
      ) &
      exec sleep 30
      ;;
  esac
done
exit 0
`)
	ready := filepath.Join(root, "cutpre-ready")
	holderStarted := filepath.Join(root, "cutpre-holder")
	releaseDescendant := filepath.Join(root, "cutpre-release-descendant")
	descendantDone := filepath.Join(root, "cutpre-descendant-done")
	t.Setenv("FAKE_GIT_READY", ready)
	t.Setenv("FAKE_GIT_HOLDER_STARTED", holderStarted)
	t.Setenv("FAKE_GIT_RELEASE_DESCENDANT", releaseDescendant)
	t.Setenv("FAKE_GIT_DESCENDANT_DONE", descendantDone)
	defer func() {
		_ = os.WriteFile(releaseDescendant, []byte{}, 0o600)
	}()

	// Exactly one reader proceeds; the other stays gated past the timed
	// close. The hook carries no stream identity, so the first invocation
	// wins the race and the assertions below follow whichever stream was
	// observed in the live buffers.
	var calls atomic.Int32
	release := make(chan struct{})
	gitCommit.gitPushDrainPreRead = func() {
		if calls.Add(1) == 1 {
			return
		}
		<-release
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	waitForTestFile(t, ready)
	waitForTestFile(t, holderStarted)
	// The observed stream proves bytes were read before closure: its full
	// marker set is visible in the live progress buffers before the
	// cancellation starts the grace timer.
	observedStdout := false
	observedStderr := false
	progressDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(progressDeadline) {
		stdoutLines, stderrLines := gitCommit.GitRemotePushOutput()
		joinedOut := strings.Join(stdoutLines, "\n")
		joinedErr := strings.Join(stderrLines, "\n")
		if strings.Contains(joinedOut, "cutpre-stdout-a") && strings.Contains(joinedOut, "cutpre-stdout-b") {
			observedStdout = true
			break
		}
		if strings.Contains(joinedErr, "cutpre-stderr-a") && strings.Contains(joinedErr, "cutpre-stderr-b") {
			observedStderr = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !observedStdout && !observedStderr {
		t.Fatal("neither stream's markers reached the live progress buffers, so no read-before-closure premise exists for the cutoff")
	}
	cancel()
	time.Sleep(cutoffHoldPastGrace())
	close(release)

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
		if observedStdout {
			if !strings.Contains(string(result.Stdout()), "cutpre-stdout-a") || !strings.Contains(string(result.Stdout()), "cutpre-stdout-b") {
				t.Errorf("bounded drain lost pre-close stdout bytes: %q", string(result.Stdout()))
			}
		}
		if observedStderr {
			if !strings.Contains(string(result.Stderr()), "cutpre-stderr-a") || !strings.Contains(string(result.Stderr()), "cutpre-stderr-b") {
				t.Errorf("bounded drain lost pre-close stderr bytes: %q", string(result.Stderr()))
			}
		}
		if len(result.Stdout()) == 0 && len(result.Stderr()) == 0 {
			t.Error("the bounded drain must retain bytes read before closure on at least the observed stream")
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("cancelled push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after the past-cutoff reader release although cancellation must bound the inherited-descriptor drain")
	}
}
