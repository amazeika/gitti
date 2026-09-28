package git

// Phase 1 ordered push-cancellation regressions for
// docs/devel/17-repair-push-and-upstream-regressions.md.
//
// Criterion map (phase 1 acceptance criteria in spec order):
//  1. F02 signal holder: TestPhase1OrderedSignalHolderAcknowledgesStartedBeforeCancel
//  2. F02 zero-exit holder: TestPhase1OrderedZeroExitHolderAcknowledgesStartedBeforeCancel
//  3. F20 exit before unconsumed notification, synchronized at a
//     per-push wait gate held across cancellation:
//     TestPhase1OrderedExitBeforeUnconsumedNotificationKeepsObservedOutcome
//
// Each holder fixture is supervised by a test-owned child process that
// receives duplicates of the push pipes' write ends over a Unix socket
// (SCM_RIGHTS) and holds them until RELEASE; STARTED is written only
// after receipt, and cleanup always waits for the real supervisor exit
// with SIGTERM/SIGKILL escalation, even when STARTED never appears.
//  4. F01, F03-F08 policy prose: TestPhase1PolicyContractDescribesMatchingSigtermOutcome
//  5. Existing regressions stay green (no new test; covered by the frozen
//     commit_test.go and push_phase13_policy_outcome_additive_test.go suites,
//     e.g. no-request SIGTERM, known completion, distinguishable SIGKILL,
//     live cancel, ordinary EOF, guard-time cancellation).
//
// Only long-standing helpers (gitCommitUnderTestWithFakeGit,
// pushRouteUnderTest) are reused; every other helper uses a unique p1o
// prefix so this file does not depend on any other additive file.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const p1oProbe = `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
`

func p1oWaitFile(t *testing.T, path string) {
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

func p1oWaitLines(t *testing.T, gc *GitCommit, wantStdout, wantStderr string) {
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

const p1oWatcherSh = `
      parent=$$
      ( while kill -0 "$parent" 2>/dev/null; do
          state=$(ps -o stat= -p "$parent" 2>/dev/null)
          case "$state" in Z*) break ;; esac
        done
        touch "$P1O_DEAD"
      ) &
`

const p1oSenderSh = `
      P1O_SOCK="$P1O_SOCK" python3 -c '
import array, os, socket, time
sock = os.environ["P1O_SOCK"]
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
for _ in range(200):
    try:
        s.connect(sock)
        break
    except OSError:
        time.sleep(0.05)
else:
    raise SystemExit("p1o sender: cannot connect to the descriptor supervisor")
s.sendmsg([b"p1o"], [(socket.SOL_SOCKET, socket.SCM_RIGHTS, array.array("i", [1, 2]).tobytes())])
'
`

const p1oSupervisorPy = `import array
import os
import socket
import sys
import time


def main():
    sock_path, started, release, done = sys.argv[1:5]
    try:
        os.unlink(sock_path)
    except FileNotFoundError:
        pass
    listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    listener.bind(sock_path)
    listener.listen(1)
    listener.settimeout(0.5)
    conn = None
    while conn is None:
        if os.path.exists(release):
            open(done, "w").close()
            return 0
        try:
            conn, _ = listener.accept()
        except socket.timeout:
            continue
    held = []
    while len(held) < 2:
        try:
            data, ancdata, _, _ = conn.recvmsg(64, 1024)
        except OSError:
            break
        if not data and not ancdata:
            break
        for level, typ, payload in ancdata:
            if level == socket.SOL_SOCKET and typ == socket.SCM_RIGHTS:
                values = array.array("i")
                values.frombytes(payload)
                for value in values.tolist():
                    if len(held) < 2:
                        held.append(value)
    try:
        conn.close()
    except OSError:
        pass
    listener.close()
    if len(held) < 2:
        return 1
    open(started, "w").close()
    while not os.path.exists(release):
        time.sleep(0.01)
    for fd in held:
        try:
            os.close(fd)
        except OSError:
            pass
    open(done, "w").close()
    return 0


sys.exit(main())
`

type p1oSupervisor struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func p1oWaitSupervisorExit(sup *p1oSupervisor, timeout time.Duration) (error, bool) {
	select {
	case <-sup.done:
		return sup.err, true
	case <-time.After(timeout):
		return nil, false
	}
}

func p1oStartSupervisor(t *testing.T, tag, root, started, release, done string) (*p1oSupervisor, string, string) {
	t.Helper()
	// macOS limits AF_UNIX paths to 104 bytes, far below a t.TempDir
	// path, so the socket (and only the socket) lives in a short dir.
	sockDir, err := os.MkdirTemp("", "p1o")
	if err != nil {
		t.Fatalf("creating the %s supervisor socket dir: %v", tag, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "s.sock")
	script := filepath.Join(root, tag+"-supervisor.py")
	if err := os.WriteFile(script, []byte(p1oSupervisorPy), 0o700); err != nil {
		t.Fatalf("writing the %s descriptor supervisor: %v", tag, err)
	}
	stderrPath := filepath.Join(root, tag+"-supervisor.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("creating the %s supervisor stderr log: %v", tag, err)
	}
	cmd := exec.Command("python3", script, sock, started, release, done)
	cmd.Stderr = stderrFile
	if err := cmd.Start(); err != nil {
		_ = stderrFile.Close()
		t.Fatalf("starting the %s descriptor supervisor: %v", tag, err)
	}
	sup := &p1oSupervisor{cmd: cmd, done: make(chan struct{})}
	go func() {
		sup.err = cmd.Wait()
		_ = stderrFile.Close()
		close(sup.done)
	}()
	return sup, sock, stderrPath
}

func p1oSupervisorDiag(tag, stderrPath string) string {
	data, err := os.ReadFile(stderrPath)
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return ""
	}
	return fmt.Sprintf("; %s supervisor stderr: %s", tag, strings.TrimSpace(string(data)))
}

func p1oReleaseSupervisor(t *testing.T, tag string, sup *p1oSupervisor, release, stderrPath string) {
	t.Helper()
	if err := os.WriteFile(release, []byte{}, 0o600); err != nil {
		t.Fatalf("releasing the %s descriptor supervisor: %v", tag, err)
	}
	if err, ok := p1oWaitSupervisorExit(sup, 10*time.Second); ok {
		if err != nil {
			t.Errorf("the %s descriptor supervisor exited with an error: %v%s", tag, err, p1oSupervisorDiag(tag, stderrPath))
		}
		return
	}
	_ = sup.cmd.Process.Kill()
	if err, ok := p1oWaitSupervisorExit(sup, 5*time.Second); ok {
		t.Errorf("the %s descriptor supervisor ignored its release and was force-killed (%v)%s", tag, err, p1oSupervisorDiag(tag, stderrPath))
		return
	}
	t.Fatalf("the %s descriptor supervisor did not exit after release and SIGKILL; possible leaked process%s", tag, p1oSupervisorDiag(tag, stderrPath))
}

// p1oCleanupSupervisor owns the supervisor on every path, including a
// Fatal before STARTED is ever observed or a release that times out: it
// releases, then escalates to SIGTERM and SIGKILL while always waiting
// for the real process exit. A pre-exit DONE file is never treated as
// reaping evidence; only the supervisor Wait counts.
func p1oCleanupSupervisor(t *testing.T, tag string, sup *p1oSupervisor, release, stderrPath string) {
	t.Helper()
	t.Cleanup(func() {
		_ = os.WriteFile(release, []byte{}, 0o600)
		if err, ok := p1oWaitSupervisorExit(sup, 10*time.Second); ok {
			if err != nil {
				t.Errorf("the %s descriptor supervisor exited with an error: %v%s", tag, err, p1oSupervisorDiag(tag, stderrPath))
			}
			return
		}
		_ = sup.cmd.Process.Signal(syscall.SIGTERM)
		if _, ok := p1oWaitSupervisorExit(sup, 2*time.Second); ok {
			t.Errorf("the %s descriptor supervisor ignored its release and was terminated%s", tag, p1oSupervisorDiag(tag, stderrPath))
			return
		}
		_ = sup.cmd.Process.Kill()
		if _, ok := p1oWaitSupervisorExit(sup, 5*time.Second); ok {
			t.Errorf("the %s descriptor supervisor ignored SIGTERM and was force-killed%s", tag, p1oSupervisorDiag(tag, stderrPath))
			return
		}
		t.Errorf("the %s descriptor supervisor did not exit after SIGKILL; possible leaked process%s", tag, p1oSupervisorDiag(tag, stderrPath))
	})
}

func TestPhase1OrderedSignalHolderAcknowledgesStartedBeforeCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the signal-specific push fixture requires POSIX shell signals")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, p1oProbe+`    push)
      echo "p1o-sig-stdout"
      echo "p1o-sig-stderr" >&2
`+p1oWatcherSh+p1oSenderSh+`      kill -TERM "$$"
      ;;
  esac
done
exit 0
`)
	dead := filepath.Join(root, "p1o-sig-dead")
	release := filepath.Join(root, "p1o-sig-release")
	done := filepath.Join(root, "p1o-sig-done")
	started := filepath.Join(root, "p1o-sig-started")
	supervisor, sock, supervisorStderr := p1oStartSupervisor(t, "p1o-sig", root, started, release, done)
	t.Setenv("P1O_SOCK", sock)
	t.Setenv("P1O_DEAD", dead)
	p1oCleanupSupervisor(t, "p1o-sig", supervisor, release, supervisorStderr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneCh := make(chan GitPushResult, 1)
	go func() { doneCh <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	// The supervisor's own STARTED marker proves a test-owned process
	// holds duplicates of the inherited pipe write ends before the
	// cancellation lands; cancelling earlier would let the drain reach
	// EOF on its own and hide the bounded-drain path this fixture pins.
	// STARTED is written only after the supervisor receives both
	// descriptors, so it cannot be faked by the exiting direct child.
	p1oWaitFile(t, started)
	p1oWaitFile(t, dead)
	p1oWaitLines(t, gitCommit, "p1o-sig-stdout", "p1o-sig-stderr")
	if _, err := os.Stat(done); err == nil {
		t.Fatal("the descriptor supervisor released the inherited descriptors before the cancellation, so the drain could already have completed")
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
		if !strings.Contains(string(result.Stdout()), "p1o-sig-stdout") ||
			!strings.Contains(string(result.Stderr()), "p1o-sig-stderr") {
			t.Errorf("signal-terminated push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		p1oReleaseSupervisor(t, "p1o-sig", supervisor, release, supervisorStderr)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the signal drain")
	}
}

func TestPhase1OrderedZeroExitHolderAcknowledgesStartedBeforeCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, p1oProbe+`    push)
      echo "p1o-zero-stdout"
      echo "p1o-zero-stderr" >&2
`+p1oWatcherSh+p1oSenderSh+`      exit 0
      ;;
  esac
done
exit 0
`)
	dead := filepath.Join(root, "p1o-zero-dead")
	release := filepath.Join(root, "p1o-zero-release")
	done := filepath.Join(root, "p1o-zero-done")
	started := filepath.Join(root, "p1o-zero-started")
	supervisor, sock, supervisorStderr := p1oStartSupervisor(t, "p1o-zero", root, started, release, done)
	t.Setenv("P1O_SOCK", sock)
	t.Setenv("P1O_DEAD", dead)
	p1oCleanupSupervisor(t, "p1o-zero", supervisor, release, supervisorStderr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneCh := make(chan GitPushResult, 1)
	go func() { doneCh <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()
	// Same supervisor-start prerequisite as the signal fixture: the
	// test-owned supervisor must provably hold the inherited write ends
	// while the direct child has already exited zero, so the late
	// cancellation exercises the bounded drain rather than an
	// already-closed EOF.
	p1oWaitFile(t, started)
	p1oWaitFile(t, dead)
	p1oWaitLines(t, gitCommit, "p1o-zero-stdout", "p1o-zero-stderr")
	if _, err := os.Stat(done); err == nil {
		t.Fatal("the descriptor supervisor released the inherited descriptors before the cancellation, so the drain could already have completed")
	}
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
			t.Errorf("Success() = false, want true: a zero exit stays successful (Err() = %v)", result.Err())
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not contain the late context cancellation", result.Err())
		}
		if !strings.Contains(string(result.Stdout()), "p1o-zero-stdout") ||
			!strings.Contains(string(result.Stderr()), "p1o-zero-stderr") {
			t.Errorf("zero-exit push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		p1oReleaseSupervisor(t, "p1o-zero", supervisor, release, supervisorStderr)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the zero-exit drain")
	}
}

func TestPhase1OrderedExitBeforeUnconsumedNotificationKeepsObservedOutcome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the signal-specific push fixture requires POSIX shell signals")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, p1oProbe+`    push)
      echo "p1o-notify-stdout"
      echo "p1o-notify-stderr" >&2
`+p1oWatcherSh+p1oSenderSh+`      exit 3
      ;;
  esac
done
exit 0
`)
	// Explicit wait-notification gate. The optional per-push hook below
	// must be invoked by the push reaper after direct cmd.Wait returns
	// and before the completion notification can be received. Holding it
	// across cancellation is what proves the notification unconsumed.
	// Production contract for the follow-up hook (api/git/commit.go):
	//   func (gc *GitCommit) SetP1oWaitGate(ack chan<- struct{}, release <-chan struct{})
	// installed before GitPush runs and read once at push start; when set,
	// the reaper closes ack after Wait returns, blocks until release
	// closes, then enqueues the buffered waitCh notification. When unset,
	// the reaper behaves exactly as today, so no ordinary push ever
	// blocks on this gate and signal classification is untouched.
	setter, ok := any(gitCommit).(interface {
		SetP1oWaitGate(ack chan<- struct{}, release <-chan struct{})
	})
	if !ok {
		t.Fatalf("phase-1 F20 prerequisite not established: GitCommit has no P1oWaitGate hook between direct Wait return and completion-notification send, so the unconsumed-notification ordering cannot be synchronized")
	}
	dead := filepath.Join(root, "p1o-notify-dead")
	release := filepath.Join(root, "p1o-notify-release")
	done := filepath.Join(root, "p1o-notify-done")
	started := filepath.Join(root, "p1o-notify-started")
	supervisor, sock, supervisorStderr := p1oStartSupervisor(t, "p1o-notify", root, started, release, done)
	t.Setenv("P1O_SOCK", sock)
	t.Setenv("P1O_DEAD", dead)
	p1oCleanupSupervisor(t, "p1o-notify", supervisor, release, supervisorStderr)
	waitAck := make(chan struct{})
	waitRelease := make(chan struct{})
	var waitReleaseOnce sync.Once
	releaseWaitGate := func() {
		waitReleaseOnce.Do(func() { close(waitRelease) })
	}
	defer releaseWaitGate()
	setter.SetP1oWaitGate(waitAck, waitRelease)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneCh := make(chan GitPushResult, 1)
	go func() { doneCh <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	// Explicit synchronization prerequisites, all established before
	// cancelling: the direct child has exited (dead marker), both readers
	// consumed the pre-exit bytes (progress lines), the supervisor still
	// holds the inherited write ends (done absent, so the drain cannot
	// have reached EOF), the reaper has returned from direct Wait but is
	// blocked before the completion notification can be received (gate
	// acknowledgment), and the push has not returned. A pass therefore
	// proves exit-before-cancel with a deterministically unconsumed
	// notification instead of inferring it from an empty doneCh.
	p1oWaitFile(t, started)
	p1oWaitFile(t, dead)
	p1oWaitLines(t, gitCommit, "p1o-notify-stdout", "p1o-notify-stderr")
	if _, err := os.Stat(done); err == nil {
		t.Fatal("phase-1 F20 prerequisite not established: the descriptor supervisor released the inherited descriptors before the cancellation")
	}
	select {
	case <-waitAck:
	case <-time.After(10 * time.Second):
		t.Fatal("phase-1 F20 prerequisite not established: wait-gate acknowledgment missing 10s after direct exit with the inherited writer live; the completion notification cannot be shown unconsumed")
	}
	select {
	case result := <-doneCh:
		t.Fatalf("phase-1 F20 prerequisite not established: GitPush already returned %v before the cancellation, so the completion notification was consumed", result)
	default:
	}
	cancel()
	// The gate stays held across the cancellation: with the notification
	// blocked and the supervisor holding the write ends, no result may
	// arrive before the gate is released.
	select {
	case result := <-doneCh:
		t.Fatalf("phase-1 F20 gate violated: GitPush returned %v while the wait gate was still held", result)
	case <-time.After(time.Second):
	}
	releaseWaitGate()

	select {
	case result := <-doneCh:
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
		if !strings.Contains(string(result.Stdout()), "p1o-notify-stdout") ||
			!strings.Contains(string(result.Stderr()), "p1o-notify-stderr") {
			t.Errorf("nonzero push lost captured streams: stdout %q, stderr %q", result.Stdout(), result.Stderr())
		}
		p1oReleaseSupervisor(t, "p1o-notify", supervisor, release, supervisorStderr)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the nonzero drain")
	}
}

func TestPhase1PolicyContractDescribesMatchingSigtermOutcome(t *testing.T) {
	commitSrc, err := os.ReadFile(filepath.Join("commit.go"))
	if err != nil {
		t.Fatalf("reading the push terminal contract: %v", err)
	}
	livenessSrc, err := os.ReadFile(filepath.Join("push_liveness_unix.go"))
	if err != nil {
		t.Fatalf("reading the Unix signal/status comments: %v", err)
	}
	sourceSpec, err := os.ReadFile(filepath.Join("..", "..", "docs", "devel", "13-harden-push-cancellation-and-partial-upstream-state.md"))
	if err != nil {
		t.Fatalf("reading the source spec: %v", err)
	}

	// F01: the terminal outcome comment must describe the accepted
	// matching-SIGTERM outcome instead of claiming only demonstrably
	// caused cancellations are reported as cancelled. Scope the check
	// to the terminal block itself: overlap language elsewhere in the
	// file does not fix the recorded defect at the verdict.
	terminal := string(commitSrc)
	const terminalAnchor = "Each terminal outcome is distinguishable"
	terminalBlock := terminal
	if idx := strings.Index(terminal, terminalAnchor); idx >= 0 {
		end := idx + 1200
		if end > len(terminal) {
			end = len(terminal)
		}
		terminalBlock = terminal[idx:end]
	}
	// F01: the terminal outcome comment states the accepted
	// matching-SIGTERM outcome as short, nonconflicting claims instead of
	// reporting only demonstrably caused cancellations. Scope the check
	// to the terminal block itself: overlap language elsewhere in the
	// file does not fix the recorded defect at the verdict.
	for _, want := range []string{
		"matching reaped SIGTERM status is reported as cancelled",
		"observationally indistinguishable",
		"claims no signal provenance",
		"never relabels a known completed process",
		"nonmatching status stays the observed process outcome",
	} {
		if !strings.Contains(terminalBlock, want) {
			t.Errorf("phase-1 terminal contract is missing the accepted-policy claim %q", want)
		}
	}
	if strings.Contains(terminalBlock, "demonstrably terminated the live direct process is reported as") {
		t.Error("phase-1 terminal contract still claims demonstrable causation: the commit.go terminal comment reports only demonstrably caused cancellation without the accepted matching-SIGTERM overlap outcome")
	}
	if !strings.Contains(terminalBlock, "sentinel.\n") || !strings.Contains(terminalBlock, "cancelled.\n") {
		t.Error("phase-1 terminal contract must stay split into short claims, one outcome per line")
	}

	// F03: the source Goal section must describe the accepted outcome
	// policy, not claim every cancelled outcome was caused by our
	// SIGTERM. Scope the check to the Goal section: matching language
	// in later criteria text does not fix the recorded Goal defect.
	goal := string(sourceSpec)
	const goalAnchor = "### Goal"
	goalSection := goal
	if idx := strings.Index(goal, goalAnchor); idx >= 0 {
		rest := goal[idx:]
		if end := strings.Index(rest, "### Use cases"); end >= 0 {
			goalSection = rest[:end]
		} else {
			goalSection = rest
		}
	}
	// F03: the source Goal section states the accepted outcome policy
	// as short claims, not that every cancelled outcome was caused by
	// our SIGTERM. Scope the check to the Goal section: matching
	// language in later criteria text does not fix the recorded Goal
	// defect.
	for _, want := range []string{
		"requested cancellation with delivered SIGTERM",
		"matching reaped SIGTERM status",
		"observationally indistinguishable from an independent same-SIGTERM race",
		"Known completed processes and nonmatching statuses keep their observed outcomes",
	} {
		if !strings.Contains(goalSection, want) {
			t.Errorf("phase-1 source Goal is missing the accepted-policy claim %q", want)
		}
	}
	if strings.Contains(goalSection, "Show cancellation only when it caused termination of the direct push process") {
		t.Error("phase-1 source Goal still claims causal proof: the source spec Goal shows cancellation only when caused without the accepted matching-SIGTERM overlap outcome")
	}
	if !strings.Contains(goalSection, "status.\nThat matching-SIGTERM overlap") || !strings.Contains(goalSection, "race.\nKnown completed processes") {
		t.Error("phase-1 source Goal must stay split into short claims, one outcome per line")
	}

	// The shipped source spec keeps its frontmatter and completed phase
	// record: history is aligned, not reinterpreted as new features.
	if !strings.Contains(goal, "status: shipped") {
		t.Error("the source spec must remain shipped while its prose is aligned")
	}
	if !strings.Contains(goal, "completed: [1.3, 2, 3, 4, 5]") {
		t.Error("the source spec must retain its completed phase record")
	}

	// F03: the direct-process attribution paragraph states the accepted
	// overlap as an outcome policy without asserting which signal caused
	// death, and keeps the exit-before-notification race oracle.
	const attributionAnchor = "### Direct-process cancellation attribution"
	attribution := goal
	if idx := strings.Index(goal, attributionAnchor); idx >= 0 {
		rest := goal[idx:]
		if end := strings.Index(rest, "### Branch upstream classification"); end >= 0 {
			attribution = rest[:end]
		} else {
			attribution = rest
		}
	}
	for _, want := range []string{
		"keeps the cancellation claim under the accepted phase-1.3 policy without asserting which signal caused death",
		"Test the race where direct exit occurs before cancellation but before completion notification is consumed",
	} {
		if !strings.Contains(attribution, want) {
			t.Errorf("phase-1 source attribution is missing the accepted-policy claim %q", want)
		}
	}

	// F04-F06: the shipped cross-file verification, frozen-test and
	// allowed-scope sentences keep agreeing with the accepted policy and
	// stay historical record rather than reinterpreted features.
	for _, want := range []string{
		"verify the phase document, commit.go and Unix probe agree on the accepted cancellation-outcome policy",
		"Do not require a final-gap same-signal differentiation oracle",
		"four explicitly authorized frozen cross-file policy-pin test paths",
		"push_liveness_unix.go` for Unix-comment ownership",
	} {
		if !strings.Contains(goal, want) {
			t.Errorf("phase-1 source history lost the recorded claim %q; historical criteria must stay, not be reinterpreted", want)
		}
	}

	// F07-F08: the Unix signal/status comments keep the distinguishable
	// signal and the observationally-indistinguishable same-SIGTERM
	// overlap without asserting signal provenance.
	liveness := string(livenessSrc)
	if !strings.Contains(liveness, "observationally indistinguishable") {
		t.Error("the Unix signal/status comments must name the observationally indistinguishable same-SIGTERM overlap")
	}
	if strings.Contains(liveness, "proves which signal caused death") || strings.Contains(liveness, "proof that our signal caused") {
		t.Error("the Unix signal/status comments must not assert signal provenance")
	}
}
