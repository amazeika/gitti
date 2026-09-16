package api

import (
	"github.com/gohyuhan/gitti/api/git"
	"github.com/gohyuhan/gitti/executor"
)

// GitOperations is one immutable generation of the live git operations
// bound to a single worktree.
//
// A worktree switch replaces the whole generation with a new struct.
// The worktree-bound routes (upstream observation, remote inventory, push,
// signing push) execute through the generation's scoped command executor
// pinned to AbsoluteWorktreePath.
//
// A route can never run in the mutable global executor's directory after a
// switch, so a stale generation's work can never touch the new worktree.
//
// ------------------------------------
type GitOperations struct {
	// AbsoluteWorktreePath is the immutable top-level command worktree this
	// generation binds to. It is the working directory every worktree-bound
	// route runs in for the whole lifetime of the generation.
	AbsoluteWorktreePath string
	// CmdExecutor is the per-generation command executor bound to
	// AbsoluteWorktreePath. Worktree-bound routes execute through it and never
	// through the mutable global executor after the generation is captured.
	CmdExecutor            *executor.CmdExecutor
	GitBranch              *git.GitBranch
	GitCommit              *git.GitCommit
	GitFiles               *git.GitFiles
	GitPull                *git.GitPull
	GitRebase              *git.GitRebase
	GitStash               *git.GitStash
	GitRemote              *git.GitRemote
	GitCommitLog           *git.GitCommitLog
	GitRefLog              *git.GitRefLog
	GitTag                 *git.GitTag
	GitStateUniversalUtils *git.GitStateUniversalUtils
	GitBlame               *git.GitBlame
	GitInteractiveRebase   *git.GitInteractiveRebase
	GitWorktree            *git.GitWorktree
}

type GitRepoPath struct {
	// having both these path is to support submodule
	AbsoluteGitRepoPath  string // this is the most root level path where .git folder is located
	RepoMainGitDirPath   string // the common (main) git dir; for linked worktrees this is the shared .git, used as the file-watch root so worktree add/remove is observed
	TopLevelRepoPath     string // this is the path where the top level .git file/folder is located at
	AbsoluteWorktreePath string // the absolute path for the current repo worktree
	RepoName             string
}
