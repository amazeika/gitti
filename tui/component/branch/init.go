package branch

import (
	"charm.land/bubbles/v2/list"
	"github.com/charmbracelet/x/ansi"
	"github.com/gohyuhan/gitti/tui/constant"
	"github.com/gohyuhan/gitti/tui/style"
	"github.com/gohyuhan/gitti/tui/types"
	"github.com/gohyuhan/gitti/tui/utils"
)

// ------------------------------------
//
//	Rebuild the branch list widget from the latest git branch data, preserve the previously
//	selected branch position, and clamp the selection index if the list shrinks.
//
// ------------------------------------
func InitBranchList(m *types.GittiModel) {
	localSnapshot := m.GitOperations.GitBranch.LocalBranchSnapshot()
	currentCheckOut := localSnapshot.CurrentCheckOut
	allBranches := localSnapshot.AllBranches
	latestBranchArray := make([]list.Item, 0, len(allBranches)+1)
	if currentCheckOut.BranchName != "" {
		latestBranchArray = append(latestBranchArray, GitBranchItem(currentCheckOut))
	}

	m.CheckOutBranch = currentCheckOut.BranchName

	previousSelectedBranch := m.CurrentRepoBranchesInfoList.SelectedItem()
	selectedBranchPosition := -1

	titleWidthLimit := m.WindowLeftPanelWidth - constant.ListItemOrTitleWidthPad - 2

	if previousSelectedBranch != nil {
		previousSelectedBranchInfo := previousSelectedBranch.(GitBranchItem)
		for index, branch := range allBranches {
			// We use branch name here because it is the canonical branch identity.
			if branch.BranchName == previousSelectedBranchInfo.BranchName {
				selectedBranchPosition = index
				if currentCheckOut.BranchName != "" {
					selectedBranchPosition++
				}
			}
			latestBranchArray = append(latestBranchArray, GitBranchItem(branch))
		}

		// if it previous selected branch name is the current checkout one, the position will be 0, as the checkout branch will always be the first in the list
		if currentCheckOut.BranchName == previousSelectedBranchInfo.BranchName {
			selectedBranchPosition = 0
		}
	} else {
		for _, branch := range allBranches {
			latestBranchArray = append(latestBranchArray, GitBranchItem(branch))
		}
	}

	latestBranchArray, selectedBranchPosition = utils.FilterListItems(latestBranchArray, m.PanelFilterQuery[constant.SHOW_LOCAL_BRANCH], previousSelectedBranch, selectedBranchPosition)

	m.CurrentRepoBranchesInfoList = list.New(latestBranchArray, GitBranchItemDelegate{}, m.WindowLeftPanelWidth, m.LocalBranchesComponentPanelHeight)
	m.CurrentRepoBranchesInfoList.SetShowPagination(false)
	m.CurrentRepoBranchesInfoList.SetShowStatusBar(false)
	m.CurrentRepoBranchesInfoList.SetFilteringEnabled(false)
	m.CurrentRepoBranchesInfoList.SetShowFilter(false)

	m.CurrentRepoBranchesInfoList.Title = ansi.Truncate(ConstructLocalBranchComponentTitle(titleWidthLimit), titleWidthLimit, "...")
	m.CurrentRepoBranchesInfoList.Styles.Title = style.TitleStyle
	m.CurrentRepoBranchesInfoList.Styles.PaginationStyle = style.PaginationStyle
	m.CurrentRepoBranchesInfoList.Styles.TitleBar = style.NewStyle
	m.CurrentRepoBranchesInfoList.Styles.HelpStyle = style.NewStyle.MarginTop(0).MarginBottom(0).PaddingTop(0).PaddingBottom(0)

	// Custom Help Model for Count Display
	m.CurrentRepoBranchesInfoList.SetShowHelp(true)
	m.CurrentRepoBranchesInfoList.KeyMap = list.KeyMap{} // Clear default keybindings to hide them
	m.CurrentRepoBranchesInfoList.AdditionalShortHelpKeys = utils.ListCounterHelper(m, &m.CurrentRepoBranchesInfoList, constant.SHOW_LOCAL_BRANCH)

	if len(latestBranchArray) < 1 {
		return
	}

	if selectedBranchPosition >= 0 {
		m.CurrentRepoBranchesInfoList.Select(selectedBranchPosition)
		m.ListNavigationIndexPosition.LocalBranchComponent = selectedBranchPosition
	} else {
		if m.ListNavigationIndexPosition.LocalBranchComponent > len(m.CurrentRepoBranchesInfoList.Items())-1 {
			m.CurrentRepoBranchesInfoList.Select(len(m.CurrentRepoBranchesInfoList.Items()) - 1)
			m.ListNavigationIndexPosition.LocalBranchComponent = len(m.CurrentRepoBranchesInfoList.Items()) - 1
		} else {
			m.CurrentRepoBranchesInfoList.Select(m.ListNavigationIndexPosition.LocalBranchComponent)
		}
	}
}
