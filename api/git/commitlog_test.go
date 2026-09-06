package git

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gohyuhan/gitti/executor"
	"github.com/gohyuhan/gitti/logging"
)

// ------------------------------------
//
//	Build a git log line in the exact NUL-separated shape the pretty format emits
//
// ------------------------------------
func commitLogLine(hash, parents, refs, subject, author string) string {
	return strings.Join([]string{hash, parents, refs, subject, author}, SEPARATOR)
}

func TestBuildCommitLogArgsRequestsRefDecorations(t *testing.T) {
	gitArgs := buildCommitLogArgs("2500", false)

	if !slices.Contains(gitArgs, "--pretty=format:%H%x00%P%x00%D%x00%s%x00%an") {
		t.Errorf("the pretty format does not carry the ref field: %v", gitArgs)
	}
	for _, want := range []string{
		"--decorate-refs=HEAD",
		"--decorate-refs=refs/heads/*",
		"--decorate-refs=refs/remotes/*",
		"--decorate-refs=refs/tags/*",
		"--decorate-refs-exclude=refs/remotes/*/HEAD",
	} {
		if !slices.Contains(gitArgs, want) {
			t.Errorf("missing %s, so the decoration set is left to the user's git config: %v", want, gitArgs)
		}
	}
	if gitArgs[len(gitArgs)-1] != "--" {
		t.Errorf("the pathspec separator must stay last, got %v", gitArgs)
	}
}

func TestBuildCommitLogArgsWalksOnlyHeadByDefault(t *testing.T) {
	gitArgs := buildCommitLogArgs("2500", false)

	for _, unwanted := range []string{"--branches", "--remotes", "--tags"} {
		if slices.Contains(gitArgs, unwanted) {
			t.Errorf("%s widens the default walk beyond the checked-out history: %v", unwanted, gitArgs)
		}
	}
}

func TestBuildCommitLogArgsSurvivesAnUnbornHeadInBothModes(t *testing.T) {
	// git exits 128 on an unborn HEAD without this, and the panel now surfaces
	// that exit status as an error on every refresh.
	for _, allBranches := range []bool{false, true} {
		gitArgs := buildCommitLogArgs("2500", allBranches)

		if !slices.Contains(gitArgs, "--ignore-missing") {
			t.Errorf("allBranches=%v: a fresh repository would abort the whole walk: %v", allBranches, gitArgs)
		}
		if !slices.Contains(gitArgs, "HEAD") {
			t.Errorf("allBranches=%v: --ignore-missing needs the revision it forgives: %v", allBranches, gitArgs)
		}
		if slices.Index(gitArgs, "HEAD") > slices.Index(gitArgs, "--") {
			t.Errorf("allBranches=%v: HEAD after -- is parsed as a pathspec: %v", allBranches, gitArgs)
		}
	}
}

func TestBuildCommitLogArgsWalksEveryRefWhenAsked(t *testing.T) {
	gitArgs := buildCommitLogArgs("2500", true)

	for _, want := range []string{"--branches", "--remotes", "--tags"} {
		if !slices.Contains(gitArgs, want) {
			t.Errorf("missing %s, so other branches contribute no rows: %v", want, gitArgs)
		}
	}
	if gitArgs[len(gitArgs)-1] != "--" {
		t.Errorf("the revision arguments must precede the pathspec separator, got %v", gitArgs)
	}
}

func TestParseCommitLogLineReadsRefs(t *testing.T) {
	cL, ok := parseCommitLogLine(commitLogLine("abc123", "def456", "HEAD -> master, origin/master", "add a thing", "Ada Lovelace"))

	if !ok {
		t.Fatal("a complete five-field line was rejected")
	}
	if cL.Refs != "HEAD -> master, origin/master" {
		t.Errorf("Refs = %q, want the decoration field", cL.Refs)
	}
	if cL.Message != "add a thing" {
		t.Errorf("Message = %q, want the subject field", cL.Message)
	}
	if cL.Author != "Ada Lovelace" {
		t.Errorf("Author = %q, want the author field", cL.Author)
	}
	if !slices.Equal(cL.Parents, []string{"def456"}) {
		t.Errorf("Parents = %v, want one parent", cL.Parents)
	}
}

func TestParseCommitLogLineAcceptsAnUndecoratedCommit(t *testing.T) {
	cL, ok := parseCommitLogLine(commitLogLine("abc123", "def456", "", "add a thing", "Ada Lovelace"))

	if !ok {
		t.Fatal("a commit with no refs was rejected")
	}
	if cL.Refs != "" {
		t.Errorf("Refs = %q, want empty", cL.Refs)
	}
}

func TestParseCommitLogLineAcceptsARootCommit(t *testing.T) {
	cL, ok := parseCommitLogLine(commitLogLine("abc123", "", "tag: v0.1.0", "first", "Ada Lovelace"))

	if !ok {
		t.Fatal("a root commit was rejected")
	}
	if len(cL.Parents) != 0 {
		t.Errorf("Parents = %v, want none for a root commit", cL.Parents)
	}
}

func TestParseCommitLogLineSplitsEveryParentOfAMerge(t *testing.T) {
	cL, ok := parseCommitLogLine(commitLogLine("abc123", "def456 789abc", "", "merge", "Ada Lovelace"))

	if !ok {
		t.Fatal("a merge commit was rejected")
	}
	if !slices.Equal(cL.Parents, []string{"def456", "789abc"}) {
		t.Errorf("Parents = %v, want both parents", cL.Parents)
	}
}

func TestParseCommitLogLineRejectsAShortLine(t *testing.T) {
	if _, ok := parseCommitLogLine(strings.Join([]string{"abc123", "def456", "subject"}, SEPARATOR)); ok {
		t.Error("a line missing fields was accepted, which would shift refs into the subject")
	}
}

func TestScanCommitLogsKeepsTheRefsOnEachCommit(t *testing.T) {
	output := strings.Join([]string{
		commitLogLine("c", "b", "HEAD -> master", "third", "Ada"),
		commitLogLine("b", "a", "tag: v1", "second", "Ada"),
		commitLogLine("a", "", "", "first", "Ada"),
	}, "\n")

	commits, err := scanCommitLogs(strings.NewReader(output))
	if err != nil {
		t.Fatalf("reading a well-formed log: %v", err)
	}

	if len(commits) != 3 {
		t.Fatalf("read %d commits, want 3", len(commits))
	}
	if commits[0].Refs != "HEAD -> master" {
		t.Errorf("first commit Refs = %q", commits[0].Refs)
	}
	if commits[1].Refs != "tag: v1" {
		t.Errorf("second commit Refs = %q", commits[1].Refs)
	}
	if len(commits[0].LaneCharInfo) == 0 {
		t.Error("the lane was not rendered for a scanned commit")
	}
}

func TestScanCommitLogsSurfacesAnOverLongLine(t *testing.T) {
	overLong := strings.Repeat("a", COMMIT_LOG_SCAN_MAX_LINE_BYTES+1)

	_, err := scanCommitLogs(strings.NewReader(overLong + "\n"))

	// Swallowing this error would return a silently truncated history as if it
	// were the whole log.
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Fatalf("err = %v, want bufio.ErrTooLong", err)
	}
}

func TestDrainAndWaitReturnsAfterAnOverLongLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the teardown contract is exercised through a POSIX shell")
	}

	// One line past the scanner's ceiling, then more output than a pipe buffer
	// holds: without the drain the child blocks on write and the wait never
	// returns, which would leave the commit log panel's in-flight flag set.
	script := "head -c 5242880 /dev/zero | tr '\\0' 'a'; echo; head -c 5242880 /dev/zero | tr '\\0' 'b'; echo"
	cmd := exec.Command("sh", "-c", script)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("opening the pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the child: %v", err)
	}

	commits, scanErr := scanCommitLogs(stdout)
	if !errors.Is(scanErr, bufio.ErrTooLong) {
		t.Fatalf("scan err = %v, want bufio.ErrTooLong", scanErr)
	}
	if len(commits) != 0 {
		t.Errorf("read %d commits from output that carries none", len(commits))
	}

	returned := make(chan error, 1)
	go func() { returned <- drainAndWait(cmd, stdout, scanErr) }()

	select {
	case <-returned:
		// A killed child reports a non-nil error; that it returned at all is the contract.
	case <-time.After(30 * time.Second):
		t.Fatal("drainAndWait did not return, leaving a blocked git process behind")
	}
}

// ------------------------------------
//
//	Create a repository in a temporary directory, point the global executor at it
//	for the duration of one test, and return its path with a runner for further
//	git commands
//
// ------------------------------------
func repositoryUnderTest(t *testing.T) (string, func(gitArgs ...string)) {
	t.Helper()

	root := t.TempDir()
	run := func(gitArgs ...string) {
		t.Helper()
		cmd := exec.Command("git", gitArgs...)
		cmd.Dir = root
		// The developer's own git config must not reach these repositories: a
		// global commit.gpgsign or hooksPath would fail or hang the test.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Ada Lovelace", "GIT_AUTHOR_EMAIL=ada@example.com",
			"GIT_COMMITTER_NAME=Ada Lovelace", "GIT_COMMITTER_EMAIL=ada@example.com",
			"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(gitArgs, " "), err, output)
		}
	}
	run("init", "-q", "-b", "master", ".")

	original := executor.GittiCmdExecutor
	t.Cleanup(func() { executor.GittiCmdExecutor = original })
	executor.InitCmdExecutor(root)

	return root, run
}

// ------------------------------------
//
//	Build a commit log reader whose log channel is wide enough that recording an
//	entry never blocks the test
//
// ------------------------------------
func commitLogUnderTest(t *testing.T, allBranches bool) (*GitCommitLog, *logging.GittiLogging) {
	t.Helper()

	gittiLogging := logging.InitGittiLogging(64, make(chan string, 256), 3)
	return InitGitCommitLog(make(chan string, 16), nil, 2500, allBranches, gittiLogging), gittiLogging
}

// ------------------------------------
//
//	Report the error entries a run recorded
//
// ------------------------------------
func errorLogs(gittiLogging *logging.GittiLogging) []string {
	var recorded []string
	for _, entry := range gittiLogging.GetFullLogs() {
		if entry.OpsSeverityLevel == logging.ERROR {
			recorded = append(recorded, entry.OpsDescription)
		}
	}
	return recorded
}

func TestGetCommitLogsAttachesRefsToEachCommit(t *testing.T) {
	_, run := repositoryUnderTest(t)
	run("commit", "-q", "--allow-empty", "-m", "first")
	run("tag", "v0.1.0")
	run("commit", "-q", "--allow-empty", "-m", "second")

	gitCommitLog, gittiLogging := commitLogUnderTest(t, false)
	gitCommitLog.GetCommitLogs()

	commits := gitCommitLog.GitCommitLogOutput()
	if len(commits) != 2 {
		t.Fatalf("read %d commits, want 2", len(commits))
	}
	if !strings.Contains(commits[0].Refs, "HEAD -> master") {
		t.Errorf("tip Refs = %q, want the HEAD decoration", commits[0].Refs)
	}
	if !strings.Contains(commits[1].Refs, "tag: v0.1.0") {
		t.Errorf("tagged commit Refs = %q, want the tag decoration", commits[1].Refs)
	}
	if recorded := errorLogs(gittiLogging); recorded != nil {
		t.Errorf("a healthy repository recorded errors: %v", recorded)
	}
}

func TestGetCommitLogsIsQuietOnAnUnbornHead(t *testing.T) {
	// A freshly initialised repository is a supported state. git log exits 128 on
	// it, and the panel refreshes on every watcher event, so a reported failure
	// here would be a permanent error in the log panel.
	repositoryUnderTest(t)

	gitCommitLog, gittiLogging := commitLogUnderTest(t, false)
	gitCommitLog.GetCommitLogs()

	if commits := gitCommitLog.GitCommitLogOutput(); len(commits) != 0 {
		t.Errorf("read %d commits from a repository with none", len(commits))
	}
	if recorded := errorLogs(gittiLogging); recorded != nil {
		t.Errorf("an empty repository recorded errors: %v", recorded)
	}
}

func TestGetCommitLogsKeepsTheLastGoodHistoryWhenTheReadFails(t *testing.T) {
	_, run := repositoryUnderTest(t)
	run("commit", "-q", "--allow-empty", "-m", "first")

	gitCommitLog, gittiLogging := commitLogUnderTest(t, false)
	gitCommitLog.GetCommitLogs()

	good := gitCommitLog.GitCommitLogOutput()
	if len(good) != 1 {
		t.Fatalf("read %d commits, want 1", len(good))
	}

	// A subject past the scanner's ceiling ends the read on the first line. The
	// panel has no error state, so publishing that would silently replace a
	// complete history with an empty one.
	subject := filepath.Join(t.TempDir(), "subject.txt")
	if err := os.WriteFile(subject, []byte(strings.Repeat("a", COMMIT_LOG_SCAN_MAX_LINE_BYTES+1)), 0o644); err != nil {
		t.Fatalf("writing the oversized subject: %v", err)
	}
	run("commit", "-q", "--allow-empty", "-F", subject)

	gitCommitLog.GetCommitLogs()

	after := gitCommitLog.GitCommitLogOutput()
	if len(after) != len(good) || (len(after) > 0 && after[0].Hash != good[0].Hash) {
		t.Errorf("a failed read published %d commits over the last good %d", len(after), len(good))
	}
	if recorded := errorLogs(gittiLogging); len(recorded) != 1 {
		t.Errorf("recorded %d errors for one failed read, want exactly 1: %v", len(recorded), recorded)
	}
}

func TestGetCommitLogsWalksOnlyHeadWhenAllBranchesIsOff(t *testing.T) {
	_, run := repositoryUnderTest(t)
	run("commit", "-q", "--allow-empty", "-m", "on master")
	run("switch", "-q", "-c", "feature")
	run("commit", "-q", "--allow-empty", "-m", "on feature")
	run("switch", "-q", "master")

	gitCommitLog, _ := commitLogUnderTest(t, false)
	gitCommitLog.GetCommitLogs()

	commits := gitCommitLog.GitCommitLogOutput()
	if len(commits) != 1 {
		t.Fatalf("read %d commits, want only the checked-out history", len(commits))
	}
	if commits[0].Message != "on master" {
		t.Errorf("read %q, want the commit on the checked-out branch", commits[0].Message)
	}
}

func TestGetCommitLogsWalksEveryBranchWhenAllBranchesIsOn(t *testing.T) {
	_, run := repositoryUnderTest(t)
	run("commit", "-q", "--allow-empty", "-m", "on master")
	run("switch", "-q", "-c", "feature")
	run("commit", "-q", "--allow-empty", "-m", "on feature")
	run("switch", "-q", "master")

	gitCommitLog, _ := commitLogUnderTest(t, true)
	gitCommitLog.GetCommitLogs()

	commits := gitCommitLog.GitCommitLogOutput()
	if len(commits) != 2 {
		t.Fatalf("read %d commits, want the diverged branch as well", len(commits))
	}
	if !strings.Contains(commits[0].Refs, "feature") {
		t.Errorf("tip Refs = %q, want the other branch labelled", commits[0].Refs)
	}
}

func TestGetCommitLogsExcludesStashAndNotesInBothModes(t *testing.T) {
	for _, allBranches := range []bool{false, true} {
		root, run := repositoryUnderTest(t)
		run("commit", "-q", "--allow-empty", "-m", "a commit")
		run("notes", "add", "-m", "a note", "HEAD")
		if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o644); err != nil {
			t.Fatalf("writing a file to stash: %v", err)
		}
		run("add", "f.txt")
		run("stash", "-q")

		gitCommitLog, _ := commitLogUnderTest(t, allBranches)
		gitCommitLog.GetCommitLogs()

		for _, commit := range gitCommitLog.GitCommitLogOutput() {
			if strings.Contains(commit.Message, "WIP on") || strings.Contains(commit.Message, "index on") ||
				strings.Contains(commit.Message, "Notes added") {
				t.Errorf("allBranches=%v: %q reached the commit rows", allBranches, commit.Message)
			}
		}
	}
}

func TestGetCommitLogsWalksTheBranchesOfAnUnbornHead(t *testing.T) {
	_, run := repositoryUnderTest(t)
	run("commit", "-q", "--allow-empty", "-m", "on master")
	run("switch", "-q", "--orphan", "unborn")

	gitCommitLog, gittiLogging := commitLogUnderTest(t, true)
	gitCommitLog.GetCommitLogs()

	// HEAD points at a branch with no commits, but the other branches are intact
	// and must still produce rows.
	commits := gitCommitLog.GitCommitLogOutput()
	if len(commits) != 1 {
		t.Fatalf("read %d commits from an unborn HEAD, want the other branch's history", len(commits))
	}
	if recorded := errorLogs(gittiLogging); recorded != nil {
		t.Errorf("an unborn HEAD recorded errors: %v", recorded)
	}
}

func TestGetCommitLogsIsQuietOnAnEmptyRepositoryInAllBranchesMode(t *testing.T) {
	repositoryUnderTest(t)

	gitCommitLog, gittiLogging := commitLogUnderTest(t, true)
	gitCommitLog.GetCommitLogs()

	if commits := gitCommitLog.GitCommitLogOutput(); len(commits) != 0 {
		t.Errorf("read %d commits from an empty repository", len(commits))
	}
	if recorded := errorLogs(gittiLogging); recorded != nil {
		t.Errorf("an empty repository recorded errors: %v", recorded)
	}
}
