package git

// Additive regression coverage for phase 1.3 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md:
//
//	Preserve causal direct-push outcomes and owned-pipe cleanup.
//
// The retained files already pin self-inflicted independent SIGTERM and
// SIGKILL deaths with reaping delayed from the start, the final
// observation-to-Kill boundary overlap, nonzero/stderr and signalled ps
// diagnostics, ordinary descriptor cleanup and guard/setup refusals. The
// tests below add the ordering the phase-1.3 step 1 still names: an
// explicitly test-ordered same-signal versus live-cancel oracle with a
// defensible seam. The independent death here is delivered by the test
// itself (an external SIGKILL, fully ordered after an acknowledged live
// verdict and fully reaped before any cancellation), while the control
// cancels the same fixture while provably live. A status-filter unit
// test documents the SIGTERM-agreement boundary without claiming
// causation from status alone, and a cross-file agreement test verifies
// the phase document, commit.go and the Unix probe share the
// conservative simultaneous-outcome requirement. Same-package helpers
// (gitCommitUnderTestWithFakeGit, pushRouteUnderTest,
// argvCarriesOperationArgs, waitForTestFile,
// waitForRegressionPushLines, releaseRegressionDescendantAndRequireExit)
// are reused from the retained files and are not redefined here.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// samesigPushScript answers the generation-executor upstream probe and
// runs a scripted push whose direct child publishes its PID, signals
// readiness, then idles (holding no drain open itself) while a
// background monitor watches the direct child's kernel state and keeps
// the inherited descriptors open until released.
const samesigPushScript = `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
    push)
      echo "samesig-stdout"
      echo "samesig-stderr" >&2
      echo $$ > "$SAMESIG_CHILD_PID"
      touch "$SAMESIG_READY"
      parent=$$
      ( while kill -0 "$parent" 2>/dev/null; do
          state=$(ps -o stat= -p "$parent" 2>/dev/null)
          case "$state" in *Z*) break ;; esac
          sleep 0.01
        done
        touch "$SAMESIG_PARENT_DEAD"
        while kill -0 "$parent" 2>/dev/null; do sleep 0.01; done
        touch "$SAMESIG_PARENT_REAPED"
        while [ ! -e "$SAMESIG_RELEASE" ]; do sleep 0.01; done
        touch "$SAMESIG_DONE"
      ) &
      while [ ! -e "$SAMESIG_RELEASE" ]; do sleep 0.01; done
      ;;
  esac
done
exit 0
`

// samesigNoStartScript answers the upstream probe but records any
// push launch through a marker file, so pre-Start refusals prove no
// process started.
const samesigNoStartScript = `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
    push)
      touch "$SAMESIG_PUSH_RAN"
      echo "must-not-run"
      exit 0
      ;;
  esac
done
exit 0
`

// samesigDualStreamScript emits a 200-line volume on both streams and
// exits zero, so concurrent draining, stream retention and accessor
// behavior are observed without any cancellation.
const samesigDualStreamScript = `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
    push)
      i=1
      while [ "$i" -le 200 ]; do
        echo "samesig-dual-stdout-$i"
        echo "samesig-dual-stderr-$i" >&2
        i=$((i + 1))
      done
      exit 0
      ;;
  esac
done
exit 0
`

// samesigFDScript emits both streams and exits zero or nonzero
// depending on SAMESIG_MODE, so ordinary-completion descriptor
// cleanup is observed on both paths.
const samesigFDScript = `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
    push)
      echo "samesig-fd-stdout"
      echo "samesig-fd-stderr" >&2
      if [ "$SAMESIG_MODE" = "fail" ]; then exit 3; fi
      exit 0
      ;;
  esac
done
exit 0
`

// samesigReadChildPid reads the direct-child PID the fixture published,
// so the test-side signal targets exactly the reaped process.
func samesigReadChildPid(t *testing.T, path string) int {
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

// samesigRequireLiveVerdict asserts the positive-liveness premise the
// oracle depends on: the child is neither already exited nor settled
// dead at the moment the ordered sequence starts.
func samesigRequireLiveVerdict(t *testing.T, pid int, what string) {
	t.Helper()
	if pushDirectProcessAlreadyExited(pid) {
		t.Fatalf("direct child PID %d already reads as exited before %s; the live-verdict premise cannot be established", pid, what)
	}
	if pushDirectProcessSettledDead(pid) {
		t.Fatalf("direct child PID %d already reads as settled dead before %s; the live-verdict premise cannot be established", pid, what)
	}
}

func TestPushSameSignalOrderedSigKillSurvivesLateCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell signals")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, samesigPushScript)
	ready := filepath.Join(root, "samesig-oracle-ready")
	childPidFile := filepath.Join(root, "samesig-oracle-child-pid")
	parentDead := filepath.Join(root, "samesig-oracle-parent-dead")
	parentReaped := filepath.Join(root, "samesig-oracle-parent-reaped")
	release := filepath.Join(root, "samesig-oracle-release")
	done := filepath.Join(root, "samesig-oracle-done")
	t.Setenv("SAMESIG_READY", ready)
	t.Setenv("SAMESIG_CHILD_PID", childPidFile)
	t.Setenv("SAMESIG_PARENT_DEAD", parentDead)
	t.Setenv("SAMESIG_PARENT_REAPED", parentReaped)
	t.Setenv("SAMESIG_RELEASE", release)
	t.Setenv("SAMESIG_DONE", done)
	defer func() {
		_ = os.WriteFile(release, []byte{}, 0o600)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(done); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pushDone := make(chan GitPushResult, 1)
	go func() { pushDone <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	// Ordering, each step an explicit signal: the direct child is
	// provably live (readiness plus both pre-exit lines consumed through
	// the live progress buffers plus a positive kernel-liveness
	// verdict), then the test delivers the independent SIGKILL itself,
	// then the monitor observes the zombie state and PID disappearance
	// proves the single reaper already returned while the descendant
	// still holds the inherited write ends so the drain cannot reach
	// EOF. Only then does the late cancellation arrive.
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "samesig-stdout", "samesig-stderr")
	waitForTestFile(t, childPidFile)
	childPid := samesigReadChildPid(t, childPidFile)
	samesigRequireLiveVerdict(t, childPid, "the test-ordered independent SIGKILL")
	proc, findErr := os.FindProcess(childPid)
	if findErr != nil {
		t.Fatalf("finding the direct child PID %d for the independent SIGKILL: %v", childPid, findErr)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("delivering the test-ordered independent SIGKILL to PID %d: %v", childPid, err)
	}
	waitForTestFile(t, parentDead)
	waitForTestFile(t, parentReaped)
	if _, err := os.Stat(done); err == nil {
		t.Fatal("descendant exited before the classification, so the direct exit no longer precedes completion notification")
	} else if !os.IsNotExist(err) {
		t.Fatalf("checking the descendant hold-open marker: %v", err)
	}
	cancel()

	select {
	case result := <-pushDone:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the signal-terminated process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("the test-ordered independent SIGKILL was relabelled cancelled by the later context cancellation")
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the real direct-process *exec.ExitError from the independent SIGKILL", result.Err())
		} else {
			if result.ExitCode() != exitErr.ExitCode() {
				t.Errorf("ExitCode() = %d, want the reaped signal status %d", result.ExitCode(), exitErr.ExitCode())
			}
			if result.ExitCode() == 0 {
				t.Error("ExitCode() = 0, want the nonzero signal status, not a clean exit")
			}
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not join the late context cancellation for the independently killed process", result.Err())
		}
		if result.Success() {
			t.Error("a SIGKILL-terminated push must not be reported as a success")
		}
		if !strings.Contains(string(result.Stdout()), "samesig-stdout") || !strings.Contains(string(result.Stderr()), "samesig-stderr") {
			t.Errorf("independently killed push lost retained streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the test-ordered SIGKILL drain")
	}
}

func TestPushSameSignalLiveCancelControlStaysCancelled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell signals")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, samesigPushScript)
	ready := filepath.Join(root, "samesig-control-ready")
	childPidFile := filepath.Join(root, "samesig-control-child-pid")
	release := filepath.Join(root, "samesig-control-release")
	done := filepath.Join(root, "samesig-control-done")
	t.Setenv("SAMESIG_READY", ready)
	t.Setenv("SAMESIG_CHILD_PID", childPidFile)
	t.Setenv("SAMESIG_PARENT_DEAD", filepath.Join(root, "samesig-control-parent-dead"))
	t.Setenv("SAMESIG_PARENT_REAPED", filepath.Join(root, "samesig-control-parent-reaped"))
	t.Setenv("SAMESIG_RELEASE", release)
	t.Setenv("SAMESIG_DONE", done)
	defer func() {
		_ = os.WriteFile(release, []byte{}, 0o600)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(done); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pushDone := make(chan GitPushResult, 1)
	go func() { pushDone <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	// Control ordering on the identical fixture: the direct child is
	// provably still running (readiness, published PID with a positive
	// liveness verdict, and both pre-cancel lines consumed) while a
	// descendant already holds the inherited write ends. The
	// cancellation therefore demonstrably terminates a live direct
	// push. The bounded return also proves the cancellation neither
	// hangs nor skips termination of the still-live child.
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "samesig-stdout", "samesig-stderr")
	waitForTestFile(t, childPidFile)
	samesigRequireLiveVerdict(t, samesigReadChildPid(t, childPidFile), "the live-cancel control")
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
		if !strings.Contains(string(result.Stdout()), "samesig-stdout") || !strings.Contains(string(result.Stderr()), "samesig-stderr") {
			t.Errorf("cancelled push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("cancelled push argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		releaseRegressionDescendantAndRequireExit(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after cancellation of the still-running direct push")
	}
}

func TestPushProcessKilledBySigTermFilterSemantics(t *testing.T) {
	if pushProcessKilledBySigTerm(nil) {
		t.Error("pushProcessKilledBySigTerm(nil) = true, want false so an unreaped status never keeps a cancellation claim")
	}
	if runtime.GOOS == "windows" {
		t.Skip("signal-identity filtering is undeterminable on Windows by design")
	}
	// A numeric exit disagrees with the handshake's SIGTERM signal: it
	// can never be relabelled cancelled, no matter when the context
	// expires.
	exited := exec.Command("sh", "-c", "exit 3")
	if err := exited.Run(); err == nil {
		t.Fatal("the exit-3 probe did not fail as expected")
	} else if pushProcessKilledBySigTerm(exited.ProcessState) {
		t.Error("a numeric exit reads as SIGTERM, which would let an independently failed child be relabelled cancelled")
	}
	// An independent SIGKILL disagrees the same way: only the
	// distinguishable SIGTERM status agrees with the cancellation
	// signal. This documents the status agreement boundary; agreement
	// alone is not causal proof, which is why the ordered oracle above
	// pairs it with probe evidence and a live-cancel control.
	sigkilled := exec.Command("sleep", "30")
	if err := sigkilled.Start(); err != nil {
		t.Fatalf("starting the SIGKILL filter process: %v", err)
	}
	if err := sigkilled.Process.Kill(); err != nil {
		_ = sigkilled.Wait()
		t.Fatalf("SIGKILLing the filter process: %v", err)
	}
	_ = sigkilled.Wait()
	if pushProcessKilledBySigTerm(sigkilled.ProcessState) {
		t.Error("a SIGKILL status reads as SIGTERM, which would let an independent SIGKILL be relabelled cancelled")
	}
	// The one stated overlap: a SIGTERM status agrees with the
	// handshake's own signal, so it preserves the existing claim for
	// the live-cancel control to exercise.
	sigterm := exec.Command("sh", "-c", "kill -TERM $$")
	if err := sigterm.Run(); err == nil {
		t.Fatal("the SIGTERM probe did not fail as expected")
	} else if !pushProcessKilledBySigTerm(sigterm.ProcessState) {
		t.Error("a reaped SIGTERM status does not read as SIGTERM, which would suppress a genuine live-kill cancellation")
	}
}

func TestPushSameSignalConservativeOutcomeAgreement(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller did not report this test file, so the cross-file agreement cannot be located")
	}
	pkgDir := filepath.Dir(thisFile)
	read := func(path string) string {
		t.Helper()
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s for the conservative-outcome agreement: %v", path, err)
		}
		return string(body)
	}
	commitGo := read(filepath.Join(pkgDir, "commit.go"))
	livenessGo := read(filepath.Join(pkgDir, "push_liveness_unix.go"))
	phaseDoc := read(filepath.Join(pkgDir, "..", "..", "docs", "devel", "13-harden-push-cancellation-and-partial-upstream-state.md"))

	// The handshake observes the process outcome without claiming
	// cancellation when Wait completion and the context notification are
	// simultaneously ready: a known completed Wait wins over a later
	// cancellation.
	if !strings.Contains(commitGo, "observes the process outcome without claiming cancellation") {
		t.Error("commit.go no longer carries the completed-Wait-before-cancel rule; the phase document and the Unix probe cannot agree with a rule the handshake dropped")
	}
	// The amended same-signal policy stays explicit in both owners: an
	// active cancellation with successful SIGTERM delivery, no known
	// earlier completion and matching reaped SIGTERM keeps the
	// cancellation claim despite an indistinguishable independent
	// same-SIGTERM race, without claiming the matching status proves
	// signal provenance.
	if !strings.Contains(commitGo, "keeps the cancellation claim") {
		t.Error("commit.go no longer states the amended active-cancellation same-SIGTERM policy; a matching SIGTERM after a delivered cancellation signal must keep the cancellation claim")
	}
	if !strings.Contains(commitGo, "matching status is not causal proof") {
		t.Error("commit.go no longer rejects matching status as causal proof; the amended policy classifies the overlap as cancelled without claiming signal provenance")
	}
	if !strings.Contains(commitGo, "No independent SIGKILL, numeric exit or other signal") {
		t.Error("commit.go no longer restricts the amended cancellation claim to matching SIGTERM; nonmatching exits must stay observed process failures")
	}
	if !strings.Contains(livenessGo, "Only that agreement keeps a cancellation claim") {
		t.Error("push_liveness_unix.go no longer states the amended matching-SIGTERM agreement rule; the probe owner contradicts the handshake")
	}
	if !strings.Contains(livenessGo, "suppresses the cancellation claim") {
		t.Error("push_liveness_unix.go no longer restricts Kill suppression to positively observed dead/zombie evidence")
	}
	// The phase document states the same amended classification: an
	// active cancellation with delivered SIGTERM and matching reaped
	// SIGTERM is cancelled despite the indistinguishable race, with no
	// request SIGTERM stays a process failure, and no final-gap
	// same-signal oracle or provenance claim is required.
	if !strings.Contains(phaseDoc, "report cancelled even if an independent simultaneous SIGTERM is indistinguishable") {
		t.Error("the phase document no longer requires the amended active-cancellation SIGTERM classification; the executable owners cannot agree with a requirement the document dropped")
	}
	if !strings.Contains(phaseDoc, "No cancellation request means SIGTERM is a process failure") {
		t.Error("the phase document no longer states the no-request SIGTERM failure rule; without a cancellation request SIGTERM must stay a process failure")
	}
	if !strings.Contains(phaseDoc, "Do not require a final-gap same-signal differentiation oracle") {
		t.Error("the phase document no longer rejects a final-gap same-signal differentiation oracle; matching status must not be claimed as signal provenance")
	}
}

// sprintfSamesigExitScript answers the generation-executor upstream
// probe and runs a scripted push that emits both streams, spawns a
// holder descendant that observes the direct child's death while
// keeping the inherited descriptors open, then exits with the given
// status.
func sprintfSamesigExitScript(marker string, exitStatus int) string {
	return fmt.Sprintf(`#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
    push)
      echo "%s-stdout"
      echo "%s-stderr" >&2
      parent=$$
      ( touch "$SAMESIG_HOLDER_STARTED"
        while kill -0 "$parent" 2>/dev/null; do sleep 0.01; done
        touch "$SAMESIG_PARENT_REAPED"
        while [ ! -e "$SAMESIG_RELEASE" ]; do sleep 0.01; done
        touch "$SAMESIG_DONE"
      ) &
      exit %d
      ;;
  esac
done
exit 0
`, marker, marker, exitStatus)
}

func TestPushSameSignalZeroExitBeforeLateCancelKeepsSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, sprintfSamesigExitScript("samesig-zero", 0))
	holderStarted := filepath.Join(root, "samesig-zero-holder")
	parentReaped := filepath.Join(root, "samesig-zero-reaped")
	release := filepath.Join(root, "samesig-zero-release")
	done := filepath.Join(root, "samesig-zero-done")
	t.Setenv("SAMESIG_HOLDER_STARTED", holderStarted)
	t.Setenv("SAMESIG_PARENT_REAPED", parentReaped)
	t.Setenv("SAMESIG_RELEASE", release)
	t.Setenv("SAMESIG_DONE", done)
	defer func() { _ = os.WriteFile(release, []byte{}, 0o600) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pushDone := make(chan GitPushResult, 1)
	go func() { pushDone <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	// The zero-exit child is reaped (PID gone) while the descendant
	// keeps the drain open; both readers consumed the success bytes
	// before the late cancellation arrives.
	waitForTestFile(t, holderStarted)
	waitForRegressionPushLines(t, gitCommit, "samesig-zero-stdout", "samesig-zero-stderr")
	waitForTestFile(t, parentReaped)
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
		if !strings.Contains(string(result.Stdout()), "samesig-zero-stdout") || !strings.Contains(string(result.Stderr()), "samesig-zero-stderr") {
			t.Errorf("successful push lost retained streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		releaseRegressionDescendantAndRequireExit(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the successfully completed drain")
	}
}

func TestPushSameSignalNonzeroExitBeforeLateCancelStaysFailed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, sprintfSamesigExitScript("samesig-nonzero", 3))
	holderStarted := filepath.Join(root, "samesig-nonzero-holder")
	parentReaped := filepath.Join(root, "samesig-nonzero-reaped")
	release := filepath.Join(root, "samesig-nonzero-release")
	done := filepath.Join(root, "samesig-nonzero-done")
	t.Setenv("SAMESIG_HOLDER_STARTED", holderStarted)
	t.Setenv("SAMESIG_PARENT_REAPED", parentReaped)
	t.Setenv("SAMESIG_RELEASE", release)
	t.Setenv("SAMESIG_DONE", done)
	defer func() { _ = os.WriteFile(release, []byte{}, 0o600) }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pushDone := make(chan GitPushResult, 1)
	go func() { pushDone <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	// The nonzero-exit child is reaped while the descendant keeps the
	// drain open, so the completion cannot be consumed through EOF;
	// the later cancellation must not relabel the real exit status.
	waitForTestFile(t, holderStarted)
	waitForRegressionPushLines(t, gitCommit, "samesig-nonzero-stdout", "samesig-nonzero-stderr")
	waitForTestFile(t, parentReaped)
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
		if !strings.Contains(string(result.Stdout()), "samesig-nonzero-stdout") || !strings.Contains(string(result.Stderr()), "samesig-nonzero-stderr") {
			t.Errorf("independent failure lost retained streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		releaseRegressionDescendantAndRequireExit(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the independently failed drain")
	}
}

func TestPushSameSignalPreStartCancellationStartsNothing(t *testing.T) {
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, samesigNoStartScript)
	pushRan := filepath.Join(root, "samesig-prestart-push-ran")
	t.Setenv("SAMESIG_PUSH_RAN", pushRan)

	// The cancellation precedes the call, so no timing is involved:
	// the attempt stays unstarted and cancelled with no exit status.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := countOpenFDsForCausality(t)
	result := gitPushWithCausalityTimeout(t, gitCommit, ctx, pushRouteUnderTest("master"), 10*time.Second, "after the pre-Start cancellation")

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

func TestPushSameSignalGuardTimeCancellationStartsNothing(t *testing.T) {
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, samesigNoStartScript)
	pushRan := filepath.Join(root, "samesig-guardtime-push-ran")
	t.Setenv("SAMESIG_PUSH_RAN", pushRan)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := filepath.Join(root, "samesig-guard-entered")
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
	waitForTestFile(t, entered)
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

func TestPushSameSignalConcurrentDrainImmutabilityAndArgv(t *testing.T) {
	gitCommit, _, _, _, _ := gitCommitUnderTestWithFakeGit(t, samesigDualStreamScript)
	result := gitPushWithCausalityTimeout(t, gitCommit, context.Background(), pushRouteUnderTest("master"), 10*time.Second, "after the dual-stream push")

	if !result.Success() {
		t.Fatalf("dual-stream push did not succeed: exit %d, cancelled %v, err %v", result.ExitCode(), result.Cancelled(), result.Err())
	}
	for _, marker := range []string{"samesig-dual-stdout-1", "samesig-dual-stdout-200"} {
		if !strings.Contains(string(result.Stdout()), marker) {
			t.Errorf("concurrent drain lost stdout marker %q", marker)
		}
	}
	for _, marker := range []string{"samesig-dual-stderr-1", "samesig-dual-stderr-200"} {
		if !strings.Contains(string(result.Stderr()), marker) {
			t.Errorf("concurrent drain lost stderr marker %q", marker)
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

func TestPushSameSignalOrdinaryCompletionClosesOwnedDescriptors(t *testing.T) {
	gitCommit, _, _, _, _ := gitCommitUnderTestWithFakeGit(t, samesigFDScript)
	// Ordinary completion with a context that never cancels must still
	// close both owned pipe read ends: the measurement never triggers
	// garbage collection, so finalizer-closed descriptors cannot hide a
	// leak.
	before := countOpenFDsForCausality(t)
	t.Setenv("SAMESIG_MODE", "ok")
	for i := 0; i < 2; i++ {
		result := gitPushWithCausalityTimeout(t, gitCommit, context.Background(), pushRouteUnderTest("master"), 10*time.Second, "after the ordinary successful push")
		if !result.Success() {
			t.Fatalf("ordinary push %d did not succeed: exit %d, err %v", i, result.ExitCode(), result.Err())
		}
	}
	t.Setenv("SAMESIG_MODE", "fail")
	for i := 0; i < 2; i++ {
		result := gitPushWithCausalityTimeout(t, gitCommit, context.Background(), pushRouteUnderTest("master"), 10*time.Second, "after the ordinary failed push")
		if result.Cancelled() {
			t.Errorf("ordinary failed push %d must not report cancelled", i)
		}
		if result.ExitCode() != 3 {
			t.Errorf("ordinary failed push %d ExitCode() = %d, want 3", i, result.ExitCode())
		}
		if result.Success() {
			t.Errorf("ordinary failed push %d must not report success", i)
		}
	}
	if delta := countOpenFDsForCausality(t) - before; delta > 2 {
		t.Errorf("open file descriptors grew by %d across four ordinary completions, want both owned read ends closed on every return", delta)
	}
}

func TestPushSameSignalLiveKillWithFailingPsDiagnosticStaysCancelled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, gitPath := gitCommitUnderTestWithFakeGit(t, samesigPushScript)
	// A completed ps diagnostic that exits nonzero with stderr proves
	// nothing about the target: only a clean unknown-PID report is
	// verified absence. The liveness probe must treat this as unknown
	// and the cancellation must still terminate the live child.
	if err := os.WriteFile(filepath.Join(filepath.Dir(gitPath), "ps"), []byte("#!/bin/sh\necho \"ps: cannot examine process $*\" >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("shadowing the ps diagnostic: %v", err)
	}
	ready := filepath.Join(root, "samesig-psfail-ready")
	childPidFile := filepath.Join(root, "samesig-psfail-child-pid")
	release := filepath.Join(root, "samesig-psfail-release")
	done := filepath.Join(root, "samesig-psfail-done")
	t.Setenv("SAMESIG_READY", ready)
	t.Setenv("SAMESIG_CHILD_PID", childPidFile)
	t.Setenv("SAMESIG_PARENT_DEAD", filepath.Join(root, "samesig-psfail-parent-dead"))
	t.Setenv("SAMESIG_PARENT_REAPED", filepath.Join(root, "samesig-psfail-parent-reaped"))
	t.Setenv("SAMESIG_RELEASE", release)
	t.Setenv("SAMESIG_DONE", done)
	defer func() {
		_ = os.WriteFile(release, []byte{}, 0o600)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(done); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pushDone := make(chan GitPushResult, 1)
	go func() { pushDone <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	// Ordering: the direct child is provably running (ready file plus
	// both pre-cancel lines consumed through the live progress buffers
	// with a positive liveness verdict) while a descendant already
	// holds the inherited write ends, so the cancellation
	// demonstrably targets a still-live direct push even though the ps
	// diagnostic itself reports failure.
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "samesig-stdout", "samesig-stderr")
	waitForTestFile(t, childPidFile)
	samesigRequireLiveVerdict(t, samesigReadChildPid(t, childPidFile), "the failing-ps live kill")
	cancel()

	select {
	case result := <-pushDone:
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
		if !strings.Contains(string(result.Stdout()), "samesig-stdout") || !strings.Contains(string(result.Stderr()), "samesig-stderr") {
			t.Errorf("live-killed push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		releaseRegressionDescendantAndRequireExit(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after cancelling the live push with a failing ps diagnostic")
	}
}

func TestPushSameSignalLiveKillWithSignalledPsDiagnosticStaysCancelled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, gitPath := gitCommitUnderTestWithFakeGit(t, samesigPushScript)
	// A ps diagnostic terminated by a signal never completed its
	// report: the target's liveness stays unknown and the live kill
	// must proceed.
	if err := os.WriteFile(filepath.Join(filepath.Dir(gitPath), "ps"), []byte("#!/bin/sh\nkill -TERM $$\n"), 0o755); err != nil {
		t.Fatalf("shadowing the ps diagnostic: %v", err)
	}
	ready := filepath.Join(root, "samesig-pssig-ready")
	childPidFile := filepath.Join(root, "samesig-pssig-child-pid")
	release := filepath.Join(root, "samesig-pssig-release")
	done := filepath.Join(root, "samesig-pssig-done")
	t.Setenv("SAMESIG_READY", ready)
	t.Setenv("SAMESIG_CHILD_PID", childPidFile)
	t.Setenv("SAMESIG_PARENT_DEAD", filepath.Join(root, "samesig-pssig-parent-dead"))
	t.Setenv("SAMESIG_PARENT_REAPED", filepath.Join(root, "samesig-pssig-parent-reaped"))
	t.Setenv("SAMESIG_RELEASE", release)
	t.Setenv("SAMESIG_DONE", done)
	defer func() {
		_ = os.WriteFile(release, []byte{}, 0o600)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(done); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pushDone := make(chan GitPushResult, 1)
	go func() { pushDone <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	// Ordering: provably running direct child (ready file, published
	// PID with a positive liveness verdict, plus both pre-cancel
	// lines) with a descendant holding the inherited write ends, so
	// the cancellation demonstrably targets a still-live direct push
	// while the ps diagnostic itself dies by signal.
	waitForTestFile(t, ready)
	waitForRegressionPushLines(t, gitCommit, "samesig-stdout", "samesig-stderr")
	waitForTestFile(t, childPidFile)
	samesigRequireLiveVerdict(t, samesigReadChildPid(t, childPidFile), "the signalled-ps live kill")
	cancel()

	select {
	case result := <-pushDone:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the live process had started before the cancellation")
		}
		if !result.Cancelled() {
			t.Error("a signalled ps diagnostic must not suppress the live kill: killing the still-running direct push must report cancelled")
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
		if !strings.Contains(string(result.Stdout()), "samesig-stdout") || !strings.Contains(string(result.Stderr()), "samesig-stderr") {
			t.Errorf("live-killed push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		releaseRegressionDescendantAndRequireExit(t, release, done)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after cancelling the live push with a signalled ps diagnostic")
	}
}
