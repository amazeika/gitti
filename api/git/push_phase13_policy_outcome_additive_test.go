package git

// Independent additive regression coverage for phase 1.3 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md:
//
//	Preserve causal direct-push outcomes and owned-pipe cleanup.
//
// Criterion map (acceptance criteria in spec order):
//  1. independent SIGTERM + late cancel stays observed failure:
//     TestPhase13PolicyIndependentSigtermLateCancelStaysFailure
//  2. live cancel reports cancelled; no-request SIGTERM is failure:
//     TestPhase13PolicyLiveCancelTerminatesStillRunningPush,
//     TestPhase13PolicySigtermWithoutCancelIsFailure
//  3. zero-exit late cancel keeps Success; nonzero/nonmatching never
//     relabelled; pre-start unstarted:
//     TestPhase13PolicyZeroExitLateCancelKeepsSuccess,
//     TestPhase13PolicyNonzeroAndPrestartStayUncancelled
//  4. concurrent drain, bounded shutdown, one reaper, immutable
//     accessors, argv:
//     TestPhase13PolicyCancelledDrainRetainsPreCloseBytes,
//     TestPhase13PolicyResultAccessorsImmutableAndArgv
//  5. ordering signals, no leaks, no wall-clock outcome sleeps:
//     construction of every test below (marker files + progress lines
//     bound every verdict; holders released via t.Cleanup)
//  6. green additive + frozen commit_test.go: this file plus the
//     untouched frozen file
//  7. R3 ps-unknown liveness + SIGKILL observed:
//     TestPhase13PolicyPsFailureStaysUnknownLiveKillProceeds,
//     TestPhase13PolicyIndependentSigkillStaysObserved
//  8. R4 EOF closure + bounded inherited drain:
//     TestPhase13PolicyOrdinaryCompletionClosesPipes,
//     TestPhase13PolicyCancelledDrainRetainsPreCloseBytes
//  9. R5 guard/setup cancel unstarted:
//     TestPhase13PolicyGuardAndSetupCancelStartNothing
//  10. accepted same-SIGTERM overlap policy: the ordered
//     independent-before-cancel tests paired with the live-cancel
//     control below; matching status is not claimed as causal proof
//  11. frozen fixtures: no frozen file is touched by this file
//
// Only long-standing helpers (gitCommitUnderTestWithFakeGit,
// pushRouteUnderTest, argvCarriesOperationArgs) are reused; every other
// helper is defined here with a unique prefix so this file does not
// depend on any other additive file.

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

const p13poProbe = `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
`

func p13poWaitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for synchronization file %q", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func p13poWaitLines(t *testing.T, gc *GitCommit, wantStdout, wantStderr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		stdoutLines, stderrLines := gc.GitRemotePushOutput()
		okOut := wantStdout == ""
		okErr := wantStderr == ""
		for _, line := range stdoutLines {
			if wantStdout != "" && strings.Contains(line, wantStdout) {
				okOut = true
				break
			}
		}
		for _, line := range stderrLines {
			if wantStderr != "" && strings.Contains(line, wantStderr) {
				okErr = true
				break
			}
		}
		if okOut && okErr {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for push progress lines stdout %q stderr %q", wantStdout, wantStderr)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func p13poRelease(t *testing.T, release, done string) {
	t.Helper()
	if err := os.WriteFile(release, []byte{}, 0o600); err != nil {
		t.Fatalf("releasing the descendant holder: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(done); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("descendant did not exit after release (missing %q); possible leaked process", done)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// p13poCleanupHolder releases a started descendant holder and awaits its
// exit before the fixture directory is removed. Registered with t.Cleanup
// (LIFO: runs before the TempDir cleanup registered earlier inside
// gitCommitUnderTestWithFakeGit), so a Fatal or timeout in the test body
// still releases and awaits instead of deleting the release path out from
// under a live holder.
func p13poCleanupHolder(t *testing.T, started, release, done string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := os.Stat(started); err != nil {
			return
		}
		_ = os.WriteFile(release, []byte{}, 0o600)
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(done); err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("descendant holder did not exit after release (missing %q); possible leaked process", done)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

func p13poPush(t *testing.T, gc *GitCommit, ctx context.Context, route GitPushRoute, what string) GitPushResult {
	t.Helper()
	done := make(chan GitPushResult, 1)
	go func() { done <- gc.GitPush(ctx, route) }()
	select {
	case result := <-done:
		return result
	case <-time.After(15 * time.Second):
		t.Fatalf("GitPush did not return %s", what)
		return GitPushResult{}
	}
}

func p13poCountFDs(t *testing.T) int {
	t.Helper()
	for _, dir := range []string{"/proc/self/fd", "/dev/fd"} {
		if entries, err := os.ReadDir(dir); err == nil {
			return len(entries)
		}
	}
	t.Skip("no file-descriptor enumeration available on this platform")
	return 0
}

func TestPhase13PolicyIndependentSigtermLateCancelStaysFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the signal-specific push fixture requires POSIX shell signals")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, p13poProbe+`    push)
      echo "p13po-sig-stdout"
      echo "p13po-sig-stderr" >&2
      parent=$$
      ( touch "$P13PO_SIG_STARTED"; while kill -0 "$parent" 2>/dev/null; do
          state=$(ps -o stat= -p "$parent" 2>/dev/null)
          case "$state" in Z*) break ;; esac
        done
        touch "$P13PO_SIG_DEAD"
        while [ ! -e "$P13PO_SIG_RELEASE" ]; do :; done
        touch "$P13PO_SIG_DONE"
      ) &
      kill -TERM "$parent"
      ;;
  esac
done
exit 0
`)
	dead := filepath.Join(root, "p13po-sig-dead")
	release := filepath.Join(root, "p13po-sig-release")
	done := filepath.Join(root, "p13po-sig-done")
	started := filepath.Join(root, "p13po-sig-started")
	t.Setenv("P13PO_SIG_DEAD", dead)
	t.Setenv("P13PO_SIG_RELEASE", release)
	t.Setenv("P13PO_SIG_DONE", done)
	t.Setenv("P13PO_SIG_STARTED", started)
	p13poCleanupHolder(t, started, release, done)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneCh := make(chan GitPushResult, 1)
	go func() { doneCh <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	// The direct child was signal-terminated while the descendant holds
	// both inherited write ends open; both readers consumed the pre-exit
	// bytes before the late cancellation below.
	p13poWaitFile(t, dead)
	p13poWaitLines(t, gitCommit, "p13po-sig-stdout", "p13po-sig-stderr")
	// Exit before completion notification: the descendant still holds
	// the inherited descriptors, so the drain cannot have reached EOF
	// before the cancellation bounds it.
	if _, err := os.Stat(done); err == nil {
		t.Fatal("the descendant released the inherited descriptors before the cancellation, so the drain could already have completed")
	}
	cancel()

	select {
	case result := <-doneCh:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the signal-terminated process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("late context cancellation relabelled the independently signal-terminated push")
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the real direct Wait *exec.ExitError", result.Err())
		} else if result.ExitCode() != exitErr.ExitCode() {
			t.Errorf("ExitCode() = %d, want the direct Wait status %d", result.ExitCode(), exitErr.ExitCode())
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not contain the late context cancellation", result.Err())
		}
		if result.Success() {
			t.Error("a signal-terminated push must not report success")
		}
		if !strings.Contains(string(result.Stdout()), "p13po-sig-stdout") ||
			!strings.Contains(string(result.Stderr()), "p13po-sig-stderr") {
			t.Errorf("signal-terminated push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		p13poRelease(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the signal drain")
	}
}

func TestPhase13PolicySigtermWithoutCancelIsFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the signal-specific push fixture requires POSIX shell signals")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, p13poProbe+`    push)
      echo "p13po-nonreq-stdout"
      echo "p13po-nonreq-stderr" >&2
      parent=$$
      ( touch "$P13PO_NONREQ_STARTED"; while kill -0 "$parent" 2>/dev/null; do
          state=$(ps -o stat= -p "$parent" 2>/dev/null)
          case "$state" in Z*) break ;; esac
        done
        touch "$P13PO_NONREQ_DEAD"
        while [ ! -e "$P13PO_NONREQ_RELEASE" ]; do :; done
        touch "$P13PO_NONREQ_DONE"
      ) &
      kill -TERM "$parent"
      ;;
  esac
done
exit 0
`)
	dead := filepath.Join(root, "p13po-nonreq-dead")
	release := filepath.Join(root, "p13po-nonreq-release")
	done := filepath.Join(root, "p13po-nonreq-done")
	started := filepath.Join(root, "p13po-nonreq-started")
	t.Setenv("P13PO_NONREQ_DEAD", dead)
	t.Setenv("P13PO_NONREQ_RELEASE", release)
	t.Setenv("P13PO_NONREQ_DONE", done)
	t.Setenv("P13PO_NONREQ_STARTED", started)
	p13poCleanupHolder(t, started, release, done)

	// No cancellation is ever requested: SIGTERM is an observed process
	// failure, never a cancellation. The release is written by a
	// background closer once the dead marker proves the direct child
	// exited, so the verdict depends on signals, not sleeps.
	go func() {
		p13poWaitFile(t, dead)
		_ = os.WriteFile(release, []byte{}, 0o600)
	}()
	result := p13poPush(t, gitCommit, context.Background(), pushRouteUnderTest("master"), "after the SIGTERM without cancellation")
	var exitErr *exec.ExitError
	if !result.Started() {
		t.Error("the signal-terminated process started, so Started() must be true")
	}
	if result.Cancelled() {
		t.Error("SIGTERM without a cancellation request must not report cancelled")
	}
	if !errors.As(result.Err(), &exitErr) {
		t.Errorf("Err() = %v, want the direct process *exec.ExitError", result.Err())
	}
	if errors.Is(result.Err(), context.Canceled) {
		t.Errorf("Err() = %v, must not contain a context-cancellation error", result.Err())
	}
	if !strings.Contains(string(result.Stdout()), "p13po-nonreq-stdout") ||
		!strings.Contains(string(result.Stderr()), "p13po-nonreq-stderr") {
		t.Errorf("SIGTERM push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
	}
	p13poRelease(t, release, done)
}

func TestPhase13PolicyLiveCancelTerminatesStillRunningPush(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the live-cancel fixture requires POSIX exec behavior")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, p13poProbe+`    push)
      echo "p13po-live-stdout"
      echo "p13po-live-stderr" >&2
      touch "$P13PO_LIVE_READY"
      exec sleep 30
      ;;
  esac
done
exit 0
`)
	ready := filepath.Join(root, "p13po-live-ready")
	t.Setenv("P13PO_LIVE_READY", ready)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneCh := make(chan GitPushResult, 1)
	go func() { doneCh <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	// The child is provably running (readiness plus both pre-exec lines
	// consumed) when the cancellation lands, so the kill is demonstrably
	// context-caused. This is the live-cancel control for the accepted
	// same-SIGTERM overlap policy.
	p13poWaitFile(t, ready)
	p13poWaitLines(t, gitCommit, "p13po-live-stdout", "p13po-live-stderr")
	cancel()

	select {
	case result := <-doneCh:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the live process had started before the cancellation")
		}
		if !result.Cancelled() {
			t.Error("a cancellation that terminates the still-running direct push must report cancelled")
		}
		if result.Success() {
			t.Error("a cancelled push must not report success")
		}
		if !errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, want the context cancellation cause for the demonstrable live kill", result.Err())
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the retained reaped process *exec.ExitError", result.Err())
		}
		if !strings.Contains(string(result.Stdout()), "p13po-live-stdout") ||
			!strings.Contains(string(result.Stderr()), "p13po-live-stderr") {
			t.Errorf("cancelled push lost captured output: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after cancellation of the live push")
	}
}

func TestPhase13PolicyZeroExitLateCancelKeepsSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, p13poProbe+`    push)
      echo "p13po-zero-stdout"
      { head -c 100000 /dev/zero | tr '\0' 'y'; } >&2
      parent=$$
      ( touch "$P13PO_ZERO_STARTED"; while kill -0 "$parent" 2>/dev/null; do
          state=$(ps -o stat= -p "$parent" 2>/dev/null)
          case "$state" in Z*) break ;; esac
        done
        touch "$P13PO_ZERO_DEAD"
        while [ ! -e "$P13PO_ZERO_RELEASE" ]; do :; done
        touch "$P13PO_ZERO_DONE"
      ) &
      exit 0
      ;;
  esac
done
exit 0
`)
	dead := filepath.Join(root, "p13po-zero-dead")
	release := filepath.Join(root, "p13po-zero-release")
	done := filepath.Join(root, "p13po-zero-done")
	started := filepath.Join(root, "p13po-zero-started")
	t.Setenv("P13PO_ZERO_DEAD", dead)
	t.Setenv("P13PO_ZERO_RELEASE", release)
	t.Setenv("P13PO_ZERO_DONE", done)
	t.Setenv("P13PO_ZERO_STARTED", started)
	p13poCleanupHolder(t, started, release, done)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneCh := make(chan GitPushResult, 1)
	go func() { doneCh <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	// The over-long stderr token fails stream capture while the direct
	// child exits zero; the late cancellation must not retroactively
	// kill it. The stderr token never surfaces as a live line, so only
	// stdout is awaited.
	p13poWaitFile(t, dead)
	p13poWaitLines(t, gitCommit, "p13po-zero-stdout", "")
	cancel()

	select {
	case result := <-doneCh:
		if !result.Started() {
			t.Error("the zero-exit process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("late drain cancellation falsely marked the completed zero-exit push killed")
		}
		if result.ExitCode() != 0 {
			t.Errorf("ExitCode() = %d, want the direct zero status", result.ExitCode())
		}
		if !result.Success() {
			t.Errorf("Success() = false, want true: a zero exit stays successful even when Err carries a stream capture failure (Err() = %v)", result.Err())
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not contain the late context cancellation", result.Err())
		}
		if !strings.Contains(string(result.Stdout()), "p13po-zero-stdout") {
			t.Errorf("zero-exit push lost stdout %q", result.Stdout())
		}
		p13poRelease(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the zero-exit drain")
	}
}

func TestPhase13PolicyNonzeroAndPrestartStayUncancelled(t *testing.T) {
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, p13poProbe+`    push)
      if [ "$P13PO_NZ_MODE" = "fail" ]; then
        echo "p13po-nz-stdout"
        echo "p13po-nz-stderr" >&2
        exit 3
      fi
      touch "$P13PO_NZ_RAN"
      echo "must-not-run"
      exit 0
      ;;
  esac
done
exit 0
`)
	ran := filepath.Join(root, "p13po-nz-ran")
	t.Setenv("P13PO_NZ_RAN", ran)

	t.Setenv("P13PO_NZ_MODE", "fail")
	failed := p13poPush(t, gitCommit, context.Background(), pushRouteUnderTest("master"), "after the independent nonzero exit")
	var exitErr *exec.ExitError
	if !failed.Started() {
		t.Error("the nonzero-exit process started, so Started() must be true")
	}
	if failed.Cancelled() {
		t.Error("an independently failed nonzero exit must never be relabelled cancelled")
	}
	if failed.ExitCode() != 3 {
		t.Errorf("ExitCode() = %d, want the direct Wait status 3", failed.ExitCode())
	}
	if !errors.As(failed.Err(), &exitErr) {
		t.Errorf("Err() = %v, want the direct process *exec.ExitError", failed.Err())
	}
	if failed.Success() {
		t.Error("a nonzero exit must not report success")
	}

	// Pre-start cancellation starts nothing: unstarted, cancelled, exit
	// -1, with the push body never executed. No timing is involved.
	t.Setenv("P13PO_NZ_MODE", "run")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	unstarted := p13poPush(t, gitCommit, ctx, pushRouteUnderTest("master"), "after the pre-start cancellation")
	if unstarted.Started() {
		t.Error("a pre-start cancellation must not start the push process")
	}
	if !unstarted.Cancelled() {
		t.Error("a pre-start cancellation must report cancelled")
	}
	if unstarted.ExitCode() != -1 {
		t.Errorf("ExitCode() = %d, want -1 when no process status exists", unstarted.ExitCode())
	}
	if unstarted.Success() {
		t.Error("an unstarted push must not report success")
	}
	if !errors.Is(unstarted.Err(), context.Canceled) {
		t.Errorf("Err() = %v, want the pre-start context cancellation", unstarted.Err())
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("the pre-start cancellation launched the push body")
	} else if !os.IsNotExist(err) {
		t.Fatalf("checking the push-ran marker: %v", err)
	}
}

func TestPhase13PolicyGuardAndSetupCancelStartNothing(t *testing.T) {
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, p13poProbe+`    push)
      touch "$P13PO_GUARD_RAN"
      echo "must-not-run"
      exit 0
      ;;
  esac
done
exit 0
`)
	ran := filepath.Join(root, "p13po-guard-ran")
	t.Setenv("P13PO_GUARD_RAN", ran)

	// A context cancelled during the ActiveGuard gate stays unstarted
	// and cancelled with exit -1, closes all setup pipe ends, and
	// launches no push. cancel() is synchronous inside the guard, so
	// ctx.Err() is already visible when the guard returns; no timing
	// is involved.
	guardErr := errors.New("stale generation")
	guardReached := false
	before := p13poCountFDs(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guardRoute := pushRouteUnderTest("master")
	guardRoute.ActiveGuard = func() error {
		guardReached = true
		cancel()
		return guardErr
	}
	guarded := p13poPush(t, gitCommit, ctx, guardRoute, "after the guard-time cancellation")
	if !guardReached {
		t.Error("the ActiveGuard gate was not reached, so the fixture did not exercise guard-time cancellation")
	}
	if guarded.Started() {
		t.Error("a guard-time cancellation must not start the push process")
	}
	if !guarded.Cancelled() {
		t.Error("a guard-time cancellation must report cancelled")
	}
	if guarded.ExitCode() != -1 {
		t.Errorf("ExitCode() = %d, want -1 when no process status exists", guarded.ExitCode())
	}
	if guarded.Success() {
		t.Error("a guard-time cancellation must not report success")
	}
	if !errors.Is(guarded.Err(), guardErr) {
		t.Errorf("Err() = %v, want the guard refusal error joined with the context cause", guarded.Err())
	}
	if !errors.Is(guarded.Err(), context.Canceled) {
		t.Errorf("Err() = %v, want the guard-time context cancellation", guarded.Err())
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("the guard-time cancellation launched the push body")
	} else if !os.IsNotExist(err) {
		t.Fatalf("checking the push-ran marker: %v", err)
	}
	if delta := p13poCountFDs(t) - before; delta != 0 {
		t.Errorf("open file descriptors grew by %d across the guard-time refusal, want all allocated pipe ends closed", delta)
	}

	// A stale-generation guard refusal without any cancellation stays
	// unstarted and uncancelled, closes the setup pipes, and launches
	// no push.
	plainRoute := pushRouteUnderTest("master")
	plainRoute.ActiveGuard = func() error { return errors.New("stale generation") }
	refused := p13poPush(t, gitCommit, context.Background(), plainRoute, "after the stale-guard refusal")
	if refused.Started() {
		t.Error("a stale-guard refusal must not start the push process")
	}
	if refused.Cancelled() {
		t.Error("a guard refusal without cancellation must not report cancelled")
	}
	if refused.ExitCode() != -1 {
		t.Errorf("ExitCode() = %d, want -1 when no process status exists", refused.ExitCode())
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("the stale-guard refusal launched the push body")
	} else if !os.IsNotExist(err) {
		t.Fatalf("checking the push-ran marker: %v", err)
	}
	if delta := p13poCountFDs(t) - before; delta != 0 {
		t.Errorf("open file descriptors grew by %d across the unstarted attempts, want all setup pipe ends closed", delta)
	}
}

func TestPhase13PolicyOrdinaryCompletionClosesPipes(t *testing.T) {
	gitCommit, _, _, _, _ := gitCommitUnderTestWithFakeGit(t, p13poProbe+`    push)
      if [ "$P13PO_EOF_MODE" = "fail" ]; then
        echo "p13po-eof-fail-stdout"
        echo "p13po-eof-fail-stderr" >&2
        exit 3
      fi
      echo "p13po-eof-stdout"
      echo "p13po-eof-stderr" >&2
      exit 0
      ;;
  esac
done
exit 0
`)
	// No cancellation, no descendants: both owned read ends must close
	// on ordinary success and failure returns without relying on
	// garbage collection.
	before := p13poCountFDs(t)
	t.Setenv("P13PO_EOF_MODE", "ok")
	succeeded := p13poPush(t, gitCommit, context.Background(), pushRouteUnderTest("master"), "after the ordinary successful push")
	if !succeeded.Success() {
		t.Fatalf("ordinary push did not succeed: exit %d, cancelled %v, err %v", succeeded.ExitCode(), succeeded.Cancelled(), succeeded.Err())
	}
	if !strings.Contains(string(succeeded.Stdout()), "p13po-eof-stdout") ||
		!strings.Contains(string(succeeded.Stderr()), "p13po-eof-stderr") {
		t.Errorf("ordinary push lost streams: stdout %q, stderr %q", succeeded.Stdout(), succeeded.Stderr())
	}
	t.Setenv("P13PO_EOF_MODE", "fail")
	failed := p13poPush(t, gitCommit, context.Background(), pushRouteUnderTest("master"), "after the ordinary failed push")
	if failed.Success() || failed.Cancelled() || failed.ExitCode() != 3 {
		t.Errorf("ordinary failed push misclassified: exit %d, cancelled %v, err %v", failed.ExitCode(), failed.Cancelled(), failed.Err())
	}
	if delta := p13poCountFDs(t) - before; delta > 2 {
		t.Errorf("open file descriptors grew by %d across ordinary completions, want both owned read ends closed on each return", delta)
	}
}

func TestPhase13PolicyCancelledDrainRetainsPreCloseBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, p13poProbe+`    push)
      echo "p13po-cutoff-stdout"
      echo "p13po-cutoff-stderr" >&2
      touch "$P13PO_CUTOFF_READY"
      ( touch "$P13PO_CUTOFF_STARTED"; while [ ! -e "$P13PO_CUTOFF_RELEASE" ]; do :; done
        touch "$P13PO_CUTOFF_DONE"
      ) &
      exec sleep 30
      ;;
  esac
done
exit 0
`)
	ready := filepath.Join(root, "p13po-cutoff-ready")
	release := filepath.Join(root, "p13po-cutoff-release")
	done := filepath.Join(root, "p13po-cutoff-done")
	started := filepath.Join(root, "p13po-cutoff-started")
	t.Setenv("P13PO_CUTOFF_READY", ready)
	t.Setenv("P13PO_CUTOFF_RELEASE", release)
	t.Setenv("P13PO_CUTOFF_DONE", done)
	t.Setenv("P13PO_CUTOFF_STARTED", started)
	p13poCleanupHolder(t, started, release, done)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneCh := make(chan GitPushResult, 1)
	go func() { doneCh <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	// The holder keeps inherited write ends open past the kill, so the
	// cancellation must bound the drain with the timed read-end closure
	// while retaining the bytes read before that closure.
	p13poWaitFile(t, ready)
	p13poWaitLines(t, gitCommit, "p13po-cutoff-stdout", "p13po-cutoff-stderr")
	cancel()

	select {
	case result := <-doneCh:
		if !result.Cancelled() {
			t.Error("cancelling the still-running push must report cancelled")
		}
		if !errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, want the context cancellation cause", result.Err())
		}
		if !strings.Contains(string(result.Stdout()), "p13po-cutoff-stdout") ||
			!strings.Contains(string(result.Stderr()), "p13po-cutoff-stderr") {
			t.Errorf("bounded drain lost pre-close bytes: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		p13poRelease(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after cancellation with an inherited-descriptor holder")
	}
}

func TestPhase13PolicyPsFailureStaysUnknownLiveKillProceeds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, gitPath := gitCommitUnderTestWithFakeGit(t, p13poProbe+`    push)
      echo "p13po-psdiag-stdout"
      echo "p13po-psdiag-stderr" >&2
      touch "$P13PO_PSDIAG_READY"
      ( touch "$P13PO_PSDIAG_STARTED"; while [ ! -e "$P13PO_PSDIAG_RELEASE" ]; do :; done
        touch "$P13PO_PSDIAG_DONE"
      ) &
      exec sleep 30
      ;;
  esac
done
exit 0
`)
	// A completed ps diagnostic that exits nonzero with stderr proves
	// nothing about the target: only a clean unknown-PID report is
	// verified absence. Liveness must treat this as unknown, and the
	// cancellation of the still-live child must neither hang nor skip
	// termination.
	if err := os.WriteFile(filepath.Join(filepath.Dir(gitPath), "ps"), []byte("#!/bin/sh\necho \"ps: cannot examine process $*\" >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("shadowing the ps diagnostic: %v", err)
	}
	ready := filepath.Join(root, "p13po-psdiag-ready")
	release := filepath.Join(root, "p13po-psdiag-release")
	done := filepath.Join(root, "p13po-psdiag-done")
	started := filepath.Join(root, "p13po-psdiag-started")
	t.Setenv("P13PO_PSDIAG_READY", ready)
	t.Setenv("P13PO_PSDIAG_RELEASE", release)
	t.Setenv("P13PO_PSDIAG_DONE", done)
	t.Setenv("P13PO_PSDIAG_STARTED", started)
	p13poCleanupHolder(t, started, release, done)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneCh := make(chan GitPushResult, 1)
	go func() { doneCh <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	p13poWaitFile(t, ready)
	p13poWaitLines(t, gitCommit, "p13po-psdiag-stdout", "p13po-psdiag-stderr")
	cancel()

	select {
	case result := <-doneCh:
		if !result.Started() {
			t.Error("the live process had started before the cancellation")
		}
		if !result.Cancelled() {
			t.Error("a failing ps diagnostic must not suppress termination of the live child")
		}
		if !errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, want the context cancellation cause", result.Err())
		}
		if !strings.Contains(string(result.Stdout()), "p13po-psdiag-stdout") ||
			!strings.Contains(string(result.Stderr()), "p13po-psdiag-stderr") {
			t.Errorf("ps-diagnostic run lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		p13poRelease(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush hung after cancellation with a failing ps diagnostic")
	}
}

func TestPhase13PolicyIndependentSigkillStaysObserved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the signal-specific push fixture requires POSIX shell signals")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, p13poProbe+`    push)
      echo "p13po-kill-stdout"
      echo "p13po-kill-stderr" >&2
      parent=$$
      ( touch "$P13PO_KILL_STARTED"; while kill -0 "$parent" 2>/dev/null; do
          state=$(ps -o stat= -p "$parent" 2>/dev/null)
          case "$state" in Z*) break ;; esac
        done
        touch "$P13PO_KILL_DEAD"
        while [ ! -e "$P13PO_KILL_RELEASE" ]; do :; done
        touch "$P13PO_KILL_DONE"
      ) &
      kill -KILL "$parent"
      ;;
  esac
done
exit 0
`)
	dead := filepath.Join(root, "p13po-kill-dead")
	release := filepath.Join(root, "p13po-kill-release")
	done := filepath.Join(root, "p13po-kill-done")
	started := filepath.Join(root, "p13po-kill-started")
	t.Setenv("P13PO_KILL_DEAD", dead)
	t.Setenv("P13PO_KILL_RELEASE", release)
	t.Setenv("P13PO_KILL_DONE", done)
	t.Setenv("P13PO_KILL_STARTED", started)
	p13poCleanupHolder(t, started, release, done)

	// An independent SIGKILL can never be produced by the handshake's
	// own SIGTERM, so even with a later cancellation request the reaped
	// status disagrees and the outcome stays an observed process
	// failure. Only verified dead/zombie evidence suppresses a kill,
	// and this SIGKILL arrives before any cancellation probe runs.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneCh := make(chan GitPushResult, 1)
	go func() { doneCh <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	p13poWaitFile(t, dead)
	p13poWaitLines(t, gitCommit, "p13po-kill-stdout", "p13po-kill-stderr")
	if _, err := os.Stat(done); err == nil {
		t.Fatal("the descendant released the inherited descriptors before the cancellation, so the drain could already have completed")
	}
	cancel()

	select {
	case result := <-doneCh:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the SIGKILL-terminated process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("an independent SIGKILL must stay an observed process outcome, never a cancellation")
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the real direct Wait *exec.ExitError", result.Err())
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not contain the late context cancellation", result.Err())
		}
		if !strings.Contains(string(result.Stdout()), "p13po-kill-stdout") ||
			!strings.Contains(string(result.Stderr()), "p13po-kill-stderr") {
			t.Errorf("SIGKILL push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		p13poRelease(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the SIGKILL drain")
	}
}

func TestPhase13PolicyResultAccessorsImmutableAndArgv(t *testing.T) {
	gitCommit, _, _, _, _ := gitCommitUnderTestWithFakeGit(t, p13poProbe+`    push)
      echo "p13po-argv-stdout"
      echo "p13po-argv-stderr" >&2
      exit 0
      ;;
  esac
done
exit 0
`)
	result := p13poPush(t, gitCommit, context.Background(), pushRouteUnderTest("master"), "after the argv push")
	if !result.Success() {
		t.Fatalf("argv push did not succeed: exit %d, cancelled %v, err %v", result.ExitCode(), result.Cancelled(), result.Err())
	}
	// The existing push argv behavior stays intact: the operation args
	// ride as a consecutive suffix tolerating the executor's injected
	// -c options.
	if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
		t.Errorf("Argv() = %v, want the existing push operation args", result.Argv())
	}
	if result.WorkingDirectory() == "" {
		t.Error("WorkingDirectory() must name the generation worktree")
	}
	// Immutable accessors: mutating a returned copy must not affect the
	// stored result.
	argv := result.Argv()
	if len(argv) == 0 {
		t.Fatal("Argv() must not be empty for a started push")
	}
	argv[0] = "MUTATED"
	stdout := result.Stdout()
	if len(stdout) == 0 {
		t.Fatal("Stdout() must retain the push bytes")
	}
	stdout[0] = 'X'
	stderr := result.Stderr()
	if len(stderr) == 0 {
		t.Fatal("Stderr() must retain the push bytes")
	}
	stderr[0] = 'X'
	fresh := gitCommit.GitPushResult()
	if fresh.Argv()[0] == "MUTATED" {
		t.Error("Argv() shares storage with the stored result")
	}
	if string(fresh.Stdout()) != string(result.Stdout()) {
		t.Error("Stdout() accessor is not a stable copy")
	}
	if fresh.Stdout()[0] == 'X' || fresh.Stderr()[0] == 'X' {
		t.Error("Stdout()/Stderr() share storage with the stored result")
	}
}
