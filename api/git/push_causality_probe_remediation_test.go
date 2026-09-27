package git

// Independent remediation coverage for phase 1.1 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md.
//
// The frozen SIGKILL revisions order the direct child's death before the
// test's cancellation, but they do not place death between the handshake's
// liveness observation (pushDirectProcessAlreadyExited) and its Kill, and
// the recorded gates do not exercise the probe-filter failure paths. This
// file adds that missing ordering: an independently SIGKILL-terminated
// direct push whose descendant holds the inherited descriptors open (so
// exit precedes both cancellation and completion-notification EOF), a
// reap-ordered variant proving completion was buffered before the late
// cancellation, the complementary still-live kill, and direct unit coverage
// of both causality filters. Same-package helpers
// (gitCommitUnderTestWithFakeGit, pushRouteUnderTest,
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

func TestGitPushProbeRemediationIndependentSigKillSurvivesLateCancellation(t *testing.T) {
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
      echo "probe-sigkill-stdout"
      echo "probe-sigkill-stderr" >&2
      parent=$$
      ( while kill -0 "$parent" 2>/dev/null; do
          state=$(ps -o stat= -p "$parent" 2>/dev/null)
          case "$state" in Z*) break ;; esac
        done
        touch "$FAKE_GIT_PARENT_DEAD"
        while [ ! -e "$FAKE_GIT_RELEASE_DESCENDANT" ]; do :; done
        touch "$FAKE_GIT_DESCENDANT_DONE"
      ) &
      kill -KILL "$parent"
      ;;
  esac
done
exit 0
`)
	parentDead := filepath.Join(root, "probe-sigkill-parent-dead")
	releaseDescendant := filepath.Join(root, "probe-sigkill-release-descendant")
	descendantDone := filepath.Join(root, "probe-sigkill-descendant-done")
	t.Setenv("FAKE_GIT_PARENT_DEAD", parentDead)
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
	// Ordering: independent SIGKILL death (parent-dead file) happens first
	// while the descendant keeps both inherited write ends open, so the
	// drain cannot reach EOF (completion notification) on its own. Both
	// readers consume the pre-exit bytes (live progress buffers) before the
	// late cancellation bounds the still-open drain.
	waitForTestFile(t, parentDead)
	waitForRegressionPushLines(t, gitCommit, "probe-sigkill-stdout", "probe-sigkill-stderr")
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
				t.Error("ExitCode() = 0, want the nonzero signal status, not a clean exit")
			}
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not contain the late context cancellation", result.Err())
		}
		if result.Success() {
			t.Error("a signal-terminated push must not be reported as a success")
		}
		if !strings.Contains(string(result.Stdout()), "probe-sigkill-stdout") || !strings.Contains(string(result.Stderr()), "probe-sigkill-stderr") {
			t.Errorf("signal failure lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("signal failure push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the SIGKILL-terminated drain")
	}
}

func TestGitPushProbeRemediationReapedExitBeforeNotificationSurvivesLateCancellation(t *testing.T) {
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
      echo "probe-reaped-stdout"
      echo "probe-reaped-stderr" >&2
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
      kill -KILL "$parent"
      ;;
  esac
done
exit 0
`)
	parentDead := filepath.Join(root, "probe-reaped-parent-dead")
	parentReaped := filepath.Join(root, "probe-reaped-parent-reaped")
	releaseDescendant := filepath.Join(root, "probe-reaped-release-descendant")
	descendantDone := filepath.Join(root, "probe-reaped-descendant-done")
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	// Ordering: independent SIGKILL death (parent-dead), then PID
	// disappearance proving the single reaper already returned and the
	// completion is buffered, all while the descendant holds the inherited
	// write ends so the drain cannot reach EOF (completion notification)
	// before the late cancellation bounds it. Both readers consume the
	// pre-exit bytes first so the retained-stream oracle is not raced by
	// the cancel-time pipe close.
	waitForTestFile(t, parentDead)
	waitForTestFile(t, parentReaped)
	waitForRegressionPushLines(t, gitCommit, "probe-reaped-stdout", "probe-reaped-stderr")
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the signal-terminated process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("late drain cancellation relabelled the reaped independently signal-terminated process as cancelled")
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
		if !strings.Contains(string(result.Stdout()), "probe-reaped-stdout") || !strings.Contains(string(result.Stderr()), "probe-reaped-stderr") {
			t.Errorf("signal failure lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("signal failure push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the reaped SIGKILL-terminated drain")
	}
}

func TestGitPushProbeRemediationLiveCancellationTerminatesStillRunningPush(t *testing.T) {
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
      echo "probe-live-stdout"
      echo "probe-live-stderr" >&2
      touch "$FAKE_GIT_READY"
      exec sleep 30
      ;;
  esac
done
exit 0
`)
	ready := filepath.Join(root, "probe-live-ready")
	t.Setenv("FAKE_GIT_READY", ready)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	// Ordering: the direct child is provably running (ready file plus both
	// pre-exec lines consumed through the live progress buffers), so the
	// cancellation demonstrably terminates a still-running direct push.
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "probe-live-stdout", "probe-live-stderr")
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the live process had started before the cancellation")
		}
		if !result.Cancelled() {
			t.Error("killing the still-running direct push must report cancelled")
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
		if !strings.Contains(string(result.Stdout()), "probe-live-stdout") || !strings.Contains(string(result.Stderr()), "probe-live-stderr") {
			t.Errorf("live-killed push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("cancelled push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after terminating the still-running process")
	}
}

func TestPushDirectProcessAlreadyExitedProbeSemantics(t *testing.T) {
	if pushDirectProcessAlreadyExited(0) || pushDirectProcessAlreadyExited(-1) {
		t.Error("pushDirectProcessAlreadyExited(<=0) = true, want false for the absent PID so a genuine live kill is still reported as cancelled")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the POSIX zombie/vanished-PID probe is undeterminable on Windows by design")
	}
	// A provably live child must not read as already exited; otherwise the
	// handshake would suppress a genuine cancellation claim.
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
		_ = live.Process.Kill()
		_ = live.Wait()
		return
	}
	// A reaped-away PID has vanished: the probe must report it exited so a
	// Kill that lands after independent termination cannot claim causation.
	short := exec.Command("true")
	if err := short.Start(); err != nil {
		t.Fatalf("starting the short probe process: %v", err)
	}
	pid := short.Process.Pid
	if err := short.Wait(); err != nil {
		t.Fatalf("reaping the short probe process: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pushDirectProcessAlreadyExited(pid) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !pushDirectProcessAlreadyExited(pid) {
		t.Errorf("a vanished PID %d still reads as live, so a Kill against an already dead child could be mistaken for causation", pid)
	}
	// A SIGKILLed-then-reaped child also vanishes: same expectation.
	killed := exec.Command("sleep", "30")
	if err := killed.Start(); err != nil {
		t.Fatalf("starting the SIGKILL probe process: %v", err)
	}
	killedPid := killed.Process.Pid
	if err := killed.Process.Kill(); err != nil {
		t.Fatalf("SIGKILLing the probe process: %v", err)
	}
	_ = killed.Wait()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pushDirectProcessAlreadyExited(killedPid) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !pushDirectProcessAlreadyExited(killedPid) {
		t.Errorf("a SIGKILLed-and-reaped PID %d still reads as live", killedPid)
	}
}

func TestPushProcessKilledBySigKillFilterSemantics(t *testing.T) {
	if pushProcessKilledBySigKill(nil) {
		t.Error("pushProcessKilledBySigKill(nil) = true, want false so an unreaped status never keeps a cancellation claim")
	}
	if runtime.GOOS == "windows" {
		t.Skip("signal-identity filtering is undeterminable on Windows by design")
	}
	exited := exec.Command("sh", "-c", "exit 3")
	if err := exited.Run(); err == nil {
		t.Fatal("the exit-3 probe did not fail as expected")
	} else if pushProcessKilledBySigKill(exited.ProcessState) {
		t.Error("a numeric exit reads as SIGKILL, which would let a Kill against an independently failed child claim cancellation")
	}
	sigterm := exec.Command("sh", "-c", "kill -TERM $$")
	if err := sigterm.Run(); err == nil {
		t.Fatal("the SIGTERM probe did not fail as expected")
	} else if pushProcessKilledBySigKill(sigterm.ProcessState) {
		t.Error("a SIGTERM status reads as SIGKILL, which would let an independent signal failure be relabelled cancelled")
	}
	sigkilled := exec.Command("sleep", "30")
	if err := sigkilled.Start(); err != nil {
		t.Fatalf("starting the SIGKILL filter process: %v", err)
	}
	if err := sigkilled.Process.Kill(); err != nil {
		_ = sigkilled.Wait()
		t.Fatalf("SIGKILLing the filter process: %v", err)
	}
	_ = sigkilled.Wait()
	if !pushProcessKilledBySigKill(sigkilled.ProcessState) {
		t.Error("a reaped SIGKILL status does not read as SIGKILL, which would suppress a genuine live-kill cancellation")
	}
}
