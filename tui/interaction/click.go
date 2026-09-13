package interaction

import (
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/gohyuhan/gitti/tui/constant"
	"github.com/gohyuhan/gitti/tui/layout"
	"github.com/gohyuhan/gitti/tui/services"
	"github.com/gohyuhan/gitti/tui/types"
)

type mousePanelRegion uint8

const (
	mousePanelRegionPrimary mousePanelRegion = iota
	mousePanelRegionDetail
	mousePanelRegionLog
)

type panelRectangle struct {
	x      int
	y      int
	width  int
	height int
}

type visiblePanelFootprint struct {
	rectangle panelRectangle
	component string
	index     int
	region    mousePanelRegion
}

func (rectangle panelRectangle) contains(x int, y int) bool {
	return rectangle.width > 0 && rectangle.height > 0 &&
		x >= rectangle.x && x < rectangle.x+rectangle.width &&
		y >= rectangle.y && y < rectangle.y+rectangle.height
}

// ------------------------------------
//
//	Handle a left mouse click on the main page. Resolve the click against the
//	currently rendered, non-overlapping panel footprints before changing focus
//	or reflowing. List row selection excludes each panel's border and title row.
//
// ------------------------------------
func handleLeftMouseClick(msg tea.MouseClickMsg, m *types.GittiModel) (*types.GittiModel, tea.Cmd) {
	if m.ShowPopUp.Load() || m.IsLineEditingState.Load() || m.IsPanelFiltering.Load() {
		return m, nil
	}

	x := msg.Mouse().X
	y := msg.Mouse().Y
	for _, footprint := range visiblePanelFootprints(m) {
		if !footprint.rectangle.contains(x, y) {
			continue
		}

		switch footprint.region {
		case mousePanelRegionPrimary:
			itemRow := y - footprint.rectangle.y - 2
			selectionChanged := selectListItemFromClick(m, footprint.component, itemRow)
			if m.CurrentSelectedComponent != footprint.component {
				m.CurrentSelectedComponent = footprint.component
				m.CurrentSelectedComponentIndex = footprint.index
				m.DetailPanelParentComponent = ""
				layout.TuiWindowSizing(m)
				services.FetchDetailComponentPanelInfoService(m, true)
			} else if selectionChanged {
				services.FetchDetailComponentPanelInfoService(m, true)
			}
		case mousePanelRegionDetail:
			focusDetailPanelFromClick(m, footprint.component)
		case mousePanelRegionLog:
			if m.CurrentSelectedComponent != constant.LogComponentPanel {
				m.CurrentSelectedComponent = constant.LogComponentPanel
				m.DetailPanelParentComponent = ""
				layout.TuiWindowSizing(m)
				services.FetchDetailComponentPanelInfoService(m, true)
			}
		}
		return m, nil
	}
	return m, nil
}

func focusDetailPanelFromClick(m *types.GittiModel, component string) {
	if m.CurrentSelectedComponent == component {
		return
	}

	switch m.CurrentSelectedComponent {
	case constant.LocalBranchOrTagOrRemoteOrWorktreeComponentPanel,
		constant.ModifiedFilesComponentPanel,
		constant.CommitLogOrRefLogComponentPanel,
		constant.StashComponentPanel,
		constant.LogComponentPanel:
		m.DetailPanelParentComponent = m.CurrentSelectedComponent
	case constant.DetailComponentPanel, constant.DetailComponentPanelTwo:
		// Preserve the existing detail parent while switching split subpanels.
	default:
		return
	}
	m.CurrentSelectedComponent = component
	layout.TuiWindowSizing(m)
}

func visiblePanelFootprints(m *types.GittiModel) []visiblePanelFootprint {
	mainHeight := m.WindowCoreContentHeight + 2
	if m.Width < constant.MinWidth || m.Height < constant.MinHeight || mainHeight <= 0 {
		return nil
	}

	switch m.ScreenMode {
	case constant.ScreenModeSingleColumn:
		return primaryPanelFootprints(m, m.Width)
	case constant.ScreenModeFocused:
		return []visiblePanelFootprint{{
			rectangle: panelRectangle{x: 0, y: 0, width: m.Width, height: mainHeight},
			component: m.CurrentSelectedComponent,
			index:     primaryComponentIndex(m.CurrentSelectedComponent),
			region:    componentMouseRegion(m.CurrentSelectedComponent),
		}}
	default:
		footprints := primaryPanelFootprints(m, m.WindowLeftPanelWidth)
		footprints = append(footprints, twoColumnDetailFootprints(m)...)
		footprints = append(footprints, visiblePanelFootprint{
			rectangle: panelRectangle{
				x:      m.WindowLeftPanelWidth,
				y:      m.DetailComponentPanelHeight + 2,
				width:  m.DetailComponentPanelWidth,
				height: m.LogComponentPanelHeight + 2,
			},
			component: constant.LogComponentPanel,
			region:    mousePanelRegionLog,
		})
		return footprints
	}
}

func primaryPanelFootprints(m *types.GittiModel, width int) []visiblePanelFootprint {
	panels := []struct {
		component string
		index     int
		height    int
	}{
		{component: constant.GitStatusComponentPanel, index: 0, height: 3},
		{component: constant.LocalBranchOrTagOrRemoteOrWorktreeComponentPanel, index: 1, height: m.LocalBranchesComponentPanelHeight + 2},
		{component: constant.ModifiedFilesComponentPanel, index: 2, height: m.ModifiedFilesComponentPanelHeight + 2},
		{component: constant.CommitLogOrRefLogComponentPanel, index: 3, height: m.CommitLogComponentPanelHeight + 2},
		{component: constant.StashComponentPanel, index: 4, height: m.StashComponentPanelHeight + 2},
	}

	footprints := make([]visiblePanelFootprint, 0, len(panels))
	top := 0
	for _, panel := range panels {
		footprints = append(footprints, visiblePanelFootprint{
			rectangle: panelRectangle{x: 0, y: top, width: width, height: panel.height},
			component: panel.component,
			index:     panel.index,
			region:    mousePanelRegionPrimary,
		})
		top += panel.height
	}
	return footprints
}

func twoColumnDetailFootprints(m *types.GittiModel) []visiblePanelFootprint {
	x := m.WindowLeftPanelWidth
	width := m.DetailComponentPanelWidth
	detailHeight := m.DetailComponentPanelHeight + 2
	if !m.ShowDetailPanelTwo.Load() {
		return []visiblePanelFootprint{{
			rectangle: panelRectangle{x: x, y: 0, width: width, height: detailHeight},
			component: constant.DetailComponentPanel,
			region:    mousePanelRegionDetail,
		}}
	}

	bodyY := 0
	bodyHeight := detailHeight
	footprints := make([]visiblePanelFootprint, 0, 3)
	if m.IsLineEditingState.Load() {
		footprints = append(footprints, visiblePanelFootprint{
			rectangle: panelRectangle{x: x, y: 0, width: width, height: 3},
			component: m.CurrentSelectedComponent,
			region:    mousePanelRegionDetail,
		})
		bodyY = 3
		bodyHeight -= 3
	}

	if m.DetailComponentPanelLayout == constant.HORIZONTAL {
		splitWidth := width / 2
		return append(footprints,
			visiblePanelFootprint{
				rectangle: panelRectangle{x: x, y: bodyY, width: splitWidth, height: bodyHeight},
				component: constant.DetailComponentPanel,
				region:    mousePanelRegionDetail,
			},
			visiblePanelFootprint{
				rectangle: panelRectangle{x: x + splitWidth, y: bodyY, width: width - splitWidth, height: bodyHeight},
				component: constant.DetailComponentPanelTwo,
				region:    mousePanelRegionDetail,
			},
		)
	}

	availableHeight := m.DetailComponentPanelHeight
	if m.IsLineEditingState.Load() {
		availableHeight -= 3
	}
	firstHeight := availableHeight/2 + 1
	return append(footprints,
		visiblePanelFootprint{
			rectangle: panelRectangle{x: x, y: bodyY, width: width, height: firstHeight},
			component: constant.DetailComponentPanel,
			region:    mousePanelRegionDetail,
		},
		visiblePanelFootprint{
			rectangle: panelRectangle{x: x, y: bodyY + firstHeight, width: width, height: bodyHeight - firstHeight},
			component: constant.DetailComponentPanelTwo,
			region:    mousePanelRegionDetail,
		},
	)
}

func primaryComponentIndex(component string) int {
	for index, primaryComponent := range constant.ComponentPanelNavigationList {
		if component == primaryComponent {
			return index
		}
	}
	return 0
}

func componentMouseRegion(component string) mousePanelRegion {
	switch component {
	case constant.DetailComponentPanel, constant.DetailComponentPanelTwo:
		return mousePanelRegionDetail
	case constant.LogComponentPanel:
		return mousePanelRegionLog
	default:
		return mousePanelRegionPrimary
	}
}

// ------------------------------------
//
//	Select the list row of the clicked left panel. itemRow is the row within the
//	list body (0 = first visible item); the absolute index is resolved through
//	the list's paginator page. Returns true when the selection changed.
//
// ------------------------------------
func selectListItemFromClick(m *types.GittiModel, component string, itemRow int) bool {
	if itemRow < 0 {
		return false
	}

	var clickedList *list.Model
	var navigationIndex *int
	switch component {
	case constant.LocalBranchOrTagOrRemoteOrWorktreeComponentPanel:
		switch m.CurrentLocalBranchOrTagOrRemoteOrWorktreeComponentShowing {
		case constant.SHOW_LOCAL_BRANCH:
			clickedList = &m.CurrentRepoBranchesInfoList
			navigationIndex = &m.ListNavigationIndexPosition.LocalBranchComponent
		case constant.SHOW_TAG:
			clickedList = &m.CurrentRepoTagInfoList
			navigationIndex = &m.ListNavigationIndexPosition.TagComponent
		case constant.SHOW_REMOTE:
			clickedList = &m.CurrentRepoRemoteInfoList
			navigationIndex = &m.ListNavigationIndexPosition.RemoteComponent
		case constant.SHOW_WORKTREE:
			clickedList = &m.CurrentRepoWorktreeInfoList
			navigationIndex = &m.ListNavigationIndexPosition.WorktreeComponent
		}
	case constant.ModifiedFilesComponentPanel:
		clickedList = &m.CurrentRepoModifiedFilesInfoList
		navigationIndex = &m.ListNavigationIndexPosition.ModifiedFilesComponent
	case constant.CommitLogOrRefLogComponentPanel:
		switch m.CurrentCommitLogOrRefLogComponentShowing {
		case constant.SHOW_COMMITLOG:
			clickedList = &m.CurrentRepoCommitLogInfoList
			navigationIndex = &m.ListNavigationIndexPosition.CommitLogComponent
		case constant.SHOW_REFLOG:
			clickedList = &m.CurrentRepoRefLogInfoList
			navigationIndex = &m.ListNavigationIndexPosition.RefLogComponent
		}
	case constant.StashComponentPanel:
		clickedList = &m.CurrentRepoStashInfoList
		navigationIndex = &m.ListNavigationIndexPosition.StashComponent
	}
	if clickedList == nil {
		return false
	}

	totalCount := len(clickedList.Items())
	page := clickedList.Paginator.Page
	perPage := clickedList.Paginator.PerPage
	itemsOnPage := min(perPage, totalCount-page*perPage)
	if itemRow >= itemsOnPage {
		return false
	}

	index := page*perPage + itemRow
	if index < 0 || index >= totalCount || index == clickedList.Index() {
		return false
	}

	clickedList.Select(index)
	*navigationIndex = index
	return true
}
