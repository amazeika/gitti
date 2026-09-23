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

// ------------------------------------
//
//	GitPushIntent is the mutation semantics the user confirmed at the push
//	confirmation popup. The route carries the intent explicitly instead of
//	inferring it from rendered UI state, so a late or reordered event can
//	never pick a different push command than the one that was confirmed.
//
// ------------------------------------
type GitPushIntent string

const (
	// PushIntentPublish is the publish-only confirmation for an
	// unpublished branch: a normal push that sets the upstream because the
	// branch is still unpublished. It never carries a force flag.
	PushIntentPublish GitPushIntent = "publish"

	// PushIntentPush is the push-type confirmation for a tracked branch:
	// normal, safe force, or dangerous force with the existing semantics.
	PushIntentPush GitPushIntent = "push"
)

// ------------------------------------
//
//	GitPushRoute is one confirmed push or publish attempt, captured by the
//	UI at confirmation time and validated again by the API immediately before
//	the process is started. RemoteName, PushType, Branch, and Intent are
//	immutable once captured; ActiveGuard, when non-nil, is checked by the
//	background route just before the process starts and must reject when the
//	Git-operations generation is no longer the one the route was captured
//	from.
//
// ------------------------------------
type GitPushRoute struct {
	RemoteName  string
	PushType    string
	Branch      string
	Intent      GitPushIntent
	ActiveGuard func() error
}
