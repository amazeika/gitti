package git

// Independent additive regression coverage for phase 1.3 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md:
//
//	Preserve causal direct-push outcomes and owned-pipe cleanup.
//
// Criterion map (acceptance criteria in spec order):
//  1. independent signal + late cancel stays observed process failure:
//     TestPhase13CausalOutcomeIndependentSignalSurvivesLateCancel
//     (includes the exit-before-completion-notification ordering check)
//  2. live cancel reports cancelled:
//     TestPhase13CausalOutcomeLiveCancelReportsCancelled
//  3. zero-exit + capture failure keeps Success; nonzero never cancelled;
//     pre-start unstarted:
//     TestPhase13CausalOutcomeZeroExitCaptureFailureKeepsSuccess,
//     TestPhase13CausalOutcomeNonzeroNeverCancelledAndPreStartUnstarted
//  4. concurrent drain, bounded shutdown, one reaper, immutable accessors,
//     argv: TestPhase13CausalOutcomeCancelledDrainRetainsPreCloseBytes
//     (both streams), TestPhase13CausalOutcomeAccessorsImmutableAndArgvIntact
//  5. ordering signals, no leaks, no wall-clock outcome sleeps: construction
//     of every test below (marker files + progress lines bound every verdict)
//  6. green additive + frozen commit_test.go: this file plus the untouched
//     frozen file
//  7. R3 ps-unknown liveness + SIGKILL observed:
//     TestPhase13CausalOutcomePsDiagnosticFailureStaysUnknown,
//     TestPhase13CausalOutcomeIndependentSigkillStaysObserved
//  8. R4 EOF closure + bounded inherited drain:
//     TestPhase13CausalOutcomeOrdinaryCompletionClosesDescriptors,
//     TestPhase13CausalOutcomeCancelledDrainRetainsPreCloseBytes
//  9. R5 guard/setup cancel unstarted:
//     TestPhase13CausalOutcomeGuardAndSetupCancelStartNothing
//  10. conservative simultaneous-outcome oracle: the SIGKILL oracle paired
//     with the live-cancel control below; the same-SIGTERM overlap is left
//     open (matching status is not causal proof)
//  11. frozen fixtures: no frozen file is touched by this file
//
// Only long-standing helpers (gitCommitUnderTestWithFakeGit,
// pushRouteUnderTest, argvCarriesOperationArgs) are reused; every other
// helper is defined here with a unique prefix so this file does not depend
// on any other additive file.

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

const p13co13Probe = `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
`

func p13co13WaitFile(t *testing.T, path string) {
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

func p13co13WaitLines(t *testing.T, gc *GitCommit, wantStdout, wantStderr string) {
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

func p13co13Release(t *testing.T, release, done string) {
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
		time.Sleep(10 * time.Millisecond)
	}
}

// p13co13CleanupHolder releases a started descendant holder and awaits its
// exit before the fixture directory is removed. Registered with t.Cleanup
// (LIFO: runs before the TempDir cleanup registered earlier inside
// gitCommitUnderTestWithFakeGit), so a Fatal or timeout in the test body
// still releases and awaits instead of deleting the release path out from
// under a live holder. When the started marker is absent the holder never
// forked, so nothing is awaited; the wait itself is bounded so a missing
// exit cannot block directory removal forever.
func p13co13CleanupHolder(t *testing.T, started, release, done string) {
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

func p13co13Push(t *testing.T, gc *GitCommit, ctx context.Context, route GitPushRoute, what string) GitPushResult {
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

func p13co13CountFDs(t *testing.T) int {
	t.Helper()
	for _, dir := range []string{"/proc/self/fd", "/dev/fd"} {
		if entries, err := os.ReadDir(dir); err == nil {
			return len(entries)
		}
	}
	t.Skip("no file-descriptor enumeration available on this platform")
	return 0
}

func TestPhase13CausalOutcomeIndependentSignalSurvivesLateCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the signal-specific push fixture requires POSIX shell signals")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, p13co13Probe+`    push)
      echo "p13co13-sig-stdout"
      echo "p13co13-sig-stderr" >&2
      parent=$$
      ( touch "$P13CO13_SIG_STARTED"; while kill -0 "$parent" 2>/dev/null; do
          state=$(ps -o stat= -p "$parent" 2>/dev/null)
          case "$state" in Z*) break ;; esac
        done
        touch "$P13CO13_SIG_DEAD"
        while [ ! -e "$P13CO13_SIG_RELEASE" ]; do :; done
        touch "$P13CO13_SIG_DONE"
      ) &
      kill -TERM "$parent"
      ;;
  esac
done
exit 0
`)
	dead := filepath.Join(root, "p13co13-sig-dead")
	release := filepath.Join(root, "p13co13-sig-release")
	done := filepath.Join(root, "p13co13-sig-done")
	started := filepath.Join(root, "p13co13-sig-started")
	t.Setenv("P13CO13_SIG_DEAD", dead)
	t.Setenv("P13CO13_SIG_RELEASE", release)
	t.Setenv("P13CO13_SIG_DONE", done)
	t.Setenv("P13CO13_SIG_STARTED", started)
	p13co13CleanupHolder(t, started, release, done)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneCh := make(chan GitPushResult, 1)
	go func() { doneCh <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	// The direct child was signal-terminated while the descendant holds
	// both inherited write ends open; both readers consumed the pre-exit
	// bytes before the late cancellation below.
	p13co13WaitFile(t, dead)
	p13co13WaitLines(t, gitCommit, "p13co13-sig-stdout", "p13co13-sig-stderr")
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
		if !strings.Contains(string(result.Stdout()), "p13co13-sig-stdout") ||
			!strings.Contains(string(result.Stderr()), "p13co13-sig-stderr") {
			t.Errorf("signal-terminated push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		p13co13Release(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the signal drain")
	}
}

func TestPhase13CausalOutcomeLiveCancelReportsCancelled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the live-cancel fixture requires POSIX exec behavior")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, p13co13Probe+`    push)
      echo "p13co13-live-stdout"
      echo "p13co13-live-stderr" >&2
      touch "$P13CO13_LIVE_READY"
      exec sleep 30
      ;;
  esac
done
exit 0
`)
	ready := filepath.Join(root, "p13co13-live-ready")
	t.Setenv("P13CO13_LIVE_READY", ready)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneCh := make(chan GitPushResult, 1)
	go func() { doneCh <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	// The child is provably running (readiness plus both pre-exec lines
	// consumed) when the cancellation lands, so the kill is demonstrably
	// context-caused. This is the live-cancel control for the
	// conservative simultaneous-outcome requirement.
	p13co13WaitFile(t, ready)
	p13co13WaitLines(t, gitCommit, "p13co13-live-stdout", "p13co13-live-stderr")
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
			t.Errorf("Err() = %v, want the reaped process *exec.ExitError alongside the context cause", result.Err())
		} else if result.ExitCode() != exitErr.ExitCode() {
			t.Errorf("ExitCode() = %d, want the reaped process status %d", result.ExitCode(), exitErr.ExitCode())
		}
		if !strings.Contains(string(result.Stdout()), "p13co13-live-stdout") ||
			!strings.Contains(string(result.Stderr()), "p13co13-live-stderr") {
			t.Errorf("live-killed push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after the demonstrable live kill")
	}
}

func TestPhase13CausalOutcomeZeroExitCaptureFailureKeepsSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell signals")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, p13co13Probe+`    push)
      echo "p13co13-zero-stdout"
      { head -c 100000 /dev/zero | tr '\0' 'y'; } >&2
      parent=$$
      ( touch "$P13CO13_ZERO_STARTED"; while kill -0 "$parent" 2>/dev/null; do
          state=$(ps -o stat= -p "$parent" 2>/dev/null)
          case "$state" in Z*) break ;; esac
        done
        touch "$P13CO13_ZERO_DEAD"
        while [ ! -e "$P13CO13_ZERO_RELEASE" ]; do :; done
        touch "$P13CO13_ZERO_DONE"
      ) &
      exit 0
      ;;
  esac
done
exit 0
`)
	dead := filepath.Join(root, "p13co13-zero-dead")
	release := filepath.Join(root, "p13co13-zero-release")
	done := filepath.Join(root, "p13co13-zero-done")
	started := filepath.Join(root, "p13co13-zero-started")
	t.Setenv("P13CO13_ZERO_DEAD", dead)
	t.Setenv("P13CO13_ZERO_RELEASE", release)
	t.Setenv("P13CO13_ZERO_DONE", done)
	t.Setenv("P13CO13_ZERO_STARTED", started)
	p13co13CleanupHolder(t, started, release, done)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneCh := make(chan GitPushResult, 1)
	go func() { doneCh <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	// The over-long stderr token fails stream capture while the direct
	// child exits zero; the late cancellation must not retroactively
	// kill it. The stderr token never surfaces as a live line, so only
	// stdout is awaited.
	p13co13WaitFile(t, dead)
	p13co13WaitLines(t, gitCommit, "p13co13-zero-stdout", "")
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
		if !strings.Contains(string(result.Stdout()), "p13co13-zero-stdout") {
			t.Errorf("zero-exit push lost stdout %q", result.Stdout())
		}
		p13co13Release(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the zero-exit drain")
	}
}

func TestPhase13CausalOutcomeNonzeroNeverCancelledAndPreStartUnstarted(t *testing.T) {
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, p13co13Probe+`    push)
      if [ "$P13CO13_NZ_MODE" = "fail" ]; then
        echo "p13co13-nz-stdout"
        echo "p13co13-nz-stderr" >&2
        exit 3
      fi
      touch "$P13CO13_NZ_RAN"
      echo "must-not-run"
      exit 0
      ;;
  esac
done
exit 0
`)
	ran := filepath.Join(root, "p13co13-nz-ran")
	t.Setenv("P13CO13_NZ_RAN", ran)

	t.Setenv("P13CO13_NZ_MODE", "fail")
	failed := p13co13Push(t, gitCommit, context.Background(), pushRouteUnderTest("master"), "after the independent nonzero exit")
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
	t.Setenv("P13CO13_NZ_MODE", "run")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	unstarted := p13co13Push(t, gitCommit, ctx, pushRouteUnderTest("master"), "after the pre-start cancellation")
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

func TestPhase13CausalOutcomeGuardAndSetupCancelStartNothing(t *testing.T) {
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, p13co13Probe+`    push)
      touch "$P13CO13_GUARD_RAN"
      echo "must-not-run"
      exit 0
      ;;
  esac
done
exit 0
`)
	ran := filepath.Join(root, "p13co13-guard-ran")
	t.Setenv("P13CO13_GUARD_RAN", ran)

	// A context cancelled during the ActiveGuard gate stays unstarted
	// and cancelled with exit -1, closes all setup pipe ends, and
	// launches no push. The guard itself delivers the cancellation, so
	// the refusal branch (not the earlier pre-launch check) carries both
	// the guard refusal and the context cause. No timing is involved:
	// cancel() is synchronous inside the guard, so ctx.Err() is already
	// visible when the guard returns.
	guardErr := errors.New("stale generation")
	guardReached := false
	before := p13co13CountFDs(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guardRoute := pushRouteUnderTest("master")
	guardRoute.ActiveGuard = func() error {
		guardReached = true
		cancel()
		return guardErr
	}
	guarded := p13co13Push(t, gitCommit, ctx, guardRoute, "after the guard-time cancellation")
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
	if delta := p13co13CountFDs(t) - before; delta != 0 {
		t.Errorf("open file descriptors grew by %d across the guard-time refusal, want all allocated pipe ends closed", delta)
	}

	// A stale-generation guard refusal without any cancellation stays
	// unstarted and uncancelled, closes the setup pipes, and launches
	// no push.
	plainRoute := pushRouteUnderTest("master")
	plainRoute.ActiveGuard = func() error { return errors.New("stale generation") }
	refused := p13co13Push(t, gitCommit, context.Background(), plainRoute, "after the stale-guard refusal")
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
	if delta := p13co13CountFDs(t) - before; delta != 0 {
		t.Errorf("open file descriptors grew by %d across the unstarted attempts, want all setup pipe ends closed", delta)
	}
}

func TestPhase13CausalOutcomeOrdinaryCompletionClosesDescriptors(t *testing.T) {
	gitCommit, _, _, _, _ := gitCommitUnderTestWithFakeGit(t, p13co13Probe+`    push)
      if [ "$P13CO13_EOF_MODE" = "fail" ]; then
        echo "p13co13-eof-fail-stdout"
        echo "p13co13-eof-fail-stderr" >&2
        exit 3
      fi
      echo "p13co13-eof-stdout"
      echo "p13co13-eof-stderr" >&2
      exit 0
      ;;
  esac
done
exit 0
`)
	// No cancellation, no descendants: both owned read ends must close
	// on ordinary success and failure returns without relying on
	// garbage collection.
	before := p13co13CountFDs(t)
	t.Setenv("P13CO13_EOF_MODE", "ok")
	succeeded := p13co13Push(t, gitCommit, context.Background(), pushRouteUnderTest("master"), "after the ordinary successful push")
	if !succeeded.Success() {
		t.Fatalf("ordinary push did not succeed: exit %d, cancelled %v, err %v", succeeded.ExitCode(), succeeded.Cancelled(), succeeded.Err())
	}
	if !strings.Contains(string(succeeded.Stdout()), "p13co13-eof-stdout") ||
		!strings.Contains(string(succeeded.Stderr()), "p13co13-eof-stderr") {
		t.Errorf("ordinary push lost streams: stdout %q, stderr %q", succeeded.Stdout(), succeeded.Stderr())
	}
	t.Setenv("P13CO13_EOF_MODE", "fail")
	failed := p13co13Push(t, gitCommit, context.Background(), pushRouteUnderTest("master"), "after the ordinary failed push")
	if failed.Success() || failed.Cancelled() || failed.ExitCode() != 3 {
		t.Errorf("ordinary failed push misclassified: exit %d, cancelled %v, err %v", failed.ExitCode(), failed.Cancelled(), failed.Err())
	}
	if delta := p13co13CountFDs(t) - before; delta > 2 {
		t.Errorf("open file descriptors grew by %d across ordinary completions, want both owned read ends closed on each return", delta)
	}
}

func TestPhase13CausalOutcomeCancelledDrainRetainsPreCloseBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, p13co13Probe+`    push)
      echo "p13co13-cutoff-stdout"
      echo "p13co13-cutoff-stderr" >&2
      touch "$P13CO13_CUTOFF_READY"
      ( touch "$P13CO13_CUTOFF_STARTED"; while [ ! -e "$P13CO13_CUTOFF_RELEASE" ]; do :; done
        touch "$P13CO13_CUTOFF_DONE"
      ) &
      exec sleep 30
      ;;
  esac
done
exit 0
`)
	ready := filepath.Join(root, "p13co13-cutoff-ready")
	release := filepath.Join(root, "p13co13-cutoff-release")
	done := filepath.Join(root, "p13co13-cutoff-done")
	started := filepath.Join(root, "p13co13-cutoff-started")
	t.Setenv("P13CO13_CUTOFF_READY", ready)
	t.Setenv("P13CO13_CUTOFF_RELEASE", release)
	t.Setenv("P13CO13_CUTOFF_DONE", done)
	t.Setenv("P13CO13_CUTOFF_STARTED", started)
	p13co13CleanupHolder(t, started, release, done)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneCh := make(chan GitPushResult, 1)
	go func() { doneCh <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	// The holder keeps inherited write ends open past the kill, so the
	// cancellation must bound the drain with the timed read-end closure
	// while retaining the bytes read before that closure.
	p13co13WaitFile(t, ready)
	p13co13WaitLines(t, gitCommit, "p13co13-cutoff-stdout", "p13co13-cutoff-stderr")
	cancel()

	select {
	case result := <-doneCh:
		if !result.Cancelled() {
			t.Error("cancelling the still-running push must report cancelled")
		}
		if !errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, want the context cancellation cause", result.Err())
		}
		if !strings.Contains(string(result.Stdout()), "p13co13-cutoff-stdout") ||
			!strings.Contains(string(result.Stderr()), "p13co13-cutoff-stderr") {
			t.Errorf("bounded drain lost pre-close bytes: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		p13co13Release(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after cancellation with an inherited-descriptor holder")
	}
}

func TestPhase13CausalOutcomePsDiagnosticFailureStaysUnknown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, gitPath := gitCommitUnderTestWithFakeGit(t, p13co13Probe+`    push)
      echo "p13co13-psdiag-stdout"
      echo "p13co13-psdiag-stderr" >&2
      touch "$P13CO13_PSDIAG_READY"
      ( touch "$P13CO13_PSDIAG_STARTED"; while [ ! -e "$P13CO13_PSDIAG_RELEASE" ]; do :; done
        touch "$P13CO13_PSDIAG_DONE"
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
	ready := filepath.Join(root, "p13co13-psdiag-ready")
	release := filepath.Join(root, "p13co13-psdiag-release")
	done := filepath.Join(root, "p13co13-psdiag-done")
	started := filepath.Join(root, "p13co13-psdiag-started")
	t.Setenv("P13CO13_PSDIAG_READY", ready)
	t.Setenv("P13CO13_PSDIAG_RELEASE", release)
	t.Setenv("P13CO13_PSDIAG_DONE", done)
	t.Setenv("P13CO13_PSDIAG_STARTED", started)
	p13co13CleanupHolder(t, started, release, done)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneCh := make(chan GitPushResult, 1)
	go func() { doneCh <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	p13co13WaitFile(t, ready)
	p13co13WaitLines(t, gitCommit, "p13co13-psdiag-stdout", "p13co13-psdiag-stderr")
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
		if !strings.Contains(string(result.Stdout()), "p13co13-psdiag-stdout") ||
			!strings.Contains(string(result.Stderr()), "p13co13-psdiag-stderr") {
			t.Errorf("ps-diagnostic run lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		p13co13Release(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush hung after cancellation with a failing ps diagnostic")
	}
}

func TestPhase13CausalOutcomeIndependentSigkillStaysObserved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the signal-specific push fixture requires POSIX shell signals")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, p13co13Probe+`    push)
      echo "p13co13-kill-stdout"
      echo "p13co13-kill-stderr" >&2
      parent=$$
      ( touch "$P13CO13_KILL_STARTED"; while kill -0 "$parent" 2>/dev/null; do
          state=$(ps -o stat= -p "$parent" 2>/dev/null)
          case "$state" in Z*) break ;; esac
        done
        touch "$P13CO13_KILL_DEAD"
        while [ ! -e "$P13CO13_KILL_RELEASE" ]; do :; done
        touch "$P13CO13_KILL_DONE"
      ) &
      kill -KILL "$parent"
      ;;
  esac
done
exit 0
`)
	dead := filepath.Join(root, "p13co13-kill-dead")
	release := filepath.Join(root, "p13co13-kill-release")
	done := filepath.Join(root, "p13co13-kill-done")
	started := filepath.Join(root, "p13co13-kill-started")
	t.Setenv("P13CO13_KILL_DEAD", dead)
	t.Setenv("P13CO13_KILL_RELEASE", release)
	t.Setenv("P13CO13_KILL_DONE", done)
	t.Setenv("P13CO13_KILL_STARTED", started)
	p13co13CleanupHolder(t, started, release, done)

	// Ambiguous independent SIGKILL in the observation-to-signal window
	// reaps a status our SIGTERM could not have produced, so it stays a
	// non-cancelled observed outcome even though cancellation was
	// requested. Together with the live-cancel control above this is the
	// ordered oracle for the conservative simultaneous-outcome
	// requirement; the same-SIGTERM overlap is left open because matching
	// status is not causal proof.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneCh := make(chan GitPushResult, 1)
	go func() { doneCh <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	p13co13WaitFile(t, dead)
	p13co13WaitLines(t, gitCommit, "p13co13-kill-stdout", "p13co13-kill-stderr")
	cancel()

	select {
	case result := <-doneCh:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the SIGKILL-terminated process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("independent SIGKILL must not be relabelled cancelled")
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the real direct Wait *exec.ExitError", result.Err())
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not contain the late context cancellation", result.Err())
		}
		if result.Success() {
			t.Error("a SIGKILL-terminated push must not report success")
		}
		if !strings.Contains(string(result.Stdout()), "p13co13-kill-stdout") ||
			!strings.Contains(string(result.Stderr()), "p13co13-kill-stderr") {
			t.Errorf("SIGKILL-terminated push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		p13co13Release(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the SIGKILL drain")
	}
}

func TestPhase13CausalOutcomeAccessorsImmutableAndArgvIntact(t *testing.T) {
	gitCommit, _, _, _, _ := gitCommitUnderTestWithFakeGit(t, p13co13Probe+`    push)
      echo "p13co13-imm-stdout"
      echo "p13co13-imm-stderr" >&2
      exit 0
      ;;
  esac
done
exit 0
`)
	result := p13co13Push(t, gitCommit, context.Background(), pushRouteUnderTest("master"), "after the ordinary successful push")
	if !result.Success() {
		t.Fatalf("ordinary push did not succeed: exit %d, err %v", result.ExitCode(), result.Err())
	}
	if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
		t.Errorf("Argv() = %v, want the existing push operation arguments preserved", result.Argv())
	}
	argv := result.Argv()
	if len(argv) > 0 {
		argv[0] = "mutated"
	}
	if strings.Contains(strings.Join(result.Argv(), " "), "mutated") {
		t.Error("Argv() shares storage with the result; accessors must be immutable")
	}
	stdout := result.Stdout()
	for i := range stdout {
		stdout[i] = 'x'
	}
	if !strings.Contains(string(result.Stdout()), "p13co13-imm-stdout") {
		t.Error("Stdout() shares storage with the result; accessors must be immutable")
	}
	stderr := result.Stderr()
	for i := range stderr {
		stderr[i] = 'x'
	}
	if !strings.Contains(string(result.Stderr()), "p13co13-imm-stderr") {
		t.Error("Stderr() shares storage with the result; accessors must be immutable")
	}
}
