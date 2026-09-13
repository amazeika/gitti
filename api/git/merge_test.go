package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/logging"
)

func runGitInDirectory(t *testing.T, directory string, gitArgs ...string) {
	t.Helper()
	cmd := exec.Command("git", gitArgs...)
	cmd.Dir = directory
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Ada Lovelace", "GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada Lovelace", "GIT_COMMITTER_EMAIL=ada@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(gitArgs, " "), err, output)
	}
}

func TestBuildMergeArgsKeepsCanonicalOperandsBehindTerminator(t *testing.T) {
	operands := []string{"+skill", "-option-shaped", "feature/z"}

	for _, test := range []struct {
		name string
		ff   bool
		want []string
	}{
		{name: "fast forward", ff: true, want: []string{"merge", "--ff", "--", "+skill", "-option-shaped", "feature/z"}},
		{name: "no fast forward", ff: false, want: []string{"merge", "--no-ff", "--", "+skill", "-option-shaped", "feature/z"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := buildMergeArgs(test.ff, operands); !slices.Equal(got, test.want) {
				t.Errorf("buildMergeArgs() = %v, want %v", got, test.want)
			}

			gitBranch := InitGitBranch(nil, test.ff, nil)
			if got := gitBranch.GitMergeWithSigning(operands); !slices.Equal(got, test.want) {
				t.Errorf("GitMergeWithSigning() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestGitMergeMergesCanonicalLinkedWorktreeBranchInBothModes(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	i18n.InitGittiLanguageMapping("en")

	for _, test := range []struct {
		name string
		ff   bool
	}{
		{name: "fast forward", ff: true},
		{name: "no fast forward", ff: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, run := repositoryUnderTest(t)
			run("config", "user.name", "Ada Lovelace")
			run("config", "user.email", "ada@example.com")
			run("commit", "-q", "--allow-empty", "-m", "base")
			run("branch", "linked")

			linkedWorktree := filepath.Join(t.TempDir(), "linked")
			run("worktree", "add", "-q", linkedWorktree, "linked")
			runGitInDirectory(t, linkedWorktree, "commit", "-q", "--allow-empty", "-m", "linked change")

			gitBranch := InitGitBranch(
				InitGitProcessLock(logging.InitGittiLogging(8, make(chan string, 16), 3)),
				test.ff,
				logging.InitGittiLogging(8, make(chan string, 16), 3),
			)
			if _, success := gitBranch.GitMerge(context.Background(), []string{"linked"}); !success {
				t.Fatal("GitMerge failed to merge the canonical linked-worktree branch")
			}
			run("merge-base", "--is-ancestor", "linked", "HEAD")
		})
	}
}
