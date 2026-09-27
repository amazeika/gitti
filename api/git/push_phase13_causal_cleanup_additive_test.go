package git

// Additive regression coverage for phase 1.3 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md:
//
//	Preserve causal direct-push outcomes and owned-pipe cleanup.
//
// Each test below uses explicit ordering signals (readiness files,
// published progress lines, descendant hold-open markers) and never
// wall-clock sleeps to decide an outcome. Same-package helpers
// (gitCommitUnderTestWithFakeGit, pushRouteUnderTest,
// argvCarriesOperationArgs, waitForTestFile,
// waitForRegressionPushLines, releaseRegressionDescendantAndRequireExit,
// countOpenFDsForCausality, gitPushWithCausalityTimeout) are reused from
// the retained files and are not redefined here.

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

// phase13CleanupProbeScript answers the generation-executor upstream probe;
// the push branch of each test below overrides the scripted behavior.
const phase13CleanupProbeScript = `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
`

func TestPhase13IndependentNonzeroExitKeepsObservedOutcome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell signals")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, phase13CleanupProbeScript+`    push)
      echo "phase13-nz-stdout"
      echo "phase13-nz-stderr" >&2
      parent=$$
      ( while kill -0 "$parent" 2>/dev/null; do
          state=$(ps -o stat= -p "$parent" 2>/dev/null)
          case "$state" in Z*) break ;; esac
        done
        touch "$PHASE13_NZ_PARENT_DEAD"
        while [ ! -e "$PHASE13_NZ_RELEASE_DESCENDANT" ]; do :; done
        touch "$PHASE13_NZ_DESCENDANT_DONE"
      ) &
      exit 3
      ;;
  esac
done
exit 0
`)
	parentDead := filepath.Join(root, "phase13-nz-parent-dead")
	releaseDescendant := filepath.Join(root, "phase13-nz-release-descendant")
	descendantDone := filepath.Join(root, "phase13-nz-descendant-done")
	t.Setenv("PHASE13_NZ_PARENT_DEAD", parentDead)
	t.Setenv("PHASE13_NZ_RELEASE_DESCENDANT", releaseDescendant)
	t.Setenv("PHASE13_NZ_DESCENDANT_DONE", descendantDone)
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
	// Ordering: the direct child exited nonzero while the descendant
	// holds both inherited write ends open, and both readers consumed
	// the pre-exit bytes before the late cancellation bounds the drain.
	waitForTestFile(t, parentDead)
	waitForRegressionPushLines(t, gitCommit, "phase13-nz-stdout", "phase13-nz-stderr")
	// The direct exit precedes completion notification: the descendant
	// still holds both inherited write ends open, so the drain cannot
	// have reached EOF before the cancellation below bounds it.
	if _, err := os.Stat(descendantDone); err == nil {
		t.Error("the descendant released the inherited descriptors before the cancellation, so the drain could already have completed")
	} else if !os.IsNotExist(err) {
		t.Fatalf("checking the descendant hold-open marker: %v", err)
	}
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the nonzero-exit process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("late context cancellation relabelled the independently failed nonzero process")
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
		if !strings.Contains(string(result.Stdout()), "phase13-nz-stdout") || !strings.Contains(string(result.Stderr()), "phase13-nz-stderr") {
			t.Errorf("nonzero push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		releaseRegressionDescendantAndRequireExit(t, releaseDescendant, descendantDone)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the nonzero drain")
	}
}

func TestPhase13LiveCancelTerminatesStillRunningPush(t *testing.T) {
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, phase13CleanupProbeScript+`    push)
      echo "phase13-live-stdout"
      echo "phase13-live-stderr" >&2
      touch "$PHASE13_LIVE_READY"
      exec sleep 30
      ;;
  esac
done
exit 0
`)
	ready := filepath.Join(root, "phase13-live-ready")
	t.Setenv("PHASE13_LIVE_READY", ready)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	// The child is provably running (readiness plus both pre-exec
	// lines consumed) when the cancellation lands, so the kill is
	// demonstrably context-caused.
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "phase13-live-stdout", "phase13-live-stderr")
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
		if !strings.Contains(string(result.Stdout()), "phase13-live-stdout") || !strings.Contains(string(result.Stderr()), "phase13-live-stderr") {
			t.Errorf("live-killed push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after the demonstrable live kill")
	}
}

func TestPhase13OrdinaryCompletionClosesOwnedDescriptors(t *testing.T) {
	gitCommit, _, _, _, _ := gitCommitUnderTestWithFakeGit(t, phase13CleanupProbeScript+`    push)
      echo "phase13-eof-stdout"
      echo "phase13-eof-stderr" >&2
      exit 0
      ;;
  esac
done
exit 0
`)
	// No cancellation, no descendants: both owned read ends must close
	// on the ordinary EOF return without relying on garbage collection.
	before := countOpenFDsForCausality(t)
	result := gitPushWithCausalityTimeout(t, gitCommit, context.Background(), pushRouteUnderTest("master"), 10*time.Second, "after the ordinary successful push")
	if !result.Success() {
		t.Fatalf("ordinary push did not succeed: exit %d, cancelled %v, err %v", result.ExitCode(), result.Cancelled(), result.Err())
	}
	if !strings.Contains(string(result.Stdout()), "phase13-eof-stdout") || !strings.Contains(string(result.Stderr()), "phase13-eof-stderr") {
		t.Errorf("ordinary push lost streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
	}
	second := gitPushWithCausalityTimeout(t, gitCommit, context.Background(), pushRouteUnderTest("master"), 10*time.Second, "after the repeated ordinary push")
	if !second.Success() {
		t.Fatalf("repeated ordinary push did not succeed: exit %d, err %v", second.ExitCode(), second.Err())
	}
	if delta := countOpenFDsForCausality(t) - before; delta > 2 {
		t.Errorf("open file descriptors grew by %d across two ordinary completions, want both owned read ends closed on each return", delta)
	}
}

func TestPhase13ResultAccessorsImmutableAndArgvIntact(t *testing.T) {
	gitCommit, _, _, _, _ := gitCommitUnderTestWithFakeGit(t, phase13CleanupProbeScript+`    push)
      echo "phase13-imm-stdout"
      echo "phase13-imm-stderr" >&2
      exit 0
      ;;
  esac
done
exit 0
`)
	result := gitPushWithCausalityTimeout(t, gitCommit, context.Background(), pushRouteUnderTest("master"), 10*time.Second, "after the accessor push")
	if !result.Success() {
		t.Fatalf("accessor push did not succeed: exit %d, err %v", result.ExitCode(), result.Err())
	}
	if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
		t.Errorf("push argv = %v, want the tracked-branch operation arguments", result.Argv())
	}
	// Mutating every returned copy must leave the stored result intact.
	argv := result.Argv()
	for i := range argv {
		argv[i] = "mutated"
	}
	stdout := result.Stdout()
	for i := range stdout {
		stdout[i] = 'X'
	}
	stderr := result.Stderr()
	for i := range stderr {
		stderr[i] = 'Y'
	}
	stored := gitCommit.GitPushResult()
	if argvCarriesOperationArgs(stored.Argv(), []string{"mutated"}) {
		t.Errorf("stored argv = %v, mutated through the accessor copy", stored.Argv())
	}
	if strings.Contains(string(stored.Stdout()), "X") || strings.Contains(string(stored.Stderr()), "Y") {
		t.Errorf("stored streams mutated through accessor copies: stdout %q, stderr %q", stored.Stdout(), stored.Stderr())
	}
	if stored.Started() != result.Started() || stored.ExitCode() != result.ExitCode() || stored.Cancelled() != result.Cancelled() {
		t.Error("stored result status diverged from the returned result")
	}
}

func TestPhase13GuardSetupCancelStartsNothing(t *testing.T) {
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, phase13CleanupProbeScript+`    push)
      touch "$PHASE13_GUARD_PUSH_RAN"
      echo "must-not-run"
      exit 0
      ;;
  esac
done
exit 0
`)
	pushRan := filepath.Join(root, "phase13-guard-push-ran")
	t.Setenv("PHASE13_GUARD_PUSH_RAN", pushRan)

	// A context cancelled while the ActiveGuard holds the pre-Start
	// gate stays unstarted and cancelled with exit -1, joining the
	// guard refusal with the context cause and launching no push. The
	// guard itself delivers the cancellation, so the refusal branch
	// (not the earlier pre-launch check) carries both causes.
	// No timing is involved: cancel() is synchronous, so ctx.Err() is
	// already visible when the guard returns.
	guardErr := errors.New("the git operations generation changed after the push was confirmed")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	route := pushRouteUnderTest("master")
	route.ActiveGuard = func() error {
		cancel()
		return guardErr
	}
	before := countOpenFDsForCausality(t)
	result := gitPushWithCausalityTimeout(t, gitCommit, ctx, route, 10*time.Second, "after the guard-time cancellation")

	if result.Started() {
		t.Error("a guard-time cancellation must not start the prepared process")
	}
	if !result.Cancelled() {
		t.Error("a guard-time cancellation must report cancelled with no exit status")
	}
	if result.ExitCode() != -1 {
		t.Errorf("ExitCode() = %d, want -1 for the absent process status", result.ExitCode())
	}
	if !errors.Is(result.Err(), guardErr) {
		t.Errorf("Err() = %v, want the guard refusal error joined with the context cause", result.Err())
	}
	if !errors.Is(result.Err(), context.Canceled) {
		t.Errorf("Err() = %v, want the context cancellation cause", result.Err())
	}
	if result.Success() {
		t.Error("a guard-time cancellation must not be reported as a success")
	}
	if len(result.Argv()) != 0 {
		t.Errorf("Argv() = %v, want no command retained after a guard-time refusal", result.Argv())
	}
	if _, err := os.Stat(pushRan); err == nil {
		t.Error("the push process launched although the guard held the pre-Start gate under cancellation")
	} else if !os.IsNotExist(err) {
		t.Errorf("checking the push launch marker: %v", err)
	}
	if delta := countOpenFDsForCausality(t) - before; delta > 2 {
		t.Errorf("open file descriptors grew by %d across the guard-time refusal, want all setup pipe ends closed", delta)
	}
}

func TestPhase13DocKeepsConservativeSimultaneousOutcomeContract(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not report this test file, so the phase document cannot be located")
	}
	pkgDir := filepath.Dir(thisFile)
	body, err := os.ReadFile(filepath.Join(pkgDir, "..", "..", "docs", "devel", "13-harden-push-cancellation-and-partial-upstream-state.md"))
	if err != nil {
		t.Fatalf("reading the phase document for the amended cancellation-outcome policy: %v", err)
	}
	doc := string(body)
	// Guards the cross-file agreement on the amended phase-1.3 policy:
	// an active cancellation with successful SIGTERM delivery, no known
	// earlier completion and matching reaped SIGTERM is cancelled despite
	// an indistinguishable independent same-SIGTERM race, with no claim
	// that matching status proves signal provenance; without a
	// cancellation request SIGTERM stays an observed process failure.
	for _, want := range []string{"report cancelled even if an independent simultaneous SIGTERM is indistinguishable", "is cancelled despite an indistinguishable independent same-SIGTERM race", "No cancellation request means SIGTERM is a process failure", "Do not require a final-gap same-signal differentiation oracle", "matching status proves signal provenance"} {
		if !strings.Contains(doc, want) {
			t.Errorf("the phase document no longer states %q; the amended active-cancellation SIGTERM policy must stay explicit", want)
		}
	}
}

func TestPhase13SigTermSigKillFilterDistinction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal reaping semantics are POSIX-specific")
	}
	// R3 pins the reaped-status filters the handshake depends on: an
	// independent SIGTERM reaps a status that agrees with the
	// handshake's own signal, while an independent SIGKILL never does,
	// so only the SIGKILL case can be told apart from a live kill by
	// status alone. Ordered with explicit process control, no sleeps.
	termVictim := exec.Command("sleep", "30")
	if err := termVictim.Start(); err != nil {
		t.Fatalf("starting the SIGTERM victim: %v", err)
	}
	if err := termVictim.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("delivering the independent SIGTERM: %v", err)
	}
	termWaitErr := termVictim.Wait()
	var termExitErr *exec.ExitError
	if !errors.As(termWaitErr, &termExitErr) {
		t.Fatalf("reaped wait error = %v, want the *exec.ExitError carrying the signal status", termWaitErr)
	}
	if !pushProcessKilledBySigTerm(termVictim.ProcessState) {
		t.Error("an independent SIGTERM victim does not read as SIGTERM; the same-signal overlap premise cannot be demonstrated")
	}
	if pushProcessKilledBySigKill(termVictim.ProcessState) {
		t.Error("an independent SIGTERM victim reads as SIGKILL; the two signal filters must stay distinguishable")
	}

	killVictim := exec.Command("sleep", "30")
	if err := killVictim.Start(); err != nil {
		t.Fatalf("starting the SIGKILL victim: %v", err)
	}
	if err := killVictim.Process.Kill(); err != nil {
		t.Fatalf("delivering the independent SIGKILL: %v", err)
	}
	killWaitErr := killVictim.Wait()
	var killExitErr *exec.ExitError
	if !errors.As(killWaitErr, &killExitErr) {
		t.Fatalf("reaped wait error = %v, want the *exec.ExitError carrying the signal status", killWaitErr)
	}
	if pushProcessKilledBySigTerm(killVictim.ProcessState) {
		t.Error("an independent SIGKILL victim reads as SIGTERM; it must stay a distinguishable observed outcome, never a cancellation claim")
	}
	if !pushProcessKilledBySigKill(killVictim.ProcessState) {
		t.Error("an independent SIGKILL victim does not read as SIGKILL; the escalation/overlap filter premise cannot be demonstrated")
	}
}

func TestPhase13FrozenCommitTestsIntact(t *testing.T) {
	// Tripwire for the frozen-fixture criterion: the frozen
	// api/git/commit_test.go must still carry its phase-1.3 regression
	// coverage. This test only reads the frozen file; any repair to it
	// needs a separately authorized test-repair decision, and a quiet
	// edit that removes one of these tests fails here instead of being
	// treated as green by a sibling test.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not report this test file, so the frozen test file cannot be located")
	}
	body, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "commit_test.go"))
	if err != nil {
		t.Fatalf("reading the frozen commit_test.go: %v", err)
	}
	frozen := string(body)
	for _, want := range []string{
		"TestGitPushIndependentSignalFailureSurvivesLateCancellation",
		"TestGitPushCancellationRetainsReapedProcessError",
		"TestGitPushZeroExitBeforeLateCancellationRetainsSuccess",
		"TestGitPushNonZeroExitBeforeLateCancellationIsNotRelabelled",
		"TestGitPushSignalExitBeforeCompletionNotificationSurvivesLateCancellation",
		"TestGitPushPreCancelledContextIsReportedAsCancellation",
	} {
		if !strings.Contains(frozen, want) {
			t.Errorf("frozen api/git/commit_test.go no longer contains %s; a frozen fixture changed without a separately authorized test-repair decision", want)
		}
	}
}
