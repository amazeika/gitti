package git

import (
	"testing"

	"github.com/gohyuhan/gitti/logging"
)

// ------------------------------------
//
//	remoteSyncHandlerUnderTest builds a GitRemote reader against the shared
//	repository fixture with a wide enough log channel that recording an
//	entry never blocks the test
//
// ------------------------------------
func remoteSyncHandlerUnderTest(t *testing.T) *GitRemote {
	t.Helper()
	gittiLogging := logging.InitGittiLogging(64, make(chan string, 256), 3)
	return InitGitRemote(make(chan string, 16), nil, gittiLogging)
}

// ------------------------------------
//
//	TestGetLatestRemoteSyncStatusAndUpstreamPublishesClearedAbsentUpstream
//	covers the transitions from a tracked branch to each supported
//	absent-upstream state: an unset upstream, a detached HEAD, and an unborn
//	branch. Each is a valid repository state, so the read publishes cleared
//	upstream and count state without an error; the last good state is
//	preserved only for an actual read failure.
//
// ------------------------------------
func TestGetLatestRemoteSyncStatusAndUpstreamPublishesClearedAbsentUpstream(t *testing.T) {
	_, run := repositoryUnderTest(t)
	run("init", "--bare", "-q", "origin.git")
	run("remote", "add", "origin", "origin.git")
	run("commit", "--allow-empty", "-q", "-m", "base")
	run("push", "-q", "-u", "origin", "master")

	gr := remoteSyncHandlerUnderTest(t)

	// baseline: a tracked branch publishes the upstream identity and counts
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the baseline tracked-branch read failed: %v", err)
	}
	if got := gr.CurrentBranchUpStream(); got != "origin/master" {
		t.Fatalf("baseline upstream = %q, want origin/master", got)
	}
	if status := gr.RemoteSyncStatus(); status != (RemoteSyncStatus{Local: "0", Remote: "0"}) {
		t.Fatalf("baseline sync status = %v, want 0 0", status)
	}

	// unset the upstream: a valid absence, not a failure
	run("branch", "--unset-upstream")
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the unset-upstream read failed: %v", err)
	}
	if got := gr.CurrentBranchUpStream(); got != "" {
		t.Errorf("the unset upstream published %q, want cleared", got)
	}
	if status := gr.RemoteSyncStatus(); status != (RemoteSyncStatus{}) {
		t.Errorf("the unset upstream published counts %v, want cleared", status)
	}

	// detach HEAD while the upstream is set: another valid absence
	run("branch", "--set-upstream-to=origin/master", "master")
	run("checkout", "-q", "--detach")
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the detached-HEAD read failed: %v", err)
	}
	if got := gr.CurrentBranchUpStream(); got != "" {
		t.Errorf("detached HEAD published %q, want cleared", got)
	}
	if status := gr.RemoteSyncStatus(); status != (RemoteSyncStatus{}) {
		t.Errorf("detached HEAD published counts %v, want cleared", status)
	}

	// delete the branch under HEAD: the unborn-branch state
	run("update-ref", "-d", "refs/heads/master")
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the unborn-branch read failed: %v", err)
	}
	if got := gr.CurrentBranchUpStream(); got != "" {
		t.Errorf("the unborn branch published %q, want cleared", got)
	}
	if status := gr.RemoteSyncStatus(); status != (RemoteSyncStatus{}) {
		t.Errorf("the unborn branch published counts %v, want cleared", status)
	}
}
