package git

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gohyuhan/gitti/executor"
	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/logging"
)

// ------------------------------------
//
//	Shadow PATH with a fake git that answers the upstream probe and performs a
//	scripted push, so process outcomes can be exercised without a remote
//
// ------------------------------------
func fakeGitOnPath(t *testing.T, script string) string {
	t.Helper()

	binDir := t.TempDir()
	gitPath := filepath.Join(binDir, "git")
	if err := os.WriteFile(gitPath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the fake git: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return gitPath
}

// ------------------------------------
//
//	Point the executor at a scratch directory and build the push handler with a
//	fake git on PATH, returning the handler and its logging for assertions
//
// ------------------------------------
func gitCommitUnderTestWithFakeGit(t *testing.T, script string) (*GitCommit, *GitProcessLock, *logging.GittiLogging, string, string) {
	t.Helper()

	i18n.InitGittiLanguageMapping("en")
	gitPath := fakeGitOnPath(t, script)

	root := t.TempDir()
	original := executor.GittiCmdExecutor
	t.Cleanup(func() { executor.GittiCmdExecutor = original })
	executor.InitCmdExecutor(root)

	gittiLogging := logging.InitGittiLogging(64, make(chan string, 256), 3)
	processLock := InitGitProcessLock(gittiLogging)
	gitCommit := InitGitCommit(make(chan string, 16), processLock, gittiLogging)

	return gitCommit, processLock, gittiLogging, root, gitPath
}

// ------------------------------------
//
//	Report whether the argv carries the operation arguments as a consecutive
//	suffix, tolerating the executor's injected -c options and lock flag
//
// ------------------------------------
func argvCarriesOperationArgs(argv, operationArgs []string) bool {
	for i := 0; i+len(operationArgs) <= len(argv); i++ {
		if slices.Equal(argv[i:i+len(operationArgs)], operationArgs) {
			return true
		}
	}
	return false
}

// ------------------------------------
//
//	Report the log entries a run recorded at the given severity level
//
// ------------------------------------
func logsAtSeverity(gittiLogging *logging.GittiLogging, severity string) []string {
	var recorded []string
	for _, entry := range gittiLogging.GetFullLogs() {
		if entry.OpsSeverityLevel == severity {
			recorded = append(recorded, entry.OpsDescription)
		}
	}
	return recorded
}

func TestBuildPushGitArgsPreservesTheExistingPushMatrix(t *testing.T) {
	for _, test := range []struct {
		name        string
		pushType    string
		hasUpstream bool
		want        []string
	}{
		{name: "tracked normal", pushType: PUSH, hasUpstream: true, want: []string{"push", "--progress", "origin"}},
		{name: "tracked safe force", pushType: FORCEPUSHSAFE, hasUpstream: true, want: []string{"push", "--progress", "--force-with-lease", "origin"}},
		{name: "tracked dangerous force", pushType: FORCEPUSHDANGEROUS, hasUpstream: true, want: []string{"push", "--progress", "--force", "origin"}},
		{name: "untracked normal", pushType: PUSH, hasUpstream: false, want: []string{"push", "-u", "--progress", "origin", "feature/x"}},
		{name: "untracked safe force", pushType: FORCEPUSHSAFE, hasUpstream: false, want: []string{"push", "-u", "--progress", "--force-with-lease", "origin", "feature/x"}},
		{name: "untracked dangerous force", pushType: FORCEPUSHDANGEROUS, hasUpstream: false, want: []string{"push", "-u", "--progress", "--force", "origin", "feature/x"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := buildPushGitArgs("origin", test.pushType, "feature/x", test.hasUpstream)
			if !slices.Equal(got, test.want) {
				t.Errorf("buildPushGitArgs() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestGitPushWithSigningSharesThePushArgumentBuilder(t *testing.T) {
	gitCommit, _, _, _, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-parse)
      if [ "$FAKE_GIT_UPSTREAM" = "absent" ]; then exit 128; fi
      echo "origin/master"
      exit 0
      ;;
  esac
done
exit 0
`)

	for upstreamState, hasUpstream := range map[string]bool{"absent": false, "present": true} {
		t.Run(upstreamState, func(t *testing.T) {
			t.Setenv("FAKE_GIT_UPSTREAM", upstreamState)
			for _, pushType := range []string{PUSH, FORCEPUSHSAFE, FORCEPUSHDANGEROUS} {
				want := buildPushGitArgs("origin", pushType, "master", hasUpstream)
				if got := gitCommit.GitPushWithSigning("origin", pushType, "master"); !slices.Equal(got, want) {
					t.Errorf("%s %s: GitPushWithSigning() = %v, want the shared builder's %v", upstreamState, pushType, got, want)
				}
			}
		})
	}
}

func TestGitPushRecordsTheDefinitiveResultForASuccessfulPush(t *testing.T) {
	root, run := repositoryUnderTest(t)
	run("commit", "-q", "--allow-empty", "-m", "base")

	bareRemote := filepath.Join(t.TempDir(), "origin.git")
	initCmd := exec.Command("git", "init", "-q", "--bare", bareRemote)
	initCmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	if output, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, output)
	}
	run("remote", "add", "origin", bareRemote)

	gitCommit := InitGitCommit(make(chan string, 16), InitGitProcessLock(logging.InitGittiLogging(64, make(chan string, 256), 3)), logging.InitGittiLogging(64, make(chan string, 256), 3))

	// First push: no upstream yet, so the -u and branch operands must be present.
	first := gitCommit.GitPush(context.Background(), "origin", PUSH, "master")
	if !first.Success() {
		t.Fatalf("first push did not succeed: exit %d, cancelled %v, err %v", first.ExitCode(), first.Cancelled(), first.Err())
	}
	if first.WorkingDirectory() != root {
		t.Errorf("WorkingDirectory() = %q, want the active repository path %q", first.WorkingDirectory(), root)
	}
	if !argvCarriesOperationArgs(first.Argv(), []string{"push", "-u", "--progress", "origin", "master"}) {
		t.Errorf("first push argv = %v, want the untracked-branch operation arguments", first.Argv())
	}
	if first.Argv()[0] != "git" {
		t.Errorf("argv[0] = %q, want the git executable rather than a reconstructed command", first.Argv()[0])
	}
	if !strings.Contains(string(first.Stdout()), "set up to track") {
		t.Errorf("retained stdout = %q, want the upstream tracking report on its own stream", string(first.Stdout()))
	}
	if !strings.Contains(string(first.Stderr()), "master -> master") {
		t.Errorf("retained stderr = %q, want the branch update report on its own stream", string(first.Stderr()))
	}
	// The stored result is the published one and carries defensive copies.
	stored := gitCommit.GitPushResult()
	if !slices.Equal(stored.Argv(), first.Argv()) || stored.ExitCode() != first.ExitCode() {
		t.Errorf("stored result diverged from the returned result: stored %v/%d, returned %v/%d", stored.Argv(), stored.ExitCode(), first.Argv(), first.ExitCode())
	}

	// Second push: the upstream now exists, so no -u or branch operand may be
	// retained from the first attempt's shape.
	second := gitCommit.GitPush(context.Background(), "origin", PUSH, "master")
	if !second.Success() {
		t.Fatalf("second push did not succeed: exit %d, err %v", second.ExitCode(), second.Err())
	}
	if argvCarriesOperationArgs(second.Argv(), []string{"push", "-u"}) {
		t.Errorf("tracked-branch push argv = %v, must not carry the first push's -u operand", second.Argv())
	}
	if !argvCarriesOperationArgs(second.Argv(), []string{"push", "--progress", "origin"}) {
		t.Errorf("tracked-branch push argv = %v, want the tracked-branch operation arguments", second.Argv())
	}
}

func TestGitPushRecordsTheExactExitCodeAndStderrForANonZeroExit(t *testing.T) {
	gitCommit, _, gittiLogging, _, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    push) echo "error: failed to push some refs" >&2; exit 1 ;;
  esac
done
exit 0
`)

	result := gitCommit.GitPush(context.Background(), "origin", PUSH, "master")

	if result.Started() != true {
		t.Error("the process started, so Started() must be true")
	}
	if result.ExitCode() != 1 {
		t.Errorf("ExitCode() = %d, want the exact nonzero exit code 1", result.ExitCode())
	}
	if result.Cancelled() {
		t.Error("a plain failure must not be reported as cancelled")
	}
	if result.Err() == nil {
		t.Error("Err() = nil, want the wait error for a nonzero exit")
	}
	if result.Success() {
		t.Error("a nonzero exit must not be reported as a success")
	}
	if !strings.Contains(string(result.Stderr()), "failed to push some refs") {
		t.Errorf("retained stderr = %q, want the rejection text", string(result.Stderr()))
	}
	if recorded := logsAtSeverity(gittiLogging, logging.ERROR); len(recorded) == 0 {
		t.Error("a nonzero exit left no error log entry")
	}
}

func TestGitPushLockRefusalReturnsATypedResult(t *testing.T) {
	gitCommit, processLock, gittiLogging, _, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
exit 0
`)

	// Another git operation is already holding the universal lock.
	if !processLock.CanProceedWithGitOps() {
		t.Fatal("the test could not hold the git operations lock")
	}
	defer processLock.ReleaseGitOpsLock()

	result := gitCommit.GitPush(context.Background(), "origin", PUSH, "master")

	if result.Started() {
		t.Error("a refused push never starts a process")
	}
	if result.ExitCode() != -1 {
		t.Errorf("ExitCode() = %d, want -1 for the absent process status", result.ExitCode())
	}
	if result.Err() == nil || !strings.Contains(result.Err().Error(), i18n.LANGUAGEMAPPING.OtherGitOpsIsRunningWarning) {
		t.Errorf("Err() = %v, want the other-operation warning so the popup can act on it", result.Err())
	}
	if result.Success() {
		t.Error("a refused push must not be reported as a success")
	}
	if recorded := logsAtSeverity(gittiLogging, logging.WARN); len(recorded) == 0 {
		t.Error("a lock refusal left no warning log entry")
	}
}

func TestGitPushCancellationIsADistinctOutcomeWithBothStreamsRetained(t *testing.T) {
	gitCommit, _, gittiLogging, _, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    push)
      echo "stdout-progress"
      echo "stderr-progress" >&2
      sleep 30
      ;;
  esac
done
exit 0
`)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan GitPushResult, 1)
	go func() {
		done <- gitCommit.GitPush(ctx, "origin", PUSH, "master")
	}()

	// Cancel only once the process is provably started: the fake writes both
	// pre-sleep lines before it hangs, and they surface through the live
	// progress buffer only after a successful start.
	started := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if stdoutLines, _ := gitCommit.GitRemotePushOutput(); len(stdoutLines) > 0 {
			started = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !started {
		t.Fatal("the fake push never reached its pre-sleep output")
	}
	cancel()

	select {
	case result := <-done:
		if !result.Started() {
			t.Error("the process had started before the cancellation")
		}
		if !result.Cancelled() {
			t.Error("a cancelled push must be distinguishable from a plain failure")
		}
		if !errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, want the context cancellation", result.Err())
		}
		if result.ExitCode() != -1 {
			t.Errorf("ExitCode() = %d, want -1 because the killed process carries no exit status", result.ExitCode())
		}
		if result.Success() {
			t.Error("a cancelled push must not be reported as a success")
		}
		if !strings.Contains(string(result.Stdout()), "stdout-progress") {
			t.Errorf("retained stdout = %q, want the pre-cancellation stream", string(result.Stdout()))
		}
		if !strings.Contains(string(result.Stderr()), "stderr-progress") {
			t.Errorf("retained stderr = %q, want the separate stream", string(result.Stderr()))
		}
		var cancelledLogged bool
		for _, entry := range gittiLogging.GetFullLogs() {
			if entry.OpsSeverityLevel == logging.WARN && strings.Contains(entry.OpsDescription, "CANCELLED") {
				cancelledLogged = true
			}
		}
		if !cancelledLogged {
			t.Error("a cancellation left no CANCELLED log entry")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after the cancellation, suggesting a pipe deadlock")
	}
}

func TestGitPushDrainsLargeStreamsConcurrentlyWithoutDeadlock(t *testing.T) {
	// More than the pipe buffer per stream, in lines short enough for the
	// scanner: without concurrent draining the child blocks on write and the
	// wait never returns.
	gitCommit, _, _, _, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    push)
      i=0
      while [ "$i" -lt 100 ]; do
        head -c 10240 /dev/zero | tr '\0' 'x'
        echo
        i=$((i+1))
      done
      i=0
      while [ "$i" -lt 100 ]; do
        { head -c 10240 /dev/zero | tr '\0' 'y'; } >&2
        echo >&2
        i=$((i+1))
      done
      ;;
  esac
done
exit 0
`)

	done := make(chan GitPushResult, 1)
	go func() {
		done <- gitCommit.GitPush(context.Background(), "origin", PUSH, "master")
	}()

	select {
	case result := <-done:
		if !result.Success() {
			t.Fatalf("the large-output push did not succeed: exit %d, err %v", result.ExitCode(), result.Err())
		}
		if got := len(result.Stdout()); got != 100*10241 {
			t.Errorf("retained stdout = %d bytes, want the exact 100 x 10241-byte stream", got)
		}
		if got := len(result.Stderr()); got != 100*10241 {
			t.Errorf("retained stderr = %d bytes, want the exact 100 x 10241-byte stream", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("GitPush did not return on a >1MB output, suggesting a pipe deadlock")
	}
}

func TestGitPushReadFailureIsRecordedWhileKeepingTheExitStatus(t *testing.T) {
	// One line past the scanner's ceiling is a read failure, not a process
	// failure: the exit status must still be recorded and the outcome logged.
	gitCommit, _, gittiLogging, _, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    push) { head -c 100000 /dev/zero | tr '\0' 'x'; } >&2 ;;
  esac
done
exit 0
`)

	result := gitCommit.GitPush(context.Background(), "origin", PUSH, "master")

	if result.ExitCode() != 0 {
		t.Errorf("ExitCode() = %d, want the exact 0 the process reported", result.ExitCode())
	}
	if result.Err() == nil {
		t.Error("Err() = nil, want the recorded read failure")
	}
	if !errors.Is(result.Err(), bufio.ErrTooLong) {
		t.Errorf("Err() = %v, want bufio.ErrTooLong", result.Err())
	}
	if got := len(result.Stderr()); got != 100000 {
		t.Errorf("retained stderr = %d bytes, want the complete 100000-byte stream after the scanner failure", got)
	}
	var readErrorLogged bool
	for _, entry := range gittiLogging.GetFullLogs() {
		if entry.OpsSeverityLevel == logging.ERROR && strings.Contains(entry.OpsDescription, "READ ERROR") {
			readErrorLogged = true
		}
	}
	if !readErrorLogged {
		t.Error("a read failure left no READ ERROR log entry")
	}
}

func TestGitPushClearsThePreviousAttemptBeforeStarting(t *testing.T) {
	gitCommit, _, _, _, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    push)
      if [ -n "$FAKE_GIT_TAG" ]; then echo "$FAKE_GIT_TAG" >&2; fi
      if [ "$FAKE_GIT_SLOW" = "1" ]; then sleep 1; fi
      exit "${FAKE_GIT_EXIT:-0}"
      ;;
  esac
done
exit 0
`)

	t.Setenv("FAKE_GIT_TAG", "FIRST-ATTEMPT")
	t.Setenv("FAKE_GIT_EXIT", "3")
	first := gitCommit.GitPush(context.Background(), "origin", PUSH, "master")
	if first.ExitCode() != 3 {
		t.Fatalf("first push ExitCode() = %d, want 3", first.ExitCode())
	}

	t.Setenv("FAKE_GIT_TAG", "SECOND-ATTEMPT")
	t.Setenv("FAKE_GIT_EXIT", "0")
	t.Setenv("FAKE_GIT_SLOW", "1")
	done := make(chan GitPushResult, 1)
	go func() {
		done <- gitCommit.GitPush(context.Background(), "origin", PUSH, "master")
	}()

	// While the second push is in flight, the stored result must no longer
	// carry the first attempt's command, status, or output.
	deadline := time.Now().Add(2 * time.Second)
	cleared := false
	for time.Now().Before(deadline) {
		stored := gitCommit.GitPushResult()
		if len(stored.Argv()) == 0 && stored.ExitCode() == -1 && !stored.Started() && string(stored.Stderr()) == "" {
			cleared = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !cleared {
		t.Fatal("the second push did not clear the first attempt's stored result")
	}

	select {
	case second := <-done:
		if !second.Success() {
			t.Fatalf("second push did not succeed: exit %d, err %v", second.ExitCode(), second.Err())
		}
		if !strings.Contains(string(second.Stderr()), "SECOND-ATTEMPT") || strings.Contains(string(second.Stderr()), "FIRST-ATTEMPT") {
			t.Errorf("second push stderr = %q, want only the second attempt's output", string(second.Stderr()))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the second push did not return")
	}
}

func TestGitPushStartFailureReturnsATypedResult(t *testing.T) {
	gitCommit, _, gittiLogging, _, fakeGit := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
exit 0
`)

	// The unexecutable fake makes the process fail to start: a setup failure
	// with its own typed result and log entry rather than a sentinel. PATH is
	// restricted to the fake's directory so LookPath has no real git to fall
	// back to.
	if err := os.Chmod(fakeGit, 0); err != nil {
		t.Fatalf("chmod the fake git: %v", err)
	}
	t.Setenv("PATH", filepath.Dir(fakeGit))

	result := gitCommit.GitPush(context.Background(), "origin", PUSH, "master")

	if result.Started() {
		t.Error("the process could not start, so Started() must be false")
	}
	if result.ExitCode() != -1 {
		t.Errorf("ExitCode() = %d, want -1 for the absent process status", result.ExitCode())
	}
	if result.Err() == nil {
		t.Error("Err() = nil, want the start error so the popup can act on it")
	}
	if result.Success() {
		t.Error("a start failure must not be reported as a success")
	}
	var startErrorLogged bool
	for _, entry := range gittiLogging.GetFullLogs() {
		if entry.OpsSeverityLevel == logging.ERROR && strings.Contains(entry.OpsDescription, "START ERROR") {
			startErrorLogged = true
		}
	}
	if !startErrorLogged {
		t.Error("a start failure left no START ERROR log entry")
	}
}

func TestGitPushRetainsCompleteOutputWhenTheReaderRunsAfterTheChildExits(t *testing.T) {
	doneMarker := "git-push-done"
	gitCommit, _, gittiLogging, root, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    push)
      echo "complete-output"
      touch "$FAKE_GIT_DONE"
      ;;
  esac
done
exit 0
`)
	t.Setenv("FAKE_GIT_DONE", filepath.Join(root, doneMarker))

	// Hold both stream readers before their first read so the child can
	// exit while the output still sits unread in the pipe buffer; the
	// retained stdout must still be complete when the readers run.
	release := make(chan struct{})
	gitCommit.gitPushDrainPreRead = func() { <-release }

	done := make(chan GitPushResult, 1)
	go func() {
		done <- gitCommit.GitPush(context.Background(), "origin", PUSH, "master")
	}()

	// Release the readers only after the child has provably exited.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(root, doneMarker)); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(root, doneMarker)); err != nil {
		t.Fatalf("the scripted push never exited: %v", err)
	}
	close(release)

	select {
	case result := <-done:
		if !result.Success() {
			t.Fatalf("the push did not succeed: exit %d, cancelled %v, err %v", result.ExitCode(), result.Cancelled(), result.Err())
		}
		if !strings.Contains(string(result.Stdout()), "complete-output") {
			t.Errorf("retained stdout = %q, want the complete output the child wrote before exiting", string(result.Stdout()))
		}
		for _, entry := range gittiLogging.GetFullLogs() {
			if entry.OpsSeverityLevel == logging.ERROR && strings.Contains(entry.OpsDescription, "READ ERROR") {
				t.Errorf("a spurious read error was recorded: %q", entry.OpsDescription)
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("GitPush did not return after the readers were released")
	}
}

func TestGitPushPreCancelledContextIsReportedAsCancellation(t *testing.T) {
	gitCommit, _, gittiLogging, _, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
exit 0
`)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := gitCommit.GitPush(ctx, "origin", PUSH, "master")

	if result.Started() {
		t.Error("the process could not start, so Started() must be false")
	}
	if result.ExitCode() != -1 {
		t.Errorf("ExitCode() = %d, want -1 for the absent process status", result.ExitCode())
	}
	if !result.Cancelled() {
		t.Error("a pre-start cancellation must be reported as cancelled, not as a setup failure")
	}
	if !errors.Is(result.Err(), context.Canceled) {
		t.Errorf("Err() = %v, want the context cancellation", result.Err())
	}
	if result.Success() {
		t.Error("a cancelled push must not be reported as a success")
	}
	var cancelledLogged, startErrorLogged bool
	for _, entry := range gittiLogging.GetFullLogs() {
		if entry.OpsSeverityLevel == logging.WARN && strings.Contains(entry.OpsDescription, "CANCELLED") {
			cancelledLogged = true
		}
		if strings.Contains(entry.OpsDescription, "START ERROR") {
			startErrorLogged = true
		}
	}
	if !cancelledLogged {
		t.Error("a pre-start cancellation left no CANCELLED log entry")
	}
	if startErrorLogged {
		t.Error("a pre-start cancellation was misreported as a START ERROR")
	}
}

func TestGitPushNonZeroExitWithStreamReadFailureRetainsBothErrors(t *testing.T) {
	// A rejected push whose stderr also cannot be read to completion must
	// keep the exit error and the stream read failure together.
	gitCommit, _, gittiLogging, _, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    push)
      { head -c 100000 /dev/zero | tr '\0' 'x'; } >&2
      echo "error: failed to push some refs"
      exit 1
      ;;
  esac
done
exit 0
`)

	result := gitCommit.GitPush(context.Background(), "origin", PUSH, "master")

	if result.Started() != true {
		t.Error("the process started, so Started() must be true")
	}
	if result.ExitCode() != 1 {
		t.Errorf("ExitCode() = %d, want the exact nonzero exit code 1", result.ExitCode())
	}
	if result.Success() {
		t.Error("a nonzero exit must not be reported as a success")
	}
	if result.Err() == nil {
		t.Fatal("Err() = nil, want both the exit error and the stream read failure")
	}
	if !errors.Is(result.Err(), bufio.ErrTooLong) {
		t.Errorf("Err() = %v, want the stream read failure retained alongside the exit error", result.Err())
	}
	var waitErrorLogged, readErrorLogged bool
	for _, entry := range gittiLogging.GetFullLogs() {
		if entry.OpsSeverityLevel != logging.ERROR {
			continue
		}
		if strings.Contains(entry.OpsDescription, logging.GIT_PUSH_OPS+" ERROR") {
			waitErrorLogged = true
		}
		if strings.Contains(entry.OpsDescription, "READ ERROR") {
			readErrorLogged = true
		}
	}
	if !waitErrorLogged {
		t.Error("the nonzero exit left no process error log entry")
	}
	if !readErrorLogged {
		t.Error("the stream read failure was hidden by the nonzero exit")
	}
}

func TestGitPushRecordsReadFailuresOnBothStreams(t *testing.T) {
	// When neither stream can be read to completion, both failures are
	// recorded stream-qualified and neither masks the other.
	gitCommit, _, gittiLogging, _, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    push)
      head -c 100000 /dev/zero | tr '\0' 'x'
      { head -c 100000 /dev/zero | tr '\0' 'y'; } >&2
      ;;
  esac
done
exit 0
`)

	result := gitCommit.GitPush(context.Background(), "origin", PUSH, "master")

	if result.ExitCode() != 0 {
		t.Errorf("ExitCode() = %d, want the exact 0 the process reported", result.ExitCode())
	}
	if result.Err() == nil {
		t.Fatal("Err() = nil, want the read failures on both streams")
	}
	if !errors.Is(result.Err(), bufio.ErrTooLong) {
		t.Errorf("Err() = %v, want the scanner read failure", result.Err())
	}
	if got := len(result.Stdout()); got != 100000 {
		t.Errorf("retained stdout = %d bytes, want the complete 100000-byte stream after the scanner failure", got)
	}
	if got := len(result.Stderr()); got != 100000 {
		t.Errorf("retained stderr = %d bytes, want the complete 100000-byte stream after the scanner failure", got)
	}
	var stdoutLogged, stderrLogged bool
	for _, entry := range gittiLogging.GetFullLogs() {
		if entry.OpsSeverityLevel != logging.ERROR || !strings.Contains(entry.OpsDescription, "READ ERROR") {
			continue
		}
		if strings.Contains(entry.OpsDescription, "stdout stream") {
			stdoutLogged = true
		}
		if strings.Contains(entry.OpsDescription, "stderr stream") {
			stderrLogged = true
		}
	}
	if !stdoutLogged || !stderrLogged {
		t.Errorf("both stream read failures must be logged stream-qualified; stdout %v, stderr %v", stdoutLogged, stderrLogged)
	}
}
