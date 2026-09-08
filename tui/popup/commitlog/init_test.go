package commitlog

import (
	"testing"

	"github.com/gohyuhan/gitti/settings"
)

// ------------------------------------
//
//	Point the package-level settings at a config with the all-branches walk on or
//	off for the duration of one test
//
// ------------------------------------
func withAllBranches(t *testing.T, allBranches bool) {
	t.Helper()

	original := settings.GITTICONFIGSETTINGS
	t.Cleanup(func() { settings.GITTICONFIGSETTINGS = original })

	cfg := settings.GittiDefaultConfigSettings
	cfg.CommitLogShowAllBranches = allBranches
	settings.GITTICONFIGSETTINGS = &cfg
}

func TestCherryPickSourceLabelIsTheCheckedOutBranchWhenTheWalkIsNarrow(t *testing.T) {
	withAllBranches(t, false)

	// Every row really is on the checked-out branch in this mode, so the label
	// must read exactly as it did before this feature existed.
	if label := cherryPickSourceLabel("HEAD -> master, origin/master", "master"); label != "master" {
		t.Errorf("label = %q, want the checked-out branch", label)
	}
	if label := cherryPickSourceLabel("", "master"); label != "master" {
		t.Errorf("label = %q, want the checked-out branch even for an undecorated commit", label)
	}
}

func TestCherryPickSourceLabelIsTheCommitsOwnRefsWhenTheWalkIsWide(t *testing.T) {
	withAllBranches(t, true)

	if label := cherryPickSourceLabel("feature, origin/feature", "master"); label != "feature, origin/feature" {
		t.Errorf("label = %q, want the commit's own refs rather than the checked-out branch", label)
	}
}

func TestCherryPickSourceLabelSaysNothingForACommitWithNoRefs(t *testing.T) {
	withAllBranches(t, true)

	// Naming the checked-out branch here would assert a provenance the commit
	// does not have, in a popup that goes on to apply it.
	if label := cherryPickSourceLabel("", "master"); label != "" {
		t.Errorf("label = %q, want nothing rather than a branch the commit is not on", label)
	}
}
