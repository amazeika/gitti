package git

// Final-boundary remediation coverage for phase 1.1.1 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md,
// findings code:R1 (final liveness observation cannot prove cancellation
// caused SIGKILL) and code:R2 (unexpected silent ps failure suppresses
// termination of a live push).
//
// What the retained files leave open: the frozen round-1 test injects
// independent death inside the ps diagnostic, before the handshake's final
// recheck, and never covers an arbitrary silent nonzero ps exit; the
// probe-kill attribution file self-SIGKILLs with reaping delayed from the
// start rather than ordering death after the final live observation. The
// tests below cover exactly those orderings: an independent SIGKILL
// delivered test-side after the observed live point and the cancellation,
// so it lands inside the final observation-to-Kill window as an unreaped
// zombie; a unit pin that a live verdict followed by Kill-nil plus a
// SIGKILL reaped status cannot attribute causation; a silent exit-42 ps
// diagnostic that must read as unknown liveness rather than verified
// absence; a live child under that silent diagnostic that must still
// terminate boundedly on cancellation; and a plain demonstrable live kill
// as the control that the harness still reports cancelled with cause,
// status and output.
//
// The final-boundary kill is delivered by the test itself (never by a
// fixture killer) because a fixture process that writes to the inherited
// pipes after the death races the cancel-closed descriptors and cannot
// reliably report its own monitoring. The only fixture descendant is a
// silent pipe holder, so the direct exit also precedes any
// completion-notification EOF.
//
// Same-package helpers (gitCommitUnderTestWithFakeGit,
// pushRouteUnderTest, argvCarriesOperationArgs, waitForTestFile,
// waitForRegressionPushLines, releaseRegressionDescendantAndRequireExit)
// are reused from the retained files and are not redefined here.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ------------------------------------
//
//	Shadow PATH with a silent ps diagnostic that exits with the given code
//
// and no output, so the probe fallback faces an unexpected silent numeric
// failure rather than a verified unknown-PID report.
//
// ------------------------------------
func shadowSilentPsExit(t *testing.T, code int) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\nexit " + itoaForSilentPs(code) + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "ps"), []byte(script), 0o755); err != nil {
		t.Fatalf("shadowing the silent ps diagnostic: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func itoaForSilentPs(code int) string {
	if code == 0 {
		return "0"
	}
	digits := ""
	for n := code; n > 0; n /= 10 {
		digits = string(rune('0'+n%10)) + digits
	}
	return digits
}

func TestPushSilentArbitraryPsExitIsUnknownLiveness(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the POSIX PID-liveness probe is undeterminable on Windows by design")
	}
	// An unexpected silent nonzero ps exit (42, empty stdout/stderr)
	// against a provably live child must stay unknown liveness (false):
	// only a verified dead/zombie observation or the known unknown-PID
	// report may suppress the cancellation Kill. On platforms whose fast
	// kernel state already reports the child definitively live this never
	// reaches ps; where the ps fallback governs, the silent 42 must
	// still read as unknown liveness rather than verified absence.
	shadowSilentPsExit(t, 42)
	live := exec.Command("sleep", "30")
	if err := live.Start(); err != nil {
		t.Fatalf("starting the live probe process: %v", err)
	}
	defer func() {
		_ = live.Process.Kill()
		_ = live.Wait()
	}()
	if pushDirectProcessAlreadyExited(live.Process.Pid) {
		t.Errorf("silent exit-42 ps diagnostic against live PID %d reads as already exited, want unknown liveness so the live child still receives its Kill", live.Process.Pid)
	}
}

func TestPushKnownUnknownPidReportStillSuppressesUnneededKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the POSIX PID-liveness probe is undeterminable on Windows by design")
	}
	// Control for the arbitrary-code case above: the known unknown-PID
	// shape (silent exit 1) keeps meaning verified absence wherever the
	// ps fallback actually governs, and a vanished PID keeps reading
	// exited, so the fix for silent 42 must key on the diagnostic result
	// rather than treating every probe failure as unknown. Where the
	// fast kernel state already reports the child definitively live
	// (Linux /proc) the fallback never runs, so the same shadow must
	// read as live rather than verified absence.
	shadowSilentPsExit(t, 1)
	live := exec.Command("sleep", "30")
	if err := live.Start(); err != nil {
		t.Fatalf("starting the live probe process: %v", err)
	}
	defer func() {
		_ = live.Process.Kill()
		_ = live.Wait()
	}()
	// Portability note: pushDirectProcessKernelState is unix-only, so a
	// direct call breaks the Windows test build. For a provably live PID
	// its definitive verdict holds exactly where the fast kernel state
	// governs (Linux /proc reports definitive life; other platforms fall
	// through to the ps diagnostic), hence runtime.GOOS selects the same
	// branch without referencing the unix-only symbol.
	if runtime.GOOS == "linux" {
		if pushDirectProcessAlreadyExited(live.Process.Pid) {
			t.Errorf("live PID %d has definitive kernel life evidence, want unknown liveness (false) because the ps fallback never runs", live.Process.Pid)
		}
	} else if !pushDirectProcessAlreadyExited(live.Process.Pid) {
		t.Error("a silent exit-1 ps report is the known unknown-PID shape under this shadow, want it to keep reading as verified absence where the fallback governs")
	}
	reaped := exec.Command("true")
	if err := reaped.Start(); err != nil {
		t.Fatalf("starting the vanished-PID probe process: %v", err)
	}
	vanishedPid := reaped.Process.Pid
	if err := reaped.Wait(); err != nil {
		t.Fatalf("reaping the vanished-PID probe process: %v", err)
	}
	if !pushDirectProcessAlreadyExited(vanishedPid) {
		t.Errorf("a reaped-away PID %d still reads as live, want verified absence so no Kill is attempted against a dead child", vanishedPid)
	}
}

func TestGitPushSilentPsFailureStillTerminatesLiveChild(t *testing.T) {
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
      echo "silent42-stdout"
      echo "silent42-stderr" >&2
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
	// The uncovered R2 shape: an arbitrary silent nonzero ps exit (42,
	// no output) rather than the exit-1-with-stderr or signalled
	// diagnostics the retained files pin. It must remain unknown
	// liveness and must not prevent bounded termination of the
	// still-live child. Where the fast kernel state already reports
	// definitive life the fallback never runs; where the ps fallback
	// governs, the current code suppresses the Kill and this hangs.
	if err := os.WriteFile(filepath.Join(filepath.Dir(gitPath), "ps"), []byte("#!/bin/sh\nexit 42\n"), 0o755); err != nil {
		t.Fatalf("shadowing the ps diagnostic with a silent exit-42: %v", err)
	}
	ready := filepath.Join(root, "silent42-ready")
	releaseDescendant := filepath.Join(root, "silent42-release-descendant")
	descendantDone := filepath.Join(root, "silent42-descendant-done")
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
	// both pre-exec lines) while a descendant already holds the
	// inherited write ends, so the cancellation demonstrably targets a
	// still-live direct push under the silent failing diagnostic.
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "silent42-stdout", "silent42-stderr")
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the live process had started before the cancellation")
		}
		if !result.Cancelled() {
			t.Error("a silent exit-42 ps diagnostic must not suppress the live kill: killing the still-running direct push must report cancelled")
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
		if !strings.Contains(string(result.Stdout()), "silent42-stdout") || !strings.Contains(string(result.Stderr()), "silent42-stderr") {
			t.Errorf("live-killed push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("cancelled push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after cancelling the live push under a silent exit-42 ps diagnostic although cancellation must bound the inherited-descriptor drain")
	}
}

func TestPushSettledLiveVerdictCannotAttributeLaterSigKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("zombie Kill semantics are POSIX-specific")
	}
	// The exact R1 counterexample at the final boundary: a settled live
	// verdict is followed by an independent SIGKILL that leaves an
	// unreaped zombie for which Kill returns nil and Wait reports
	// SIGKILL. That tuple (live verdict, Kill success, SIGKILL status)
	// therefore cannot prove the later Kill caused the death; the
	// handshake must carry stronger attribution instead of lengthening
	// the settling poll.
	victim := exec.Command("sleep", "30")
	if err := victim.Start(); err != nil {
		t.Fatalf("starting the boundary victim: %v", err)
	}
	pid := victim.Process.Pid
	defer func() { _ = victim.Wait() }()
	if pushDirectProcessSettledDead(pid) {
		t.Fatalf("a provably live PID %d already reads as settled dead, so the live-verdict premise of the final-boundary race cannot be established", pid)
	}
	if err := victim.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("delivering the independent SIGKILL: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !pushDirectProcessAlreadyExited(pid) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !pushDirectProcessAlreadyExited(pid) {
		t.Fatalf("an independently SIGKILLed unreaped PID %d still reads as live; the probe cannot supply the death evidence the handshake needs", pid)
	}
	// The handshake's Kill landing now succeeds on the zombie without
	// having caused anything: Kill success after a stale live verdict
	// proves nothing by itself.
	if err := victim.Process.Kill(); err != nil {
		t.Errorf("Kill against the unreaped SIGKILLed zombie = %v, want nil success so the test proves Kill-after-live-verdict cannot attribute causation", err)
	}
	waitErr := victim.Wait()
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("reaped wait error = %v, want the *exec.ExitError carrying the signal status", waitErr)
	}
	if !pushProcessKilledBySigKill(victim.ProcessState) {
		t.Error("reaped SIGKILL status does not read as SIGKILL, want the status filter to agree so the ambiguity is isolated to causation rather than status")
	}
}

func TestGitPushFinalBoundaryIndependentSigKillSurvivesCancel(t *testing.T) {
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
      echo "finalbound-stdout"
      echo "finalbound-stderr" >&2
      echo $$ > "$FAKE_GIT_CHILD_PID"
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
	ready := filepath.Join(root, "finalbound-ready")
	childPidFile := filepath.Join(root, "finalbound-child-pid")
	releaseDescendant := filepath.Join(root, "finalbound-release-descendant")
	descendantDone := filepath.Join(root, "finalbound-descendant-done")
	t.Setenv("FAKE_GIT_READY", ready)
	t.Setenv("FAKE_GIT_CHILD_PID", childPidFile)
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
	// Ordering, unlike the frozen inside-ps race: the direct child is
	// provably live (ready file, published PID file, plus both pre-exec
	// lines consumed through the live progress buffers) at the moment of
	// the ordered stop-before-cancel-before-kill sequence, and the test
	// itself then delivers the independent SIGKILL, so the death lands
	// inside the final observation-to-Kill window as an unreaped zombie
	// whose reaped status is deterministically SIGKILL. The stop freezes
	// the live child first, so the handshake's post-cancel SIGTERM can
	// only pend and cannot win the race to determine the exit status. The kill stays test-side rather than a
	// fixture killer: a fixture process that writes to the inherited
	// pipes after the death races the cancel-closed descriptors and dies
	// before its own monitor can report. The silent holder descendant
	// keeps the inherited write ends open throughout, so the direct exit
	// also precedes any completion-notification EOF: the late
	// cancellation bounds the drain rather than racing it.
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "finalbound-stdout", "finalbound-stderr")
	waitForTestFile(t, childPidFile)
	pidRaw, err := os.ReadFile(childPidFile)
	if err != nil {
		t.Fatalf("reading the direct-child PID file: %v", err)
	}
	childPid, err := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if err != nil || childPid <= 0 {
		t.Fatalf("parsing the direct-child PID %q: %v", strings.TrimSpace(string(pidRaw)), err)
	}
	// Stop-before-cancel-before-kill (finding R3): without the stop the
	// handshake's cancellation SIGTERM could win the race, and a
	// cancelled outcome would then be the correct classification. The
	// stop keeps the child a definitive live candidate so the SIGTERM can
	// only pend, while the test-side SIGKILL below is deterministically
	// the reaper-observed killer. Program order on this goroutine (stop,
	// cancel, kill) is the explicit ordering; no sleep participates.
	proc, findErr := os.FindProcess(childPid)
	if findErr != nil {
		t.Fatalf("finding the direct child PID %d for the ordered final-boundary sequence: %v", childPid, findErr)
	}
	// Failure cleanup: a stopped child stranded by a later failure would
	// survive the descendant release, so best-effort resume and kill it
	// without a Fatal that could mask the original failure.
	t.Cleanup(func() {
		_ = proc.Signal(syscall.SIGCONT)
		_ = proc.Kill()
	})
	if err := proc.Signal(syscall.SIGSTOP); err != nil {
		t.Fatalf("stopping the direct child PID %d: %v", childPid, err)
	}
	if pushDirectProcessAlreadyExited(childPid) {
		t.Fatalf("direct child PID %d reads as exited after SIGSTOP; the stopped-live premise cannot be established", childPid)
	}
	cancel()
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("delivering the independent final-boundary SIGKILL to PID %d: %v", childPid, err)
	}
	// Non-vacuous oracle: the independent death must be positively
	// observed (unreaped zombie, or PID vanished once the single reaper
	// returned) before the classification below is judged.
	killDeadline := time.Now().Add(5 * time.Second)
	for !pushDirectProcessAlreadyExited(childPid) && time.Now().Before(killDeadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !pushDirectProcessAlreadyExited(childPid) {
		t.Fatalf("the independently SIGKILLed direct child PID %d never showed death evidence; without the ordered death the classification oracle is vacuous", childPid)
	}
	// Exit before completion notification: the silent holder still keeps
	// the inherited write ends open, so the direct exit precedes drain EOF.
	if _, err := os.Stat(descendantDone); err == nil {
		t.Fatalf("descendant exited before the classification, so the direct exit no longer precedes completion notification")
	} else if !os.IsNotExist(err) {
		t.Fatalf("checking the descendant hold-open marker: %v", err)
	}

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the signal-terminated process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("independent SIGKILL in the final observation-to-Kill window relabelled the signal-terminated process as cancelled")
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the direct process *exec.ExitError from the independent signal termination", result.Err())
		} else {
			if !pushProcessKilledBySigKill(exitErr.ProcessState) {
				t.Error("the reaped status is not SIGKILL, so this run did not reach the claimed final-boundary branch where the independent killer wins")
			}
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
		if !strings.Contains(string(result.Stdout()), "finalbound-stdout") || !strings.Contains(string(result.Stderr()), "finalbound-stderr") {
			t.Errorf("signal failure lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("signal failure push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the final-boundary SIGKILL drain")
	}
}

func TestGitPushDemonstrableLiveKillRetainsCauseAndOutput(t *testing.T) {
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
      echo "livekill-stdout"
      echo "livekill-stderr" >&2
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
	// Control for the independent-death test above with no shadowed ps
	// and no independent killer: the cancellation demonstrably
	// terminates the still-running direct push, so the result must be
	// cancelled while retaining the reaped status/error, the context
	// cause and the captured output.
	ready := filepath.Join(root, "livekill-ready")
	releaseDescendant := filepath.Join(root, "livekill-release-descendant")
	descendantDone := filepath.Join(root, "livekill-descendant-done")
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
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "livekill-stdout", "livekill-stderr")
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
		if !strings.Contains(string(result.Stdout()), "livekill-stdout") || !strings.Contains(string(result.Stderr()), "livekill-stderr") {
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
