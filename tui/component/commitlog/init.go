package commitlog

import (
	"charm.land/bubbles/v2/list"
	"github.com/charmbracelet/x/ansi"
	"github.com/gohyuhan/gitti/api/git"
	"github.com/gohyuhan/gitti/tui/constant"
	"github.com/gohyuhan/gitti/tui/style"
	"github.com/gohyuhan/gitti/tui/types"
	"github.com/gohyuhan/gitti/tui/utils"
)

// ------------------------------------
//
//	Rebuild the commit log list widget from the latest git log data, preserve the previously
//	selected commit by hash, and return true if the selected commit changed (signals that the
//	detail panel needs to be reinitialized).
//
// ------------------------------------
func InitGitCommitLogList(m *types.GittiModel) bool {
	latestGitCommitLog := m.GitOperations.GitCommitLog.GitCommitLogOutput()
	latestGitCommitLogItemArray := make([]list.Item, 0, len(latestGitCommitLog))

	// get the previous selected commit log and see if it was within the new list if yes get the latest position of the previous selected file
	previousSelectedCommitLog := m.CurrentRepoCommitLogInfoList.SelectedItem()
	var prevHash string
	if previousSelectedCommitLog != nil {
		prevHash = previousSelectedCommitLog.(GitCommitLogItem).Hash
	}
	titleWidthLimit := m.WindowLeftPanelWidth - constant.ListItemOrTitleWidthPad - 2

	for _, commitLog := range latestGitCommitLog {
		laneCharList := make([]Cell, len(commitLog.LaneCharInfo))
		for i, c := range commitLog.LaneCharInfo {
			laneCharList[i] = Cell{
				Char:    c.Char,
				ColorID: c.ColorID,
			}
		}

		latestGitCommitLogItemArray = append(latestGitCommitLogItemArray, GitCommitLogItem{
			Hash:         commitLog.Hash,
			Parents:      commitLog.Parents,
			Refs:         git.CompactDecorations(commitLog.Refs),
			RefsFilter:   git.DecorationFilterText(commitLog.Refs),
			BranchLabel:  git.DecorationBranchLabel(commitLog.Refs),
			Message:      commitLog.Message,
			Author:       commitLog.Author,
			LaneCharList: laneCharList,
			ColorID:      commitLog.ColorID,
		})
	}

	// The position FilterListItems derives is discarded on purpose. It recovers the
	// cursor by comparing filter values, and a commit's refs change under it on an
	// ordinary refresh: a new commit moves HEAD, a fetch adds a remote branch, a tag
	// appears. The hash is the only identity that survives that, and reset, revert
	// and tag all act on whatever ends up selected.
	latestGitCommitLogItemArray, _ = utils.FilterListItems(latestGitCommitLogItemArray, m.PanelFilterQuery[constant.SHOW_COMMITLOG], previousSelectedCommitLog, -1)
	selectedCommitLogPosition := positionOfCommit(latestGitCommitLogItemArray, prevHash)

	previousCommitLogCount := len(m.CurrentRepoCommitLogInfoList.Items())

	m.CurrentRepoCommitLogInfoList = list.New(latestGitCommitLogItemArray, GitCommitLogItemDelegate{}, m.WindowLeftPanelWidth, m.CommitLogComponentPanelHeight)
	m.CurrentRepoCommitLogInfoList.SetShowPagination(false)
	m.CurrentRepoCommitLogInfoList.SetShowStatusBar(false)
	m.CurrentRepoCommitLogInfoList.SetFilteringEnabled(false)
	m.CurrentRepoCommitLogInfoList.SetShowFilter(false)
	m.CurrentRepoCommitLogInfoList.Title = ansi.Truncate(ConstructCommitLogComponentTitle(titleWidthLimit), titleWidthLimit, "...")
	m.CurrentRepoCommitLogInfoList.Styles.Title = style.TitleStyle
	m.CurrentRepoCommitLogInfoList.Styles.PaginationStyle = style.PaginationStyle
	m.CurrentRepoCommitLogInfoList.Styles.TitleBar = style.NewStyle
	m.CurrentRepoCommitLogInfoList.Styles.HelpStyle = style.NewStyle.MarginTop(0).MarginBottom(0).PaddingTop(0).PaddingBottom(0)

	// Custom Help Model for Count Display
	m.CurrentRepoCommitLogInfoList.SetShowHelp(true)
	m.CurrentRepoCommitLogInfoList.KeyMap = list.KeyMap{} // Clear default keybindings to hide them
	m.CurrentRepoCommitLogInfoList.AdditionalShortHelpKeys = utils.ListCounterHelper(m, &m.CurrentRepoCommitLogInfoList, constant.SHOW_COMMITLOG)

	if len(latestGitCommitLogItemArray) < 1 {
		return len(latestGitCommitLogItemArray) != previousCommitLogCount
	}

	selectedCommitLogPosition = selectionIndex(
		len(m.CurrentRepoCommitLogInfoList.Items()),
		selectedCommitLogPosition,
		prevHash != "",
		m.ListNavigationIndexPosition.CommitLogComponent,
	)
	m.CurrentRepoCommitLogInfoList.Select(selectedCommitLogPosition)
	m.ListNavigationIndexPosition.CommitLogComponent = selectedCommitLogPosition

	if previousSelectedCommitLog != nil {
		curr := m.CurrentRepoCommitLogInfoList.SelectedItem()
		if curr != nil && curr.(GitCommitLogItem).Hash == prevHash {
			return false
		}
	}
	return true
}

// ------------------------------------
//
//	Locate a commit in the rebuilt list by hash, reporting -1 when it is not
//	listed so the caller alone decides what an absent selection means
//
// ------------------------------------
func positionOfCommit(items []list.Item, commitHash string) int {
	if commitHash == "" {
		return -1
	}

	for index, item := range items {
		if commitLogItem, ok := item.(GitCommitLogItem); ok && commitLogItem.Hash == commitHash {
			return index
		}
	}

	return -1
}

// ------------------------------------
//
//	Decide which row to select after a rebuild. A commit that was selected and is
//	no longer listed sends the cursor to the top rather than to the position it
//	used to occupy: a filter keyed on ref text drops a commit as soon as its refs
//	move, a rewrite drops it from an unfiltered list, and reset, revert and tag
//	act on whatever is selected
//
// ------------------------------------
func selectionIndex(itemCount int, foundPosition int, hadSelection bool, rememberedPosition int) int {
	if foundPosition >= 0 {
		return foundPosition
	}
	if hadSelection {
		return 0
	}

	return min(max(rememberedPosition, 0), itemCount-1)
}
