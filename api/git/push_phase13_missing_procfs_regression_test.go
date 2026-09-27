//go:build unix

package git

// Focused regression for phase 1.3 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md:
//
//	Preserve causal direct-push outcomes and owned-pipe cleanup.
//
// Target: code:R1 — api/git/push_liveness_unix.go reports a missing
// /proc/<pid>/stat entry as definitive death even after a successful
// signal-0 probe. In an environment without procfs (chroot, unmounted
// /proc), every live child reads as dead: cancellation skips both
// termination signals while still awaiting Wait, and GitPush hangs on
// the descendant-held drain.
//
// A test-only mount namespace is out of scope for this session, so the
// missing-procfs branch itself cannot be forced without privileged
// mounts. The regression net below pins the observable contract that
// branch must satisfy, and fails against the buggy verdict wherever the
// verdict is observable:
//
//  1. Unit invariant: a provably live child (signal 0 succeeds) is never
//     reported exited — TestPhase13MissingProcfsLiveChildStaysCandidate.
//     Under the bug, any live child with an unreadable /proc entry takes
//     the (true, true) path and violates this invariant.
//  2. Outcome: cancellation of that still-live child terminates promptly
//     with retained status, context cause and streams —
//     TestPhase13MissingProcfsLiveCancelTerminatesPromptly (plus the
//     failing-ps shadow variant
//     TestPhase13MissingProcfsFailingPsStillCancelsLiveChild, which keeps
//     a live child a signal candidate when the diagnostic cannot verify
//     death).
//  3. Boundary: only confirmed dead/zombie suppresses signalling — the
//     reaped-and-vanished control in the unit test plus
//     TestPhase13MissingProcfsIndependentSigkillStaysObserved (ambiguous
//     SIGKILL stays a non-cancelled observed outcome) and
//     TestPhase13MissingProcfsNoRequestSigtermStaysFailure (SIGTERM with
//     no cancellation request stays a process failure, never cancelled).
//
// Ordering rides explicit signals only (ready files, published PIDs,
// consumed progress lines, kernel death evidence, descendant markers).
// Same-package helpers (gitCommitUnderTestWithFakeGit,
// pushRouteUnderTest, argvCarriesOperationArgs, waitForTestFile,
// waitForRegressionPushLines, releaseRegressionDescendantAndRequireExit,
// phase13ReadChildPid, phase13RequireDeathEvidence) are reused and not
// redefined here.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// p13mpProbe answers the generation-executor upstream probe; each test
// appends its own push case publishing the direct-child PID and holding
// inherited descriptors open via a silent descendant.
const p13mpProbe = `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
`

// p13mpLivePush idles as sleep after publishing readiness, both stream
// lines and the direct-child PID, while a silent descendant inherits the
// pipe write ends.
const p13mpLivePush = p13mpProbe + `    push)
      echo "p13mp-stdout"
      echo "p13mp-stderr" >&2
      echo $$ > "$P13MP_CHILD_PID"
      ( touch "$P13MP_HOLDER_STARTED"
        touch "$P13MP_READY"
        while [ ! -e "$P13MP_RELEASE" ]; do :; done
        touch "$P13MP_DONE"
      ) &
      exec sleep 30
      ;;
  esac
done
exit 0
`

// p13mpStartLivePush launches the live fixture and returns the ordered
// handles. The caller owns the descendant release markers.
func p13mpStartLivePush(t *testing.T, prefix string) (*GitCommit, chan GitPushResult, context.CancelFunc, int, string, string) {
	t.Helper()
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, p13mpLivePush)
	ready := filepath.Join(root, prefix+"-ready")
	childPidFile := filepath.Join(root, prefix+"-child-pid")
	holderStarted := filepath.Join(root, prefix+"-holder-started")
	release := filepath.Join(root, prefix+"-release")
	done := filepath.Join(root, prefix+"-done")
	t.Setenv("P13MP_READY", ready)
	t.Setenv("P13MP_CHILD_PID", childPidFile)
	t.Setenv("P13MP_HOLDER_STARTED", holderStarted)
	t.Setenv("P13MP_RELEASE", release)
	t.Setenv("P13MP_DONE", done)
	t.Cleanup(func() {
		_ = os.WriteFile(release, []byte{}, 0o600)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(done); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	resultCh := make(chan GitPushResult, 1)
	go func() { resultCh <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	// Ordered live verdict: the holder forked (so the inherited-
	// descriptor premise holds), readiness plus both pre-exec lines are
	// consumed, the published PID resolves, and the kernel reports no
	// positive death evidence for it.
	waitForTestFile(t, holderStarted)
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "p13mp-stdout", "p13mp-stderr")
	waitForTestFile(t, childPidFile)
	childPid := phase13ReadChildPid(t, childPidFile)
	if pushDirectProcessAlreadyExited(childPid) {
		t.Fatalf("direct child PID %d already reads as exited before cancellation; the live-verdict premise cannot be established", childPid)
	}
	return gitCommit, resultCh, cancel, childPid, release, done
}

func TestPhase13MissingProcfsLiveChildStaysCandidate(t *testing.T) {
	// A provably live child — signal 0 succeeds — must never be
	// reported exited, however /proc reads behave. The R1 defect maps
	// exactly this case (successful signal 0, missing /proc entry) to
	// definitive death, suppressing the cancellation signal.
	victim := exec.Command("sleep", "30")
	if err := victim.Start(); err != nil {
		t.Fatalf("starting the live victim: %v", err)
	}
	pid := victim.Process.Pid
	defer func() {
		_ = victim.Process.Kill()
		_ = victim.Wait()
	}()
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("signal 0 against the live victim PID %d = %v, want nil success so the live premise holds", pid, err)
	}
	if exited, _ := pushDirectProcessKernelState(pid); exited {
		t.Error("pushDirectProcessKernelState reports a signal-0-live child as exited; a missing /proc entry must not suppress cancellation of a live child")
	}
	if pushDirectProcessAlreadyExited(pid) {
		t.Error("pushDirectProcessAlreadyExited reports a signal-0-live child as exited; only confirmed dead/zombie evidence may suppress a kill")
	}
	if pushDirectProcessSettledDead(pid) {
		t.Error("pushDirectProcessSettledDead reports a signal-0-live child as dead; an undeterminable state must stay a live signal candidate")
	}

	// Control: only confirmed death suppresses. Reap the victim and wait
	// for PID vanish (ESRCH), then the probe must report positive death.
	if err := victim.Process.Kill(); err != nil {
		t.Fatalf("killing the victim for the dead control: %v", err)
	}
	_ = victim.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err != nil && errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("victim PID %d never vanished after reaping; the confirmed-death control cannot be established", pid)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !pushDirectProcessAlreadyExited(pid) {
		t.Error("pushDirectProcessAlreadyExited = false for a reaped-and-vanished PID, want positive death evidence to suppress a kill")
	}
}

func TestPhase13MissingProcfsLiveCancelTerminatesPromptly(t *testing.T) {
	// The R1 outcome: a live child that available kernel state cannot
	// prove dead remains a signal candidate, so cancellation terminates
	// promptly with retained status, cause and streams instead of
	// hanging on the descendant-held drain.
	_, done, cancel, _, release, descendantDone := p13mpStartLivePush(t, "p13mp-live")
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the live process had started before the cancellation")
		}
		if !result.Cancelled() {
			t.Error("cancellation of the still-live direct child must report cancelled; skipping the signal hangs the inherited-descriptor drain")
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
			t.Error("a cancelled push must not report success")
		}
		if !strings.Contains(string(result.Stdout()), "p13mp-stdout") || !strings.Contains(string(result.Stderr()), "p13mp-stderr") {
			t.Errorf("live-killed push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("cancelled push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, release, descendantDone)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after cancelling the live child; the termination signal must not be skipped")
	}
}

func TestPhase13MissingProcfsFailingPsStillCancelsLiveChild(t *testing.T) {
	// R3 outcome half: a completed ps diagnostic that exits nonzero with
	// stderr is unknown liveness, not verified absence, so it must not
	// suppress termination of the live child either.
	gitCommit, _, _, root, gitPath := gitCommitUnderTestWithFakeGit(t, p13mpLivePush)
	if err := os.WriteFile(filepath.Join(filepath.Dir(gitPath), "ps"), []byte("#!/bin/sh\necho \"ps: cannot examine process $*\" >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("shadowing the ps diagnostic: %v", err)
	}
	ready := filepath.Join(root, "p13mp-psdiag-ready")
	childPidFile := filepath.Join(root, "p13mp-psdiag-child-pid")
	holderStarted := filepath.Join(root, "p13mp-psdiag-holder-started")
	release := filepath.Join(root, "p13mp-psdiag-release")
	descendantDone := filepath.Join(root, "p13mp-psdiag-done")
	t.Setenv("P13MP_READY", ready)
	t.Setenv("P13MP_CHILD_PID", childPidFile)
	t.Setenv("P13MP_HOLDER_STARTED", holderStarted)
	t.Setenv("P13MP_RELEASE", release)
	t.Setenv("P13MP_DONE", descendantDone)
	t.Cleanup(func() {
		_ = os.WriteFile(release, []byte{}, 0o600)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(descendantDone); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	waitForTestFile(t, holderStarted)
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "p13mp-stdout", "p13mp-stderr")
	waitForTestFile(t, childPidFile)
	if childPid := phase13ReadChildPid(t, childPidFile); pushDirectProcessAlreadyExited(childPid) {
		t.Fatalf("direct child PID %d already reads as exited before cancellation; the live-verdict premise cannot be established", childPid)
	}
	cancel()

	select {
	case result := <-done:
		if !result.Started() {
			t.Error("the live process had started before the cancellation")
		}
		if !result.Cancelled() {
			t.Error("a failing ps diagnostic must not suppress termination: the live child must still report cancelled")
		}
		if !errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, want the context cancellation cause", result.Err())
		}
		if !strings.Contains(string(result.Stdout()), "p13mp-stdout") || !strings.Contains(string(result.Stderr()), "p13mp-stderr") {
			t.Errorf("ps-diagnostic run lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		releaseRegressionDescendantAndRequireExit(t, release, descendantDone)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush hung after cancellation with a failing ps diagnostic; unknown liveness must stay a signal candidate")
	}
}

func TestPhase13MissingProcfsIndependentSigkillStaysObserved(t *testing.T) {
	// Ambiguous independent SIGKILL in the observation-to-signal window
	// reaps a status the handshake SIGTERM could not have produced, so it
	// stays a non-cancelled observed outcome even though cancellation was
	// requested. The SIGSTOP freeze makes the independent killer
	// deterministically the reaper-observed one through either production
	// path (pre-signal suppression or the reaped-status filter).
	_, done, cancel, childPid, release, descendantDone := p13mpStartLivePush(t, "p13mp-kill")
	proc, err := os.FindProcess(childPid)
	if err != nil {
		t.Fatalf("finding the direct child PID %d: %v", childPid, err)
	}
	if err := proc.Signal(syscall.SIGSTOP); err != nil {
		t.Fatalf("stopping the direct child PID %d: %v", childPid, err)
	}
	if pushDirectProcessAlreadyExited(childPid) {
		t.Fatalf("direct child PID %d reads as exited after SIGSTOP; the stopped-live premise cannot be established", childPid)
	}
	cancel()
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("delivering the independent SIGKILL to PID %d: %v", childPid, err)
	}
	phase13RequireDeathEvidence(t, childPid)

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the signal-terminated process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("an independent SIGKILL relabelled the signal-terminated process as cancelled; a distinguishable status must stay an observed process failure")
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Fatalf("Err() = %v, want the real direct-process *exec.ExitError from the independent SIGKILL", result.Err())
		}
		if !pushProcessKilledBySigKill(exitErr.ProcessState) {
			t.Error("the reaped status is not SIGKILL, so this run did not reach the claimed independent-killer branch")
		}
		if result.ExitCode() != exitErr.ExitCode() || result.ExitCode() == 0 {
			t.Errorf("ExitCode() = %d, want the nonzero reaped SIGKILL status %d", result.ExitCode(), exitErr.ExitCode())
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not join the concurrent context cancellation for the independently terminated process", result.Err())
		}
		if result.Success() {
			t.Error("a signal-terminated push must not report success")
		}
		if !strings.Contains(string(result.Stdout()), "p13mp-stdout") || !strings.Contains(string(result.Stderr()), "p13mp-stderr") {
			t.Errorf("signal failure lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if _, err := os.Stat(descendantDone); err == nil {
			t.Error("the descendant exited before the classification, so the direct exit no longer precedes completion notification")
		} else if !os.IsNotExist(err) {
			t.Fatalf("checking the descendant hold-open marker: %v", err)
		}
		releaseRegressionDescendantAndRequireExit(t, release, descendantDone)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after the independent SIGKILL although cancellation must bound the inherited-descriptor drain")
	}
}

func TestPhase13MissingProcfsNoRequestSigtermStaysFailure(t *testing.T) {
	// Without a cancellation request, SIGTERM is an observed process
	// failure, never a cancellation — the R1 boundary in the other
	// direction from the live-cancel tests above.
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, p13mpProbe+`    push)
      echo "p13mp-term-stdout"
      echo "p13mp-term-stderr" >&2
      echo $$ > "$P13MP_CHILD_PID"
      touch "$P13MP_READY"
      ( touch "$P13MP_HOLDER_STARTED"
        while [ ! -e "$P13MP_RELEASE" ]; do :; done
        touch "$P13MP_DONE"
      ) &
      kill -TERM $$
      ;;
  esac
done
exit 0
`)
	ready := filepath.Join(root, "p13mp-term-ready")
	childPidFile := filepath.Join(root, "p13mp-term-child-pid")
	holderStarted := filepath.Join(root, "p13mp-term-holder-started")
	release := filepath.Join(root, "p13mp-term-release")
	descendantDone := filepath.Join(root, "p13mp-term-done")
	t.Setenv("P13MP_READY", ready)
	t.Setenv("P13MP_CHILD_PID", childPidFile)
	t.Setenv("P13MP_HOLDER_STARTED", holderStarted)
	t.Setenv("P13MP_RELEASE", release)
	t.Setenv("P13MP_DONE", descendantDone)
	t.Cleanup(func() {
		_ = os.WriteFile(release, []byte{}, 0o600)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(descendantDone); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})

	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(context.Background(), pushRouteUnderTest("master")) }()
	// No cancellation is ever requested: the direct child terminates
	// itself with SIGTERM while the descendant briefly holds the
	// descriptors. The holder is released up front so the ordinary
	// (uncancelled) drain can reach EOF on its own; the test still
	// observes the independently caused death before the result.
	waitForTestFile(t, holderStarted)
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "p13mp-term-stdout", "p13mp-term-stderr")
	waitForTestFile(t, childPidFile)
	phase13RequireDeathEvidence(t, phase13ReadChildPid(t, childPidFile))
	if err := os.WriteFile(release, []byte{}, 0o600); err != nil {
		t.Fatalf("releasing the descendant holder: %v", err)
	}

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the signal-terminated process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("a SIGTERM without a cancellation request must not report cancelled")
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Fatalf("Err() = %v, want the real direct-process *exec.ExitError from the SIGTERM", result.Err())
		}
		if !pushProcessKilledBySigTerm(exitErr.ProcessState) {
			t.Error("the reaped status does not read as SIGTERM, so this run did not reach the claimed no-request-SIGTERM branch")
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not carry a context-cancellation error when none was requested", result.Err())
		}
		if result.Success() {
			t.Error("a signal-terminated push must not report success")
		}
		if !strings.Contains(string(result.Stdout()), "p13mp-term-stdout") || !strings.Contains(string(result.Stderr()), "p13mp-term-stderr") {
			t.Errorf("signal failure lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		releaseRegressionDescendantAndRequireExit(t, release, descendantDone)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after the no-request SIGTERM")
	}
}
