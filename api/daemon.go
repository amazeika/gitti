package api

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/fsnotify/fsnotify"

	"github.com/gohyuhan/gitti/api/git"
	"github.com/gohyuhan/gitti/logging"
	"github.com/gohyuhan/gitti/settings"
)

type GitDaemon struct {
	mainGitRepoPath                     string
	watcher                             *fsnotify.Watcher
	debounceDur                         time.Duration
	gitFilesActiveRefreshDur            time.Duration
	gitRemoteSyncStatusActiveRefreshDur time.Duration
	isGitFilesPassiveActiveRunning      atomic.Bool
	isGitRefLogPassiveRunning           atomic.Bool
	isGitStashPassiveRunning            atomic.Bool
	isGitTagPassiveRunning              atomic.Bool
	isGitRemotePassiveRunning           atomic.Bool
	isGitWorktreePassiveRunning         atomic.Bool
	isGitFetchRunning                   atomic.Bool
	// branchStateDomain, remoteUpstreamStateDomain, and commitLogStateDomain
	// are the three generation-aware state refresh domains affected by push;
	// every other daemon domain keeps its existing passive running flag
	branchStateDomain              stateRefreshDomain
	remoteUpstreamStateDomain      stateRefreshDomain
	commitLogStateDomain           stateRefreshDomain
	watcherTimer                   *time.Timer
	gitFilesActiveTimer            *time.Timer
	gitRemoteSyncStatusActiveTimer *time.Timer
	stopChannel                    chan struct{}
	stopOnce                       sync.Once
	errorLog                       []error
	updateChannel                  chan string // to communicate back to main thread for an update event
	daemonReceiverChannel          chan string // this is used to receive signal from main thread by the daemon
	gitOperations                  atomic.Pointer[GitOperations]
	allowCommitGraphWrite          bool
	gittiLogger                    *logging.GittiLogging
	postPushTicketsMu              sync.Mutex
	postPushTickets                []*PostPushRefreshTicket
	// stateBranchPassPreRead, stateRemoteUpstreamPassPreRead, and
	// stateCommitLogPassPreRead are invoked by the daemon worker just before
	// running each passive state pass, and stateFetchPreRead just before a
	// network fetch starts; tests use them to hold a pass or fetch in flight.
	// They are atomic because a test may install one while a worker from an
	// earlier request is already looping.
	stateBranchPassPreRead         atomic.Pointer[func()]
	stateRemoteUpstreamPassPreRead atomic.Pointer[func()]
	stateCommitLogPassPreRead      atomic.Pointer[func()]
	stateFetchPreRead              atomic.Pointer[func()]
}

var GITDAEMON *GitDaemon

// ------------------------------------
//
//	Initialize the file system watcher daemon for monitoring git repository changes
//
// ------------------------------------
func InitGitDaemon(absoluteMainGitPath string, updateChannel chan string, gitOperations *GitOperations, allowCommitGraphWrite bool, daemonReceiverChannel chan string, gittiLogger *logging.GittiLogging) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		// Log the critical error - this means file watching won't work
		// Set a nil daemon to prevent crashes when accessed
		fmt.Printf("CRITICAL: Failed to initialize Git file watcher: %v\n", err)
		GITDAEMON = nil
		os.Exit(1)
	}

	debounce := time.Duration(settings.GITTICONFIGSETTINGS.FileWatcherDebounceMS) * time.Millisecond
	gitFilesActiveRefreshDur := time.Duration(settings.GITTICONFIGSETTINGS.GitFilesActiveRefreshDurationMS) * time.Millisecond
	gitRemoteSyncStatusActiveRefreshDur := time.Duration(settings.GITTICONFIGSETTINGS.GitRemoteSyncStatusDurationMS) * time.Millisecond
	gd := &GitDaemon{
		mainGitRepoPath:                     absoluteMainGitPath,
		watcher:                             w,
		debounceDur:                         debounce,
		gitFilesActiveRefreshDur:            gitFilesActiveRefreshDur,
		gitRemoteSyncStatusActiveRefreshDur: gitRemoteSyncStatusActiveRefreshDur,
		watcherTimer:                        time.NewTimer(debounce), // milliseconds
		gitFilesActiveTimer:                 time.NewTimer(gitFilesActiveRefreshDur),
		gitRemoteSyncStatusActiveTimer:      time.NewTimer(gitRemoteSyncStatusActiveRefreshDur),
		stopChannel:                         make(chan struct{}),
		errorLog:                            make([]error, 0),
		updateChannel:                       updateChannel,
		daemonReceiverChannel:               daemonReceiverChannel,
		allowCommitGraphWrite:               allowCommitGraphWrite,
		gittiLogger:                         gittiLogger,
	}
	gd.branchStateDomain.name = statePassDomainBranch
	gd.remoteUpstreamStateDomain.name = statePassDomainRemoteUpstream
	gd.commitLogStateDomain.name = statePassDomainCommitLog
	gd.gitOperations.Store(gitOperations)
	gd.isGitFilesPassiveActiveRunning.Store(false)
	gd.isGitFetchRunning.Store(false)
	gd.isGitRefLogPassiveRunning.Store(false)
	gd.isGitStashPassiveRunning.Store(false)
	gd.isGitTagPassiveRunning.Store(false)
	gd.isGitRemotePassiveRunning.Store(false)
	gd.watcherTimer.Stop()
	gd.gitFilesActiveTimer.Stop()
	gd.gitRemoteSyncStatusActiveTimer.Stop()
	gd.watchPath()

	GITDAEMON = gd
}

// ------------------------------------
//
//	UpdateGitOperations swaps the daemon's GitOperations reference, used after a worktree switch when a
//	fresh GitOperations (with re-resolved paths) is rebuilt. Mirrors how the cmd
//	executor's repo dir is updated.
//
// ------------------------------------
func (gd *GitDaemon) UpdateGitOperations(gitOperations *GitOperations) {
	gd.gitOperations.Store(gitOperations)
}

// ------------------------------------
//
//	WaitStatePassesIdle blocks until no state pass worker or network fetch is
//	in flight, or until the timeout elapses, reporting whether the daemon
//	quiesced. It lets an embedded caller observe a settled daemon without
//	racing the workers itself.
//
// ------------------------------------
func (gd *GitDaemon) WaitStatePassesIdle(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !gd.isGitFetchRunning.Load() &&
			!gd.branchStateDomain.isActive() &&
			!gd.remoteUpstreamStateDomain.isActive() &&
			!gd.commitLogStateDomain.isActive() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

// ------------------------------------
//
//	Trigger one full latest-info fetch across all git operations (with remote fetch),
//	used to repopulate state immediately after a worktree switch.
//
// ------------------------------------
func (gd *GitDaemon) TriggerFullInfoFetch() {
	gd.gitLatestInfoFetch(true)
}

// ------------------------------------
//
//	Register the repo directory paths for file watching, skipping noisy/irrelevant
//	dirs (objects, hooks, lfs, rr-cache, lost-found)
//
// ------------------------------------
func (gd *GitDaemon) watchPath() {
	err := gd.watcher.Add(gd.mainGitRepoPath)
	if err != nil {
		gd.errorLog = append(gd.errorLog, err)
	}
	err = filepath.WalkDir(gd.mainGitRepoPath, func(path string, d fs.DirEntry, err error) error {
		if d.IsDir() {
			if gd.isSkippableGitDir(path) {
				return fs.SkipDir
			}
			gd.watcher.Add(path)
		}
		return nil
	})
	if err != nil {
		gd.errorLog = append(gd.errorLog, err)
	}
}

// ------------------------------------
//
//	Report whether path is a metadata dir directly under a git dir root (the
//	top-level .git or any submodule gitdir under .git/modules/...) that is
//	noisy/irrelevant to git state and should not be watched. The parent is
//	treated as a git dir root only when it contains a HEAD file, so branch/ref
//	namespaces like refs/heads/lfs/... stay watched while submodule object/pack
//	dirs are skipped.
//
// ------------------------------------
func (gd *GitDaemon) isSkippableGitDir(path string) bool {
	switch filepath.Base(path) {
	case "objects", "hooks", "lfs", "rr-cache", "lost-found":
	default:
		return false
	}
	parent := filepath.Dir(path)
	if _, err := os.Stat(filepath.Join(parent, "HEAD")); err == nil {
		return true
	}
	return false
}

// ------------------------------------
//
//	Start the daemon's event loop to listen for file changes and trigger git info refresh
//
// ------------------------------------
func (gd *GitDaemon) Start() {
	go func() {
		// Initial call to get info of git
		if gd.updateChannel != nil {
			gd.gitLatestInfoFetch(true)
		}
		gd.gitFilesActiveTimer.Reset(gd.gitFilesActiveRefreshDur)
		gd.gitRemoteSyncStatusActiveTimer.Reset(gd.gitRemoteSyncStatusActiveRefreshDur)

		// commit graph write once when started (if enabled)
		gd.commitGraphWriteOnce()

		// loop to stay active
		for {
			select {
			case event := <-gd.watcher.Events:
				if gd.isRelevantEvent(event) {
					gd.resetDebounce()
				}
			case err := <-gd.watcher.Errors:
				lipgloss.Println("Watcher error:", err)

			case <-gd.watcherTimer.C:
				gd.gitLatestInfoFetch(false)
			case <-gd.gitFilesActiveTimer.C:
				// reset first to avoid losing ticks, then run work in background
				gd.gitFilesActiveTimer.Reset(gd.gitFilesActiveRefreshDur)
				go func() {
					if gd.isGitFilesPassiveActiveRunning.CompareAndSwap(false, true) {
						// Mark as running
						defer gd.isGitFilesPassiveActiveRunning.Store(false)
						gitOps := gd.gitOperations.Load()
						gitOps.GitFiles.GetGitFilesStatus()
						gitOps.GitStateUniversalUtils.CheckCurrentGitState()
						gd.updateChannel <- git.GIT_FILES_STATUS_UPDATE
						gd.updateChannel <- git.GIT_STATE_UPDATE
					}
				}()
			case <-gd.gitRemoteSyncStatusActiveTimer.C:
				// reset immediately; git remote sync status operation
				gd.gitRemoteSyncStatusActiveTimer.Reset(gd.gitRemoteSyncStatusActiveRefreshDur)
				// the timer keeps its existing policy: a background (non-user)
				// network fetch plus a remote/upstream local state read; neither
				// can drop the other behind a busy guard
				gd.requestFetch(false)
				gd.requestStatePass(&gd.remoteUpstreamStateDomain)
			case signal := <-gd.daemonReceiverChannel:
				switch signal {
				case git.GIT_FETCH:
					// the manual fetch keeps its existing policy: one fetch in
					// flight at a time, a WARN when a user-triggered request is
					// skipped, and the fetch's completion requests its own
					// remote/upstream local state read
					gd.requestFetch(true)
				}
			case <-gd.stopChannel:
				gd.watcher.Close()
				return
			}
		}
	}()
}

// ------------------------------------
//
//	Reset the debounce timer to coalesce rapid file system events
//
// ------------------------------------
func (gd *GitDaemon) resetDebounce() {
	if !gd.watcherTimer.Stop() {
		select {
		case <-gd.watcherTimer.C:
		default:
		}
	}
	gd.watcherTimer.Reset(gd.debounceDur)
}

// ------------------------------------
//
//	Fetch latest git info concurrently across all operations (files, branches, remote, logs, stash, tags)
//
// ------------------------------------
func (gd *GitDaemon) gitLatestInfoFetch(needFetch bool) {
	go func() {
		if gd.isGitFilesPassiveActiveRunning.CompareAndSwap(false, true) {
			defer gd.isGitFilesPassiveActiveRunning.Store(false)
			gitOps := gd.gitOperations.Load()
			gitOps.GitFiles.GetGitFilesStatus()
			gitOps.GitStateUniversalUtils.CheckCurrentGitState()
			gd.updateChannel <- git.GIT_FILES_STATUS_UPDATE
			gd.updateChannel <- git.GIT_STATE_UPDATE
		}
	}()
	// the three push-affected state domains route through the generation-aware
	// coordinator so a request coalesced into an in-flight pass still gets a
	// pass that begins after it; the other domains keep their existing
	// passive running flags
	gd.requestStatePass(&gd.branchStateDomain)
	if needFetch {
		gd.requestFetch(false)
	}
	gd.requestStatePass(&gd.remoteUpstreamStateDomain)
	gd.requestStatePass(&gd.commitLogStateDomain)
	go func() {
		if gd.isGitRefLogPassiveRunning.CompareAndSwap(false, true) {
			defer gd.isGitRefLogPassiveRunning.Store(false)
			gd.gitOperations.Load().GitRefLog.GetLatestRefLog()
			gd.updateChannel <- git.GIT_REFLOG_UPDATE
		}
	}()
	go func() {
		if gd.isGitStashPassiveRunning.CompareAndSwap(false, true) {
			defer gd.isGitStashPassiveRunning.Store(false)
			gd.gitOperations.Load().GitStash.GetLatestStashInfo()
			gd.updateChannel <- git.GIT_STASH_UPDATE
		}
	}()
	go func() {
		if gd.isGitTagPassiveRunning.CompareAndSwap(false, true) {
			defer gd.isGitTagPassiveRunning.Store(false)
			gd.gitOperations.Load().GitTag.GetLatestGitTag()
			gd.updateChannel <- git.GIT_TAG_UPDATE
		}
	}()
	go func() {
		if gd.isGitRemotePassiveRunning.CompareAndSwap(false, true) {
			defer gd.isGitRemotePassiveRunning.Store(false)
			gd.gitOperations.Load().GitRemote.CheckRemoteExist(true)
			gd.updateChannel <- git.GIT_REMOTE_UPDATE
		}
	}()
	go func() {
		if gd.isGitWorktreePassiveRunning.CompareAndSwap(false, true) {
			defer gd.isGitWorktreePassiveRunning.Store(false)
			gd.gitOperations.Load().GitWorktree.GetLatestWorktreeInfos()
			gd.updateChannel <- git.GIT_WORKTREE_UPDATE
		}
	}()
}

// ------------------------------------
//
//	Check if file system event is relevant to git operations
//
// ------------------------------------
func (gd *GitDaemon) isRelevantEvent(event fsnotify.Event) bool {
	// Only watch .git subpaths
	if !strings.Contains(event.Name, filepath.Join(gd.mainGitRepoPath)) {
		return false
	}

	// Ignore lock and temp files that git touches rapidly
	base := filepath.Base(event.Name)
	if strings.HasSuffix(base, ".lock") || base == "FETCH_HEAD" {
		return false
	}

	// Handle new directories
	if event.Op&fsnotify.Create == fsnotify.Create {
		fi, err := os.Stat(event.Name)
		if err == nil && fi.IsDir() {
			filepath.WalkDir(event.Name, func(path string, d fs.DirEntry, err error) error {
				if err == nil && d.IsDir() {
					if gd.isSkippableGitDir(path) {
						return fs.SkipDir
					}
					_ = gd.watcher.Add(path)
				}
				return nil
			})
		}
		return true
	}

	// Trigger only for relevant ops
	if event.Op&(fsnotify.Write|fsnotify.Remove|fsnotify.Rename) != 0 {
		return true
	}

	return false
}

// ------------------------------------
//
//	Write commit graph once on daemon start to improve git log retrieval performance
//
// ------------------------------------
func (gd *GitDaemon) commitGraphWriteOnce() {
	go func() {
		if gd.allowCommitGraphWrite {
			gd.gitOperations.Load().GitCommitLog.WriteCommitGraph()
		}
	}()
}

// ------------------------------------
//
//	Stop the daemon and close the file watcher
//
// ------------------------------------
func (gd *GitDaemon) Stop() {
	gd.stopOnce.Do(func() {
		close(gd.stopChannel)
	})
}
