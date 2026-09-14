package git

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/gohyuhan/gitti/executor"
	"github.com/gohyuhan/gitti/logging"
)

// ------------------------------------
//
//	Build a branch handler with a non-blocking test logger.
//
// ------------------------------------
func branchUnderTest(t *testing.T) *GitBranch {
	t.Helper()
	return InitGitBranch(nil, false, logging.InitGittiLogging(64, make(chan string, 128), 3))
}

func TestGitBranchPublishesCanonicalLinkedWorktreeState(t *testing.T) {
	_, run := repositoryUnderTest(t)
	run("commit", "-q", "--allow-empty", "-m", "first")
	run("branch", "feature")
	run("worktree", "add", "-q", filepath.Join(t.TempDir(), "linked worktree with spaces"), "feature")

	gitBranch := branchUnderTest(t)
	gitBranch.GetLatestBranchesInfo(nil)

	snapshot := gitBranch.LocalBranchSnapshot()
	if snapshot.CurrentCheckOut != (BranchInfo{BranchName: "master", IsCheckedOut: true}) {
		t.Errorf("current = %#v, want canonical master", snapshot.CurrentCheckOut)
	}
	if len(snapshot.AllBranches) != 1 {
		t.Fatalf("other branches = %#v, want feature", snapshot.AllBranches)
	}
	feature := snapshot.AllBranches[0]
	if feature.BranchName != "feature" || feature.IsCheckedOut || !feature.IsCheckedOutInLinkedWorktree {
		t.Errorf("linked branch = %#v, want canonical linked feature", feature)
	}
	if slices.ContainsFunc(snapshot.AllBranches, func(branch BranchInfo) bool { return branch.BranchName == "master" }) {
		t.Error("AllBranches contains the current branch")
	}
}

func TestGitBranchClassifiesUnbornDetachedAndAttachedTransitions(t *testing.T) {
	_, run := repositoryUnderTest(t)
	gitBranch := branchUnderTest(t)

	gitBranch.GetLatestBranchesInfo(nil)
	if current := gitBranch.CurrentCheckOut(); current != (BranchInfo{BranchName: "master", IsCheckedOut: true}) || !gitBranch.IsRepoUnborn() {
		t.Errorf("fresh repository published current=%#v unborn=%v, want unborn master", current, gitBranch.IsRepoUnborn())
	}

	run("commit", "-q", "--allow-empty", "-m", "first")
	run("branch", "feature")
	run("switch", "-q", "--detach", "HEAD")
	gitBranch.GetLatestBranchesInfo(nil)
	if current := gitBranch.CurrentCheckOut(); current != (BranchInfo{}) || gitBranch.IsRepoUnborn() {
		t.Errorf("detached repository published current=%#v unborn=%v, want no current", current, gitBranch.IsRepoUnborn())
	}
	if got := branchNames(gitBranch.AllBranches()); !slices.Equal(got, []string{"feature", "master"}) {
		t.Errorf("detached all branches = %v, want every discovered ref", got)
	}

	run("switch", "-q", "master")
	gitBranch.GetLatestBranchesInfo(nil)
	if current := gitBranch.CurrentCheckOut(); current != (BranchInfo{BranchName: "master", IsCheckedOut: true}) || gitBranch.IsRepoUnborn() {
		t.Errorf("reattached repository published current=%#v unborn=%v, want attached master", current, gitBranch.IsRepoUnborn())
	}

	run("switch", "-q", "--orphan", "orphan")
	gitBranch.GetLatestBranchesInfo(nil)
	if current := gitBranch.CurrentCheckOut(); current != (BranchInfo{BranchName: "orphan", IsCheckedOut: true}) || !gitBranch.IsRepoUnborn() {
		t.Errorf("orphan repository published current=%#v unborn=%v, want unborn orphan", current, gitBranch.IsRepoUnborn())
	}
	if got := branchNames(gitBranch.AllBranches()); !slices.Equal(got, []string{"feature", "master"}) {
		t.Errorf("orphan all branches = %v, want existing refs retained", got)
	}
}

func TestGitBranchClassifiesUnbornHeadWithSameNamedTag(t *testing.T) {
	_, run := repositoryUnderTest(t)
	run("commit", "-q", "--allow-empty", "-m", "first")
	run("tag", "orphan")
	run("switch", "-q", "--orphan", "orphan")

	gitBranch := branchUnderTest(t)
	gitBranch.GetLatestBranchesInfo(nil)

	if current := gitBranch.CurrentCheckOut(); current != (BranchInfo{BranchName: "orphan", IsCheckedOut: true}) {
		t.Errorf("current = %#v, want canonical unborn orphan", current)
	}
	if !gitBranch.IsRepoUnborn() {
		t.Error("same-named tag prevented publishing the unborn branch")
	}
	if got := branchNames(gitBranch.AllBranches()); !slices.Equal(got, []string{"master"}) {
		t.Errorf("other branches = %v, want existing master", got)
	}
}

func TestGitBranchKeepsLastGoodSnapshotAndDefendsReaders(t *testing.T) {
	root, run := repositoryUnderTest(t)
	run("commit", "-q", "--allow-empty", "-m", "first")
	run("branch", "feature")
	gitBranch := branchUnderTest(t)
	gitBranch.GetLatestBranchesInfo(nil)
	good := gitBranch.LocalBranchSnapshot()

	returned := gitBranch.AllBranches()
	returned[0].BranchName = "mutated"
	if got := gitBranch.AllBranches()[0].BranchName; got == "mutated" {
		t.Error("mutating AllBranches result changed the published snapshot")
	}

	executor.InitCmdExecutor(filepath.Join(t.TempDir(), "missing"))
	gitBranch.GetLatestBranchesInfo(nil)
	executor.InitCmdExecutor(root)
	if after := gitBranch.LocalBranchSnapshot(); after.CurrentCheckOut != good.CurrentCheckOut || !slices.Equal(after.AllBranches, good.AllBranches) || after.IsRepoUnborn != good.IsRepoUnborn {
		t.Errorf("failed refresh changed last-good snapshot from %#v to %#v", good, after)
	}

	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: \n"), 0o644); err != nil {
		t.Fatalf("corrupting HEAD for symbolic-ref failure: %v", err)
	}
	gitBranch.GetLatestBranchesInfo(nil)
	if after := gitBranch.LocalBranchSnapshot(); after.CurrentCheckOut != good.CurrentCheckOut || !slices.Equal(after.AllBranches, good.AllBranches) || after.IsRepoUnborn != good.IsRepoUnborn {
		t.Errorf("symbolic-ref failure changed last-good snapshot from %#v to %#v", good, after)
	}
}

func TestGitBranchConcurrentReadersObserveCompleteSnapshots(t *testing.T) {
	_, run := repositoryUnderTest(t)
	run("commit", "-q", "--allow-empty", "-m", "first")
	run("branch", "feature")
	gitBranch := branchUnderTest(t)
	gitBranch.GetLatestBranchesInfo(nil)

	var waitGroup sync.WaitGroup
	for range 8 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for range 20 {
				snapshot := gitBranch.LocalBranchSnapshot()
				if snapshot.CurrentCheckOut.BranchName != "master" || len(snapshot.AllBranches) != 1 || snapshot.AllBranches[0].BranchName != "feature" {
					t.Errorf("reader observed incomplete snapshot %#v", snapshot)
					return
				}
			}
		}()
	}
	for range 4 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for range 5 {
				gitBranch.GetLatestBranchesInfo(nil)
			}
		}()
	}
	waitGroup.Wait()
}

// ------------------------------------
//
//	Project branch records to canonical names for assertions.
//
// ------------------------------------
func branchNames(branches []BranchInfo) []string {
	branchNames := make([]string, 0, len(branches))
	for _, branch := range branches {
		branchNames = append(branchNames, branch.BranchName)
	}
	return branchNames
}
