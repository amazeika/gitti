package git

// ------------------------------------
//
//	PublishGuard is checked by a passive state refresh immediately before it
//	stores a freshly read snapshot. It returns nil when the refresh may
//	publish, or an error describing why the refresh must abort without
//	touching the stored state. The daemon uses it to reject a refresh whose
//	worktree/Git-operations generation went stale while the pass was running,
//	so old-worktree results are never published into a newly selected
//	worktree.
//
// ------------------------------------
type PublishGuard func() error
