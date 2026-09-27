package git

// Independently authored additive regression coverage for phase 1.1.1 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md:
//
//	Preserve causal direct-push outcomes and owned-pipe cleanup.
//
// Every test exercises only the observable GitPush contract (Started,
// Cancelled, ExitCode, Success, Err, Stdout, Stderr, Argv,
// WorkingDirectory) plus directly observable descriptor accounting.
// Ordering uses explicit file and progress signals: a test cancels only
// after a synchronization file or consumed progress line proves the
// required state. No test depends on wall-clock sleeps for correctness,
// and every spawned descendant holder is released and reaped by
// teardown. Same-package fixtures (gitCommitUnderTestWithFakeGit,
// pushRouteUnderTest, argvCarriesOperationArgs,
// waitForRegressionPushLines, releaseRegressionDescendantAndRequireExit,
// countOpenFDsForCausality, gitPushWithCausalityTimeout) are reused from
// the retained files and are not redefined here; all other identifiers
// below carry a cau111 prefix so this file is self-contained.

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

// cau111ProbeHeader answers the generation-executor upstream probe and
// rev/count reads so the push route reaches process start without a
// remote.
const cau111ProbeHeader = `case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
`

func cau111WaitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for synchronization file %q", path)
}

func cau111RunPush(t *testing.T, gc *GitCommit, ctx context.Context, route GitPushRoute, what string) GitPushResult {
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

// TestCau111IndependentSignalDeathSurvivesLateCancel pins criterion 1
// with a SIGTERM independent death: the direct push terminates itself by
// signal while a descendant holds the inherited descriptors open, the
// single reaper observes the death (zombie marker, then PID disappearance
// proving Wait already returned while the drain is still held open), and
// only then does the context cancel. The outcome must stay a
// non-cancelled process failure with the real Wait error/status,
// retained streams and no context-cancellation contamination.
func TestCau111IndependentSignalDeathSurvivesLateCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the signal-specific push fixture requires POSIX shell signals")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+cau111ProbeHeader+`    push)
      echo "cau111-sigterm-stdout"
      echo "cau111-sigterm-stderr" >&2
      parent=$$
      ( while kill -0 "$parent" 2>/dev/null; do
          state=$(ps -o stat= -p "$parent" 2>/dev/null)
          case "$state" in *Z*) break ;; esac
          sleep 0.01
        done
        touch "$CAU111_PARENT_DEAD"
        while kill -0 "$parent" 2>/dev/null; do sleep 0.01; done
        touch "$CAU111_PARENT_REAPED"
        while [ ! -e "$CAU111_RELEASE" ]; do sleep 0.01; done
        touch "$CAU111_DONE"
      ) &
      kill -TERM "$parent"
      ;;
  esac
done
exit 0
`)
	parentDead := filepath.Join(root, "cau111-sigterm-dead")
	parentReaped := filepath.Join(root, "cau111-sigterm-reaped")
	release := filepath.Join(root, "cau111-sigterm-release")
	done := filepath.Join(root, "cau111-sigterm-done")
	t.Setenv("CAU111_PARENT_DEAD", parentDead)
	t.Setenv("CAU111_PARENT_REAPED", parentReaped)
	t.Setenv("CAU111_RELEASE", release)
	t.Setenv("CAU111_DONE", done)
	defer func() { _ = os.WriteFile(release, []byte{}, 0o600) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pushDone := make(chan GitPushResult, 1)
	go func() { pushDone <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	// The direct child died by its own signal before any cancellation:
	// zombie observed, then PID disappearance proves the reaper already
	// returned while the held-open drain still blocks EOF. Both readers
	// consumed the pre-exit bytes before the late cancel arrives.
	cau111WaitForFile(t, parentDead)
	cau111WaitForFile(t, parentReaped)
	waitForRegressionPushLines(t, gitCommit, "cau111-sigterm-stdout", "cau111-sigterm-stderr")
	cancel()

	select {
	case result := <-pushDone:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the signal-terminated process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("an independently signal-terminated push must not be reported as cancelled after a later context cancellation")
		}
		if result.Success() {
			t.Error("a signal-terminated push must not be reported as a success")
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the real direct-process *exec.ExitError", result.Err())
		} else {
			if result.ExitCode() != exitErr.ExitCode() {
				t.Errorf("ExitCode() = %d, want the reaped signal status %d", result.ExitCode(), exitErr.ExitCode())
			}
			if result.ExitCode() != -1 {
				t.Errorf("ExitCode() = %d, want -1 for the signal-terminated direct process", result.ExitCode())
			}
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not join the late context cancellation for an independent failure", result.Err())
		}
		if !strings.Contains(string(result.Stdout()), "cau111-sigterm-stdout") {
			t.Errorf("independent failure lost retained stdout: %q", result.Stdout())
		}
		if !strings.Contains(string(result.Stderr()), "cau111-sigterm-stderr") {
			t.Errorf("independent failure lost retained stderr: %q", result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the independently signal-terminated drain")
	}
}

// TestCau111NonzeroExitBeforeNotificationSurvivesLateCancel pins the
// second half of criterion 1 and the nonzero half of criterion 3: a
// direct push that exits nonzero while a descendant keeps the drain open
// (so the completion notification cannot be consumed through EOF) still
// reports its real exit status after a later cancellation, and the
// nonzero exit is never relabelled cancelled.
func TestCau111NonzeroExitBeforeNotificationSurvivesLateCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+cau111ProbeHeader+`    push)
      echo "cau111-nonzero-stdout"
      echo "cau111-nonzero-stderr" >&2
      parent=$$
      ( touch "$CAU111_HOLDER_STARTED"
        while kill -0 "$parent" 2>/dev/null; do sleep 0.01; done
        touch "$CAU111_PARENT_REAPED"
        while [ ! -e "$CAU111_RELEASE" ]; do sleep 0.01; done
        touch "$CAU111_DONE"
      ) &
      exit 3
      ;;
  esac
done
exit 0
`)
	holderStarted := filepath.Join(root, "cau111-nonzero-holder")
	parentReaped := filepath.Join(root, "cau111-nonzero-reaped")
	release := filepath.Join(root, "cau111-nonzero-release")
	done := filepath.Join(root, "cau111-nonzero-done")
	t.Setenv("CAU111_HOLDER_STARTED", holderStarted)
	t.Setenv("CAU111_PARENT_REAPED", parentReaped)
	t.Setenv("CAU111_RELEASE", release)
	t.Setenv("CAU111_DONE", done)
	defer func() { _ = os.WriteFile(release, []byte{}, 0o600) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pushDone := make(chan GitPushResult, 1)
	go func() { pushDone <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	// The holder exists (so EOF cannot arrive on its own), both readers
	// consumed the pre-exit bytes, and the reaper collected the exit
	// before the cancellation lands.
	cau111WaitForFile(t, holderStarted)
	waitForRegressionPushLines(t, gitCommit, "cau111-nonzero-stdout", "cau111-nonzero-stderr")
	cau111WaitForFile(t, parentReaped)
	cancel()

	select {
	case result := <-pushDone:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the nonzero-exit process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("an independently failed push must not be relabelled cancelled by a later context cancellation")
		}
		if result.ExitCode() != 3 {
			t.Errorf("ExitCode() = %d, want the real nonzero status 3", result.ExitCode())
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the real direct-process *exec.ExitError", result.Err())
		} else if result.ExitCode() != exitErr.ExitCode() {
			t.Errorf("ExitCode() = %d, want the reaped status %d", result.ExitCode(), exitErr.ExitCode())
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not join the late context cancellation for an independent failure", result.Err())
		}
		if result.Success() {
			t.Error("a nonzero-exit push must not be reported as a success")
		}
		if !strings.Contains(string(result.Stdout()), "cau111-nonzero-stdout") {
			t.Errorf("independent failure lost retained stdout: %q", result.Stdout())
		}
		if !strings.Contains(string(result.Stderr()), "cau111-nonzero-stderr") {
			t.Errorf("independent failure lost retained stderr: %q", result.Stderr())
		}
		releaseRegressionDescendantAndRequireExit(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the independently failed drain")
	}
}

// TestCau111LiveCancellationTerminatesWithCauseAndOutput pins criterion
// 2 and the R3 no-hang requirement: cancellation of a provably
// still-running direct push reports cancelled while retaining the reaped
// process status/error, the context cause, and the output captured
// before termination. The bounded return also proves the cancellation
// neither hangs nor skips termination of the still-live child.
func TestCau111LiveCancellationTerminatesWithCauseAndOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+cau111ProbeHeader+`    push)
      echo "cau111-live-stdout"
      echo "cau111-live-stderr" >&2
      touch "$CAU111_READY"
      ( while [ ! -e "$CAU111_RELEASE" ]; do sleep 0.01; done
        touch "$CAU111_DONE"
      ) &
      exec sleep 30
      ;;
  esac
done
exit 0
`)
	ready := filepath.Join(root, "cau111-live-ready")
	release := filepath.Join(root, "cau111-live-release")
	done := filepath.Join(root, "cau111-live-done")
	t.Setenv("CAU111_READY", ready)
	t.Setenv("CAU111_RELEASE", release)
	t.Setenv("CAU111_DONE", done)
	defer func() { _ = os.WriteFile(release, []byte{}, 0o600) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pushDone := make(chan GitPushResult, 1)
	go func() { pushDone <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	// The direct child is provably still running: it touched readiness
	// and both readers consumed its pre-cancel bytes.
	cau111WaitForFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "cau111-live-stdout", "cau111-live-stderr")
	cancel()

	select {
	case result := <-pushDone:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the live process had started before the cancellation")
		}
		if !result.Cancelled() {
			t.Error("cancellation that terminates a still-running direct push must report cancelled")
		}
		if result.Success() {
			t.Error("a cancelled push must not be reported as a success")
		}
		if !errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, want the joined context-cancellation cause", result.Err())
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the reaped process *exec.ExitError alongside the context cause", result.Err())
		} else if result.ExitCode() != exitErr.ExitCode() {
			t.Errorf("ExitCode() = %d, want the reaped process status %d", result.ExitCode(), exitErr.ExitCode())
		}
		if !strings.Contains(string(result.Stdout()), "cau111-live-stdout") {
			t.Errorf("cancelled push lost captured stdout: %q", result.Stdout())
		}
		if !strings.Contains(string(result.Stderr()), "cau111-live-stderr") {
			t.Errorf("cancelled push lost captured stderr: %q", result.Stderr())
		}
		releaseRegressionDescendantAndRequireExit(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after cancellation of the still-running direct push")
	}
}

// TestCau111ZeroExitBeforeLateCancelKeepsSuccess pins the first half of
// criterion 3: a direct process that completed successfully before a late
// drain cancellation is not falsely marked killed. Success stays true
// with exit 0 even if Err carries a stream-capture failure from the
// cancellation-time pipe close.
func TestCau111ZeroExitBeforeLateCancelKeepsSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+cau111ProbeHeader+`    push)
      echo "cau111-zero-stdout"
      echo "cau111-zero-stderr" >&2
      parent=$$
      ( touch "$CAU111_HOLDER_STARTED"
        while kill -0 "$parent" 2>/dev/null; do sleep 0.01; done
        touch "$CAU111_PARENT_REAPED"
        while [ ! -e "$CAU111_RELEASE" ]; do sleep 0.01; done
        touch "$CAU111_DONE"
      ) &
      exit 0
      ;;
  esac
done
exit 0
`)
	holderStarted := filepath.Join(root, "cau111-zero-holder")
	parentReaped := filepath.Join(root, "cau111-zero-reaped")
	release := filepath.Join(root, "cau111-zero-release")
	done := filepath.Join(root, "cau111-zero-done")
	t.Setenv("CAU111_HOLDER_STARTED", holderStarted)
	t.Setenv("CAU111_PARENT_REAPED", parentReaped)
	t.Setenv("CAU111_RELEASE", release)
	t.Setenv("CAU111_DONE", done)
	defer func() { _ = os.WriteFile(release, []byte{}, 0o600) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pushDone := make(chan GitPushResult, 1)
	go func() { pushDone <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	// The zero-exit child is reaped (PID gone) while the descendant keeps
	// the drain open; both readers consumed the success bytes before the
	// late cancellation arrives.
	cau111WaitForFile(t, holderStarted)
	waitForRegressionPushLines(t, gitCommit, "cau111-zero-stdout", "cau111-zero-stderr")
	cau111WaitForFile(t, parentReaped)
	cancel()

	select {
	case result := <-pushDone:
		if !result.Started() {
			t.Error("the successful process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("a push that completed successfully before late drain cancellation must not be marked cancelled")
		}
		if result.ExitCode() != 0 {
			t.Errorf("ExitCode() = %d, want the real zero status 0", result.ExitCode())
		}
		if !result.Success() {
			t.Errorf("Success() = false with exit %d cancelled %v err %v, want the success contract to survive late drain cancellation", result.ExitCode(), result.Cancelled(), result.Err())
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not carry the late context cancellation on the success path", result.Err())
		}
		if !strings.Contains(string(result.Stdout()), "cau111-zero-stdout") {
			t.Errorf("successful push lost retained stdout: %q", result.Stdout())
		}
		if !strings.Contains(string(result.Stderr()), "cau111-zero-stderr") {
			t.Errorf("successful push lost retained stderr: %q", result.Stderr())
		}
		releaseRegressionDescendantAndRequireExit(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the successfully completed drain")
	}
}

// TestCau111PreStartCancellationStartsNothing pins the pre-start half of
// criterion 3 and the setup half of R5: a context already cancelled
// before Start launches no push and reports an unstarted cancellation
// with exit -1 and no setup descriptor leak.
func TestCau111PreStartCancellationStartsNothing(t *testing.T) {
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+cau111ProbeHeader+`    push)
      touch "$CAU111_PUSH_RAN"
      echo "must-not-run"
      exit 0
      ;;
  esac
done
exit 0
`)
	pushRan := filepath.Join(root, "cau111-prestart-push-ran")
	t.Setenv("CAU111_PUSH_RAN", pushRan)

	// The cancellation precedes the call, so no timing is involved: the
	// attempt stays unstarted and cancelled with no exit status.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	route := pushRouteUnderTest("master")
	route.ActiveGuard = func() error { return nil }
	before := countOpenFDsForCausality(t)
	result := gitPushWithCausalityTimeout(t, gitCommit, ctx, route, 10*time.Second, "after the pre-Start cancellation")

	if result.Started() {
		t.Error("a pre-Start cancellation must not start the prepared process")
	}
	if !result.Cancelled() {
		t.Error("a pre-Start cancellation must report cancelled with no exit status rather than a setup outcome")
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
	if delta := countOpenFDsForCausality(t) - before; delta > 2 {
		t.Errorf("open file descriptors grew by %d across the pre-Start refusal, want all setup pipe ends closed", delta)
	}
}

// TestCau111GuardTimeCancellationStartsNothing pins R5: a context
// cancelled while ActiveGuard holds the pre-Start gate produces an
// unstarted cancelled result with exit -1, closes all setup pipe ends
// and launches no push.
func TestCau111GuardTimeCancellationStartsNothing(t *testing.T) {
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+cau111ProbeHeader+`    push)
      touch "$CAU111_PUSH_RAN"
      echo "must-not-run"
      exit 0
      ;;
  esac
done
exit 0
`)
	pushRan := filepath.Join(root, "cau111-guardtime-push-ran")
	t.Setenv("CAU111_PUSH_RAN", pushRan)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := filepath.Join(root, "cau111-guard-entered")
	route := pushRouteUnderTest("master")
	route.ActiveGuard = func() error {
		_ = os.WriteFile(entered, []byte{}, 0o600)
		<-ctx.Done()
		return ctx.Err()
	}
	before := countOpenFDsForCausality(t)
	pushDone := make(chan GitPushResult, 1)
	go func() { pushDone <- gitCommit.GitPush(ctx, route) }()

	// The guard holds the pre-Start gate; the cancellation lands while
	// the guard is still holding, so the attempt must stay unstarted.
	cau111WaitForFile(t, entered)
	cancel()
	select {
	case result := <-pushDone:
		if result.Started() {
			t.Error("a guard-time cancellation must not start the prepared process")
		}
		if !result.Cancelled() {
			t.Error("a guard-time cancellation must report cancelled with no exit status rather than a setup failure")
		}
		if result.ExitCode() != -1 {
			t.Errorf("ExitCode() = %d, want -1 for the absent process status", result.ExitCode())
		}
		if !errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, want the guard-time context cancellation", result.Err())
		}
		if result.Success() {
			t.Error("a guard-time cancellation must not be reported as a success")
		}
		if _, err := os.Stat(pushRan); err == nil {
			t.Error("the push process launched although the guard held the pre-Start gate during cancellation")
		} else if !os.IsNotExist(err) {
			t.Errorf("checking the push launch marker: %v", err)
		}
		if delta := countOpenFDsForCausality(t) - before; delta > 2 {
			t.Errorf("open file descriptors grew by %d across the guard-time refusal, want all setup pipe ends closed", delta)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after the guard-time cancellation")
	}
}

// TestCau111ConcurrentDrainImmutabilityAndArgv pins criterion 4: both
// streams drain concurrently without deadlock or loss, the result
// accessors return defensive copies, and the existing push argv behavior
// is intact.
func TestCau111ConcurrentDrainImmutabilityAndArgv(t *testing.T) {
	gitCommit, _, _, _, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+cau111ProbeHeader+`    push)
      i=1
      while [ "$i" -le 200 ]; do
        echo "cau111-dual-stdout-$i"
        echo "cau111-dual-stderr-$i" >&2
        i=$((i + 1))
      done
      exit 0
      ;;
  esac
done
exit 0
`)
	result := cau111RunPush(t, gitCommit, context.Background(), pushRouteUnderTest("master"), "after the dual-stream push")

	if !result.Success() {
		t.Fatalf("dual-stream push did not succeed: exit %d, cancelled %v, err %v", result.ExitCode(), result.Cancelled(), result.Err())
	}
	for _, marker := range []string{"cau111-dual-stdout-1", "cau111-dual-stdout-200"} {
		if !strings.Contains(string(result.Stdout()), marker) {
			t.Errorf("concurrent drain lost stdout marker %q: %q", marker, result.Stdout())
		}
	}
	for _, marker := range []string{"cau111-dual-stderr-1", "cau111-dual-stderr-200"} {
		if !strings.Contains(string(result.Stderr()), marker) {
			t.Errorf("concurrent drain lost stderr marker %q: %q", marker, result.Stderr())
		}
	}
	if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
		t.Errorf("argv = %v, want the tracked-branch operation arguments", result.Argv())
	}
	if result.WorkingDirectory() == "" {
		t.Error("WorkingDirectory() must record the directory the push ran in")
	}

	// Accessors return defensive copies: mutating a returned slice must
	// not change the stored result.
	argv := result.Argv()
	if len(argv) == 0 {
		t.Fatal("argv must not be empty for a started push")
	}
	argv[0] = "mutated"
	if result.Argv()[0] == "mutated" {
		t.Error("Argv() shares storage with the stored result instead of returning a copy")
	}
	stdout := result.Stdout()
	if len(stdout) == 0 {
		t.Fatal("retained stdout must not be empty for the dual-stream push")
	}
	stdout[0] ^= 0xff
	if result.Stdout()[0] == stdout[0] {
		t.Error("Stdout() shares storage with the stored result instead of returning a copy")
	}
	stderr := result.Stderr()
	if len(stderr) == 0 {
		t.Fatal("retained stderr must not be empty for the dual-stream push")
	}
	stderr[0] ^= 0xff
	if result.Stderr()[0] == stderr[0] {
		t.Error("Stderr() shares storage with the stored result instead of returning a copy")
	}
	stored := gitCommit.GitPushResult()
	if stored.ExitCode() != result.ExitCode() || stored.Cancelled() != result.Cancelled() || stored.Started() != result.Started() {
		t.Error("the published result diverged from the returned result")
	}
}

// TestCau111OrdinaryCompletionClosesOwnedDescriptors pins the first half
// of R4: ordinary successful and failed push completion closes both
// owned pipe read ends without relying on garbage collection, so
// repeated runs leave descriptor usage stable.
func TestCau111OrdinaryCompletionClosesOwnedDescriptors(t *testing.T) {
	gitCommit, _, _, _, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+cau111ProbeHeader+`    push)
      echo "cau111-fd-stdout"
      echo "cau111-fd-stderr" >&2
      if [ "$CAU111_MODE" = "fail" ]; then exit 1; fi
      exit 0
      ;;
  esac
done
exit 0
`)
	before := countOpenFDsForCausality(t)
	t.Setenv("CAU111_MODE", "ok")
	for i := 0; i < 2; i++ {
		result := cau111RunPush(t, gitCommit, context.Background(), pushRouteUnderTest("master"), "after the ordinary successful push")
		if !result.Success() {
			t.Fatalf("ordinary push %d did not succeed: exit %d, err %v", i, result.ExitCode(), result.Err())
		}
	}
	t.Setenv("CAU111_MODE", "fail")
	for i := 0; i < 2; i++ {
		result := cau111RunPush(t, gitCommit, context.Background(), pushRouteUnderTest("master"), "after the ordinary failed push")
		if result.Cancelled() {
			t.Errorf("ordinary failed push %d must not report cancelled", i)
		}
		if result.ExitCode() != 1 {
			t.Errorf("ordinary failed push %d ExitCode() = %d, want 1", i, result.ExitCode())
		}
		if result.Success() {
			t.Errorf("ordinary failed push %d must not report success", i)
		}
		if !strings.Contains(string(result.Stderr()), "cau111-fd-stderr") {
			t.Errorf("ordinary failed push %d lost retained stderr: %q", i, result.Stderr())
		}
	}
	if delta := countOpenFDsForCausality(t) - before; delta > 2 {
		t.Errorf("open file descriptors grew by %d across four ordinary completions, want both owned read ends closed on every return", delta)
	}
}

// TestCau111CancelBoundsInheritedDrainRetainsPreCloseBytes pins the
// second half of R4: cancellation bounds the inherited-descriptor drain
// so the push returns promptly, while bytes the direct child queued
// before closure are still retained.
func TestCau111CancelBoundsInheritedDrainRetainsPreCloseBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+cau111ProbeHeader+`    push)
      i=1
      while [ "$i" -le 200 ]; do
        echo "cau111-vol-stdout-$i"
        echo "cau111-vol-stderr-$i" >&2
        i=$((i + 1))
      done
      touch "$CAU111_VOLUME_DONE"
      ( touch "$CAU111_HOLDER_STARTED"
        while [ ! -e "$CAU111_RELEASE" ]; do sleep 0.01; done
        touch "$CAU111_DONE"
      ) &
      exec sleep 30
      ;;
  esac
done
exit 0
`)
	volumeDone := filepath.Join(root, "cau111-vol-complete")
	holderStarted := filepath.Join(root, "cau111-vol-holder")
	release := filepath.Join(root, "cau111-vol-release")
	done := filepath.Join(root, "cau111-vol-done")
	t.Setenv("CAU111_VOLUME_DONE", volumeDone)
	t.Setenv("CAU111_HOLDER_STARTED", holderStarted)
	t.Setenv("CAU111_RELEASE", release)
	t.Setenv("CAU111_DONE", done)
	defer func() { _ = os.WriteFile(release, []byte{}, 0o600) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pushDone := make(chan GitPushResult, 1)
	go func() { pushDone <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	// The full volume is queued and a holder keeps the drain open, so the
	// return below can only come from the cancellation bound; the first
	// progress lines prove bytes were already readable before closure.
	cau111WaitForFile(t, volumeDone)
	cau111WaitForFile(t, holderStarted)
	waitForRegressionPushLines(t, gitCommit, "cau111-vol-stdout-1", "cau111-vol-stderr-1")
	cancel()

	select {
	case result := <-pushDone:
		if !result.Started() {
			t.Error("the volume push had started before the cancellation")
		}
		if !result.Cancelled() {
			t.Error("cancellation of the volume push held open by an inherited descriptor must report cancelled")
		}
		if !strings.Contains(string(result.Stdout()), "cau111-vol-stdout-1") {
			t.Errorf("bounded drain lost pre-close stdout bytes: %q", result.Stdout())
		}
		if !strings.Contains(string(result.Stderr()), "cau111-vol-stderr-1") {
			t.Errorf("bounded drain lost pre-close stderr bytes: %q", result.Stderr())
		}
		if len(result.Stdout()) == 0 || len(result.Stderr()) == 0 {
			t.Error("the bounded drain must retain bytes read before closure on both streams")
		}
		releaseRegressionDescendantAndRequireExit(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after cancellation of the inherited-descriptor drain")
	}
}
