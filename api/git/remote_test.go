package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
//	absent-upstream state: an unset upstream (unpublished), a detached HEAD,
//	and an unborn branch (both not-applicable). Each is a valid repository
//	state, so the read publishes the classified state with cleared upstream
//	and count payload without an error; the last good payload is preserved
//	only for an actual read failure.
//
// ------------------------------------
func TestGetLatestRemoteSyncStatusAndUpstreamPublishesClearedAbsentUpstream(t *testing.T) {
	_, run := repositoryUnderTest(t)
	run("init", "--bare", "-q", "origin.git")
	run("remote", "add", "origin", "origin.git")
	run("commit", "--allow-empty", "-q", "-m", "base")
	run("push", "-q", "-u", "origin", "master")

	gr := remoteSyncHandlerUnderTest(t)

	// before the first read the observation is pending, not an absence
	if got := gr.RemoteSyncStatusAndUpstream().ObservationState; got != UpstreamStatePending {
		t.Fatalf("initial observation state = %s, want pending", got)
	}

	// baseline: a tracked branch publishes the upstream identity, counts,
	// and the observed branch
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the baseline tracked-branch read failed: %v", err)
	}
	baseline := gr.RemoteSyncStatusAndUpstream()
	if baseline.ObservationState != UpstreamStateTracked || baseline.ObservedBranch != "master" {
		t.Fatalf("baseline observation = %s/%s, want tracked/master", baseline.ObservationState, baseline.ObservedBranch)
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
	if got := gr.RemoteSyncStatusAndUpstream(); got.ObservationState != UpstreamStateUnpublished || got.ObservedBranch != "master" {
		t.Errorf("unset upstream observation = %s/%s, want unpublished/master", got.ObservationState, got.ObservedBranch)
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
	if got := gr.RemoteSyncStatusAndUpstream(); got.ObservationState != UpstreamStateNotApplicable {
		t.Errorf("detached HEAD observation = %s, want not-applicable", got.ObservationState)
	}
	if got := gr.CurrentBranchUpStream(); got != "" {
		t.Errorf("detached HEAD published %q, want cleared", got)
	}
	if status := gr.RemoteSyncStatus(); status != (RemoteSyncStatus{}) {
		t.Errorf("detached HEAD published counts %v, want cleared", status)
	}

	// an unborn branch: a symbolic branch with no commit
	run("checkout", "-q", "master")
	run("checkout", "-q", "--orphan", "unborn")
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the unborn-branch read failed: %v", err)
	}
	if got := gr.RemoteSyncStatusAndUpstream(); got.ObservationState != UpstreamStateNotApplicable || got.ObservedBranch != "unborn" {
		t.Errorf("unborn branch observation = %s/%s, want not-applicable/unborn", got.ObservationState, got.ObservedBranch)
	}
	if got := gr.CurrentBranchUpStream(); got != "" {
		t.Errorf("the unborn branch published %q, want cleared", got)
	}
	if status := gr.RemoteSyncStatus(); status != (RemoteSyncStatus{}) {
		t.Errorf("the unborn branch published counts %v, want cleared", status)
	}
}

// ------------------------------------
//
//	TestFailedRemoteReadPublishesUnavailablePreservingLastGood proves a
//	failed current-generation read changes only the observation health to
//	unavailable: the observed branch, upstream, icon, and counts stay at the
//	last good values, the error is returned, and the next successful read
//	restores the tracked state.
//
// ------------------------------------
func TestFailedRemoteReadPublishesUnavailablePreservingLastGood(t *testing.T) {
	_, run := repositoryUnderTest(t)
	run("init", "--bare", "-q", "origin.git")
	run("remote", "add", "origin", "origin.git")
	run("commit", "--allow-empty", "-q", "-m", "base")
	run("push", "-q", "-u", "origin", "master")

	gr := remoteSyncHandlerUnderTest(t)
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the baseline tracked-branch read failed: %v", err)
	}
	iconBefore := gr.UpStreamRemoteIcon()

	// break upstream resolution: the configured upstream points at a ref
	// that does not exist (set directly in the config because git refuses
	// --set-upstream-to for a missing remote-tracking ref)
	run("config", "branch.master.remote", "origin")
	run("config", "branch.master.merge", "refs/heads/does-not-exist")
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
		t.Fatal("the failed read returned nil, want the resolution error")
	}

	snapshot := gr.RemoteSyncStatusAndUpstream()
	if snapshot.ObservationState != UpstreamStateUnavailable {
		t.Errorf("observation state = %s, want unavailable", snapshot.ObservationState)
	}
	if snapshot.ObservedBranch != "master" {
		t.Errorf("the failed read overwrote the last good observed branch: %q", snapshot.ObservedBranch)
	}
	if snapshot.CurrentBranchUpStream != "origin/master" {
		t.Errorf("the failed read overwrote the last good upstream: %q", snapshot.CurrentBranchUpStream)
	}
	if snapshot.RemoteSyncStatus != (RemoteSyncStatus{Local: "0", Remote: "0"}) {
		t.Errorf("the failed read overwrote the last good counts: %v", snapshot.RemoteSyncStatus)
	}
	if got := gr.UpStreamRemoteIcon(); got != iconBefore {
		t.Errorf("the failed read overwrote the last good icon: %q -> %q", iconBefore, got)
	}

	// the next successful read restores the tracked state
	run("branch", "--set-upstream-to=origin/master", "master")
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the recovery read failed: %v", err)
	}
	if got := gr.RemoteSyncStatusAndUpstream(); got.ObservationState != UpstreamStateTracked || got.CurrentBranchUpStream != "origin/master" {
		t.Errorf("recovered observation = %s/%s, want tracked/origin/master", got.ObservationState, got.CurrentBranchUpStream)
	}
}

// ------------------------------------
//
//	TestStaleGenerationGuardSuppressesPublication proves a publish guard
//	rejection stores neither data nor health and returns the guard's error,
//	so a stale worktree's results never land in a new generation.
//
// ------------------------------------
func TestStaleGenerationGuardSuppressesPublication(t *testing.T) {
	_, run := repositoryUnderTest(t)
	run("init", "--bare", "-q", "origin.git")
	run("remote", "add", "origin", "origin.git")
	run("commit", "--allow-empty", "-q", "-m", "base")
	run("push", "-q", "-u", "origin", "master")

	gr := remoteSyncHandlerUnderTest(t)
	staleErr := errors.New("stale worktree generation")
	guard := func() error { return staleErr }

	if err := gr.GetLatestRemoteSyncStatusAndUpstream(guard); err != staleErr {
		t.Fatalf("guarded read error = %v, want the guard's error", err)
	}
	if got := gr.RemoteSyncStatusAndUpstream().ObservationState; got != UpstreamStatePending {
		t.Errorf("the stale read published state %s, want the prior pending state", got)
	}

	// the guard also blocks publication of a later read, so the snapshot
	// still holds the prior generation
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(guard); err != staleErr {
		t.Fatalf("guarded read error = %v, want the guard's error", err)
	}
	if got := gr.RemoteSyncStatusAndUpstream(); got.ObservationState != UpstreamStatePending {
		t.Errorf("the stale failed read published health %s, want the prior pending state", got.ObservationState)
	}
}

// ------------------------------------
//
//	TestUpstreamObservationStaysPinnedToTheCapturedBranch swaps in a git
//	wrapper that checks the working tree out to a different branch at the
//	first upstream-domain command, then proves one published observation
//	never mixes the captured branch's name with another branch's upstream
//	or counts
//
// ------------------------------------
func TestUpstreamObservationStaysPinnedToTheCapturedBranch(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("locating the real git: %v", err)
	}

	_, run := repositoryUnderTest(t)
	run("init", "--bare", "-q", "origin.git")
	run("remote", "add", "origin", "origin.git")
	run("commit", "--allow-empty", "-q", "-m", "base")
	run("branch", "side")
	run("push", "-q", "-u", "origin", "master")
	run("commit", "--allow-empty", "-q", "-m", "ahead")
	run("push", "-q", "-u", "origin", "side")

	// master sits one commit ahead of origin/master; side sits level with
	// origin/side, so a mixed payload is visible in the upstream and counts
	wrapperScript := fmt.Sprintf(`#!/bin/sh
for a in "$@"; do
  case "$a" in
    *"@{u"*)
      %q checkout -q side 2>/dev/null || exit 1
      break
      ;;
  esac
done
exec %q "$@"
`, realGit, realGit)
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(wrapperScript), 0o755); err != nil {
		t.Fatalf("writing the git wrapper: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	gr := remoteSyncHandlerUnderTest(t)
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the pinned-branch read failed: %v", err)
	}

	snapshot := gr.RemoteSyncStatusAndUpstream()
	if snapshot.ObservationState != UpstreamStateTracked {
		t.Fatalf("observation state = %s, want tracked", snapshot.ObservationState)
	}
	if snapshot.ObservedBranch != "master" {
		t.Errorf("observed branch = %q, want master", snapshot.ObservedBranch)
	}
	if snapshot.CurrentBranchUpStream != "origin/master" {
		t.Errorf("upstream = %q, want origin/master (the captured branch's own upstream)", snapshot.CurrentBranchUpStream)
	}
	if status := snapshot.RemoteSyncStatus; status != (RemoteSyncStatus{Local: "1", Remote: "0"}) {
		t.Errorf("counts = %v, want 1 0 for master against origin/master", status)
	}
}
