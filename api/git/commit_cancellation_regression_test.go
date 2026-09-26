package git

// Additive regression coverage for phase 1.1 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md.
//
// The frozen api/git/commit_test.go already pins the ordered independent
// signal failure, the exit-before-notification case, the still-live kill,
// the plain zero/nonzero late-drain outcomes and the pre-start refusal.
// The tests below add only the genuinely missing orderings: a zero-exit
// direct process whose stream capture already failed before a late drain
// cancellation (Success must survive for the success-only reconciliation
// path), a still-live kill while an inherited descriptor holder keeps the
// pipes open (cancellation must still be bounded and retain the reaped
// status), and the immutability of the result accessors. Same-package
// helpers (gitCommitUnderTestWithFakeGit, pushRouteUnderTest,
// argvCarriesOperationArgs, waitForTestFile) are reused from the frozen
// file and are not redefined here.

import (
	"bufio"
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

// ------------------------------------
//
//	Block until both live push progress buffers carry the named pre-exit
//	lines, so a later cancellation cannot race the retained-stream oracle
//	by closing the read ends before the readers consumed asserted bytes.
//	An empty want matches immediately for streams that never surface as
//	live lines (for example an over-long scanner token).
//
// ------------------------------------
func waitForRegressionPushLines(t *testing.T, gc *GitCommit, wantStdout, wantStderr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		stdoutLines, stderrLines := gc.GitRemotePushOutput()
		haveStdout := wantStdout == ""
		haveStderr := wantStderr == ""
		for _, line := range stdoutLines {
			if wantStdout != "" && strings.Contains(line, wantStdout) {
				haveStdout = true
				break
			}
		}
		for _, line := range stderrLines {
			if wantStderr != "" && strings.Contains(line, wantStderr) {
				haveStderr = true
				break
			}
		}
		if haveStdout && haveStderr {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for push progress lines stdout %q stderr %q", wantStdout, wantStderr)
}

// ------------------------------------
//
//	Release a descriptor-holding descendant and require it to exit, so a
//	test that ordered on its held-open pipes also proves it leaks no
//	descendant process.
//
// ------------------------------------
func releaseRegressionDescendantAndRequireExit(t *testing.T, release, done string) {
	t.Helper()
	if err := os.WriteFile(release, []byte{}, 0o600); err != nil {
		t.Fatalf("releasing the descendant holder: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(done); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("the descendant did not exit after its release, suggesting a leaked process still holding push descriptors (missing %q)", done)
}

func TestGitPushZeroExitCaptureFailureSurvivesLateDrainCancellation(t *testing.T) {
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
      echo "zero-stdout"
      { head -c 100000 /dev/zero | tr '\0' 'y'; } >&2
      parent=$$
      ( while kill -0 "$parent" 2>/dev/null; do
          state=$(ps -o stat= -p "$parent" 2>/dev/null)
          case "$state" in Z*) break ;; esac
        done
        touch "$FAKE_GIT_PARENT_DEAD"
        while [ ! -e "$FAKE_GIT_RELEASE_DESCENDANT" ]; do :; done
        touch "$FAKE_GIT_DESCENDANT_DONE"
      ) &
      exit 0
      ;;
  esac
done
exit 0
`)
	parentDead := filepath.Join(root, "capture-parent-dead")
	releaseDescendant := filepath.Join(root, "capture-release-descendant")
	descendantDone := filepath.Join(root, "capture-descendant-done")
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
	// The direct child exited zero while the descendant keeps both inherited
	// write ends open, so the drain cannot reach EOF on its own. The stdout
	// line is provably consumed before the late cancellation bounds that
	// drain; the oversized stderr token never surfaces as a live line, so
	// only stdout is gated.
	waitForTestFile(t, parentDead)
	waitForRegressionPushLines(t, gitCommit, "zero-stdout", "")
	cancel()

	select {
	case result := <-done:
		if !result.Started() {
			t.Error("the zero-exit process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("late drain cancellation relabelled the independently completed zero-exit process as cancelled")
		}
		if result.ExitCode() != 0 {
			t.Errorf("ExitCode() = %d, want the direct Wait status 0", result.ExitCode())
		}
		if !result.Success() {
			t.Error("late drain cancellation of a zero-exit push must keep Success() == true so the push still takes the success-only reconciliation path")
		}
		if result.Err() == nil {
			t.Error("Err() = nil, want the retained stderr capture failure recorded alongside the zero exit")
		} else {
			if !errors.Is(result.Err(), bufio.ErrTooLong) {
				t.Errorf("Err() = %v, want the stderr stream capture failure retained with the zero exit", result.Err())
			}
			if errors.Is(result.Err(), context.Canceled) {
				t.Errorf("Err() = %v, must not contain the late drain cancellation", result.Err())
			}
		}
		if !strings.Contains(string(result.Stdout()), "zero-stdout") {
			t.Errorf("zero-exit push lost its stdout: %q", string(result.Stdout()))
		}
		if len(result.Stderr()) == 0 {
			t.Error("zero-exit push retained no stderr bytes despite the oversized stream")
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("zero-exit push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the zero-exit drain")
	}
}

func TestGitPushLiveKillWithInheritedDescriptorsStaysCancelled(t *testing.T) {
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
      echo "cancel-stdout"
      echo "cancel-stderr" >&2
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
	ready := filepath.Join(root, "live-kill-ready")
	releaseDescendant := filepath.Join(root, "live-kill-release-descendant")
	descendantDone := filepath.Join(root, "live-kill-descendant-done")
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
	// The direct child is provably running (it printed both pre-exec lines)
	// while the backgrounded descendant already holds the inherited write
	// ends, so the cancellation must both terminate the live process and
	// bound a drain that can never reach EOF on its own.
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "cancel-stdout", "cancel-stderr")
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
		if !strings.Contains(string(result.Stdout()), "cancel-stdout") || !strings.Contains(string(result.Stderr()), "cancel-stderr") {
			t.Errorf("live-killed push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("cancelled push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after the live kill although cancellation must bound the inherited-descriptor drain")
	}
}

func TestGitPushSignalFailureWithStderrCaptureFailureSurvivesLateCancellation(t *testing.T) {
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
      { head -c 100000 /dev/zero | tr '\0' 'y'; } >&2
      echo "sigcap-stdout"
      parent=$$
      ( while kill -0 "$parent" 2>/dev/null; do
          state=$(ps -o stat= -p "$parent" 2>/dev/null)
          case "$state" in Z*) break ;; esac
        done
        touch "$FAKE_GIT_PARENT_DEAD"
        while [ ! -e "$FAKE_GIT_RELEASE_DESCENDANT" ]; do :; done
        touch "$FAKE_GIT_DESCENDANT_DONE"
      ) &
      kill -TERM "$parent"
      ;;
  esac
done
exit 0
`)
	parentDead := filepath.Join(root, "sigcap-parent-dead")
	releaseDescendant := filepath.Join(root, "sigcap-release-descendant")
	descendantDone := filepath.Join(root, "sigcap-descendant-done")
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
	// The direct child wrote the oversized stderr token before the stdout
	// line (single-writer order), then died by its own signal while the
	// descendant keeps both inherited write ends open, so the drain cannot
	// reach EOF on its own. The consumed stdout line proves the stderr
	// bytes entered the pipe ahead of it, hence the scanner capture
	// failure is ordered before the late cancellation that bounds the
	// still-open drain.
	waitForTestFile(t, parentDead)
	waitForRegressionPushLines(t, gitCommit, "sigcap-stdout", "")
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
				t.Errorf("ExitCode() = 0, want the nonzero signal status, not a clean exit")
			}
		}
		if result.Err() == nil {
			t.Error("Err() = nil, want the signal wait error joined with the stderr capture failure")
		} else {
			if !errors.Is(result.Err(), bufio.ErrTooLong) {
				t.Errorf("Err() = %v, want the stderr stream capture failure retained alongside the signal error", result.Err())
			}
			if errors.Is(result.Err(), context.Canceled) {
				t.Errorf("Err() = %v, must not contain the late drain cancellation", result.Err())
			}
		}
		if result.Success() {
			t.Error("a signal-terminated push must not be reported as a success")
		}
		if !strings.Contains(string(result.Stdout()), "sigcap-stdout") {
			t.Errorf("signal failure lost its stdout: %q", string(result.Stdout()))
		}
		if len(result.Stderr()) == 0 {
			t.Error("signal failure retained no stderr bytes despite the oversized stream")
		} else if !strings.Contains(string(result.Stderr()[:1]), "y") {
			t.Errorf("signal failure stderr does not carry the oversized stream bytes: %q", string(result.Stderr()[:16]))
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("signal failure push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the signal-terminated drain")
	}
}

func TestGitPushResultAccessorsReturnDefensiveCopies(t *testing.T) {
	gitCommit, _, _, _, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
    push)
      echo "copy-stdout"
      echo "copy-stderr" >&2
      ;;
  esac
done
exit 0
`)

	result := gitCommit.GitPush(context.Background(), pushRouteUnderTest("master"))
	if !result.Success() {
		t.Fatalf("the push did not succeed: exit %d, cancelled %v, err %v", result.ExitCode(), result.Cancelled(), result.Err())
	}

	argv := result.Argv()
	if len(argv) == 0 {
		t.Fatal("Argv() = [], want the executed push argv")
	}
	argv[0] = "mutated"
	if got := gitCommit.GitPushResult().Argv()[0]; got != "git" {
		t.Errorf("stored Argv()[0] = %q after mutating a returned copy, want the git executable", got)
	}
	if got := result.Argv()[0]; got != "git" {
		t.Errorf("a second Argv() call returned %q, want a fresh defensive copy", got)
	}

	stdout := result.Stdout()
	if len(stdout) == 0 {
		t.Fatal("Stdout() is empty, want the retained push stdout")
	}
	stdout[0] ^= 0xff
	if !strings.Contains(string(gitCommit.GitPushResult().Stdout()), "copy-stdout") {
		t.Error("the stored stdout changed through a mutated Stdout() copy")
	}

	stderr := result.Stderr()
	if len(stderr) == 0 {
		t.Fatal("Stderr() is empty, want the retained push stderr")
	}
	stderr[0] ^= 0xff
	if !strings.Contains(string(gitCommit.GitPushResult().Stderr()), "copy-stderr") {
		t.Error("the stored stderr changed through a mutated Stderr() copy")
	}

	outLines, errLines := gitCommit.GitRemotePushOutput()
	if len(outLines) == 0 || len(errLines) == 0 {
		t.Fatalf("the live push buffers are empty: stdout %q stderr %q", outLines, errLines)
	}
	outLines[0] = "mutated"
	errLines[0] = "mutated"
	freshOut, freshErr := gitCommit.GitRemotePushOutput()
	if strings.Contains(freshOut[0], "mutated") || strings.Contains(freshErr[0], "mutated") {
		t.Errorf("the live push buffers changed through returned slices: stdout %q stderr %q", freshOut, freshErr)
	}
}
