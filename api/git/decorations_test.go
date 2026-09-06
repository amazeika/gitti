package git

import "testing"

// The decoration lists in these tests are real --decorate=full output, captured
// from a repository with a local branch, several remotes and both tag kinds.

func TestCompactDecorationsCollapsesAPushedBranch(t *testing.T) {
	got := CompactDecorations("HEAD -> refs/heads/main, refs/remotes/origin/main")

	// git names the same branch twice. That repetition, not the labels, is what
	// makes the raw list too wide for a row.
	if want := "*main^"; got != want {
		t.Errorf("CompactDecorations = %q, want %q", got, want)
	}
}

func TestCompactDecorationsMarksAnUnpushedBranchDifferently(t *testing.T) {
	got := CompactDecorations("HEAD -> refs/heads/wip")

	if want := "*wip"; got != want {
		t.Errorf("CompactDecorations = %q, want %q: no remote marker when no remote has it", got, want)
	}
}

func TestCompactDecorationsMarksOnlyTheCheckedOutBranch(t *testing.T) {
	got := CompactDecorations("HEAD -> refs/heads/main, refs/heads/other")

	if want := "*main other"; got != want {
		t.Errorf("CompactDecorations = %q, want %q", got, want)
	}
}

func TestCompactDecorationsKeepsTheRemoteNameWhenNoLocalBranchExists(t *testing.T) {
	// Here "origin/" is the information rather than a duplicate: there is no
	// local branch of that name to collapse into.
	got := CompactDecorations("refs/remotes/origin/release")

	if want := "origin/release"; got != want {
		t.Errorf("CompactDecorations = %q, want %q", got, want)
	}
}

func TestCompactDecorationsCollapsesSeveralRemotesIntoOneMarker(t *testing.T) {
	got := CompactDecorations("HEAD -> refs/heads/main, refs/remotes/origin/main, refs/remotes/upstream/main")

	// The marker says "at least one remote is here too", so two remotes cost no
	// more room than one.
	if want := "*main^"; got != want {
		t.Errorf("CompactDecorations = %q, want %q", got, want)
	}
}

func TestCompactDecorationsDropsTheTagNamespace(t *testing.T) {
	got := CompactDecorations("tag: refs/tags/v2.0, tag: refs/tags/v1.0")

	if want := "v2.0 v1.0"; got != want {
		t.Errorf("CompactDecorations = %q, want %q", got, want)
	}
}

func TestCompactDecorationsMarksADetachedHead(t *testing.T) {
	got := CompactDecorations("HEAD, refs/heads/main")

	// The row already prints the commit hash, so the marker only has to say that
	// this is where HEAD sits.
	if want := "*HEAD main"; got != want {
		t.Errorf("CompactDecorations = %q, want %q", got, want)
	}
}

func TestCompactDecorationsHandlesABranchNameContainingSlashes(t *testing.T) {
	got := CompactDecorations("HEAD -> refs/heads/feature/x, refs/remotes/origin/feature/x")

	// The remote name is only the first segment; the rest is the branch.
	if want := "*feature/x^"; got != want {
		t.Errorf("CompactDecorations = %q, want %q", got, want)
	}
}

func TestCompactDecorationsDoesNotTreatALocalBranchAsARemote(t *testing.T) {
	// A local branch may legitimately be called "origin/x". Under the short
	// decoration form it was indistinguishable from a remote branch; the
	// namespace makes it unambiguous.
	got := CompactDecorations("refs/heads/origin/x")

	if want := "origin/x"; got != want {
		t.Errorf("CompactDecorations = %q, want %q", got, want)
	}
	if collapsed := CompactDecorations("refs/heads/origin/x, refs/remotes/origin/origin/x"); collapsed != "origin/x^" {
		t.Errorf("CompactDecorations = %q, want the local branch marked as pushed, not dropped", collapsed)
	}
}

func TestCompactDecorationsOrdersRefsTheSameWayEveryTime(t *testing.T) {
	// git's own order varies with how the refs were written; a row should not.
	raw := "HEAD -> refs/heads/main, tag: refs/tags/v1.0, refs/remotes/origin/release, refs/heads/other"

	if want := "*main other origin/release v1.0"; CompactDecorations(raw) != want {
		t.Errorf("CompactDecorations = %q, want %q", CompactDecorations(raw), want)
	}
}

func TestCompactDecorationsIsEmptyForAnUndecoratedCommit(t *testing.T) {
	if got := CompactDecorations(""); got != "" {
		t.Errorf("CompactDecorations = %q, want nothing for a commit with no refs", got)
	}
}

func TestCompactDecorationsKeepsAnUnexpectedRefVisible(t *testing.T) {
	// Better a decoration that looks odd than one that silently vanishes.
	if got := CompactDecorations("refs/notes/commits"); got != "refs/notes/commits" {
		t.Errorf("CompactDecorations = %q, want the ref shown as written", got)
	}
}

func TestDecorationFilterTextKeepsTheRemoteNameTheRowDrops(t *testing.T) {
	got := DecorationFilterText("HEAD -> refs/heads/main, refs/remotes/origin/main")

	// The row shows "*main^" only, but a filter typed as "origin/main" must
	// still match the commit.
	if want := "main origin/main"; got != want {
		t.Errorf("DecorationFilterText = %q, want %q", got, want)
	}
}

func TestDecorationFilterTextOmitsTheNamespaces(t *testing.T) {
	got := DecorationFilterText("HEAD -> refs/heads/main, tag: refs/tags/v1.0")

	// Leaving "refs/heads/" in would make a filter of "heads" match every
	// decorated commit in the log.
	if want := "main v1.0"; got != want {
		t.Errorf("DecorationFilterText = %q, want %q", got, want)
	}
}

func TestDecorationBranchLabelPrefersTheCheckedOutBranch(t *testing.T) {
	got := DecorationBranchLabel("refs/heads/other, HEAD -> refs/heads/main")

	if want := "main"; got != want {
		t.Errorf("DecorationBranchLabel = %q, want %q", got, want)
	}
}

func TestDecorationBranchLabelFallsBackToARemoteBranch(t *testing.T) {
	got := DecorationBranchLabel("refs/remotes/origin/release")

	if want := "origin/release"; got != want {
		t.Errorf("DecorationBranchLabel = %q, want %q", got, want)
	}
}

func TestDecorationBranchLabelNamesNoBranchForATagOnlyCommit(t *testing.T) {
	// "from branch: v1.0.0" would be a lie. Nothing is the honest answer.
	if got := DecorationBranchLabel("tag: refs/tags/v1.0.0"); got != "" {
		t.Errorf("DecorationBranchLabel = %q, want nothing: a tag is not a branch", got)
	}
}

func TestParseDecorationsClassifiesEveryNamespace(t *testing.T) {
	decorations := ParseDecorations("HEAD -> refs/heads/main, tag: refs/tags/v1.0, refs/remotes/origin/release")

	if len(decorations) != 3 {
		t.Fatalf("parsed %d decorations, want 3", len(decorations))
	}
	if decorations[0].Kind != LocalBranchDecoration || !decorations[0].IsHead || decorations[0].Name != "main" {
		t.Errorf("first = %+v, want the checked-out local branch main", decorations[0])
	}
	if decorations[1].Kind != TagDecoration || decorations[1].Name != "v1.0" {
		t.Errorf("second = %+v, want tag v1.0", decorations[1])
	}
	if decorations[2].Kind != RemoteBranchDecoration || decorations[2].Remote != "origin" || decorations[2].Name != "release" {
		t.Errorf("third = %+v, want origin's release branch", decorations[2])
	}
}
