package layout

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/gohyuhan/gitti/api/git"
	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/tui/constant"
	"github.com/gohyuhan/gitti/tui/types"
)

// ------------------------------------
//
//	gitStatusPanelModel builds a left-column model in the named observation
//	state with the matching payload: tracked carries counts and the upstream,
//	unavailable carries a stale tracked payload that must not be rendered
//
// ------------------------------------
func gitStatusPanelModel(t *testing.T, state git.UpstreamObservationState) *types.GittiModel {
	t.Helper()
	model := initScreenModeLayoutModel(t, 100, 30)
	model.RepoName = "repository"
	model.CheckOutBranch = "master"
	model.RemoteSyncObservationState = state
	model.RemoteSyncObservedBranch = "master"
	switch state {
	case git.UpstreamStateTracked:
		model.RemoteSyncLocalState = "3"
		model.RemoteSyncRemoteState = "2"
		model.BranchUpStream = "origin/master"
		model.TrackedUpstreamOrBranchIcon = "\uea84"
	case git.UpstreamStateUnavailable:
		// last-good payload from before the read failed
		model.RemoteSyncLocalState = "3"
		model.RemoteSyncRemoteState = "2"
		model.BranchUpStream = "origin/master"
		model.TrackedUpstreamOrBranchIcon = "\uea84"
	}
	return model
}

func TestGitStatusPanelRendersEachObservationState(t *testing.T) {
	tests := []struct {
		name      string
		state     git.UpstreamObservationState
		want      []string
		doNotWant []string
	}{
		{
			name:  "tracked renders the ahead and behind counters with the upstream",
			state: git.UpstreamStateTracked,
			want:  []string{"3↑", "2↓", "origin/master"},
		},
		{
			name:      "unpublished renders the neutral local only marker with the local branch",
			state:     git.UpstreamStateUnpublished,
			want:      []string{"Local only", "master"},
			doNotWant: []string{"origin/master"},
		},
		{
			name:      "pending renders the non-failure loading marker",
			state:     git.UpstreamStatePending,
			want:      []string{"…"},
			doNotWant: []string{"Local only", "3↑"},
		},
		{
			name:  "not applicable renders the neutral no branch to sync marker",
			state: git.UpstreamStateNotApplicable,
			want:  []string{"No branch to sync"},
		},
		{
			name:      "unavailable renders the warning with the localized text and no stale payload",
			state:     git.UpstreamStateUnavailable,
			want:      []string{"Upstream unavailable", "master"},
			doNotWant: []string{"3↑", "2↓", "origin/master"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := gitStatusPanelModel(t, tt.state)
			rendered := ansi.Strip(renderGitStatusComponentPanel(60, 1, model))

			for _, wanted := range tt.want {
				if !strings.Contains(rendered, wanted) {
					t.Errorf("panel = %q, want it to contain %q", rendered, wanted)
				}
			}
			for _, absent := range tt.doNotWant {
				if strings.Contains(rendered, absent) {
					t.Errorf("panel = %q, want it to not contain %q", rendered, absent)
				}
			}
		})
	}
}

func TestGitStatusPanelUnpublishedMarkerFollowsTheCheckedOutBranch(t *testing.T) {
	model := gitStatusPanelModel(t, git.UpstreamStateUnpublished)
	model.RemoteSyncObservedBranch = "other-branch"

	rendered := ansi.Strip(renderGitStatusComponentPanel(60, 1, model))
	if strings.Contains(rendered, "Local only") {
		t.Errorf("panel = %q, want no Local only marker for a stale observed branch", rendered)
	}
	if !strings.Contains(rendered, "…") {
		t.Errorf("panel = %q, want the pending marker while the observation is out of order", rendered)
	}
}

// ------------------------------------
//
//	TestGitStatusPanelTrackedPayloadFollowsTheCheckedOutBranch proves the
//	tracked counters and upstream render only while the observed branch is
//	the checked-out branch, and a stale or empty observation branch falls
//	back to the pending marker with the local branch name
//
// ------------------------------------
func TestGitStatusPanelTrackedPayloadFollowsTheCheckedOutBranch(t *testing.T) {
	for _, observedBranch := range []string{"other-branch", ""} {
		model := gitStatusPanelModel(t, git.UpstreamStateTracked)
		model.RemoteSyncObservedBranch = observedBranch

		rendered := ansi.Strip(renderGitStatusComponentPanel(60, 1, model))
		for _, stale := range []string{"3↑", "2↓", "origin/master"} {
			if strings.Contains(rendered, stale) {
				t.Errorf("observed branch %q: panel = %q, want no stale tracked payload for %q", observedBranch, rendered, stale)
			}
		}
		if !strings.Contains(rendered, "…") {
			t.Errorf("observed branch %q: panel = %q, want the pending marker while the observation is out of order", observedBranch, rendered)
		}
		if !strings.Contains(rendered, "master") {
			t.Errorf("observed branch %q: panel = %q, want the checked-out branch name", observedBranch, rendered)
		}
	}
}

func TestGitStatusPanelInProgressStateStillTakesPrecedence(t *testing.T) {
	model := gitStatusPanelModel(t, git.UpstreamStateUnpublished)
	model.CurrentGitRepoStatus = "MERGE"

	rendered := ansi.Strip(renderGitStatusComponentPanel(60, 1, model))
	if !strings.Contains(rendered, "MERGE IN PROGRESS") {
		t.Errorf("panel = %q, want the in-progress state line", rendered)
	}
	if strings.Contains(rendered, "Local only") {
		t.Errorf("panel = %q, want no observation marker while a git state is in progress", rendered)
	}
}

// ------------------------------------
//
//	TestGitStatusPanelPreservesWidthBudgetAcrossLocalesAndModes renders
//	every observation state in every locale at each width a screen mode can
//	give the panel, and asserts no rendered line exceeds the panel width
//
// ------------------------------------
func TestGitStatusPanelPreservesWidthBudgetAcrossLocalesAndModes(t *testing.T) {
	states := []git.UpstreamObservationState{
		git.UpstreamStatePending,
		git.UpstreamStateTracked,
		git.UpstreamStateUnpublished,
		git.UpstreamStateNotApplicable,
		git.UpstreamStateUnavailable,
	}
	// widths a left-column panel actually sees in the three screen modes at
	// a 120 column terminal, plus narrow widths down to the panel minimum
	widths := []int{12, 16, 20, 24, 30, 36, 60, 100}

	for _, locale := range []string{"en", "ja", "zh-hans", "zh-hant"} {
		for _, state := range states {
			model := gitStatusPanelModel(t, state)
			i18n.InitGittiLanguageMapping(locale)
			model.RepoName = "very-long-repository-name"
			for _, width := range widths {
				rendered := renderGitStatusComponentPanel(width, 1, model)
				for _, line := range strings.Split(rendered, "\n") {
					if got := ansi.StringWidth(line); got > width {
						t.Errorf("locale %s state %s width %d: line %q has display width %d", locale, state, width, line, got)
					}
				}
			}
		}
	}
}

// ------------------------------------
//
//	TestGitStatusKeybindingAdvertisesPublishOnlyForTheCurrentUnpublishedBranch
//	proves the keybinding bar advertises the publication hint only when the
//	observation is unpublished for the branch the model is showing, and
//	keeps the default instruction otherwise
//
// ------------------------------------
func TestGitStatusKeybindingAdvertisesPublishOnlyForTheCurrentUnpublishedBranch(t *testing.T) {
	model := gitStatusPanelModel(t, git.UpstreamStateUnpublished)
	model.CurrentSelectedComponent = constant.GitStatusComponentPanel
	defaultHint := i18n.LANGUAGEMAPPING.KeyBindingForGitStatusComponent
	if rendered := ansi.Strip(renderKeyBindingComponentPanel(80, model)); !strings.Contains(rendered, i18n.LANGUAGEMAPPING.GitStatusPanelPublishBranchHint) {
		t.Errorf("keybinding bar = %q, want the publish branch hint", rendered)
	}

	// a stale observed branch must not advertise publishing
	model.RemoteSyncObservedBranch = "other-branch"
	if rendered := ansi.Strip(renderKeyBindingComponentPanel(80, model)); !strings.Contains(rendered, defaultHint[0]) {
		t.Errorf("keybinding bar = %q, want the default instruction for a stale observed branch", rendered)
	}

	// every other state keeps the default instruction
	for _, state := range []git.UpstreamObservationState{
		git.UpstreamStatePending,
		git.UpstreamStateTracked,
		git.UpstreamStateNotApplicable,
		git.UpstreamStateUnavailable,
	} {
		model = gitStatusPanelModel(t, state)
		model.CurrentSelectedComponent = constant.GitStatusComponentPanel
		if rendered := ansi.Strip(renderKeyBindingComponentPanel(80, model)); !strings.Contains(rendered, defaultHint[0]) {
			t.Errorf("state %s: keybinding bar = %q, want the default instruction", state, rendered)
		}
	}
}
