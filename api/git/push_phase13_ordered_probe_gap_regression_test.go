//go:build unix

package git

// Additive regression coverage for phase 1.3 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md:
//
//	Preserve causal direct-push outcomes and owned-pipe cleanup.
//
// The retained files pin pre-cancel independent deaths (SIGKILL and
// SIGTERM ordered before cancellation with the production reaper's return
// acknowledged) and the live-cancel control, plus a unit pin that
// signal-success plus a matching status cannot attribute causation. What
// no fixture yet covers is a death ordered into the production probe
// window itself: an independent termination landing at or after
// cancellation, concurrent with the handshake's own liveness sample and
// signal, while inherited descriptors stay held open. The tests below add
// that remainder: a distinguishable-signal (SIGKILL) death concurrent
// with cancellation that must stay a non-cancelled observed failure
// through either production path (pre-signal probe suppression or the
// reaped-status filter), a live-cancel control on the same fixture, a
// unit pin that the same-signal (SIGTERM) tuple in that window stays
// indistinguishable, and source-text pins keeping the document,
// commit.go and the Unix probe in agreement on the conservative
// simultaneous-outcome contract.
//
// Determinism note: the SIGKILL test first stops the direct child with
// SIGSTOP before cancelling. A stopped child keeps its PID and stays a
// definitive live candidate, so the handshake's SIGTERM can only pend
// while the test-side SIGKILL is always the reaper-observed killer; the
// reaped status is therefore deterministically SIGKILL however the probe
// and the signal interleave. No wall-clock sleep orders any step: every
// ordering rides an explicit signal (readiness file, published PID,
// consumed progress lines, kernel death evidence, descendant markers).
// Same-package helpers (gitCommitUnderTestWithFakeGit,
// pushRouteUnderTest, argvCarriesOperationArgs, waitForTestFile,
// waitForRegressionPushLines, releaseRegressionDescendantAndRequireExit,
// phase13ReadChildPid, phase13RequireDeathEvidence) are reused from the
// retained files and are not redefined here.

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

// probeGapPushScript answers the generation-executor upstream probe and
// runs a scripted push whose direct child publishes its PID, signals
// readiness, then idles as sleep while a silent descendant holds the
// inherited descriptors open, so the direct exit precedes any
// completion-notification EOF.
const probeGapPushScript = `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
    push)
      echo "probe-gap-stdout"
      echo "probe-gap-stderr" >&2
      echo $$ > "$PROBE_GAP_CHILD_PID"
      ( touch "$PROBE_GAP_HOLDER_STARTED"
        touch "$PROBE_GAP_READY"
        while [ ! -e "$PROBE_GAP_RELEASE_DESCENDANT" ]; do :; done
        touch "$PROBE_GAP_DESCENDANT_DONE"
      ) &
      exec sleep 30
      ;;
  esac
done
exit 0
`

// probeGapStartPush launches the fixture and returns the handles the
// ordered tests need. The caller owns the descendant release markers.
func probeGapStartPush(t *testing.T, script, prefix string) (*GitCommit, chan GitPushResult, context.CancelFunc, int, string, string) {
	t.Helper()
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, script)
	ready := filepath.Join(root, prefix+"-ready")
	childPidFile := filepath.Join(root, prefix+"-child-pid")
	holderStarted := filepath.Join(root, prefix+"-holder-started")
	releaseDescendant := filepath.Join(root, prefix+"-release-descendant")
	descendantDone := filepath.Join(root, prefix+"-descendant-done")
	t.Setenv("PROBE_GAP_READY", ready)
	t.Setenv("PROBE_GAP_CHILD_PID", childPidFile)
	t.Setenv("PROBE_GAP_HOLDER_STARTED", holderStarted)
	t.Setenv("PROBE_GAP_RELEASE_DESCENDANT", releaseDescendant)
	t.Setenv("PROBE_GAP_DESCENDANT_DONE", descendantDone)
	// Best-effort failure-path teardown: release the holder even when the
	// test fails early, then wait for its exit without a Fatal that could
	// mask the original failure, so no run can strand the holder.
	t.Cleanup(func() {
		_ = os.WriteFile(releaseDescendant, []byte{}, 0o600)
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
	// Ordered live verdict: holder acknowledgment (published by the holder
	// itself after fork), readiness, both pre-exec lines consumed
	// through the live progress buffers, the published PID, and positive
	// kernel evidence that the child has not already exited. Awaiting the
	// holder-started marker before any direct-child signal proves the
	// inherited-descriptor premise; stopping or killing the parent before
	// the fork would otherwise invalidate the descendant-done oracle.
	waitForTestFile(t, holderStarted)
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "probe-gap-stdout", "probe-gap-stderr")
	waitForTestFile(t, childPidFile)
	childPid := phase13ReadChildPid(t, childPidFile)
	if pushDirectProcessAlreadyExited(childPid) {
		t.Fatalf("direct child PID %d already reads as exited before the ordered termination; the live-verdict premise cannot be established", childPid)
	}
	return gitCommit, done, cancel, childPid, releaseDescendant, descendantDone
}

// probeGapFindChild resolves the direct child to a signal target.
func probeGapFindChild(t *testing.T, pid int) *os.Process {
	t.Helper()
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("finding the direct child PID %d: %v", pid, err)
	}
	return proc
}

func TestPhase13OrderedProbeGapSigKillSurvivesCancel(t *testing.T) {
	_, done, cancel, childPid, releaseDescendant, descendantDone := probeGapStartPush(t, probeGapPushScript, "probe-gap-kill")
	proc := probeGapFindChild(t, childPid)
	// Freeze the live child first: a stopped child stays a definitive
	// live candidate, so the handshake's post-cancel SIGTERM can only
	// pend while the independent SIGKILL below is deterministically the
	// reaper-observed killer, however the probe and the signal
	// interleave. Program order on this goroutine (stop, cancel, kill)
	// is the explicit ordering; no sleep participates.
	if err := proc.Signal(syscall.SIGSTOP); err != nil {
		t.Fatalf("stopping the direct child PID %d: %v", childPid, err)
	}
	if pushDirectProcessAlreadyExited(childPid) {
		t.Fatalf("direct child PID %d reads as exited after SIGSTOP; the stopped-live premise cannot be established", childPid)
	}
	cancel()
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("delivering the independent probe-window SIGKILL to PID %d: %v", childPid, err)
	}
	phase13RequireDeathEvidence(t, childPid)

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the signal-terminated process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("an independent probe-window SIGKILL relabelled the signal-terminated process as cancelled; a distinguishable status must stay an observed process failure")
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Fatalf("Err() = %v, want the real direct-process *exec.ExitError from the independent SIGKILL", result.Err())
		}
		if !pushProcessKilledBySigKill(exitErr.ProcessState) {
			t.Error("the reaped status is not SIGKILL, so this run did not reach the claimed probe-window branch where the independent killer wins")
		}
		if result.ExitCode() != exitErr.ExitCode() {
			t.Errorf("ExitCode() = %d, want the reaped SIGKILL status %d", result.ExitCode(), exitErr.ExitCode())
		}
		if result.ExitCode() == 0 {
			t.Error("ExitCode() = 0, want the nonzero signal status, not a clean exit")
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not join the concurrent context cancellation for the independently terminated process", result.Err())
		}
		if result.Success() {
			t.Error("a signal-terminated push must not be reported as a success")
		}
		if !strings.Contains(string(result.Stdout()), "probe-gap-stdout") || !strings.Contains(string(result.Stderr()), "probe-gap-stderr") {
			t.Errorf("signal failure lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("signal failure push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		// Exit before completion notification: the silent holder still
		// keeps the inherited write ends open at result time, so the
		// direct exit preceded drain EOF.
		if _, err := os.Stat(descendantDone); err == nil {
			t.Error("the descendant exited before the classification, so the direct exit no longer precedes completion notification")
		} else if !os.IsNotExist(err) {
			t.Fatalf("checking the descendant hold-open marker: %v", err)
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after the probe-window SIGKILL although cancellation must bound the inherited-descriptor drain")
	}
}

func TestPhase13OrderedProbeGapLiveCancelControl(t *testing.T) {
	_, done, cancel, _, releaseDescendant, descendantDone := probeGapStartPush(t, probeGapPushScript, "probe-gap-live")
	// Control for the probe-window oracle above on the same fixture
	// with no independent killer and no stop: the cancellation
	// demonstrably terminates the still-running direct push, so a
	// policy of never cancelling concurrent cases would misclassify
	// this genuine live kill.
	cancel()

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
		if !strings.Contains(string(result.Stdout()), "probe-gap-stdout") || !strings.Contains(string(result.Stderr()), "probe-gap-stderr") {
			t.Errorf("live-killed push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("cancelled push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after the demonstrable live kill although cancellation must bound the inherited-descriptor drain")
	}
}

func TestPhase13ProbeGapSameSigtermOverlapProvesNothing(t *testing.T) {
	// The R1 indistinguishability pin for the probe window itself: a
	// live verdict is followed by an independent SIGTERM that leaves an
	// unreaped zombie for which a further SIGTERM succeeds and Wait
	// reports a SIGTERM status agreeing with the handshake's own
	// signal. That tuple therefore cannot prove the handshake's signal
	// caused the death. No ordered final-gap same-SIGTERM GitPush
	// assertion is claimed here: such a death racing the handshake's
	// own SIGTERM is observationally indistinguishable, so the
	// conservative contract keeps it an open blocked decision rather
	// than accepted coverage.
	victim := exec.Command("sleep", "30")
	if err := victim.Start(); err != nil {
		t.Fatalf("starting the boundary victim: %v", err)
	}
	pid := victim.Process.Pid
	defer func() { _ = victim.Wait() }()
	if pushDirectProcessAlreadyExited(pid) {
		t.Fatalf("a provably live PID %d already reads as exited, so the live-verdict premise of the same-signal race cannot be established", pid)
	}
	if err := victim.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("delivering the independent SIGTERM: %v", err)
	}
	phase13RequireDeathEvidence(t, pid)
	if err := victim.Process.Signal(syscall.SIGTERM); err != nil {
		t.Errorf("signal against the unreaped SIGTERMed zombie = %v, want nil success so the test proves signal-after-live-verdict cannot attribute causation", err)
	}
	waitErr := victim.Wait()
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("reaped wait error = %v, want the *exec.ExitError carrying the signal status", waitErr)
	}
	if !pushProcessKilledBySigTerm(exitErr.ProcessState) {
		t.Error("the reaped SIGTERM status does not read as SIGTERM; without the agreement the overlap with the handshake signal cannot be demonstrated")
	}
}

func TestPhase13ProbeGapContractAgreement(t *testing.T) {
	// Guards the cross-file amended phase-1.3 cancellation-outcome
	// policy: an active cancellation with successful SIGTERM delivery, no
	// known earlier completion and matching reaped SIGTERM is cancelled
	// despite the indistinguishable same-SIGTERM race, with no request
	// SIGTERM stays a process failure and no claim that matching status
	// proves signal provenance. No final-gap distinction oracle or
	// blocked/unresolved state is required.
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "devel", "13-harden-push-cancellation-and-partial-upstream-state.md"))
	if err != nil {
		t.Fatalf("reading the phase document for the distinction requirement: %v", err)
	}
	for _, want := range []string{
		"report cancelled even if an independent simultaneous SIGTERM is indistinguishable",
		"is cancelled despite an indistinguishable independent same-SIGTERM race",
		"No cancellation request means SIGTERM is a process failure",
		"Do not require a final-gap same-signal differentiation oracle",
		"matching status proves signal provenance",
	} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("the phase document no longer states %q; the amended active-cancellation SIGTERM policy must stay explicit", want)
		}
	}
	commitGo, err := os.ReadFile("commit.go")
	if err != nil {
		t.Fatalf("reading commit.go for the amended policy record: %v", err)
	}
	for _, want := range []string{
		"observes the process outcome without claiming cancellation",
		"keeps the cancellation claim",
		"matching status is not causal proof",
		"No independent SIGKILL, numeric exit or other signal",
	} {
		if !strings.Contains(string(commitGo), want) {
			t.Errorf("commit.go no longer records %q; the amended same-signal cancellation outcome must stay explicit", want)
		}
	}
	probe, err := os.ReadFile("push_liveness_unix.go")
	if err != nil {
		t.Fatalf("reading the Unix probe for the amended policy record: %v", err)
	}
	for _, want := range []string{
		"Only that agreement keeps a cancellation claim",
		"suppresses the cancellation claim",
		"Kill success alone never proves",
	} {
		if !strings.Contains(string(probe), want) {
			t.Errorf("push_liveness_unix.go no longer records %q; the probe must agree with the amended cancellation-outcome policy", want)
		}
	}
}
