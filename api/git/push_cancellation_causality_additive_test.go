package git

// Additive regression coverage for phase 1.1.1 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md.
//
// The frozen api/git/commit_test.go pins the ordered independent signal
// failure, the exit-before-notification case, the still-live kill, the
// plain zero/nonzero late-drain outcomes and the pre-start refusal. The
// revision, remediation and round-1 files pin the SIGKILL orderings, the
// probe-filter unit semantics and one failing-ps variant. The tests below
// add only the orderings step 1 still names: completed nonzero/stderr and
// signalled ps diagnostics on the fallback path while the direct child is
// alive (unknown liveness must not suppress the live kill), positive
// zombie evidence observed directly through the probe, a nonzero exit
// reaped before the completion notification is consumed, repeated
// normal-return descriptor cleanup without relying on garbage collection,
// a volume-retention bound on the inherited-descriptor drain, and
// guard-time pre-Start cancellation. Same-package helpers
// (gitCommitUnderTestWithFakeGit, pushRouteUnderTest,
// argvCarriesOperationArgs, waitForTestFile, waitForRegressionPushLines,
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
	"syscall"
	"testing"
	"time"
)

// ------------------------------------
//
//	Count the process's currently open file descriptors through the
//	platform enumeration, so owned-pipe cleanup is observed directly
//	instead of through garbage collection. The listing descriptor
//	itself is opened and closed symmetrically around every
//	measurement, so only the delta between two measurements matters.
//
// ------------------------------------
func countOpenFDsForCausality(t *testing.T) int {
	t.Helper()
	for _, dir := range []string{"/proc/self/fd", "/dev/fd"} {
		entries, err := os.ReadDir(dir)
		if err == nil {
			return len(entries)
		}
	}
	t.Skip("no file-descriptor enumeration available on this platform")
	return 0
}

// ------------------------------------
//
//	Run one push with a bounded wait so a regression that never
//	terminates the direct child fails the test instead of hanging
//	the suite.
//
// ------------------------------------
func gitPushWithCausalityTimeout(t *testing.T, gc *GitCommit, ctx context.Context, route GitPushRoute, timeout time.Duration, what string) GitPushResult {
	t.Helper()
	done := make(chan GitPushResult, 1)
	go func() { done <- gc.GitPush(ctx, route) }()
	select {
	case result := <-done:
		return result
	case <-time.After(timeout):
		t.Fatalf("GitPush did not return %s", what)
		return GitPushResult{}
	}
}

func TestGitPushLiveKillWithNonzeroPsDiagnosticStaysCancelled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, gitPath := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
    push)
      echo "psdiag-stdout"
      echo "psdiag-stderr" >&2
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
	// A completed ps diagnostic that exits nonzero with stderr proves
	// nothing about the target: only a clean unknown-PID report is
	// verified absence. The liveness probe must treat this as unknown
	// and the cancellation must still terminate the live child.
	if err := os.WriteFile(filepath.Join(filepath.Dir(gitPath), "ps"), []byte("#!/bin/sh\necho \"ps: cannot examine process $*\" >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("shadowing the ps diagnostic: %v", err)
	}
	ready := filepath.Join(root, "psdiag-nonzero-ready")
	releaseDescendant := filepath.Join(root, "psdiag-nonzero-release-descendant")
	descendantDone := filepath.Join(root, "psdiag-nonzero-descendant-done")
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
	// Ordering: the direct child is provably running (ready file plus
	// both pre-exec lines consumed through the live progress buffers)
	// while a descendant already holds the inherited write ends, so the
	// cancellation demonstrably targets a still-live direct push even
	// though the ps diagnostic itself reports failure.
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "psdiag-stdout", "psdiag-stderr")
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the live process had started before the cancellation")
		}
		if !result.Cancelled() {
			t.Error("a nonzero ps diagnostic must not suppress the live kill: killing the still-running direct push must report cancelled")
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
		if !strings.Contains(string(result.Stdout()), "psdiag-stdout") || !strings.Contains(string(result.Stderr()), "psdiag-stderr") {
			t.Errorf("live-killed push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("cancelled push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after cancelling the live push with a nonzero ps diagnostic")
	}
}

func TestGitPushLiveKillWithSignalledPsDiagnosticStaysCancelled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, gitPath := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
    push)
      echo "pssig-stdout"
      echo "pssig-stderr" >&2
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
	// A ps diagnostic terminated by a signal never completed its report:
	// the target's liveness stays unknown and the live kill must proceed.
	if err := os.WriteFile(filepath.Join(filepath.Dir(gitPath), "ps"), []byte("#!/bin/sh\nkill -TERM $$\n"), 0o755); err != nil {
		t.Fatalf("shadowing the ps diagnostic: %v", err)
	}
	ready := filepath.Join(root, "pssig-ready")
	releaseDescendant := filepath.Join(root, "pssig-release-descendant")
	descendantDone := filepath.Join(root, "pssig-descendant-done")
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
	// Ordering: provably running direct child (ready file plus both
	// pre-exec lines) with a descendant holding the inherited write
	// ends, so the cancellation demonstrably targets a still-live
	// direct push while the ps diagnostic itself dies by signal.
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "pssig-stdout", "pssig-stderr")
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the live process had started before the cancellation")
		}
		if !result.Cancelled() {
			t.Error("a signalled ps diagnostic must not suppress the live kill: killing the still-running direct push must report cancelled")
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
		if !strings.Contains(string(result.Stdout()), "pssig-stdout") || !strings.Contains(string(result.Stderr()), "pssig-stderr") {
			t.Errorf("live-killed push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("cancelled push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after cancelling the live push with a signalled ps diagnostic")
	}
}

func TestPushDirectProcessAlreadyExitedDetectsPositiveZombieEvidence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the POSIX zombie/vanished-PID probe is undeterminable on Windows by design")
	}
	if pushDirectProcessAlreadyExited(0) || pushDirectProcessAlreadyExited(-1) {
		t.Error("pushDirectProcessAlreadyExited(<=0) = true, want false for the absent PID so a genuine live kill is still reported as cancelled")
	}
	// A provably live child must not read as already exited; otherwise
	// the handshake would suppress a genuine cancellation claim.
	live := exec.Command("sleep", "30")
	if err := live.Start(); err != nil {
		t.Fatalf("starting the live probe process: %v", err)
	}
	defer func() {
		_ = live.Process.Kill()
		_ = live.Wait()
	}()
	if pushDirectProcessAlreadyExited(live.Process.Pid) {
		t.Error("a live child reads as already exited, which would suppress a genuine live-kill cancellation")
		return
	}
	// An exited but unreaped child is positive zombie evidence: the
	// probe must report it exited so a Kill that lands after
	// independent termination cannot claim causation. The exit races
	// the first observation, so poll for the zombie state.
	exited := exec.Command("true")
	if err := exited.Start(); err != nil {
		t.Fatalf("starting the exited probe process: %v", err)
	}
	exitedPid := exited.Process.Pid
	defer func() { _ = exited.Wait() }()
	exitedDeadline := time.Now().Add(5 * time.Second)
	for !pushDirectProcessAlreadyExited(exitedPid) && time.Now().Before(exitedDeadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !pushDirectProcessAlreadyExited(exitedPid) {
		t.Errorf("an exited unreaped PID %d still reads as live, so a Kill against an already dead child could be mistaken for causation", exitedPid)
	}
	// An independently signal-terminated unreaped child is the same
	// positive evidence through the non-SIGKILL signal path.
	signalled := exec.Command("sleep", "30")
	if err := signalled.Start(); err != nil {
		t.Fatalf("starting the signalled probe process: %v", err)
	}
	signalledPid := signalled.Process.Pid
	defer func() { _ = signalled.Wait() }()
	if err := signalled.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signalling the probe process: %v", err)
	}
	signalledDeadline := time.Now().Add(5 * time.Second)
	for !pushDirectProcessAlreadyExited(signalledPid) && time.Now().Before(signalledDeadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !pushDirectProcessAlreadyExited(signalledPid) {
		t.Errorf("a SIGTERM-terminated unreaped PID %d still reads as live", signalledPid)
	}
}

func TestGitPushNonzeroExitReapedBeforeNotificationSurvivesLateCancellation(t *testing.T) {
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
      echo "reaped-nonzero-stdout"
      echo "reaped-nonzero-stderr" >&2
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
      exit 3
      ;;
  esac
done
exit 0
`)
	parentDead := filepath.Join(root, "reaped-nonzero-parent-dead")
	parentReaped := filepath.Join(root, "reaped-nonzero-parent-reaped")
	releaseDescendant := filepath.Join(root, "reaped-nonzero-release-descendant")
	descendantDone := filepath.Join(root, "reaped-nonzero-descendant-done")
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
	// Ordering: independent nonzero death (parent-dead), then PID
	// disappearance proving the single reaper already returned and the
	// completion is buffered, all while the descendant holds the
	// inherited write ends so the drain cannot reach EOF (completion
	// notification) before the late cancellation bounds it. Both
	// readers consume the pre-exit bytes first so the retained-stream
	// oracle is not raced by the cancel-time pipe close.
	waitForTestFile(t, parentDead)
	waitForTestFile(t, parentReaped)
	waitForRegressionPushLines(t, gitCommit, "reaped-nonzero-stdout", "reaped-nonzero-stderr")
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the failed process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("late drain cancellation relabelled the reaped independently failed nonzero process as cancelled")
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
			t.Errorf("Err() = %v, must not contain the late context cancellation", result.Err())
		}
		if result.Success() {
			t.Error("a nonzero exit must not be reported as a success")
		}
		if !strings.Contains(string(result.Stdout()), "reaped-nonzero-stdout") || !strings.Contains(string(result.Stderr()), "reaped-nonzero-stderr") {
			t.Errorf("nonzero push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("nonzero push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the reaped nonzero drain")
	}
}

func TestGitPushRepeatedSuccessfulPushesCloseOwnedDescriptors(t *testing.T) {
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
      echo "repeat-stdout"
      echo "repeat-stderr" >&2
      exit 0
      ;;
  esac
done
exit 0
`)
	// Ordinary completion with a context that never cancels must still
	// close both owned pipe read ends: the measurement never triggers
	// garbage collection, so finalizer-closed descriptors cannot hide
	// a leak. Six pushes leaking two read ends each would grow the
	// table by twelve; runtime noise stays within the small slack.
	before := countOpenFDsForCausality(t)
	for i := 0; i < 6; i++ {
		result := gitPushWithCausalityTimeout(t, gitCommit, context.Background(), pushRouteUnderTest("master"), 10*time.Second, "after the ordinary successful completion")
		if !result.Success() {
			t.Fatalf("push %d did not succeed: exit %d, cancelled %v, err %v", i, result.ExitCode(), result.Cancelled(), result.Err())
		}
		if !strings.Contains(string(result.Stdout()), "repeat-stdout") || !strings.Contains(string(result.Stderr()), "repeat-stderr") {
			t.Fatalf("push %d lost captured streams: stdout %q, stderr %q", i, result.Stdout(), result.Stderr())
		}
	}
	if delta := countOpenFDsForCausality(t) - before; delta > 4 {
		t.Errorf("open file descriptors grew by %d across six ordinary successful pushes, want both owned pipe read ends closed on every normal return", delta)
	}
}

func TestGitPushRepeatedFailedPushesCloseOwnedDescriptors(t *testing.T) {
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
      echo "repeat-fail-stdout"
      echo "repeat-fail-stderr" >&2
      exit 3
      ;;
  esac
done
exit 0
`)
	// The failed-completion path owns the same descriptors as the
	// successful one: repeated nonzero exits must not accumulate
	// open read ends either.
	before := countOpenFDsForCausality(t)
	for i := 0; i < 6; i++ {
		result := gitPushWithCausalityTimeout(t, gitCommit, context.Background(), pushRouteUnderTest("master"), 10*time.Second, "after the ordinary failed completion")
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Fatalf("push %d did not start", i)
		}
		if result.Cancelled() {
			t.Fatalf("push %d without cancellation must not report cancelled", i)
		}
		if result.ExitCode() != 3 {
			t.Fatalf("push %d ExitCode() = %d, want the direct Wait status 3", i, result.ExitCode())
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Fatalf("push %d Err() = %v, want the direct process *exec.ExitError", i, result.Err())
		}
	}
	if delta := countOpenFDsForCausality(t) - before; delta > 4 {
		t.Errorf("open file descriptors grew by %d across six ordinary failed pushes, want both owned pipe read ends closed on every normal return", delta)
	}
}

func TestGitPushCancellationBoundsInheritedDrainAndRetainsPreCloseBytes(t *testing.T) {
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
        echo "vol-stdout-$i"
        echo "vol-stderr-$i" >&2
        i=$((i + 1))
      done
      touch "$FAKE_GIT_VOLUME_COMPLETE"
      exec sleep 30
      ;;
  esac
done
exit 0
`)
	descendantStarted := filepath.Join(root, "vol-descendant-started")
	volumeComplete := filepath.Join(root, "vol-volume-complete")
	releaseDescendant := filepath.Join(root, "vol-release-descendant")
	descendantDone := filepath.Join(root, "vol-descendant-done")
	t.Setenv("FAKE_GIT_DESCENDANT_STARTED", descendantStarted)
	t.Setenv("FAKE_GIT_VOLUME_COMPLETE", volumeComplete)
	t.Setenv("FAKE_GIT_RELEASE_DESCENDANT", releaseDescendant)
	t.Setenv("FAKE_GIT_DESCENDANT_DONE", descendantDone)
	// Conditional teardown releases only a descendant proven to exist:
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
	// now safe) while the direct child stays alive in exec sleep 30;
	// 2. the shell finished the full 500-line loop before any
	// cancellation, so the asserted volume cannot be raced;
	// 3. both readers provably consumed the final lines of both
	// streams through the live progress buffers, so the retained-byte
	// assertions below match explicitly observed progress (>1024
	// bytes per stream: 500 lines at ~13 bytes each).
	waitForTestFile(t, descendantStarted)
	waitForTestFile(t, volumeComplete)
	waitForRegressionPushLines(t, gitCommit, "vol-stdout-500", "vol-stderr-500")
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
		if !errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, want the context cancellation cause for the live kill", result.Err())
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the reaped process *exec.ExitError alongside the context cause", result.Err())
		} else if result.ExitCode() != exitErr.ExitCode() {
			t.Errorf("ExitCode() = %d, want the reaped process status %d", result.ExitCode(), exitErr.ExitCode())
		}
		if result.Success() {
			t.Error("a cancelled push must not be reported as a success")
		}
		// Retained bytes match the progress observed before
		// cancellation: both the head and the final lines of the
		// 500-line volume on each stream, plus the >1024-byte bound
		// that volume implies.
		if !strings.Contains(string(result.Stdout()), "vol-stdout-1") || !strings.Contains(string(result.Stdout()), "vol-stdout-500") {
			t.Errorf("cancelled push lost ordered stdout volume: %d bytes retained", len(result.Stdout()))
		}
		if !strings.Contains(string(result.Stderr()), "vol-stderr-1") || !strings.Contains(string(result.Stderr()), "vol-stderr-500") {
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

func TestGitPushGuardTimeCancellationIsUnstartedAndCancelled(t *testing.T) {
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
      touch "$FAKE_GIT_PUSH_RAN"
      echo "must-not-run"
      exit 0
      ;;
  esac
done
exit 0
`)
	pushRan := filepath.Join(root, "guard-push-ran")
	t.Setenv("FAKE_GIT_PUSH_RAN", pushRan)

	// The guard observes a context cancelled while it holds the
	// pre-Start gate: the attempt must stay unstarted, report
	// cancelled with exit -1, and never launch the push process.
	guardEntered := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	route := pushRouteUnderTest("master")
	route.ActiveGuard = func() error {
		select {
		case guardEntered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	}
	before := countOpenFDsForCausality(t)
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, route) }()
	select {
	case <-guardEntered:
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("GitPush did not reach the execution guard")
	}
	cancel()

	select {
	case result := <-done:
		if result.Started() {
			t.Error("a guard-time cancellation must not start the prepared process")
		}
		if !result.Cancelled() {
			t.Error("guard-time cancellation must report cancelled with no exit status rather than a setup outcome")
		}
		if result.ExitCode() != -1 {
			t.Errorf("ExitCode() = %d, want -1 for the absent process status", result.ExitCode())
		}
		if !errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, want the guard-observed context cancellation", result.Err())
		}
		if result.Success() {
			t.Error("a guard-time cancellation must not be reported as a success")
		}
		if len(result.Argv()) != 0 {
			t.Errorf("Argv() = %v, want no command retained after a guard-time cancellation", result.Argv())
		}
		if _, err := os.Stat(pushRan); err == nil {
			t.Error("the push process launched although the guard observed the cancellation before Start")
		} else if !os.IsNotExist(err) {
			t.Errorf("checking the push launch marker: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after the guard-time cancellation")
	}
	if delta := countOpenFDsForCausality(t) - before; delta > 2 {
		t.Errorf("open file descriptors grew by %d across the guard-time refusal, want all setup pipe ends closed", delta)
	}
}

func TestGitPushPreCancelledContextWithGuardStartsNothing(t *testing.T) {
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
      touch "$FAKE_GIT_PUSH_RAN"
      echo "must-not-run"
      exit 0
      ;;
  esac
done
exit 0
`)
	pushRan := filepath.Join(root, "precancel-push-ran")
	t.Setenv("FAKE_GIT_PUSH_RAN", pushRan)

	// A context already cancelled before the attempt reaches Start is
	// an unstarted cancellation even with an execution guard present:
	// no process status exists and no push may launch.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	route := pushRouteUnderTest("master")
	route.ActiveGuard = func() error { return nil }
	result := gitPushWithCausalityTimeout(t, gitCommit, ctx, route, 10*time.Second, "after the pre-Start cancellation")

	if result.Started() {
		t.Error("a pre-Start cancellation must not start the prepared process")
	}
	if !result.Cancelled() {
		t.Error("a pre-Start cancellation must report cancelled with no exit status")
	}
	if result.ExitCode() != -1 {
		t.Errorf("ExitCode() = %d, want -1 for the absent process status", result.ExitCode())
	}
	if !errors.Is(result.Err(), context.Canceled) {
		t.Errorf("Err() = %v, want the pre-Start context cancellation", result.Err())
	}
	if result.Success() {
		t.Error("a pre-Start cancellation must not be reported as a success")
	}
	if _, err := os.Stat(pushRan); err == nil {
		t.Error("the push process launched although the context was already cancelled before Start")
	} else if !os.IsNotExist(err) {
		t.Errorf("checking the push launch marker: %v", err)
	}
}

func TestGitPushOrdinaryZeroExitRetainsSuccessContract(t *testing.T) {
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
      echo "plain-stdout"
      echo "plain-stderr" >&2
      exit 0
      ;;
  esac
done
exit 0
`)
	// The ordinary zero-exit completion without cancellation or
	// capture failure anchors the Success contract the late-drain
	// tests must preserve: started, uncancelled, exit 0, no error.
	result := gitPushWithCausalityTimeout(t, gitCommit, context.Background(), pushRouteUnderTest("master"), 10*time.Second, "after the ordinary zero-exit completion")
	if !result.Started() {
		t.Error("the completed process started, so Started() must be true")
	}
	if result.Cancelled() {
		t.Error("an ordinary completion without cancellation must not report cancelled")
	}
	if result.ExitCode() != 0 {
		t.Errorf("ExitCode() = %d, want the direct Wait status 0", result.ExitCode())
	}
	if !result.Success() {
		t.Error("an uncancelled zero-exit push must keep Success() == true")
	}
	if result.Err() != nil {
		t.Errorf("Err() = %v, want nil for the clean ordinary completion", result.Err())
	}
	if !strings.Contains(string(result.Stdout()), "plain-stdout") || !strings.Contains(string(result.Stderr()), "plain-stderr") {
		t.Errorf("ordinary push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
	}
	if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
		t.Errorf("ordinary push argv = %v, want the tracked-branch operation arguments", result.Argv())
	}
}

func TestGitPushCausalityResultAccessorsReturnDefensiveCopies(t *testing.T) {
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
      exit 0
      ;;
  esac
done
exit 0
`)
	result := gitPushWithCausalityTimeout(t, gitCommit, context.Background(), pushRouteUnderTest("master"), 10*time.Second, "after the ordinary successful completion")
	if !result.Success() {
		t.Fatalf("the push did not succeed: exit %d, cancelled %v, err %v", result.ExitCode(), result.Cancelled(), result.Err())
	}
	argv := result.Argv()
	if len(argv) == 0 {
		t.Fatal("Argv() = [], want the executed push argv")
	}
	argv[0] = "mutated"
	if got := result.Argv()[0]; got == "mutated" {
		t.Errorf("a second Argv() call returned the mutated copy, want a fresh defensive copy")
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
}

func TestGitPushStaleGuardWithConcurrentCancellationJoinsBothCauses(t *testing.T) {
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
      touch "$FAKE_GIT_PUSH_RAN"
      echo "must-not-run"
      exit 0
      ;;
  esac
done
exit 0
`)
	pushRan := filepath.Join(root, "stale-guard-cancel-push-ran")
	t.Setenv("FAKE_GIT_PUSH_RAN", pushRan)

	// R5: the guard refuses with a stale-generation error while the
	// context is cancelled during the guard hold. The attempt must stay
	// unstarted and cancelled with exit -1, joining both causes, and
	// must close every setup pipe end without launching the push.
	// Ordering is explicit: the guard signals entry, the test cancels,
	// the guard observes the cancellation before returning its refusal.
	guardErr := errors.New("the git operations generation changed after the push was confirmed")
	guardEntered := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	route := pushRouteUnderTest("master")
	route.ActiveGuard = func() error {
		select {
		case guardEntered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return guardErr
	}
	before := countOpenFDsForCausality(t)
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, route) }()
	select {
	case <-guardEntered:
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("GitPush did not reach the execution guard")
	}
	cancel()

	select {
	case result := <-done:
		if result.Started() {
			t.Error("a stale-guard refusal during cancellation must not start the prepared process")
		}
		if !result.Cancelled() {
			t.Error("a stale-guard refusal that observes context cancellation must report cancelled rather than a plain setup refusal")
		}
		if result.ExitCode() != -1 {
			t.Errorf("ExitCode() = %d, want -1 for the absent process status", result.ExitCode())
		}
		if !errors.Is(result.Err(), guardErr) {
			t.Errorf("Err() = %v, want the stale-generation guard refusal joined into the result", result.Err())
		}
		if !errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, want the guard-observed context cancellation joined into the result", result.Err())
		}
		if result.Success() {
			t.Error("a stale-guard cancellation must not be reported as a success")
		}
		if len(result.Argv()) != 0 {
			t.Errorf("Argv() = %v, want no command retained after a guard-time cancellation", result.Argv())
		}
		if _, err := os.Stat(pushRan); err == nil {
			t.Error("the push process launched although the guard refused the attempt before Start")
		} else if !os.IsNotExist(err) {
			t.Errorf("checking the push launch marker: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after the stale-guard cancellation")
	}
	if delta := countOpenFDsForCausality(t) - before; delta > 2 {
		t.Errorf("open file descriptors grew by %d across the stale-guard cancellation, want all setup pipe ends closed", delta)
	}
}

func TestGitPushSetupCancellationWithoutGuardStartsNothing(t *testing.T) {
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
      touch "$FAKE_GIT_PUSH_RAN"
      echo "must-not-run"
      exit 0
      ;;
  esac
done
exit 0
`)
	pushRan := filepath.Join(root, "setup-cancel-no-guard-push-ran")
	t.Setenv("FAKE_GIT_PUSH_RAN", pushRan)

	// R5 without an execution guard: a context already cancelled in
	// setup before Start produces an unstarted cancelled result with
	// exit -1 and launches no push. No timing is involved: the
	// cancellation precedes the call, so no descendant or sleep exists.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := countOpenFDsForCausality(t)
	result := gitPushWithCausalityTimeout(t, gitCommit, ctx, pushRouteUnderTest("master"), 10*time.Second, "after the setup cancellation without a guard")

	if result.Started() {
		t.Error("a setup cancellation must not start the prepared process")
	}
	if !result.Cancelled() {
		t.Error("a setup cancellation must report cancelled with no exit status rather than a setup outcome")
	}
	if result.ExitCode() != -1 {
		t.Errorf("ExitCode() = %d, want -1 for the absent process status", result.ExitCode())
	}
	if !errors.Is(result.Err(), context.Canceled) {
		t.Errorf("Err() = %v, want the setup-observed context cancellation", result.Err())
	}
	if result.Success() {
		t.Error("a setup cancellation must not be reported as a success")
	}
	if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
		t.Errorf("setup-cancelled push argv = %v, want the recorded process identity retained for the unstarted attempt", result.Argv())
	}
	if _, err := os.Stat(pushRan); err == nil {
		t.Error("the push process launched although the context was already cancelled in setup before Start")
	} else if !os.IsNotExist(err) {
		t.Errorf("checking the push launch marker: %v", err)
	}
	if delta := countOpenFDsForCausality(t) - before; delta > 2 {
		t.Errorf("open file descriptors grew by %d across the setup cancellation, want all setup pipe ends closed", delta)
	}
}
