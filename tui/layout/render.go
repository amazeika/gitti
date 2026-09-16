package layout

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/gohyuhan/gitti/api/git"
	gitticonst "github.com/gohyuhan/gitti/constant"
	"github.com/gohyuhan/gitti/i18n"
	branchComponent "github.com/gohyuhan/gitti/tui/component/branch"
	filesComponent "github.com/gohyuhan/gitti/tui/component/files"
	worktreeComponent "github.com/gohyuhan/gitti/tui/component/worktree"
	"github.com/gohyuhan/gitti/tui/constant"
	blamePopUp "github.com/gohyuhan/gitti/tui/popup/blame"
	branchPopUp "github.com/gohyuhan/gitti/tui/popup/branch"
	stashPopUp "github.com/gohyuhan/gitti/tui/popup/stash"
	"github.com/gohyuhan/gitti/tui/style"
	"github.com/gohyuhan/gitti/tui/types"
	"github.com/gohyuhan/gitti/tui/utils"
)

// ------------------------------------
//
//	Render the git status panel (top of the left column). Shows the repo name,
//	tracked branch with upstream icon, and remote sync counters (↑/↓). When a
//	git operation is in progress (e.g. rebase, merge), shows a warning line
//	instead.
//
// ------------------------------------
func renderGitStatusComponentPanel(width int, height int, m *types.GittiModel) string {
	borderStyle := style.PanelBorderStyle
	if m.CurrentSelectedComponent == constant.GitStatusComponentPanel {
		borderStyle = style.SelectedBorderStyle
	}
	if m.CurrentGitRepoStatus == "" {
		availableWidth := width - constant.ListItemOrTitleWidthPad
		// the prefix is measured before the repo/branch portion is
		// truncated, so the whole line always fits the panel's width budget
		remoteSyncStateLineString := remoteSyncStatusPrefix(availableWidth, m)
		additionalWidth := lipgloss.Width(remoteSyncStateLineString)

		// the tracked payload is exposed only when the observed branch is
		// the branch the model is actually showing, so an out-of-order
		// branch or remote event never renders another branch's upstream
		// or counts
		trackedMatchesCheckedOut := m.RemoteSyncObservedBranch != "" && m.RemoteSyncObservedBranch == m.CheckOutBranch
		trackedUpStreamOrBranchName := m.CheckOutBranch
		if trackedMatchesCheckedOut && m.RemoteSyncObservationState == git.UpstreamStateTracked && m.BranchUpStream != "" {
			trackedUpStreamOrBranchName = m.BranchUpStream
		}

		upStreamIcon := m.TrackedUpstreamOrBranchIcon
		if upStreamIcon == "" {
			upStreamIcon = git.DefaultUpStreamRemoteIcon
		}

		repoTrackBranchName := fmt.Sprintf(" %s -> %s %s", m.RepoName, upStreamIcon, trackedUpStreamOrBranchName)

		repoTrackBranchName = utils.TruncateString(repoTrackBranchName, availableWidth-additionalWidth)

		return borderStyle.
			Width(width).
			Height(height).
			Render(fmt.Sprintf("%s%s", remoteSyncStateLineString, repoTrackBranchName))
	} else {
		var gitStateInProgress string

		gitStateInProgress = utils.TruncateString(fmt.Sprintf(i18n.LANGUAGEMAPPING.GitCertainStateStillInProgress, m.CurrentGitRepoStatus), width-constant.ListItemOrTitleWidthPad-2)

		return borderStyle.
			Width(width).
			Height(height).
			Render(fmt.Sprintf("%s %s", lipgloss.NewStyle().Foreground(style.ColorError).Render("!"), gitStateInProgress))
	}
}

// ------------------------------------
//
//	Render the remote-sync prefix for the git status panel from the typed
//	upstream observation state. The old empty-string inference (counts
//	present = up to date, counts absent = not synced) is gone, so a failed
//	read is never rendered as a first push. tracked shows the ahead/behind
//	counters only when the observed branch matches the checked-out branch,
//	and falls back to the pending marker on a mismatch. unpublished shows a
//	neutral "Local only" marker for the checked-out branch only.
//	not-applicable and pending show non-failure markers. unavailable shows
//	an error marker with localized text instead of a stale count.
//	The prefix is measured before the repo/branch portion is truncated.
//	A label wider than the panel's content budget (e.g. CJK in a narrow
//	panel) falls back to a plain truncated label, so the line always fits.
//
// ------------------------------------
func remoteSyncStatusPrefix(availableWidth int, m *types.GittiModel) string {
	var styled string
	switch m.RemoteSyncObservationState {
	case git.UpstreamStateTracked:
		if m.RemoteSyncObservedBranch == "" || m.RemoteSyncObservedBranch != m.CheckOutBranch {
			// a tracked payload from another branch renders as pending
			// until the observation catches up
			styled = style.NeutralStatusStyle.Render(i18n.LANGUAGEMAPPING.GitStatusPanelPending)
			break
		}
		local := style.LocalStatusStyle.Render(fmt.Sprintf("%s↑", m.RemoteSyncLocalState))
		remote := style.RemoteStatusStyle.Render(fmt.Sprintf("%s↓", m.RemoteSyncRemoteState))
		styled = local + " " + remote
	case git.UpstreamStateUnpublished:
		// the Local only marker is only exposed when the observed branch is
		// the branch the model is actually showing, so an out-of-order
		// branch or remote event never advertises publishing for a stale
		// state
		if m.RemoteSyncObservedBranch != "" && m.RemoteSyncObservedBranch == m.CheckOutBranch {
			styled = style.LocalOnlyStatusStyle.Render(i18n.LANGUAGEMAPPING.GitStatusPanelLocalOnly)
		} else {
			styled = style.NeutralStatusStyle.Render(i18n.LANGUAGEMAPPING.GitStatusPanelPending)
		}
	case git.UpstreamStateNotApplicable:
		styled = style.NeutralStatusStyle.Render(i18n.LANGUAGEMAPPING.GitStatusPanelNotApplicable)
	case git.UpstreamStateUnavailable:
		marker := lipgloss.NewStyle().Foreground(style.ColorError).Render("!")
		styled = fmt.Sprintf("%s %s", marker, style.UnavailableStatusStyle.Render(i18n.LANGUAGEMAPPING.GitStatusPanelUpstreamUnavailable))
	case git.UpstreamStatePending:
		styled = style.NeutralStatusStyle.Render(i18n.LANGUAGEMAPPING.GitStatusPanelPending)
	default:
		styled = style.NeutralStatusStyle.Render(i18n.LANGUAGEMAPPING.GitStatusPanelPending)
	}
	if lipgloss.Width(styled) > availableWidth {
		return utils.TruncateString(ansi.Strip(styled), availableWidth)
	}
	return styled
}

// ------------------------------------
//
//	Render the left-column panel that switches between local branches, tags,
//	remotes, and worktrees based on m.CurrentLocalBranchOrTagOrRemoteOrWorktreeComponentShowing.
//
// ------------------------------------
func renderLocalBranchesOrTagOrRemoteOrWorktreeComponentPanel(width int, height int, m *types.GittiModel) string {
	borderStyle := style.PanelBorderStyle
	if m.CurrentSelectedComponent == constant.LocalBranchOrTagOrRemoteOrWorktreeComponentPanel {
		borderStyle = style.SelectedBorderStyle
	}
	var content string
	switch m.CurrentLocalBranchOrTagOrRemoteOrWorktreeComponentShowing {
	case constant.SHOW_LOCAL_BRANCH:
		content = m.CurrentRepoBranchesInfoList.View()
	case constant.SHOW_TAG:
		content = m.CurrentRepoTagInfoList.View()
	case constant.SHOW_REMOTE:
		content = m.CurrentRepoRemoteInfoList.View()
	case constant.SHOW_WORKTREE:
		content = m.CurrentRepoWorktreeInfoList.View()
	}
	return renderBorderedPanel(borderStyle, width, height, content)
}

// ------------------------------------
//
//	Render the modified files panel showing the list of staged/unstaged/untracked
//	and conflicted files in the working tree.
//
// ------------------------------------
func renderModifiedFilesComponentPanel(width int, height int, m *types.GittiModel) string {
	borderStyle := style.PanelBorderStyle
	if m.CurrentSelectedComponent == constant.ModifiedFilesComponentPanel {
		borderStyle = style.SelectedBorderStyle
	}
	return renderBorderedPanel(borderStyle, width, height, m.CurrentRepoModifiedFilesInfoList.View())
}

// ------------------------------------
//
//	Render the commit log / reflog panel, switching between commit history and
//	reflog views based on m.CurrentCommitLogOrRefLogComponentShowing.
//
// ------------------------------------
func renderCommitLogOrRefLogComponentPanel(width int, height int, m *types.GittiModel) string {
	borderStyle := style.PanelBorderStyle
	if m.CurrentSelectedComponent == constant.CommitLogOrRefLogComponentPanel {
		borderStyle = style.SelectedBorderStyle
	}

	var content string
	switch m.CurrentCommitLogOrRefLogComponentShowing {
	case constant.SHOW_COMMITLOG:
		content = m.CurrentRepoCommitLogInfoList.View()
	case constant.SHOW_REFLOG:
		content = m.CurrentRepoRefLogInfoList.View()
	}

	return renderBorderedPanel(borderStyle, width, height, content)
}

// ------------------------------------
//
//	Render the detail component panel (right side of the window).
//	Content is dynamic based on the current selected component.
//	Handles both normal mode and line-editing mode layout, including
//	the split staged/unstaged view in line-editing mode.
//
// ------------------------------------
func renderDetailComponentPanel(width int, height int, m *types.GittiModel) string {
	detailComponentBorderStyle := style.PanelBorderStyle
	detailComponentTwoBorderStyle := style.PanelBorderStyle

	// determine the border style based on the current selected component
	switch m.CurrentSelectedComponent {
	case constant.DetailComponentPanel:
		detailComponentBorderStyle = style.SelectedBorderStyle
	case constant.DetailComponentPanelTwo:
		detailComponentTwoBorderStyle = style.SelectedBorderStyle
	}

	var content string
	ogHeight := height
	ogWidth := width

	// if it is in line editing mode, we need to minus 3 for the title
	if m.IsLineEditingState.Load() {
		ogHeight = height - 3
	}

	if m.IsLineEditingState.Load() {
		// we first define the border height
		borderHeight := 2
		if m.ShowDetailPanelTwo.Load() {
			detailPanelHeight := int(ogHeight / 2)
			detailPanelWidth := int(ogWidth / 2)
			if m.DetailComponentPanelLayout == constant.HORIZONTAL {
				// Horizontal Split Layout for Line Editing
				// Join Cursor Viewport + Content Viewport for Panel 1
				detailPanelViewportContent := lipgloss.JoinHorizontal(
					lipgloss.Top,
					style.NewStyle.Width(3).Height(ogHeight-borderHeight).Render(m.LineEditingIndexCursorViewport.View()),
					style.NewStyle.Width(detailPanelWidth-3).Height(ogHeight-borderHeight).Render(m.DetailPanelViewport.View()),
				)
				// Join Cursor Viewport + Content Viewport for Panel 2
				detailPanelTwoViewportContent := lipgloss.JoinHorizontal(
					lipgloss.Top,
					style.NewStyle.Width(3).Height(ogHeight-borderHeight).Render(m.LineEditingIndexCursorTwoViewport.View()),
					style.NewStyle.Width(detailPanelWidth-3).Height(ogHeight-borderHeight).Render(m.DetailPanelTwoViewport.View()),
				)

				// Combine both panels horizontally
				content = lipgloss.JoinHorizontal(
					lipgloss.Top,
					detailComponentBorderStyle.Width(detailPanelWidth).Height(ogHeight).Render(detailPanelViewportContent),
					detailComponentTwoBorderStyle.Width(width-detailPanelWidth).Height(ogHeight).Render(detailPanelTwoViewportContent),
				)
			} else {
				// Vertical Split Layout for Line Editing
				// Join Cursor + Content for Panel 1 (Top)
				detailPanelViewportContent := lipgloss.JoinHorizontal(
					lipgloss.Top,
					style.NewStyle.Width(3).Height(detailPanelHeight-borderHeight).Render(m.LineEditingIndexCursorViewport.View()),
					style.NewStyle.Width(ogWidth-3).Height(detailPanelHeight-borderHeight).Render(m.DetailPanelViewport.View()),
				)
				// Join Cursor + Content for Panel 2 (Bottom)
				detailPanelTwoViewportContent := lipgloss.JoinHorizontal(
					lipgloss.Top,
					style.NewStyle.Width(3).Height(ogHeight-detailPanelHeight-borderHeight).Render(m.LineEditingIndexCursorTwoViewport.View()),
					style.NewStyle.Width(ogWidth-3).Height(ogHeight-detailPanelHeight-borderHeight).Render(m.DetailPanelTwoViewport.View()),
				)

				// Combine both panels vertically
				content = lipgloss.JoinVertical(
					lipgloss.Left,
					detailComponentBorderStyle.Width(ogWidth).Height(detailPanelHeight).Render(detailPanelViewportContent),
					detailComponentTwoBorderStyle.Width(ogWidth).Height(ogHeight-detailPanelHeight).Render(detailPanelTwoViewportContent),
				)
			}
		} else {
			// Single Panel Layout for Line Editing
			detailPanelViewportContent := lipgloss.JoinHorizontal(
				lipgloss.Top,
				style.NewStyle.Width(3).Height(ogHeight-borderHeight).Render(m.LineEditingIndexCursorViewport.View()),
				style.NewStyle.Width(ogWidth-3).Height(ogHeight-borderHeight).Render(m.DetailPanelViewport.View()),
			)
			content = lipgloss.JoinHorizontal(
				lipgloss.Top,
				detailComponentBorderStyle.Width(ogWidth).Height(ogHeight).Render(detailPanelViewportContent),
			)
		}
	} else {
		// Standard Rendering (Not Line Editing Mode)
		if m.ShowDetailPanelTwo.Load() {
			detailPanelHeight := int(ogHeight / 2)
			detailPanelWidth := int(ogWidth / 2)
			if m.DetailComponentPanelLayout == constant.HORIZONTAL {
				content = lipgloss.JoinHorizontal(
					lipgloss.Top,
					detailComponentBorderStyle.Width(detailPanelWidth).Height(ogHeight).Render(m.DetailPanelViewport.View()),
					detailComponentTwoBorderStyle.Width(width-detailPanelWidth).Height(ogHeight).Render(m.DetailPanelTwoViewport.View()),
				)
			} else {
				content = lipgloss.JoinVertical(
					lipgloss.Left,
					detailComponentBorderStyle.Width(ogWidth).Height(detailPanelHeight).Render(m.DetailPanelViewport.View()),
					detailComponentTwoBorderStyle.Width(ogWidth).Height(ogHeight-detailPanelHeight).Render(m.DetailPanelTwoViewport.View()),
				)
			}
		} else {
			content = lipgloss.JoinHorizontal(
				lipgloss.Top,
				detailComponentBorderStyle.Width(ogWidth).Height(ogHeight).Render(m.DetailPanelViewport.View()),
			)
		}
	}

	// Add Title Block for Line Editing Mode
	if m.IsLineEditingState.Load() {
		inLineEditingModeNotifyBlock := style.PanelBorderStyle.Width(ogWidth).Render(utils.TruncateString(i18n.LANGUAGEMAPPING.LineEditingModeTitle, ogWidth-4))
		content = lipgloss.JoinVertical(
			lipgloss.Top,
			inLineEditingModeNotifyBlock,
			content,
		)
	}

	return style.NewStyle.
		Width(width).
		Height(height).
		Render(content)
}

// ------------------------------------
//
//	Render the stash panel showing the list of git stash entries.
//
// ------------------------------------
func renderFocusedDetailComponentPanel(width int, height int, m *types.GittiModel) string {
	borderStyle := style.SelectedBorderStyle
	selectedViewport := &m.DetailPanelViewport
	selectedCursorViewport := &m.LineEditingIndexCursorViewport
	if m.CurrentSelectedComponent == constant.DetailComponentPanelTwo {
		selectedViewport = &m.DetailPanelTwoViewport
		selectedCursorViewport = &m.LineEditingIndexCursorTwoViewport
	}

	panelHeight := height
	content := selectedViewport.View()
	if m.IsLineEditingState.Load() {
		panelHeight -= 3
		content = lipgloss.JoinHorizontal(
			lipgloss.Top,
			style.NewStyle.Width(3).Height(panelHeight).Render(selectedCursorViewport.View()),
			selectedViewport.View(),
		)
	}

	content = borderStyle.Width(width).Height(panelHeight).Render(content)
	if m.IsLineEditingState.Load() {
		lineEditingTitle := style.PanelBorderStyle.Width(width).Render(
			utils.TruncateString(i18n.LANGUAGEMAPPING.LineEditingModeTitle, width-4),
		)
		content = lipgloss.JoinVertical(lipgloss.Top, lineEditingTitle, content)
	}
	return content
}

func renderStashComponentPanel(width int, height int, m *types.GittiModel) string {
	borderStyle := style.PanelBorderStyle
	if m.CurrentSelectedComponent == constant.StashComponentPanel {
		borderStyle = style.SelectedBorderStyle
	}
	return renderBorderedPanel(borderStyle, width, height, m.CurrentRepoStashInfoList.View())
}

// ------------------------------------
//
//	Render the log panel showing the gitti application log viewport at the
//	bottom of the right column.
//
// ------------------------------------
func renderLogComponentPanel(width int, height int, m *types.GittiModel) string {
	borderStyle := style.PanelBorderStyle
	if m.CurrentSelectedComponent == constant.LogComponentPanel {
		borderStyle = style.SelectedBorderStyle
	}
	return renderBorderedPanel(borderStyle, width, height, m.CurrentLogComponentViewport.View())
}

func renderBorderedPanel(borderStyle lipgloss.Style, width int, height int, content string) string {
	lines := strings.Split(content, "\n")
	contentWidth := max(0, width-2)
	for index := range lines {
		lines[index] = ansi.Truncate(lines[index], contentWidth, "…")
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return borderStyle.Width(width).Height(height).Render(strings.Join(lines, "\n"))
}

// ------------------------------------
//
//	Render the keybinding help bar at the bottom of the screen. Shows
//	context-sensitive key hints for the currently focused panel, with the
//	app version right-aligned.
//
// ------------------------------------
func renderKeyBindingComponentPanel(width int, m *types.GittiModel) string {
	keys := []string{""} // to prevent a misconfiguration on key binding will not crash the program

	if m.ShowPopUp.Load() {
		//-----------------------------
		//
		// for popup keybinding render
		//
		//-----------------------------
		switch m.PopUpType {
		case constant.CommitPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForCommitPopUp
		case constant.AmendCommitPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForAmendCommitPopUp
		case constant.AddRemotePromptPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForAddRemotePromptPopUp
		case constant.GitRemotePushPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitRemotePushPopUp
		case constant.ChooseRemotePopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForChooseRemotePopUp
		case constant.ChoosePushTypePopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForChoosePushTypePopUp
		case constant.PublishBranchConfirmationPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForChoosePushTypePopUp
		case constant.ChooseNewBranchTypePopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForChooseNewBranchTypePopUp
		case constant.CreateNewBranchPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForCreateNewBranchPopUp
		case constant.WorktreeAddNewWorktreePopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForWorktreeAddNewWorktreePopUp
		case constant.WorktreeAddNewWorktreeOutputPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForWorktreeAddNewWorktreeOutputPopUp
		case constant.WorktreeLockReasonInputPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForWorktreeLockReasonInputPopUp
		case constant.WorktreeRemoveWorktreeConfirmationPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForWorktreeRemoveConfirmationPopUp
		case constant.ChooseSwitchBranchTypePopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForChooseSwitchBranchTypePopUp
		case constant.SwitchBranchOutputPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForChooseSwitchBranchTypePopUp
			popUp, ok := m.PopUpModel.(*branchPopUp.SwitchBranchOutputPopUpModel)
			if ok {
				if popUp.IsProcessing.Load() {
					keys = []string{"..."} // nothing can be done during switching, only force quit gitti is possible
				}
			}
		case constant.ChooseGitPullTypePopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForChooseGitPullTypePopUp
		case constant.GitPullOutputPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitPullOutputPopUp
		case constant.GitStashMessagePopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitStashMessagePopUp
		case constant.KeybindingAndFeatureInstructionsPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForKeybindingAndFeatureInstructionsPopUp
		case constant.GitDiscardTypeOptionPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitDiscardTypeOptionPopUp
		case constant.GitDiscardConfirmPromptPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitDiscardConfirmPromptPopUp
		case constant.GitStashOperationOutputPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitStashOperationOutputPopUp
			popUp, ok := m.PopUpModel.(*stashPopUp.GitStashOperationOutputPopUpModel)
			if ok {
				if popUp.IsProcessing.Load() {
					keys = []string{"..."} // nothing can be done during stash operation, only force quit gitti is possible
				}
			}
		case constant.GitStashConfirmPromptPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitStashConfirmPromptPopUp
		case constant.GitDeleteBranchConfirmPromptPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitDeleteBranchConfirmPromptPopUp
		case constant.GitDeleteBranchOutputPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitDeleteBranchOutputPopUp
			popUp, ok := m.PopUpModel.(*branchPopUp.GitDeleteBranchOutputPopUpModel)
			if ok {
				if popUp.IsProcessing.Load() {
					keys = []string{"..."} // nothing can be done during stash operation, only force quit gitti is possible
				}
			}
		case constant.CreateBranchBasedOnRemotePopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForCreateBranchBasedOnRemotePopUp
		case constant.CreateBranchBasedOnRemoteOutputPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForCreateBranchBasedOnRemoteOutputPopUp
			popUp, ok := m.PopUpModel.(*branchPopUp.CreateBranchBasedOnRemoteOutputPopUpModel)
			if ok {
				if popUp.IsProcessing.Load() {
					keys = []string{"..."} // nothing can be done during stash operation, only force quit gitti is possible
				}
			}
		case constant.GitResetLatestCommitTypeOptionPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitResetLatestCommitTypeOptionPopUp
		case constant.GitResetLatestCommitConfirmPromptPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitResetLatestCommitConfirmPromptPopUp
		case constant.GitResetToSelectedCommitTypeOptionPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitResetToSelectedCommitTypeOptionPopUp
		case constant.GitResetToSelectedCommitConfirmPromptPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitResetToSelectedCommitConfirmPromptPopUp
		case constant.GitCherryPickOptionSelectionPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitCherryPickOptionSelectionPopUp
		case constant.GitCherryPickPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitCherryPickPopUp
		case constant.GitEditCherryPickPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitEditCherryPickPopUp
		case constant.GitCherryPickApplyConfirmPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitCherryPickApplyConfirmPopUp
		case constant.GitDiscardFileLineChangeConfirmPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitDiscardFileLineChangeConfirmPopUp
		case constant.CreateTagPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForCreateTagPopUp
		case constant.CreateTagConfirmationPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForCreateTagConfirmationPopUp
		case constant.ChooseDeleteTagOptionPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForChooseDeleteTagOptionPopUp
		case constant.ChooseRemoteForDeleteRemoteTagPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForChooseRemoteForDeleteRemoteTagPopUp
		case constant.DeleteTagOutputPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForDeleteTagOutputPopUp
		case constant.ChoosePushTagOptionPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForChoosePushTagOptionPopUp
		case constant.PushTagOutputPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForPushTagOutputPopUp
		case constant.ChooseFetchTagOptionPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForChooseFetchTagOptionPopUp
		case constant.FetchTagOutputPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForFetchTagOutputPopUp
		case constant.RemoveRemoteConfirmationPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForRemoveRemoteConfirmationPopUp
		case constant.RemoteAsTrackingUpstreamConfirmationPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForRemoteAsTrackingUpstreamConfirmationPopUp
		case constant.EditRemotePromptPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForEditRemotePromptPopUp
		case constant.GitRevertParentOptionSelectionPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitRevertParentOptionSelectionPopUp
		case constant.GitRevertConfirmationPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitRevertConfirmationPopUp
		case constant.GitCherryPickFromRefLogApplyConfirmationPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitCherryPickFromRefLogApplyConfirmationPopUp
		case constant.GitRebaseBranchInputPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitRebaseBranchInputPopUp
		case constant.GitRebaseOutputPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForGitRebaseOutputPopUp
		case constant.ChooseRemoteBranchOptionPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForChooseRemoteBranchOptionPopUp
		case constant.ChooseBranchOptionForMergePopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForChooseBranchOptionForMergePopUp
		case constant.BranchMergeOutputPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForBranchMergeOutputPopUp
		case constant.BlamePopUp:
			popUp, ok := m.PopUpModel.(*blamePopUp.BlamePopUpModel)
			if ok {
				if !popUp.ShowingBlameInfo {
					keys = i18n.LANGUAGEMAPPING.KeyBindingForBlamePopUpFilePathSelection
				} else {
					keys = i18n.LANGUAGEMAPPING.KeyBindingForBlamePopUpBlameView
				}
			}
		case constant.InteractiveRebaseOptionPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForInteractiveRebaseOptionPopUp
		case constant.InteractiveRebaseFixupSquashSelectionPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForInteractiveRebaseFixupSquashSelectionPopUp
		case constant.InteractiveRebaseFixupSquashCommitPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForInteractiveRebaseFixupSquashCommitPopUp
		case constant.InteractiveRebaseFixupSquashOutputPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForInteractiveRebaseFixupSquashOutputPopUp
		case constant.InteractiveRebaseRewordSelectionPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForInteractiveRebaseRewordSelectionPopUp
		case constant.InteractiveRebaseRewordCommitPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForInteractiveRebaseRewordCommitPopUp
		case constant.InteractiveRebaseRewordOutputPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForInteractiveRebaseRewordOutputPopUp
		case constant.InteractiveRebaseDropSelectionPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForInteractiveRebaseDropSelectionPopUp
		case constant.InteractiveRebaseDropOutputPopUp:
			keys = i18n.LANGUAGEMAPPING.KeyBindingForInteractiveRebaseDropOutputPopUp
		}
	} else {
		//-----------------------------
		//
		// for non-popup keybinding render
		//
		//-----------------------------
		switch m.CurrentSelectedComponent {
		case constant.GitStatusComponentPanel:
			// publication is advertised only when the observation is unpublished
			// for the branch the model is actually showing
			if m.RemoteSyncObservationState == git.UpstreamStateUnpublished && m.RemoteSyncObservedBranch != "" && m.RemoteSyncObservedBranch == m.CheckOutBranch {
				keys = []string{i18n.LANGUAGEMAPPING.GitStatusPanelPublishBranchHint}
			} else {
				keys = i18n.LANGUAGEMAPPING.KeyBindingForGitStatusComponent
			}
		case constant.LocalBranchOrTagOrRemoteOrWorktreeComponentPanel:
			switch m.CurrentLocalBranchOrTagOrRemoteOrWorktreeComponentShowing {
			case constant.SHOW_LOCAL_BRANCH:
				currentSelectedBranch := m.CurrentRepoBranchesInfoList.SelectedItem()
				if currentSelectedBranch == nil {
					keys = i18n.LANGUAGEMAPPING.KeyBindingLocalBranchComponentNone
				} else {
					isCurrentSelectedBranchCheckedOutBranch := currentSelectedBranch.(branchComponent.GitBranchItem).IsCheckedOut
					if isCurrentSelectedBranchCheckedOutBranch {
						keys = i18n.LANGUAGEMAPPING.KeyBindingLocalBranchComponentIsCheckOut
					} else {
						keys = i18n.LANGUAGEMAPPING.KeyBindingLocalBranchComponentDefault
					}
				}
			case constant.SHOW_TAG:
				currentSelectedTag := m.CurrentRepoTagInfoList.SelectedItem()
				if currentSelectedTag == nil {
					keys = i18n.LANGUAGEMAPPING.KeyBindingTagComponentNone
				} else {
					keys = i18n.LANGUAGEMAPPING.KeyBindingTagComponentDefault
				}
			case constant.SHOW_REMOTE:
				currentSelectedRemote := m.CurrentRepoRemoteInfoList.SelectedItem()
				if currentSelectedRemote == nil {
					keys = i18n.LANGUAGEMAPPING.KeyBindingRemoteComponentNone
				} else {
					keys = i18n.LANGUAGEMAPPING.KeyBindingRemoteComponentDefault
				}
			case constant.SHOW_WORKTREE:
				currentSelectedWorktree := m.CurrentRepoWorktreeInfoList.SelectedItem()
				if currentSelectedWorktree != nil {
					selectedWorktree := currentSelectedWorktree.(worktreeComponent.GitWorktreeItem)
					// switchable variants append the [enter] switch hint; used only when the
					// worktree is a valid switch target (not current and not prunable). the main
					// worktree can't be removed/locked so it keeps its own reduced key set.
					if selectedWorktree.IsMain {
						if selectedWorktree.IsInCurrentWorktree {
							keys = i18n.LANGUAGEMAPPING.KeyBindingWorktreeComponentMainWorktree
						} else {
							keys = i18n.LANGUAGEMAPPING.KeyBindingWorktreeComponentMainWorktreeSwitchable
						}
					} else {
						if selectedWorktree.IsInCurrentWorktree || selectedWorktree.IsPrunable {
							keys = i18n.LANGUAGEMAPPING.KeyBindingWorktreeComponent
						} else {
							keys = i18n.LANGUAGEMAPPING.KeyBindingWorktreeComponentSwitchable
						}
					}
				}
			}
		case constant.ModifiedFilesComponentPanel:
			CurrentSelectedFile := m.CurrentRepoModifiedFilesInfoList.SelectedItem()
			if CurrentSelectedFile == nil {
				keys = i18n.LANGUAGEMAPPING.KeyBindingModifiedFilesComponentNone
			} else {
				file := CurrentSelectedFile.(filesComponent.GitModifiedFilesItem)
				if file.HasConflict {
					keys = i18n.LANGUAGEMAPPING.KeyBindingModifiedFilesComponentConflict
				} else {
					if file.IndexState == "?" && file.WorkTree == "?" {
						// not tracked
						keys = i18n.LANGUAGEMAPPING.KeyBindingModifiedFilesComponentDefault
					} else if file.IndexState != " " && file.WorkTree != " " {
						// staged but have modification later
						keys = i18n.LANGUAGEMAPPING.KeyBindingModifiedFilesComponentDefault
					} else if file.IndexState != " " && file.WorkTree == " " {
						// staged and no latest modification
						keys = i18n.LANGUAGEMAPPING.KeyBindingModifiedFilesComponentIsStaged
					} else if file.IndexState == " " && file.WorkTree != " " {
						// tracked but not staged
						keys = i18n.LANGUAGEMAPPING.KeyBindingModifiedFilesComponentDefault
					}
				}
			}
		case constant.CommitLogOrRefLogComponentPanel:
			switch m.CurrentCommitLogOrRefLogComponentShowing {
			case constant.SHOW_COMMITLOG:
				if len(m.CurrentRepoCommitLogInfoList.Items()) > 0 {
					keys = i18n.LANGUAGEMAPPING.KeyBindingCommitLogComponent
				} else {
					keys = i18n.LANGUAGEMAPPING.KeyBindingCommitLogComponentNone
				}
			case constant.SHOW_REFLOG:
				if len(m.CurrentRepoRefLogInfoList.Items()) > 0 {
					keys = i18n.LANGUAGEMAPPING.KeyBindingRefLogComponent
				} else {
					keys = i18n.LANGUAGEMAPPING.KeyBindingRefLogComponentNone
				}
			}
		case constant.DetailComponentPanel:
			if m.IsLineEditingState.Load() {
				keys = i18n.LANGUAGEMAPPING.KeyBindingKeyDetailComponentLineEditing
			} else if m.DetailPanelParentComponent == constant.ModifiedFilesComponentPanel {
				keys = i18n.LANGUAGEMAPPING.KeyBindingKeyDetailComponentLineEditingEligible
			} else {
				keys = i18n.LANGUAGEMAPPING.KeyBindingKeyDetailComponent
			}

		case constant.DetailComponentPanelTwo:
			if m.IsLineEditingState.Load() {
				keys = i18n.LANGUAGEMAPPING.KeyBindingKeyDetailComponentLineEditing
			} else if m.DetailPanelParentComponent == constant.ModifiedFilesComponentPanel {
				keys = i18n.LANGUAGEMAPPING.KeyBindingKeyDetailComponentLineEditingEligible
			} else {
				keys = i18n.LANGUAGEMAPPING.KeyBindingKeyDetailComponent
			}
		case constant.StashComponentPanel:
			if len(m.CurrentRepoStashInfoList.Items()) > 0 {
				keys = i18n.LANGUAGEMAPPING.KeyBindingKeyStashComponent
			} else {
				keys = i18n.LANGUAGEMAPPING.KeyBindingKeyStashComponentNone
			}
		case constant.LogComponentPanel:
			keys = i18n.LANGUAGEMAPPING.KeyBindingLogComponent
		}
	}

	if pageNavigationAvailable(m) {
		pageNavigationHelp := fmt.Sprintf("[%s] %s", i18n.LANGUAGEMAPPING.PageNavigationKey, i18n.LANGUAGEMAPPING.PageNavigationDescription)
		keys = append([]string{pageNavigationHelp}, keys...)
	}
	if !m.ShowPopUp.Load() {
		screenModeHelp := fmt.Sprintf("[%s] %s", i18n.LANGUAGEMAPPING.ScreenModeNavigationKey, i18n.LANGUAGEMAPPING.ScreenModeNavigationDescription)
		keys = append([]string{screenModeHelp}, keys...)
	}

	var keyBindingLine string
	keyBindingLine = strings.Join(keys, "  |  ")
	processedWidth := width - lipgloss.Width(gitticonst.APPVERSION) - 3
	keyBindingLine = utils.TruncateString(keyBindingLine, processedWidth)
	versionLine := style.NewStyle.Foreground(style.ColorYellowWarm).Render(gitticonst.APPVERSION)
	parsedKeyBindingLine := style.NewStyle.Width(processedWidth).Align(lipgloss.Left).Render(keyBindingLine)

	content := lipgloss.JoinHorizontal(
		lipgloss.Top,
		parsedKeyBindingLine,
		" ",
		versionLine,
	)

	return style.BottomKeyBindingStyle.
		Width(width).
		Height(constant.MainPageKeyBindingLayoutPanelHeight).
		Render(content)
}

func pageNavigationAvailable(m *types.GittiModel) bool {
	if !m.ShowPopUp.Load() {
		switch m.CurrentSelectedComponent {
		case constant.LocalBranchOrTagOrRemoteOrWorktreeComponentPanel,
			constant.ModifiedFilesComponentPanel,
			constant.CommitLogOrRefLogComponentPanel,
			constant.StashComponentPanel,
			constant.DetailComponentPanel,
			constant.DetailComponentPanelTwo:
			return true
		}
		return false
	}

	switch m.PopUpType {
	case constant.ChooseRemotePopUp,
		constant.ChoosePushTypePopUp,
		constant.ChooseNewBranchTypePopUp,
		constant.ChooseSwitchBranchTypePopUp,
		constant.ChooseGitPullTypePopUp,
		constant.GitDiscardTypeOptionPopUp,
		constant.GitResolveConflictOptionPopUp,
		constant.GitResetLatestCommitTypeOptionPopUp,
		constant.GitResetToSelectedCommitTypeOptionPopUp,
		constant.GitCherryPickOptionSelectionPopUp,
		constant.GitCherryPickPopUp,
		constant.GitEditCherryPickPopUp,
		constant.ChooseDeleteTagOptionPopUp,
		constant.ChooseRemoteForDeleteRemoteTagPopUp,
		constant.ChoosePushTagOptionPopUp,
		constant.ChooseFetchTagOptionPopUp,
		constant.GitRevertParentOptionSelectionPopUp,
		constant.ChooseRemoteBranchOptionPopUp,
		constant.ChooseBranchOptionForMergePopUp,
		constant.BlamePopUp,
		constant.InteractiveRebaseOptionPopUp,
		constant.InteractiveRebaseFixupSquashSelectionPopUp,
		constant.InteractiveRebaseRewordSelectionPopUp,
		constant.InteractiveRebaseDropSelectionPopUp,
		constant.KeybindingAndFeatureInstructionsPopUp:
		return true
	}
	return false
}
