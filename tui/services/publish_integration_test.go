package services

import (
	"os"
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
	"github.com/gohyuhan/gitti/tui/types"
)

// ------------------------------------
//
//	TestPublishLocalOnlyBranchFromLinkedWorktreeThroughTheService runs the
//	Phase 2 publish flow end to end against real git.
//
//	The fixture creates a bare local remote and a linked worktree.
//	The worktree checks out a local-only branch with one commit.
//	The daemon observes the branch as unpublished before the push.
//
//	The confirmed route pushes through the worktree-bound executor.
//	The push carries --set-upstream with HEAD as the source.
//
//	After the push, the reconciliation reports the branch as tracked.
//	The ahead and behind counts are zero.
//	The commit log decorates the pushed tip with both refs.
//	The remote inventory carries the published remote with both URL sets.
//
// ------------------------------------
func TestPublishLocalOnlyBranchFromLinkedWorktreeThroughTheService(t *testing.T) {
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
	runGit(t, main, "commit", "--allow-empty", "-q", "-m", "feature work")
	runGit(t, main, "checkout", "-q", "master")
	runGit(t, main, "worktree", "add", "-q", worktree, "feature/x")

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
	api.GITDAEMON.Start()
	t.Cleanup(func() {
		api.GITDAEMON.WaitStatePassesIdle(5 * time.Second)
		api.GITDAEMON.Stop()
		api.GITDAEMON = previousDaemon
	})

	// before the publish, the daemon's fresh observation classifies the
	// local-only branch as unpublished with the branch identified
	waitFor(t, 30*time.Second, func() bool {
		observed := gitOps.GitRemote.RemoteSyncStatusAndUpstream()
		return observed.ObservationState == gitapi.UpstreamStateUnpublished && observed.ObservedBranch == "feature/x"
	}, "the initial observation to classify feature/x as unpublished")

	model := &types.GittiModel{
		Width:            100,
		GitOperations:    gitOps,
		CheckOutBranch:   "feature/x",
		GittiLogger:      gittiLogging,
		TuiUpdateChannel: make(chan interface{}, 16),
	}

	publishRoute := gitapi.GitPushRoute{
		RemoteName:  "origin",
		Branch:      "feature/x",
		Intent:      gitapi.PushIntentPublish,
		ActiveGuard: api.WorktreeGenerationGuard(gitOps),
	}
	InitGitRemotePushPopUpModelAndStartGitRemotePushService(model, publishRoute)
	data := readPushResultEvent(t, model, 30*time.Second)

	if !data.Success || !data.Result.Success() {
		t.Fatalf("the publish did not succeed: exit %d, err %v, stderr %s", data.Result.ExitCode(), data.Result.Err(), data.Result.Stderr())
	}
	if data.Result.WorkingDirectory() != worktree {
		t.Errorf("the publish ran in %q, want the linked worktree %q", data.Result.WorkingDirectory(), worktree)
	}
	// the confirmed route is the publish push: --set-upstream, HEAD as the
	// source, and no force or -u flag
	argv := data.Result.Argv()
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--set-upstream") {
		t.Errorf("the publish argv is missing --set-upstream: %v", argv)
	}
	if !strings.HasSuffix(joined, "origin HEAD") {
		t.Errorf("the publish argv = %v, want the confirmed form ending in \"origin HEAD\"", argv)
	}
	for _, argument := range argv {
		if argument == "--force" || argument == "--force-with-lease" || argument == "-u" {
			t.Errorf("the publish argv carries %q: %v", argument, argv)
		}
	}

	// the push moved the remote ref and the remote-tracking ref to HEAD and
	// configured the upstream for the branch
	head := runGit(t, worktree, "rev-parse", "HEAD")
	if got := runGit(t, bare, "rev-parse", "refs/heads/feature/x"); got != head {
		t.Errorf("the remote branch = %s, want the published tip %s", got, head)
	}
	if got := runGit(t, worktree, "rev-parse", "refs/remotes/origin/feature/x"); got != head {
		t.Errorf("the remote-tracking ref = %s, want the published tip %s", got, head)
	}
	if got := runGit(t, worktree, "rev-parse", "--abbrev-ref", "feature/x@{upstream}"); got != "origin/feature/x" {
		t.Errorf("the published upstream = %q, want origin/feature/x", got)
	}

	if data.Refresh == nil {
		t.Fatal("the successful publish did not carry the refresh outcome")
	}
	if !data.Refresh.Refreshed {
		t.Fatalf("the reconciliation failed: %s", data.Refresh.FailureSummary())
	}

	// the reconciliation's remote/upstream pass re-reads the configured
	// remote inventory: the published remote is present with both URL sets
	var originEntry gitapi.GitRemoteInventoryEntry
	originFound := false
	for _, entry := range gitOps.GitRemote.RemoteInventory().Entries() {
		if entry.Name == "origin" {
			originEntry = entry
			originFound = true
		}
	}
	if !originFound {
		t.Error("the reconciled remote inventory is missing the origin remote")
	} else if len(originEntry.FetchURLs) == 0 || len(originEntry.PushURLs) == 0 {
		t.Errorf("the origin inventory entry is missing a URL set: fetch %v, push %v", originEntry.FetchURLs, originEntry.PushURLs)
	}

	// the reconciled observation: tracked at zero ahead/behind, and the
	// Commit Log decorations place the local branch and the remote on the
	// same (pushed tip) commit
	waitFor(t, 30*time.Second, func() bool {
		observed := gitOps.GitRemote.RemoteSyncStatusAndUpstream()
		return observed.ObservationState == gitapi.UpstreamStateTracked &&
			observed.CurrentBranchUpStream == "origin/feature/x" &&
			observed.RemoteSyncStatus == (gitapi.RemoteSyncStatus{Local: "0", Remote: "0"})
	}, "the reconciled observation to report feature/x as tracked at 0 0")

	commitLogs := gitOps.GitCommitLog.GitCommitLogOutput()
	if len(commitLogs) == 0 {
		t.Fatal("the reconciled commit log is empty")
	}
	tip := commitLogs[0]
	if tip.Hash != head {
		t.Errorf("the commit log tip = %s, want the published tip %s", tip.Hash, head)
	}
	if !strings.Contains(tip.Refs, "feature/x") || !strings.Contains(tip.Refs, "origin/feature/x") {
		t.Errorf("the commit log tip decorations = %q, want both feature/x and origin/feature/x", tip.Refs)
	}
}
