package services

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gohyuhan/gitti/api"
	gitapi "github.com/gohyuhan/gitti/api/git"
	"github.com/gohyuhan/gitti/executor"
	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/logging"
	"github.com/gohyuhan/gitti/settings"
	"github.com/gohyuhan/gitti/tui/constant"
	pushPopUp "github.com/gohyuhan/gitti/tui/popup/push"
	"github.com/gohyuhan/gitti/tui/types"
)

// ------------------------------------
//
//	fakeServiceGitScript stands in for git so the push service tests run
//	deterministically without a repository. The push subcommand honours the
//	exit/tag/hold environment switches, and the branch pass can be held in
//	flight through the FAKE_BRANCH_HOLD_FILE while the fake answers every
//	passive read the daemon's reconciliation passes make.
//
// ------------------------------------
const fakeServiceGitScript = `#!/bin/sh
if [ -n "$FAKE_GIT_CALL_LOG" ]; then
  for a in "$@"; do
    case "$a" in
      for-each-ref|rev-parse|rev-list|fetch|push)
        echo "$a" >> "$FAKE_GIT_CALL_LOG"
        break
        ;;
    esac
  done
fi
for a in "$@"; do
  case "$a" in
    push)
      while [ -f "$FAKE_PUSH_HOLD_FILE" ]; do sleep 0.02; done
      if [ -n "$FAKE_PUSH_TAG" ]; then echo "$FAKE_PUSH_TAG" >&2; fi
      exit "${FAKE_PUSH_EXIT:-0}"
      ;;
    for-each-ref)
      while [ -f "$FAKE_BRANCH_HOLD_FILE" ]; do sleep 0.02; done
      printf 'master\0001\0000\n'
      exit 0
      ;;
    symbolic-ref)
      echo "refs/heads/master"
      exit 0
      ;;
    rev-parse)
      echo "origin/master"
      exit 0
      ;;
    rev-list)
      echo "0 0"
      exit 0
      ;;
    branch)
      echo "  origin/master"
      exit 0
      ;;
    remote)
      echo "https://github.com/example/repo.git"
      exit 0
      ;;
    log)
      exit 0
      ;;
  esac
done
exit 0
`

// ------------------------------------
//
//	serviceHarness wires the fake git, a real GitDaemon, and a push-ready
//	model against the service under test
//
// ------------------------------------
type serviceHarness struct {
	model         *types.GittiModel
	daemonEvents  chan string
	callLog       string
	branchHoldDir string
	pushHoldFile  string
}

// ------------------------------------
//
//	serviceUnderTest builds the harness. All daemon- and process-level globals
//	are restored on cleanup.
//
// ------------------------------------
func serviceUnderTest(t *testing.T) *serviceHarness {
	t.Helper()

	originalSettings := settings.GITTICONFIGSETTINGS
	cfg := settings.GittiDefaultConfigSettings
	settings.GITTICONFIGSETTINGS = &cfg
	t.Cleanup(func() { settings.GITTICONFIGSETTINGS = originalSettings })

	i18n.InitGittiLanguageMapping("en")

	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(fakeServiceGitScript), 0o755); err != nil {
		t.Fatalf("writing the fake git: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	callLog := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("FAKE_GIT_CALL_LOG", callLog)
	branchHoldDir := t.TempDir()
	t.Setenv("FAKE_BRANCH_HOLD_FILE", filepath.Join(branchHoldDir, "hold"))
	pushHoldDir := t.TempDir()
	pushHoldFile := filepath.Join(pushHoldDir, "hold")
	t.Setenv("FAKE_PUSH_HOLD_FILE", pushHoldFile)

	originalExecutor := executor.GittiCmdExecutor
	t.Cleanup(func() { executor.GittiCmdExecutor = originalExecutor })
	executor.InitCmdExecutor(t.TempDir())

	gittiLogging := logging.InitGittiLogging(4096, make(chan string, 4096), 3)
	daemonEvents := make(chan string, 4096)

	gitOps := api.InitGitOperations(t.TempDir(), t.TempDir(), make(chan string, 64), gittiLogging)
	previousDaemon := api.GITDAEMON
	api.InitGitDaemon(t.TempDir(), daemonEvents, gitOps, false, make(chan string, 16), gittiLogging)
	t.Cleanup(func() {
		// let the daemon quiesce before the executor and settings globals are
		// restored, so a late pass cannot exec against the next test's setup
		api.GITDAEMON.WaitStatePassesIdle(5 * time.Second)
		api.GITDAEMON.Stop()
		api.GITDAEMON = previousDaemon
	})

	model := &types.GittiModel{
		Width:            100,
		GitOperations:    gitOps,
		CheckOutBranch:   "master",
		GittiLogger:      gittiLogging,
		TuiUpdateChannel: make(chan interface{}, 16),
	}
	pushPopUp.InitGitRemotePushPopUpModel(model)

	return &serviceHarness{model: model, daemonEvents: daemonEvents, callLog: callLog, branchHoldDir: branchHoldDir, pushHoldFile: pushHoldFile}
}

// ------------------------------------
//
//	readPushResultEvent blocks until the push result event arrives or fails
//	the test at the deadline
//
// ------------------------------------
func readPushResultEvent(t *testing.T, model *types.GittiModel, timeout time.Duration) types.GitPushResultEventDataStructure {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case msg := <-model.TuiUpdateChannel:
			updated, ok := msg.(types.GittiTuiUpdateMsg)
			if !ok {
				continue
			}
			if updated.Event != constant.GIT_PUSH_RESULT_EVENT {
				t.Fatalf("unexpected event %s before the push result", updated.Event)
			}
			return updated.Data.(types.GitPushResultEventDataStructure)
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	t.Fatalf("no push result event within %v", timeout)
	return types.GitPushResultEventDataStructure{}
}

// ------------------------------------
//
//	assertNoPushResultEvent fails if a push result event arrives within the
//	window
//
// ------------------------------------
func assertNoPushResultEvent(t *testing.T, model *types.GittiModel, window time.Duration) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		select {
		case msg := <-model.TuiUpdateChannel:
			updated, ok := msg.(types.GittiTuiUpdateMsg)
			if ok && updated.Event == constant.GIT_PUSH_RESULT_EVENT {
				t.Fatal("a push result event arrived that the test expected to be delayed")
			}
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ------------------------------------
//
//	TestSuccessfulBackgroundPushHoldsItsFinalSuccessUntilTheTicketCompletes
//	covers the zero-status background push contract: the popup enters the
//	visible reconciliation stage, no final success is published while the
//	post-push refresh ticket is in flight, and the success event carries the
//	refresh outcome once the ticket completes.
//
// ------------------------------------
func TestSuccessfulBackgroundPushHoldsItsFinalSuccessUntilTheTicketCompletes(t *testing.T) {
	h := serviceUnderTest(t)

	// hold the branch pass the ticket triggers so the ticket stays open
	holdFile := filepath.Join(h.branchHoldDir, "hold")
	if err := os.WriteFile(holdFile, nil, 0o644); err != nil {
		t.Fatalf("installing the branch pass hold: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(holdFile) })

	GitRemotePushService(h.model, "origin", gitapi.PUSH)

	popUp := h.model.PopUpModel.(*pushPopUp.GitRemotePushPopUpModel)
	waitFor(t, 10*time.Second, popUp.IsReconciling.Load, "the popup to enter the reconciliation stage")

	// while the ticket is open the final success must not be published
	assertNoPushResultEvent(t, h.model, 250*time.Millisecond)

	view := pushPopUp.RenderGitRemotePushPopUp(h.model)
	if !strings.Contains(view, i18n.LANGUAGEMAPPING.GitPushPopUpReconciling) {
		t.Errorf("the reconciliation stage popup =\n%s\nmissing the stage text", view)
	}

	// releasing the held pass lets the ticket complete
	if err := os.Remove(holdFile); err != nil {
		t.Fatalf("releasing the branch pass hold: %v", err)
	}
	data := readPushResultEvent(t, h.model, 10*time.Second)
	if !data.Success {
		t.Error("the final event did not report the successful push")
	}
	if data.Refresh == nil {
		t.Fatal("the final success event did not carry the refresh outcome")
	}
	if !data.Refresh.Refreshed {
		t.Errorf("the refresh outcome reported a failure: %s", data.Refresh.FailureSummary())
	}
	// dispatch the event to the popup the way the TUI update loop does
	pushPopUp.UpdateGitPushResultEvent(h.model, data)
	if !popUp.ProcessSuccess.Load() || popUp.HasError.Load() {
		t.Errorf("popup flags = success %v, error %v, want a successful push", popUp.ProcessSuccess.Load(), popUp.HasError.Load())
	}
	if popUp.IsReconciling.Load() {
		t.Error("the popup is still in the reconciliation stage after the final result")
	}
}

// ------------------------------------
//
//	TestFailedPushPublishesWithoutRequestingATicket covers a nonzero push:
//	the final result is published immediately and the success-only
//	reconciliation ticket is never requested, so no state pass runs.
//
// ------------------------------------
func TestFailedPushPublishesWithoutRequestingATicket(t *testing.T) {
	h := serviceUnderTest(t)
	t.Setenv("FAKE_PUSH_EXIT", "1")

	GitRemotePushService(h.model, "origin", gitapi.PUSH)

	data := readPushResultEvent(t, h.model, 10*time.Second)
	if data.Success {
		t.Error("a failed push published a success")
	}
	if data.Refresh != nil {
		t.Error("a failed push requested the success-only refresh ticket")
	}

	// give any (incorrectly) requested pass a beat to run, then check the
	// daemon performed none of the reconciliation reads
	time.Sleep(300 * time.Millisecond)
	logData, err := os.ReadFile(h.callLog)
	if err != nil {
		t.Fatalf("reading the fake git call log: %v", err)
	}
	if strings.Contains(string(logData), "for-each-ref") {
		t.Error("a failed push triggered a branch state pass")
	}
	if strings.Contains(string(logData), "fetch") {
		t.Error("a failed push triggered a fetch")
	}
}

// ------------------------------------
//
//	TestCancelledPushPublishesWithoutRequestingATicket covers a push the user
//	cancels: the cancelled result is published without the success-only
//	ticket.
//
// ------------------------------------
func TestCancelledPushPublishesWithoutRequestingATicket(t *testing.T) {
	h := serviceUnderTest(t)

	// hold the push process in flight
	if err := os.WriteFile(h.pushHoldFile, nil, 0o644); err != nil {
		t.Fatalf("installing the push hold: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(h.pushHoldFile) })

	GitRemotePushService(h.model, "origin", gitapi.PUSH)

	// let the push start, then cancel it like the user would
	time.Sleep(200 * time.Millisecond)
	GitRemotePushCancelService(h.model)

	data := readPushResultEvent(t, h.model, 10*time.Second)
	if data.Success {
		t.Error("a cancelled push published a success")
	}
	if !data.Result.Cancelled() {
		t.Error("the cancelled push did not report cancellation")
	}
	if data.Refresh != nil {
		t.Error("a cancelled push requested the success-only refresh ticket")
	}
}

// ------------------------------------
//
//	TestSignedPushCompletionRequestsReconciliationOnlyOnSuccess covers the
//	terminal-interactive signing route: a successful signing push completion
//	requests the same post-push reconciliation, while a failed one and a
//	different signing operation do not.
//
// ------------------------------------
func TestSignedPushCompletionRequestsReconciliationOnlyOnSuccess(t *testing.T) {
	h := serviceUnderTest(t)

	successMsg := types.GitOperationRequiredSigningFinishedMsg{
		GitOperationOpsTypeForLogging: logging.GIT_PUSH_WITH_SIGNING_OPS,
		Err:                           nil,
	}
	RequestPostPushRefreshForSignedPush(h.model, successMsg)

	// the ticket's passes run against the fake git and emit their update
	// events once each target pass has published
	waitFor(t, 10*time.Second, func() bool {
		return daemonEventArrived(h.daemonEvents, gitapi.GIT_BRANCH_UPDATE, gitapi.GIT_REMOTE_SYNC_STATUS_AND_UPSTREAM_UPDATE, gitapi.GIT_COMMITLOG_UPDATE)
	}, "a state pass from the signing push reconciliation")
}

// ------------------------------------
//
//	daemonEventArrived drains the daemon event channel and reports whether
//	any of the wanted events is present, without blocking
//
// ------------------------------------
func daemonEventArrived(ch chan string, wanted ...string) bool {
	for {
		select {
		case got := <-ch:
			for _, w := range wanted {
				if got == w {
					return true
				}
			}
		default:
			return false
		}
	}
}

// ------------------------------------
//
//	TestSignedPushCompletionDoesNotRequestReconciliationOnFailureOrOtherOps
//
// ------------------------------------
func TestSignedPushCompletionDoesNotRequestReconciliationOnFailureOrOtherOps(t *testing.T) {
	h := serviceUnderTest(t)

	RequestPostPushRefreshForSignedPush(h.model, types.GitOperationRequiredSigningFinishedMsg{
		GitOperationOpsTypeForLogging: logging.GIT_PUSH_WITH_SIGNING_OPS,
		Err:                           errors.New("gpg failed"),
	})
	RequestPostPushRefreshForSignedPush(h.model, types.GitOperationRequiredSigningFinishedMsg{
		GitOperationOpsTypeForLogging: logging.COMMIT_WITH_SIGNING_OPS,
		Err:                           nil,
	})

	// no state pass may run for either message
	for i := 0; i < 20; i++ {
		select {
		case got := <-h.daemonEvents:
			t.Fatalf("an unrelated signing completion triggered a state pass event %s", got)
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
}

// ------------------------------------
//
//	TestLinkedWorktreeFastForwardPushReconcilesToZeroAheadBehind is the
//	integration fixture: a bare local remote and a linked worktree on an
//	existing tracked feature branch four fast-forward commits ahead. A normal
//	push moves the remote and remote-tracking refs to HEAD, the reconciliation
//	reaches zero ahead/behind, and the Commit Log decorations place the local
//	branch and the remote on the same commit.
//
// ------------------------------------
func TestLinkedWorktreeFastForwardPushReconcilesToZeroAheadBehind(t *testing.T) {
	// The fixture commands and the application's executor must inherit an
	// isolated Git configuration: a developer's global commit.gpgsign,
	// push.gpgSign, or core.hooksPath would fail the commits or pushes,
	// prompt interactively, or run unrelated hooks. The deterministic
	// author/committer identity comes from the same environment.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Gitti Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "gitti@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Gitti Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "gitti@example.com")

	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	main := filepath.Join(root, "main")
	worktree := filepath.Join(root, "worktree")

	runGit(t, root, "init", "--bare", "--initial-branch=master", bare)
	runGit(t, root, "clone", "--quiet", bare, main)
	runGit(t, main, "commit", "--allow-empty", "-q", "-m", "base")
	runGit(t, main, "push", "-q", "-u", "origin", "master")
	runGit(t, main, "checkout", "-q", "-b", "feature/x")
	runGit(t, main, "push", "-q", "-u", "origin", "feature/x")
	runGit(t, main, "checkout", "-q", "master")
	runGit(t, main, "worktree", "add", "-q", worktree, "feature/x")
	for i := 1; i <= 4; i++ {
		runGit(t, worktree, "commit", "--allow-empty", "-q", "-m", fmt.Sprintf("fast-forward %d", i))
	}
	// the branch is now four commits ahead of its upstream
	if ahead := runGit(t, worktree, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"); ahead != "4\t0" {
		t.Fatalf("fixture ahead/behind = %q, want 4 ahead 0 behind", ahead)
	}

	originalSettings := settings.GITTICONFIGSETTINGS
	cfg := settings.GittiDefaultConfigSettings
	settings.GITTICONFIGSETTINGS = &cfg
	t.Cleanup(func() { settings.GITTICONFIGSETTINGS = originalSettings })
	i18n.InitGittiLanguageMapping("en")

	originalExecutor := executor.GittiCmdExecutor
	t.Cleanup(func() { executor.GittiCmdExecutor = originalExecutor })
	executor.InitCmdExecutor(worktree)

	gittiLogging := logging.InitGittiLogging(4096, make(chan string, 4096), 3)
	gitOps := api.InitGitOperations(runGit(t, worktree, "rev-parse", "--absolute-git-dir"), worktree, make(chan string, 64), gittiLogging)
	daemonEvents := make(chan string, 4096)
	previousDaemon := api.GITDAEMON
	api.InitGitDaemon(filepath.Join(main, ".git"), daemonEvents, gitOps, false, make(chan string, 16), gittiLogging)
	t.Cleanup(func() {
		api.GITDAEMON.WaitStatePassesIdle(5 * time.Second)
		api.GITDAEMON.Stop()
		api.GITDAEMON = previousDaemon
	})

	model := &types.GittiModel{
		Width:            100,
		GitOperations:    gitOps,
		CheckOutBranch:   "feature/x",
		GittiLogger:      gittiLogging,
		TuiUpdateChannel: make(chan interface{}, 16),
	}
	pushPopUp.InitGitRemotePushPopUpModel(model)

	GitRemotePushService(model, "origin", gitapi.PUSH)
	data := readPushResultEvent(t, model, 30*time.Second)

	if !data.Success || !data.Result.Success() {
		t.Fatalf("the push did not succeed: exit %d, err %v", data.Result.ExitCode(), data.Result.Err())
	}
	if data.Result.WorkingDirectory() != worktree {
		t.Errorf("the push ran in %q, want the linked worktree %q", data.Result.WorkingDirectory(), worktree)
	}
	for _, argument := range data.Result.Argv() {
		if argument == "--force" || argument == "--force-with-lease" || argument == "-u" {
			t.Errorf("the normal push argv carries %q: %v", argument, data.Result.Argv())
		}
	}

	// the push moved the remote ref and the remote-tracking ref to HEAD
	head := runGit(t, worktree, "rev-parse", "HEAD")
	if got := runGit(t, bare, "rev-parse", "refs/heads/feature/x"); got != head {
		t.Errorf("the remote branch = %s, want the pushed tip %s", got, head)
	}
	if got := runGit(t, worktree, "rev-parse", "refs/remotes/origin/feature/x"); got != head {
		t.Errorf("the remote-tracking ref = %s, want the pushed tip %s", got, head)
	}

	if data.Refresh == nil {
		t.Fatal("the successful push did not carry the refresh outcome")
	}
	if !data.Refresh.Refreshed {
		t.Fatalf("the reconciliation failed: %s", data.Refresh.FailureSummary())
	}

	// the reconciled state: zero ahead/behind, current checkout and upstream
	if status := gitOps.GitRemote.RemoteSyncStatus(); status != (gitapi.RemoteSyncStatus{Local: "0", Remote: "0"}) {
		t.Errorf("reconciled sync status = %+v, want 0 0", status)
	}
	if upstream := gitOps.GitRemote.CurrentBranchUpStream(); upstream != "origin/feature/x" {
		t.Errorf("reconciled upstream = %q, want origin/feature/x", upstream)
	}
	if current := gitOps.GitBranch.CurrentCheckOut(); current.BranchName != "feature/x" || !current.IsCheckedOut {
		t.Errorf("reconciled checkout = %+v, want checked-out feature/x", current)
	}

	// the Commit Log decorations place the local branch and the remote on the
	// same (pushed tip) commit
	commitLogs := gitOps.GitCommitLog.GitCommitLogOutput()
	if len(commitLogs) == 0 {
		t.Fatal("the reconciled commit log is empty")
	}
	tip := commitLogs[0]
	if tip.Hash != head {
		t.Errorf("the commit log tip = %s, want the pushed tip %s", tip.Hash, head)
	}
	if !strings.Contains(tip.Refs, "feature/x") || !strings.Contains(tip.Refs, "origin/feature/x") {
		t.Errorf("the commit log tip decorations = %q, want both feature/x and origin/feature/x", tip.Refs)
	}

	// the reconciliation emitted all three update events. The ticket only
	// completed after every target pass emitted its event, so all three are
	// already in the channel once the result event was read
	missing := map[string]struct{}{
		gitapi.GIT_BRANCH_UPDATE:                          {},
		gitapi.GIT_REMOTE_SYNC_STATUS_AND_UPSTREAM_UPDATE: {},
		gitapi.GIT_COMMITLOG_UPDATE:                       {},
	}
drain:
	for len(missing) > 0 {
		select {
		case event := <-daemonEvents:
			delete(missing, event)
		default:
			break drain
		}
	}
	for event := range missing {
		t.Errorf("the reconciliation did not emit %s", event)
	}
}

// ------------------------------------
//
//	runGit runs a real git command in dir and returns its trimmed output
//
// ------------------------------------
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}
