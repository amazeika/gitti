package keyutil

import (
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/gohyuhan/gitti/tui/constant"
	blamePopUp "github.com/gohyuhan/gitti/tui/popup/blame"
	branchPopUp "github.com/gohyuhan/gitti/tui/popup/branch"
	commitPopUp "github.com/gohyuhan/gitti/tui/popup/commit"
	commitLogPopUp "github.com/gohyuhan/gitti/tui/popup/commitlog"
	discardPopUp "github.com/gohyuhan/gitti/tui/popup/discard"
	interactiverebasePopUp "github.com/gohyuhan/gitti/tui/popup/interactive-rebase"
	pullPopUp "github.com/gohyuhan/gitti/tui/popup/pull"
	pushPopUp "github.com/gohyuhan/gitti/tui/popup/push"
	remotePopUp "github.com/gohyuhan/gitti/tui/popup/remote"
	resolvePopUp "github.com/gohyuhan/gitti/tui/popup/resolve"
	tagPopUp "github.com/gohyuhan/gitti/tui/popup/tag"
	"github.com/gohyuhan/gitti/tui/types"
)

// ------------------------------------
//
//	Page the active popup list or viewport up or down by its visible height.
//
// ------------------------------------
func PageKeyPressMsgUpdateForPopUp(msg tea.KeyPressMsg, m *types.GittiModel) (*types.GittiModel, tea.Cmd) {
	direction := 1
	if msg.String() == "pgup" {
		direction = -1
	}

	var popupList *list.Model
	switch m.PopUpType {
	case constant.ChooseRemotePopUp:
		if popup, ok := m.PopUpModel.(*remotePopUp.ChooseRemotePopUpModel); ok {
			popupList = &popup.RemoteList
		}
	case constant.ChoosePushTypePopUp:
		if popup, ok := m.PopUpModel.(*pushPopUp.ChoosePushTypePopUpModel); ok {
			popupList = &popup.PushOptionList
		}
	case constant.ChooseNewBranchTypePopUp:
		if popup, ok := m.PopUpModel.(*branchPopUp.ChooseNewBranchTypeOptionPopUpModel); ok {
			popupList = &popup.NewBranchTypeOptionList
		}
	case constant.ChooseSwitchBranchTypePopUp:
		if popup, ok := m.PopUpModel.(*branchPopUp.ChooseSwitchBranchTypePopUpModel); ok {
			popupList = &popup.SwitchTypeOptionList
		}
	case constant.ChooseGitPullTypePopUp:
		if popup, ok := m.PopUpModel.(*pullPopUp.ChooseGitPullTypePopUpModel); ok {
			popupList = &popup.PullTypeOptionList
		}
	case constant.GitDiscardTypeOptionPopUp:
		if popup, ok := m.PopUpModel.(*discardPopUp.GitDiscardTypeOptionPopUpModel); ok {
			popupList = &popup.DiscardTypeOptionList
		}
	case constant.GitResolveConflictOptionPopUp:
		if popup, ok := m.PopUpModel.(*resolvePopUp.GitResolveConflictOptionPopUpModel); ok {
			popupList = &popup.ResolveConflictOptionList
		}
	case constant.GitResetLatestCommitTypeOptionPopUp:
		if popup, ok := m.PopUpModel.(*commitPopUp.GitResetLatestCommitTypeOptionPopUpModel); ok {
			popupList = &popup.ResetLatestCommitTypeOptionList
		}
	case constant.GitResetToSelectedCommitTypeOptionPopUp:
		if popup, ok := m.PopUpModel.(*commitPopUp.GitResetToSelectedCommitTypeOptionPopUpModel); ok {
			popupList = &popup.ResetToSelectedCommitTypeOptionList
		}
	case constant.GitCherryPickOptionSelectionPopUp:
		if popup, ok := m.PopUpModel.(*commitLogPopUp.GitCherryPickOptionSelectionPopUpModel); ok {
			popupList = &popup.CherryPickedOpsOption
		}
	case constant.GitCherryPickPopUp:
		if popup, ok := m.PopUpModel.(*commitLogPopUp.GitCherryPickPopUpModel); ok {
			popupList = &popup.CurrentBranchCherryPickCommitLog
		}
	case constant.GitEditCherryPickPopUp:
		if popup, ok := m.PopUpModel.(*commitLogPopUp.GitEditCherryPickPopUpModel); ok {
			popupList = &popup.CherryPickedCommitLog
		}
	case constant.ChooseDeleteTagOptionPopUp:
		if popup, ok := m.PopUpModel.(*tagPopUp.ChooseDeleteTagOptionPopUpModel); ok {
			popupList = &popup.DeleteOptionList
		}
	case constant.ChooseRemoteForDeleteRemoteTagPopUp:
		if popup, ok := m.PopUpModel.(*tagPopUp.ChooseRemoteForDeleteRemoteTagPopUpModel); ok {
			popupList = &popup.RemoteList
		}
	case constant.ChoosePushTagOptionPopUp:
		if popup, ok := m.PopUpModel.(*tagPopUp.ChoosePushTagOptionPopUpModel); ok {
			popupList = &popup.PushOptionList
		}
	case constant.ChooseFetchTagOptionPopUp:
		if popup, ok := m.PopUpModel.(*tagPopUp.ChooseFetchTagOptionPopUpModel); ok {
			popupList = &popup.FetchOptionList
		}
	case constant.GitRevertParentOptionSelectionPopUp:
		if popup, ok := m.PopUpModel.(*commitLogPopUp.GitRevertParentOptionSelectionPopUpModel); ok {
			popupList = &popup.GitRevertParentOption
		}
	case constant.ChooseRemoteBranchOptionPopUp:
		if popup, ok := m.PopUpModel.(*branchPopUp.ChooseRemoteBranchOptionPopUpModel); ok {
			popupList = &popup.RemoteBranchOptionList
		}
	case constant.ChooseBranchOptionForMergePopUp:
		if popup, ok := m.PopUpModel.(*branchPopUp.ChooseBranchOptionForMergePopUpModel); ok {
			if popup.BranchOptionSectionSelected.Load() {
				popupList = &popup.BranchOptionList
			} else if popup.SelectedBranchSectionSelected.Load() {
				popupList = &popup.SelectedBranchList
			}
		}
	case constant.InteractiveRebaseOptionPopUp:
		if popup, ok := m.PopUpModel.(*interactiverebasePopUp.InteractiveRebaseOptionPopUpModel); ok {
			popupList = &popup.InteractiveRebaseOptionList
		}
	case constant.InteractiveRebaseRewordSelectionPopUp:
		if popup, ok := m.PopUpModel.(*interactiverebasePopUp.InteractiveRebaseRewordSelectionPopUpModel); ok {
			popupList = &popup.CommitList
		}
	case constant.InteractiveRebaseDropSelectionPopUp:
		if popup, ok := m.PopUpModel.(*interactiverebasePopUp.InteractiveRebaseDropSelectionPopUpModel); ok {
			popupList = &popup.CommitList
		}
	case constant.InteractiveRebaseFixupSquashSelectionPopUp:
		if popup, ok := m.PopUpModel.(*interactiverebasePopUp.InteractiveRebaseFixupSquashSelectionPopUpModel); ok {
			if popup.IsCommitListSelected {
				popupList = &popup.CommitList
			} else if popup.IsCommitFixupSquashViewportSelected {
				triggerViewportVerticalScrollFromKey(msg, &popup.CommitFixupSquashViewport)
				return m, nil
			}
		}
	case constant.BlamePopUp:
		if popup, ok := m.PopUpModel.(*blamePopUp.BlamePopUpModel); ok {
			if !popup.ShowingBlameInfo {
				popupList = &popup.CurrentGitTrackedFilesPathList
			} else {
				triggerViewportVerticalScrollFromKey(msg, &popup.BlameViewport)
				return m, nil
			}
		}
	}

	if popupList != nil {
		MoveListSelectionByPage(popupList, direction, 0)
		return m, nil
	}

	// Existing popup viewport routing also applies to page keys.
	return UpDownKeyPressMsgUpdateForPopUp(msg, m)
}
