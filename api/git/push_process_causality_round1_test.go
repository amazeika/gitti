package git

// Round-1 causality coverage for phase 1.1 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md.
//
// The frozen fixtures order independent death strictly before the test's
// cancellation and always against a working `ps` diagnostic. This file
// closes the two remaining orderings: an independent SIGKILL that lands
// between the handshake's liveness probe (pushDirectProcessAlreadyExited)
// and its Kill, and a failing `ps` diagnostic against a live child. Both
// keep a descendant holding the inherited descriptors open so the drain
// cannot reach EOF (completion notification) on its own. Same-package
// helpers (gitCommitUnderTestWithFakeGit, pushRouteUnderTest,
// argvCarriesOperationArgs, waitForTestFile, waitForRegressionPushLines,
// releaseRegressionDescendantAndRequireExit) are reused from the frozen
// files and are not redefined here.

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

// ------------------------------------
//
//	Shadow PATH with a `ps` that, while armed, independently SIGKILLs the
//	queried PID inside the probe window and then reports a live state, so
//	the handshake proceeds to a Kill that can only land on an already
//	dead direct child. Disarmed it delegates to the real ps.
//
// ------------------------------------
func armRaceWindowPs(t *testing.T, binDir, armFile string) {
	t.Helper()

	script := `#!/bin/sh
if [ -n "$FAKE_PS_ARM" ] && [ -e "$FAKE_PS_ARM" ]; then
  for arg in "$@"; do
    case "$arg" in
      ''|*[!0-9]*) ;;
      *) kill -KILL "$arg" 2>/dev/null ;;
    esac
  done
  echo "S"
  exit 0
fi
exec /bin/ps "$@"
`
	if err := os.WriteFile(filepath.Join(binDir, "ps"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing the race-window ps: %v", err)
	}
	if err := os.WriteFile(armFile, []byte{}, 0o600); err != nil {
		t.Fatalf("arming the race-window ps: %v", err)
	}
	t.Setenv("FAKE_PS_ARM", armFile)
}

func TestGitPushIndependentSigKillBetweenProbeAndKillSurvives(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell signals")
	}
	if runtime.GOOS == "linux" {
		t.Skip("the race orders death inside the ps diagnostic fallback, which Linux bypasses via /proc")
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
      echo "race-stdout"
      echo "race-stderr" >&2
      ( while [ ! -e "$FAKE_GIT_RELEASE_DESCENDANT" ]; do :; done
        touch "$FAKE_GIT_DESCENDANT_DONE"
      ) &
      touch "$FAKE_GIT_READY"
      exec sleep 30
      ;;
  esac
done
exit 0
`)
	armRaceWindowPs(t, filepath.Dir(gitPath), filepath.Join(root, "race-ps-arm"))
	ready := filepath.Join(root, "race-ready")
	releaseDescendant := filepath.Join(root, "race-release-descendant")
	descendantDone := filepath.Join(root, "race-descendant-done")
	t.Setenv("FAKE_GIT_READY", ready)
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
	// Ordering: the direct child is provably live (ready file plus both
	// pre-exec lines consumed through the live progress buffers) when the
	// cancellation is requested. The cancellation handshake then probes
	// liveness, and the armed `ps` independently SIGKILLs the child inside
	// that probe while still reporting live, so the handshake's Kill lands
	// on an already dead child and cannot have caused the termination. The
	// descendant holds the inherited write ends open throughout, so the
	// exit also precedes any completion-notification EOF.
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "race-stdout", "race-stderr")
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the signal-terminated process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("independent SIGKILL between the liveness probe and Kill relabeled the signal-terminated process as cancelled")
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
		if !strings.Contains(string(result.Stdout()), "race-stdout") || !strings.Contains(string(result.Stderr()), "race-stderr") {
			t.Errorf("signal failure lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("signal failure push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after cancellation of the probe-raced SIGKILL drain")
	}
}

func TestGitPushLiveKillWithFailingPsDiagnosticStaysCancelled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	if runtime.GOOS == "linux" {
		t.Skip("the ps diagnostic fallback is bypassed on Linux via /proc, so a failing-ps fixture cannot exercise the diagnostic start failure there")
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
      echo "psfail-stdout"
      echo "psfail-stderr" >&2
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
	// Break the `ps` diagnostic itself: an executable file the kernel cannot
	// run fails process Start (an exec-format error, never an
	// *exec.ExitError), so the probe is undeterminable rather than evidence
	// of absence. The executable mode matters: Go's Unix executable lookup
	// skips non-executable PATH entries, so a mode-0644 shadow would
	// silently resolve to the system ps and never exercise this branch. The
	// assertions below record the basis: lookup reaches the injected
	// candidate and invoking it fails Start without an exit status. (On
	// Linux the /proc observation governs instead and the ps fallback is
	// never reached for a live child, hence the skip above: the child is
	// verifiably live there, so the Kill still proceeds, but that pass
	// would not be diagnostic-start-failure evidence.)
	shadowPs := filepath.Join(filepath.Dir(gitPath), "ps")
	if err := os.WriteFile(shadowPs, []byte("not a usable ps"), 0o755); err != nil {
		t.Fatalf("breaking the ps diagnostic: %v", err)
	}
	if resolved, err := exec.LookPath("ps"); err != nil || resolved != shadowPs {
		t.Fatalf("exec.LookPath(ps) = %q, %v; want the injected %q so the probe hits the start failure", resolved, err, shadowPs)
	}
	if _, err := exec.Command("ps", "-o", "stat=", "-p", "1").Output(); err == nil {
		t.Fatal("the injected ps unexpectedly ran; want its Start to fail so the probe stays undeterminable")
	} else {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("injected ps err = %v; want a Start failure, never an *exec.ExitError", err)
		}
	}
	ready := filepath.Join(root, "psfail-ready")
	releaseDescendant := filepath.Join(root, "psfail-release-descendant")
	descendantDone := filepath.Join(root, "psfail-descendant-done")
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
	// Ordering: the direct child is provably running (ready file plus both
	// pre-exec lines) while a descendant already holds the inherited write
	// ends. The undeterminable probe must not suppress the Kill: the
	// cancellation demonstrably terminates the still-running direct push.
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "psfail-stdout", "psfail-stderr")
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the live process had started before the cancellation")
		}
		if !result.Cancelled() {
			t.Error("a failing ps diagnostic must not suppress the live kill: killing the still-running direct push must report cancelled")
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
		if !strings.Contains(string(result.Stdout()), "psfail-stdout") || !strings.Contains(string(result.Stderr()), "psfail-stderr") {
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
