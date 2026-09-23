package executor

import (
	"context"
	"os"
	"os/exec"
)

type CmdExecutor struct {
	repoPath string
}

var GittiCmdExecutor *CmdExecutor

// ------------------------------------
//
//	Initialize the global command executor with the given repo path
//
// ------------------------------------
func InitCmdExecutor(repoPath string) {
	GittiCmdExecutor = &CmdExecutor{
		repoPath: repoPath,
	}
}

// ------------------------------------
//
//	Initialize a scoped command executor pinned to the given repo path,
//	without replacing the shared global executor. A per-generation executor
//	lets a Git-operations generation bind its worktree-bound routes (upstream
//	observation, remote inventory, push, signing push) to the immutable
//	worktree the generation was captured from, while the rest of the app keeps
//	using the global executor.
//
// ------------------------------------
func InitScopedCmdExecutor(repoPath string) *CmdExecutor {
	return &CmdExecutor{
		repoPath: repoPath,
	}
}

// ------------------------------------
//
//	Return the directory this executor runs git commands in
//
// ------------------------------------
func (c *CmdExecutor) RepoPath() string {
	return c.repoPath
}

// ------------------------------------
//
//	Execute a git command with the given arguments
//
// ------------------------------------
func (c *CmdExecutor) RunGitCmd(gitArgs []string, colorized bool) *exec.Cmd {
	if colorized {
		gitArgs = append([]string{"-c", "color.ui=always"}, gitArgs...)
	}
	gitArgs = append([]string{
		"-c", "core.whitespace=cr-at-eol",
		"-c", "diff.ignore-cr-at-eol=true",
		// SUPPRESS EDITOR (No interactive message editing)
		"-c", "core.editor=true",
		"--no-optional-locks",
	}, gitArgs...)
	cmd := exec.Command("git", gitArgs...)
	cmd.Dir = c.repoPath

	cmd.Env = append(os.Environ(), "GIT_ASKPASS=true", "GIT_TERMINAL_PROMPT=0")
	return cmd
}

// ------------------------------------
//
//	Create a git command that respects context cancellation and terminates automatically
//
// ------------------------------------
func (c *CmdExecutor) RunGitCmdWithContext(ctx context.Context, gitArgs []string, colorized bool) *exec.Cmd {
	if colorized {
		gitArgs = append([]string{"-c", "color.ui=always"}, gitArgs...)
	}
	gitArgs = append([]string{
		"-c", "core.whitespace=cr-at-eol",
		"-c", "diff.ignore-cr-at-eol=true",
		// SUPPRESS EDITOR (No interactive message editing)
		"-c", "core.editor=true",
		"--no-optional-locks",
	}, gitArgs...)
	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	cmd.Dir = c.repoPath

	cmd.Env = append(os.Environ(), "GIT_ASKPASS=true", "GIT_TERMINAL_PROMPT=0")

	return cmd
}

// ------------------------------------
//
//	Update the working directory for all subsequent command executions
//
// ------------------------------------
func (c *CmdExecutor) UpdateRepoPath(updatedRepoPath string) {
	c.repoPath = updatedRepoPath
}
