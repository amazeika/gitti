package git

// Additive regression coverage for phase 1.3 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md:
//
//	Preserve causal direct-push outcomes and owned-pipe cleanup.
//
// The retained files pin distinguishable SIGKILL orderings, live-cancel
// controls, filter semantics for numeric/SIGKILL exits, ordinary
// descriptor cleanup and guard refusals. The tests below add the phase-1.3
// remainder: an ordered pre-cancel same-SIGTERM oracle (independent SIGTERM
// delivered test-side after a live verdict, with death and the production
// reaper's return both acknowledged before the late cancellation, while
// inherited descriptors stay held open) paired with a live-cancel control on
// the same fixture, a unit pin that signal success plus a matching
// SIGTERM status cannot attribute causation, and source-text pins for
// the R3/R4 split-comment requirement and the cross-file conservative
// simultaneous-outcome agreement. Same-package helpers
// (gitCommitUnderTestWithFakeGit, pushRouteUnderTest,
// argvCarriesOperationArgs, waitForTestFile,
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

// phase13GapPushScript answers the generation-executor upstream probe and
// runs a scripted push whose direct child publishes its PID, signals
// readiness, then idles as sleep while a silent descendant holds the
// inherited descriptors open, so the direct exit precedes any
// completion-notification EOF.
const phase13GapPushScript = `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
    push)
      echo "phase13-gap-stdout"
      echo "phase13-gap-stderr" >&2
      echo $$ > "$PHASE13_GAP_CHILD_PID"
      touch "$PHASE13_GAP_READY"
      ( while [ ! -e "$PHASE13_GAP_RELEASE_DESCENDANT" ]; do :; done
        touch "$PHASE13_GAP_DESCENDANT_DONE"
      ) &
      exec sleep 30
      ;;
  esac
done
exit 0
`

// phase13ReadChildPid reads the direct-child PID the fixture published,
// so the test-side signal targets exactly the reaped process.
func phase13ReadChildPid(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the direct-child PID file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		t.Fatalf("parsing the direct-child PID %q: %v", strings.TrimSpace(string(raw)), err)
	}
	return pid
}

// phase13RequireDeathEvidence waits for positive kernel evidence that the
// independently signalled child died unreaped, so the classification
// below is judged on an ordered death rather than a vacuous race.
func phase13RequireDeathEvidence(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !pushDirectProcessAlreadyExited(pid) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !pushDirectProcessAlreadyExited(pid) {
		t.Fatalf("the independently SIGTERMed direct child PID %d never showed death evidence; without the ordered death the classification oracle is vacuous", pid)
	}
}

// phase13RequireProductionReaperReturn polls until signal 0 reports ESRCH,
// proving GitPush's single reaper already returned (PID vanished) before the
// late cancellation. A zombie proves death; only disappearance proves the
// production boundary observed it. EPERM or any other error still means the
// PID exists, so the wait continues until the deadline.
func phase13RequireProductionReaperReturn(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil && errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); err != nil && errors.Is(err, syscall.ESRCH) {
		return
	}
	t.Fatalf("direct child PID %d never vanished before the late cancellation; without the production-reaper acknowledgment the pre-cancel ordering is vacuous", pid)
}

func TestPhase13SameSigtermGapSurvivesCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell signals")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, phase13GapPushScript)
	ready := filepath.Join(root, "phase13-gap-ready")
	childPidFile := filepath.Join(root, "phase13-gap-child-pid")
	releaseDescendant := filepath.Join(root, "phase13-gap-release-descendant")
	descendantDone := filepath.Join(root, "phase13-gap-descendant-done")
	t.Setenv("PHASE13_GAP_READY", ready)
	t.Setenv("PHASE13_GAP_CHILD_PID", childPidFile)
	t.Setenv("PHASE13_GAP_RELEASE_DESCENDANT", releaseDescendant)
	t.Setenv("PHASE13_GAP_DESCENDANT_DONE", descendantDone)
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
	// Ordering, each step an explicit signal (R2 repair): the direct child is
	// provably live (readiness, published PID, both pre-exec lines consumed
	// through the live progress buffers, positive kernel liveness with no
	// settled death) before the test itself delivers the independent SIGTERM.
	// The test then observes death (zombie) and the production reaper's return
	// (PID vanished via ESRCH) before cancelling, so the classification below
	// judges an independently-terminated-before-cancel outcome with the drain
	// still held open. This oracle deliberately does NOT claim the final
	// observation-to-signal gap: a same-SIGTERM death racing the handshake's
	// own SIGTERM is observationally indistinguishable (commit.go and
	// push_liveness_unix.go) and stays unresolved without a production seam,
	// so no ordered final-gap same-SIGTERM coverage is claimed here.
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "phase13-gap-stdout", "phase13-gap-stderr")
	waitForTestFile(t, childPidFile)
	childPid := phase13ReadChildPid(t, childPidFile)
	if pushDirectProcessAlreadyExited(childPid) {
		t.Fatalf("direct child PID %d already reads as exited before the test-ordered independent SIGTERM; the live-verdict premise cannot be established", childPid)
	}
	if pushDirectProcessSettledDead(childPid) {
		t.Fatalf("direct child PID %d already reads as settled dead before the test-ordered independent SIGTERM; the live-verdict premise cannot be established", childPid)
	}
	proc, findErr := os.FindProcess(childPid)
	if findErr != nil {
		t.Fatalf("finding the direct child PID %d for the independent SIGTERM: %v", childPid, findErr)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("delivering the independent pre-cancel SIGTERM to PID %d: %v", childPid, err)
	}
	phase13RequireDeathEvidence(t, childPid)
	phase13RequireProductionReaperReturn(t, childPid)
	// Exit before completion notification: the single reaper returned but the
	// silent holder still keeps the inherited write ends open, so the drain
	// cannot have reached EOF and GitPush cannot have returned yet.
	select {
	case unexpected := <-done:
		t.Fatalf("GitPush returned before the late cancellation (cancelled=%v exit=%d); the direct exit must precede completion notification", unexpected.Cancelled(), unexpected.ExitCode())
	default:
	}
	if _, err := os.Stat(descendantDone); err == nil {
		t.Error("the descendant exited before the classification, so the direct exit no longer precedes completion notification")
	} else if !os.IsNotExist(err) {
		t.Fatalf("checking the descendant hold-open marker: %v", err)
	}
	// The late cancellation bounds the inherited-descriptor drain after the
	// independently reaped SIGTERM death above.
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the signal-terminated process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("an independent pre-cancel SIGTERM relabelled the signal-terminated process as cancelled although the production reaper had already returned before cancellation")
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the real direct-process *exec.ExitError from the independent SIGTERM", result.Err())
		} else {
			if result.ExitCode() != exitErr.ExitCode() {
				t.Errorf("ExitCode() = %d, want the reaped signal status %d", result.ExitCode(), exitErr.ExitCode())
			}
			if result.ExitCode() == 0 {
				t.Error("ExitCode() = 0, want the nonzero signal status, not a clean exit")
			}
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not join the late context cancellation for the independently terminated process", result.Err())
		}
		if result.Success() {
			t.Error("a signal-terminated push must not be reported as a success")
		}
		if !strings.Contains(string(result.Stdout()), "phase13-gap-stdout") || !strings.Contains(string(result.Stderr()), "phase13-gap-stderr") {
			t.Errorf("signal failure lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("signal failure push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the final-gap SIGTERM drain")
	}
}

func TestPhase13LiveCancelControlStaysCancelled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell signals")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, phase13GapPushScript)
	ready := filepath.Join(root, "phase13-live-ready")
	childPidFile := filepath.Join(root, "phase13-live-child-pid")
	releaseDescendant := filepath.Join(root, "phase13-live-release-descendant")
	descendantDone := filepath.Join(root, "phase13-live-descendant-done")
	t.Setenv("PHASE13_GAP_READY", ready)
	t.Setenv("PHASE13_GAP_CHILD_PID", childPidFile)
	t.Setenv("PHASE13_GAP_RELEASE_DESCENDANT", releaseDescendant)
	t.Setenv("PHASE13_GAP_DESCENDANT_DONE", descendantDone)
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
	// Control for the independent-death oracle above on the same
	// fixture with no independent killer: the cancellation demonstrably
	// terminates the still-running direct push.
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "phase13-gap-stdout", "phase13-gap-stderr")
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
		if !strings.Contains(string(result.Stdout()), "phase13-gap-stdout") || !strings.Contains(string(result.Stderr()), "phase13-gap-stderr") {
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

func TestPhase13IndependentSigtermAgreementProvesNothingByItself(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("zombie signal semantics are POSIX-specific")
	}
	// The R1 indistinguishability pin for the same signal: a settled
	// live verdict is followed by an independent SIGTERM that leaves an
	// unreaped zombie for which a further signal succeeds and Wait
	// reports a SIGTERM status agreeing with the handshake's own
	// signal. That tuple therefore cannot prove the handshake's signal
	// caused the death; the ordered GitPush oracle above must carry the
	// attribution instead of the status filter.
	victim := exec.Command("sleep", "30")
	if err := victim.Start(); err != nil {
		t.Fatalf("starting the boundary victim: %v", err)
	}
	pid := victim.Process.Pid
	defer func() { _ = victim.Wait() }()
	if pushDirectProcessSettledDead(pid) {
		t.Fatalf("a provably live PID %d already reads as settled dead, so the live-verdict premise of the same-signal race cannot be established", pid)
	}
	if err := victim.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("delivering the independent SIGTERM: %v", err)
	}
	phase13RequireDeathEvidence(t, pid)
	// A further signal landing now succeeds on the zombie without having
	// caused anything: signal success after a stale live verdict proves
	// nothing by itself.
	if err := victim.Process.Signal(syscall.SIGTERM); err != nil {
		t.Errorf("signal against the unreaped SIGTERMed zombie = %v, want nil success so the test proves signal-after-live-verdict cannot attribute causation", err)
	}
	waitErr := victim.Wait()
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("reaped wait error = %v, want the *exec.ExitError carrying the signal status", waitErr)
	}
	if !pushProcessKilledBySigTerm(victim.ProcessState) {
		t.Error("the reaped SIGTERM status does not read as SIGTERM; without the agreement the overlap with the handshake signal cannot be demonstrated")
	}
}

// phase13NormalizeComment collapses a source window to single spaces so
// sentence boundaries are comparable across comment line breaks.
func phase13NormalizeComment(s string) string {
	s = strings.ReplaceAll(s, "//", " ")
	return strings.Join(strings.Fields(s), " ")
}

// phase13SourceWindow returns the normalized source between two anchors,
// failing the test when the anchor text moved.
func phase13SourceWindow(t *testing.T, body, start, end string) string {
	t.Helper()
	startIdx := strings.Index(body, start)
	if startIdx < 0 {
		t.Fatalf("anchor %q not found; the comment moved and this pin needs updating", start)
	}
	rest := body[startIdx:]
	endIdx := strings.Index(rest, end)
	if endIdx < 0 {
		t.Fatalf("anchor %q not found after %q; the comment moved and this pin needs updating", end, start)
	}
	return phase13NormalizeComment(rest[:endIdx+len(end)])
}

func TestPhase13R3ReapedStatusCommentSplit(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not report this test file, so the R3 comment cannot be located")
	}
	body, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "commit.go"))
	if err != nil {
		t.Fatalf("reading commit.go for the R3 split: %v", err)
	}
	commitGo := string(body)
	// R3: state the SIGTERM status check first, then put the independent
	// exit cases and their consequence in a separate sentence.
	window := phase13SourceWindow(t, commitGo,
		"The reaped status check below must still",
		"even though cancellation was requested.")
	splitIdx := strings.Index(window, "independent SIGKILL")
	if splitIdx < 0 {
		t.Fatalf("the R3 independent-exit counterexample is missing from the status-check comment: %q", window)
	}
	if strings.Count(window[:splitIdx], ".") < 1 {
		t.Errorf("R3 status-check comment still joins the SIGTERM criterion to the independent-exit consequence with a colon; want the SIGTERM criterion and the independent-exit consequence in separate sentences: %q", window)
	}
	// The amended phase-1.3 policy keeps the cancellation claim for the
	// same-SIGTERM overlap: an active cancellation with successful SIGTERM
	// delivery, no known earlier completion and matching reaped SIGTERM is
	// cancelled despite the indistinguishable race, without claiming the
	// matching status proves signal provenance. Nonmatching exits stay
	// observed failures.
	keptClaim := phase13SourceWindow(t, commitGo,
		"Only a successfully signalled",
		"can be relabelled that way.")
	if !strings.Contains(keptClaim, "keeps the cancellation claim") && !strings.Contains(commitGo, "keeps the cancellation claim") {
		t.Errorf("commit.go no longer keeps the amended cancellation claim for the same-SIGTERM overlap: %q", keptClaim)
	}
	if !strings.Contains(commitGo, "matching status is not causal proof") {
		t.Error("commit.go no longer rejects matching status as causal proof; the amended policy classifies the overlap as cancelled without claiming signal provenance")
	}
	if !strings.Contains(commitGo, "No independent SIGKILL, numeric exit or other signal") {
		t.Error("commit.go no longer restricts the amended cancellation claim to matching SIGTERM; nonmatching exits must stay observed process failures")
	}
}

func TestPhase13R4SignalFilterCommentSplit(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not report this test file, so the R4 comment cannot be located")
	}
	body, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "commit.go"))
	if err != nil {
		t.Fatalf("reading commit.go for the R4 split: %v", err)
	}
	commitGo := string(body)
	if !strings.Contains(commitGo, "never retroactively attributed") {
		t.Error("the R4 zero-exit guard moved; a zero-exit Wait must stay independently completed and never be retroactively attributed to cancellation")
	}
	// R4: split the SIGTERM criterion from the examples of outcomes
	// that do not match it.
	window := phase13SourceWindow(t, commitGo,
		"A signal success alone is not proof",
		"likewise stays observed.")
	splitIdx := strings.Index(window, "independent SIGKILL")
	if splitIdx < 0 {
		t.Fatalf("the R4 nonmatching-outcome examples are missing from the signal-filter comment: %q", window)
	}
	if !strings.Contains(window, "agrees with our distinguishable") {
		t.Errorf("the R4 SIGTERM criterion moved out of the signal-filter comment: %q", window)
	}
	seg := strings.TrimSpace(window[:splitIdx])
	if !strings.HasSuffix(seg, ".") {
		t.Errorf("R4 signal-filter comment still joins the SIGTERM criterion to the nonmatching examples with a comma; want the criterion closed as its own sentence before the examples: %q", window)
	}
}

func TestPhase13DocRequiresOrderedOracleForClaimedDistinction(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not report this test file, so the phase document cannot be located")
	}
	pkgDir := filepath.Dir(thisFile)
	body, err := os.ReadFile(filepath.Join(pkgDir, "..", "..", "docs", "devel", "13-harden-push-cancellation-and-partial-upstream-state.md"))
	if err != nil {
		t.Fatalf("reading the phase document for the distinction requirement: %v", err)
	}
	doc := string(body)
	// Guards the document on the amended phase-1.3 policy: an active
	// cancellation with delivered SIGTERM and matching reaped SIGTERM is
	// cancelled despite the indistinguishable same-SIGTERM race, with no
	// request SIGTERM stays a process failure, and no final-gap oracle or
	// provenance claim is required.
	if !strings.Contains(doc, "report cancelled even if an independent simultaneous SIGTERM is indistinguishable") {
		t.Error("the phase document no longer states the amended active-cancellation SIGTERM classification")
	}
	if !strings.Contains(doc, "No cancellation request means SIGTERM is a process failure") {
		t.Error("the phase document no longer states the no-request SIGTERM failure rule")
	}
	if !strings.Contains(doc, "Do not require a final-gap same-signal differentiation oracle") {
		t.Error("the phase document no longer rejects a final-gap same-signal differentiation oracle; matching status must not be claimed as signal provenance")
	}
	if !strings.Contains(doc, "matching status proves signal provenance") {
		t.Error("the phase document no longer rejects matching status as signal provenance proof")
	}
}
