package interaction

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/gohyuhan/gitti/api"
	"github.com/gohyuhan/gitti/api/git"
	"github.com/gohyuhan/gitti/tui/constant"
	"github.com/gohyuhan/gitti/tui/layout"
)

func TestPanelFilterTreatsModeKeysAsQueryText(t *testing.T) {
	model := initMouseScreenModeModel(t)
	model.ScreenMode = constant.ScreenModeSingleColumn
	model.CurrentSelectedComponent = constant.LocalBranchOrTagOrRemoteOrWorktreeComponentPanel
	model.CurrentLocalBranchOrTagOrRemoteOrWorktreeComponentShowing = constant.SHOW_LOCAL_BRANCH
	model.GitOperations = &api.GitOperations{GitBranch: git.InitGitBranch(nil, false, nil)}
	model.IsPanelFiltering.Store(true)
	layout.TuiWindowSizing(model)

	GittiKeyInteraction(interactionKeyPress("="), model)
	GittiKeyInteraction(interactionKeyPress("_"), model)

	if got := model.PanelFilterQuery[constant.SHOW_LOCAL_BRANCH]; got != "=_" {
		t.Errorf("filter query = %q, want %q", got, "=_")
	}
	if model.ScreenMode != constant.ScreenModeSingleColumn {
		t.Errorf("ScreenMode = %d, want filter input to retain Single-column", model.ScreenMode)
	}
}

func interactionKeyPress(key string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Text: key, Code: []rune(key)[0]})
}
