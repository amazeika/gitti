package nontyping

import (
	tea "charm.land/bubbletea/v2"
	"github.com/gohyuhan/gitti/tui/constant"
	"github.com/gohyuhan/gitti/tui/interaction/handler/keyutil"
	"github.com/gohyuhan/gitti/tui/services"
	"github.com/gohyuhan/gitti/tui/types"
)

// handleNonTypingPageKeyBindingInteraction moves the focused list or viewport
// by one visible page. List selection changes follow the same state and detail
// refresh path as single-row navigation.
func handleNonTypingPageKeyBindingInteraction(msg tea.KeyPressMsg, m *types.GittiModel) (*types.GittiModel, tea.Cmd) {
	if m.ShowPopUp.Load() {
		return keyutil.PageKeyPressMsgUpdateForPopUp(msg, m)
	}

	direction := 1
	if msg.String() == "pgup" {
		direction = -1
	}

	selectionChanged := false
	switch m.CurrentSelectedComponent {
	case constant.LocalBranchOrTagOrRemoteOrWorktreeComponentPanel:
		switch m.CurrentLocalBranchOrTagOrRemoteOrWorktreeComponentShowing {
		case constant.SHOW_LOCAL_BRANCH:
			m.ListNavigationIndexPosition.LocalBranchComponent, selectionChanged = keyutil.MoveListSelectionByPage(&m.CurrentRepoBranchesInfoList, direction, visibleListRows(m.LocalBranchesComponentPanelHeight))
		case constant.SHOW_TAG:
			m.ListNavigationIndexPosition.TagComponent, selectionChanged = keyutil.MoveListSelectionByPage(&m.CurrentRepoTagInfoList, direction, visibleListRows(m.TagsComponentPanelHeight))
		case constant.SHOW_REMOTE:
			m.ListNavigationIndexPosition.RemoteComponent, selectionChanged = keyutil.MoveListSelectionByPage(&m.CurrentRepoRemoteInfoList, direction, visibleListRows(m.RemoteComponentPanelHeight))
		case constant.SHOW_WORKTREE:
			m.ListNavigationIndexPosition.WorktreeComponent, selectionChanged = keyutil.MoveListSelectionByPage(&m.CurrentRepoWorktreeInfoList, direction, visibleListRows(m.WorktreeComponentPanelHeight))
		}
	case constant.ModifiedFilesComponentPanel:
		m.ListNavigationIndexPosition.ModifiedFilesComponent, selectionChanged = keyutil.MoveListSelectionByPage(&m.CurrentRepoModifiedFilesInfoList, direction, visibleListRows(m.ModifiedFilesComponentPanelHeight))
	case constant.CommitLogOrRefLogComponentPanel:
		switch m.CurrentCommitLogOrRefLogComponentShowing {
		case constant.SHOW_COMMITLOG:
			m.ListNavigationIndexPosition.CommitLogComponent, selectionChanged = keyutil.MoveListSelectionByPage(&m.CurrentRepoCommitLogInfoList, direction, visibleListRows(m.CommitLogComponentPanelHeight))
		case constant.SHOW_REFLOG:
			m.ListNavigationIndexPosition.RefLogComponent, selectionChanged = keyutil.MoveListSelectionByPage(&m.CurrentRepoRefLogInfoList, direction, visibleListRows(m.RefLogComponentPanelHeight))
		}
	case constant.StashComponentPanel:
		m.ListNavigationIndexPosition.StashComponent, selectionChanged = keyutil.MoveListSelectionByPage(&m.CurrentRepoStashInfoList, direction, visibleListRows(m.StashComponentPanelHeight))
	case constant.DetailComponentPanel:
		pageDetailViewport(m, false, direction)
	case constant.DetailComponentPanelTwo:
		pageDetailViewport(m, true, direction)
	}

	if selectionChanged {
		services.FetchDetailComponentPanelInfoService(m, true)
	}
	return m, nil
}

// Main lists reserve one line each for their title and item counter.
func visibleListRows(panelHeight int) int {
	return max(1, panelHeight-2)
}

func pageDetailViewport(m *types.GittiModel, secondPanel bool, direction int) {
	if !m.IsLineEditingState.Load() {
		if secondPanel {
			if direction < 0 {
				m.DetailPanelTwoViewport.PageUp()
			} else {
				m.DetailPanelTwoViewport.PageDown()
			}
		} else if direction < 0 {
			m.DetailPanelViewport.PageUp()
		} else {
			m.DetailPanelViewport.PageDown()
		}
		return
	}

	// Line-editing mode owns a cursor in addition to the viewport offset. Reuse
	// row navigation for one visible page so both stay synchronized.
	pageSize := m.DetailPanelViewport.VisibleLineCount()
	if secondPanel {
		pageSize = m.DetailPanelTwoViewport.VisibleLineCount()
	}
	for range max(1, pageSize) {
		if direction < 0 {
			handleNonTypingUpkKeyBindingInteraction(tea.KeyPressMsg{}, m)
		} else {
			handleNonTypingDownjKeyBindingInteraction(tea.KeyPressMsg{}, m)
		}
	}
}
