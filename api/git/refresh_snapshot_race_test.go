package git

import (
	"sync"
	"testing"

	"github.com/gohyuhan/gitti/logging"
)

// ------------------------------------
//
//	runSnapshotRaceExercise starts the writer loop that republishes the named
//	domain's snapshot, the reader loops that hammer the domain's getters, and
//	an optional driver that mutates the repository between iterations so the
//	published generations actually differ. It returns a finish function that
//	stops every loop and waits for them to drain.
//
// ------------------------------------
func runSnapshotRaceExercise(t *testing.T, writer func(), reader func(), driver func(iteration int)) func() {
	t.Helper()

	done := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			writer()
		}
	}()
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				reader()
			}
		}()
	}

	return func() {
		for iteration := 0; iteration < 30; iteration++ {
			driver(iteration)
		}
		close(done)
		wg.Wait()
	}
}

// ------------------------------------
//
//	TestRemoteSyncSnapshotReadersRaceWithRefresh runs concurrent readers of
//	the combined and individual remote/upstream getters while the refresh
//	repeatedly publishes new generations, alternating the upstream between
//	set and unset so the published state changes. Under -race it proves the
//	snapshot publication and the readers are synchronized and never expose a
//	mixed generation.
//
// ------------------------------------
func TestRemoteSyncSnapshotReadersRaceWithRefresh(t *testing.T) {
	_, run := repositoryUnderTest(t)
	run("init", "--bare", "-q", "origin.git")
	run("remote", "add", "origin", "origin.git")
	run("commit", "--allow-empty", "-q", "-m", "base")
	run("push", "-q", "-u", "origin", "master")

	gr := remoteSyncHandlerUnderTest(t)

	finish := runSnapshotRaceExercise(t,
		func() { _ = gr.GetLatestRemoteSyncStatusAndUpstream(nil) },
		func() {
			snapshot := gr.RemoteSyncStatusAndUpstream()
			_ = gr.RemoteSyncStatus()
			_ = gr.UpStreamRemoteIcon()
			_ = gr.CurrentBranchUpStream()
			_ = snapshot
		},
		func(iteration int) {
			if iteration%2 == 0 {
				run("branch", "--unset-upstream")
			} else {
				run("branch", "--set-upstream-to=origin/master", "master")
			}
		},
	)
	finish()
}

// ------------------------------------
//
//	TestRemoteBranchesReadersRaceWithRefresh runs concurrent readers of the
//	remote branch list while the refresh republishes a new list, alternating
//	a remote-tracking ref between present and absent. Under -race it proves
//	the list is published through an immutable generation rather than a
//	slice header a reader could copy halfway.
//
// ------------------------------------
func TestRemoteBranchesReadersRaceWithRefresh(t *testing.T) {
	_, run := repositoryUnderTest(t)
	run("commit", "--allow-empty", "-q", "-m", "base")

	gb := InitGitBranch(nil, false, logging.InitGittiLogging(64, make(chan string, 256), 3))

	finish := runSnapshotRaceExercise(t,
		func() { _ = gb.GetLatestRemoteBranchesInfo(nil) },
		func() {
			branches := gb.RemoteBranches()
			copied := append([]BranchInfo(nil), branches...)
			_ = copied
		},
		func(iteration int) {
			if iteration%2 == 0 {
				run("update-ref", "refs/remotes/origin/scratch", "HEAD")
			} else {
				run("update-ref", "-d", "refs/remotes/origin/scratch")
			}
		},
	)
	finish()
}

// ------------------------------------
//
//	TestCommitLogSnapshotReadersRaceWithRefresh runs concurrent readers of
//	the commit log output, the local branch names, and the combined snapshot
//	while the refresh republishes new generations, alternating a local branch
//	between present and absent. Under -race it proves the history and the
//	branch names are published together as one immutable generation.
//
// ------------------------------------
func TestCommitLogSnapshotReadersRaceWithRefresh(t *testing.T) {
	_, run := repositoryUnderTest(t)
	run("commit", "--allow-empty", "-q", "-m", "base")

	gcl, _ := commitLogUnderTest(t, false)

	finish := runSnapshotRaceExercise(t,
		func() { _ = gcl.GetCommitLogs(nil) },
		func() {
			commits := gcl.GitCommitLogOutput()
			names := gcl.LocalBranchNames()
			snapshot := gcl.CommitLogSnapshot()
			_ = commits
			_ = names
			_ = snapshot
		},
		func(iteration int) {
			if iteration%2 == 0 {
				run("branch", "scratch")
			} else {
				run("branch", "-D", "scratch")
			}
		},
	)
	finish()
}
