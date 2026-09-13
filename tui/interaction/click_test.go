package interaction

import (
	"io"
	"testing"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/logging"
	"github.com/gohyuhan/gitti/tui/component/branch"
	"github.com/gohyuhan/gitti/tui/constant"
	"github.com/gohyuhan/gitti/tui/layout"
	"github.com/gohyuhan/gitti/tui/types"
)

type mouseTestItem string

func (item mouseTestItem) FilterValue() string { return string(item) }

type mouseTestDelegate struct{}

func (mouseTestDelegate) Height() int                                  { return 1 }
func (mouseTestDelegate) Spacing() int                                 { return 0 }
func (mouseTestDelegate) Update(tea.Msg, *list.Model) tea.Cmd          { return nil }
func (mouseTestDelegate) Render(io.Writer, list.Model, int, list.Item) {}

func TestTwoColumnClickUsesVisiblePanelFootprints(t *testing.T) {
	t.Run("primary adjacent borders", func(t *testing.T) {
		model := initMouseScreenModeModel(t)
		model.ScreenMode = constant.ScreenModeTwoColumn
		model.CurrentSelectedComponent = constant.ModifiedFilesComponentPanel
		layout.TuiWindowSizing(model)

		click(model, 0, 2)
		if model.CurrentSelectedComponent != constant.GitStatusComponentPanel {
			t.Errorf("Git-status bottom border selected %q", model.CurrentSelectedComponent)
		}

		model.CurrentSelectedComponent = constant.ModifiedFilesComponentPanel
		model.CurrentSelectedComponentIndex = 2
		layout.TuiWindowSizing(model)
		click(model, 0, 3)
		if model.CurrentSelectedComponent != constant.LocalBranchOrTagOrRemoteOrWorktreeComponentPanel {
			t.Errorf("branch top border selected %q", model.CurrentSelectedComponent)
		}
	})

	t.Run("single detail and log boundary", func(t *testing.T) {
		model := initMouseScreenModeModel(t)
		model.ScreenMode = constant.ScreenModeTwoColumn
		model.CurrentSelectedComponent = constant.ModifiedFilesComponentPanel
		layout.TuiWindowSizing(model)
		rightX := model.WindowLeftPanelWidth

		click(model, rightX, model.DetailComponentPanelHeight+1)
		if model.CurrentSelectedComponent != constant.DetailComponentPanel {
			t.Errorf("detail bottom border selected %q", model.CurrentSelectedComponent)
		}

		model.CurrentSelectedComponent = constant.ModifiedFilesComponentPanel
		model.DetailPanelParentComponent = ""
		layout.TuiWindowSizing(model)
		click(model, rightX, model.DetailComponentPanelHeight+2)
		if model.CurrentSelectedComponent != constant.LogComponentPanel {
			t.Errorf("log top border selected %q", model.CurrentSelectedComponent)
		}
	})
}

func TestTwoColumnDualDetailClickAssignsEverySplitBorderOnce(t *testing.T) {
	for _, tt := range []struct {
		name      string
		configure func(*types.GittiModel)
		points    func(*types.GittiModel) [][3]interface{}
	}{
		{
			name: "horizontal",
			configure: func(m *types.GittiModel) {
				m.WindowLeftPanelRatio = 0.30
			},
			points: func(m *types.GittiModel) [][3]interface{} {
				splitWidth := m.DetailComponentPanelWidth / 2
				return [][3]interface{}{
					{m.WindowLeftPanelWidth + splitWidth - 1, 0, constant.DetailComponentPanel},
					{m.WindowLeftPanelWidth + splitWidth, 0, constant.DetailComponentPanelTwo},
				}
			},
		},
		{
			name: "vertical",
			configure: func(m *types.GittiModel) {
				m.WindowLeftPanelRatio = 0.65
			},
			points: func(m *types.GittiModel) [][3]interface{} {
				splitHeight := m.DetailComponentPanelHeight / 2
				return [][3]interface{}{
					{m.WindowLeftPanelWidth, splitHeight, constant.DetailComponentPanel},
					{m.WindowLeftPanelWidth, splitHeight + 1, constant.DetailComponentPanelTwo},
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, point := range func() [][3]interface{} {
				model := initMouseScreenModeModel(t)
				model.ScreenMode = constant.ScreenModeTwoColumn
				model.CurrentSelectedComponent = constant.ModifiedFilesComponentPanel
				model.ShowDetailPanelTwo.Store(true)
				tt.configure(model)
				layout.TuiWindowSizing(model)
				if model.DetailComponentPanelLayout != map[string]string{"horizontal": constant.HORIZONTAL, "vertical": constant.VERTICAL}[tt.name] {
					t.Fatalf("DetailComponentPanelLayout = %q", model.DetailComponentPanelLayout)
				}
				return tt.points(model)
			}() {
				// Recreate the pre-click geometry for each border point because a click changes focus.
				model := initMouseScreenModeModel(t)
				model.ScreenMode = constant.ScreenModeTwoColumn
				model.CurrentSelectedComponent = constant.ModifiedFilesComponentPanel
				model.ShowDetailPanelTwo.Store(true)
				tt.configure(model)
				layout.TuiWindowSizing(model)
				click(model, point[0].(int), point[1].(int))
				if model.CurrentSelectedComponent != point[2].(string) {
					t.Errorf("click (%d,%d) selected %q, want %q", point[0], point[1], model.CurrentSelectedComponent, point[2])
				}
			}
		})
	}
}

func TestSingleColumnClickOnlyUsesFullWidthPrimaryStack(t *testing.T) {
	model := initMouseScreenModeModel(t)
	model.ScreenMode = constant.ScreenModeSingleColumn
	model.CurrentSelectedComponent = constant.ModifiedFilesComponentPanel
	layout.TuiWindowSizing(model)

	stashTop := 3 + (model.LocalBranchesComponentPanelHeight + 2) + (model.ModifiedFilesComponentPanelHeight + 2) + (model.CommitLogComponentPanelHeight + 2)
	click(model, model.Width-1, stashTop)

	if model.CurrentSelectedComponent != constant.StashComponentPanel {
		t.Errorf("full-width stash border selected %q", model.CurrentSelectedComponent)
	}
	if model.ScreenMode != constant.ScreenModeSingleColumn {
		t.Errorf("ScreenMode = %d, want retained Single-column", model.ScreenMode)
	}
}

func TestFocusedListClickUsesFullHeightPaginatorWithoutChangingFocus(t *testing.T) {
	model := initMouseScreenModeModel(t)
	model.ScreenMode = constant.ScreenModeFocused
	model.CurrentSelectedComponent = constant.LocalBranchOrTagOrRemoteOrWorktreeComponentPanel
	items := make([]list.Item, 100)
	for index := range items {
		items[index] = branch.GitBranchItem{BranchName: string(rune('a' + index%26))}
	}
	model.CurrentRepoBranchesInfoList.SetItems(items)
	layout.TuiWindowSizing(model)
	model.CurrentRepoBranchesInfoList.Paginator.Page = 1
	wantIndex := model.CurrentRepoBranchesInfoList.Paginator.PerPage + 1

	click(model, model.Width-1, 3)

	if model.CurrentSelectedComponent != constant.LocalBranchOrTagOrRemoteOrWorktreeComponentPanel {
		t.Errorf("focused click changed focus to %q", model.CurrentSelectedComponent)
	}
	if got := model.CurrentRepoBranchesInfoList.Index(); got != wantIndex {
		t.Errorf("selected list index = %d, want full-height page index %d", got, wantIndex)
	}

	previous := model.CurrentRepoBranchesInfoList.Index()
	click(model, 5, 1)
	if got := model.CurrentRepoBranchesInfoList.Index(); got != previous {
		t.Errorf("title-row click changed selection to %d", got)
	}
}

func TestFocusedClickTreatsDualDetailAsOneFullMainRectangle(t *testing.T) {
	model := initMouseScreenModeModel(t)
	model.ScreenMode = constant.ScreenModeFocused
	model.CurrentSelectedComponent = constant.DetailComponentPanelTwo
	model.DetailPanelParentComponent = constant.ModifiedFilesComponentPanel
	model.ShowDetailPanelTwo.Store(true)
	layout.TuiWindowSizing(model)

	click(model, 0, model.Height-2)

	if model.CurrentSelectedComponent != constant.DetailComponentPanelTwo {
		t.Errorf("focused detail click changed focus to %q", model.CurrentSelectedComponent)
	}
}

func TestVisiblePanelFootprintsPartitionMainArea(t *testing.T) {
	for _, tt := range []struct {
		name        string
		mode        constant.ScreenMode
		showTwo     bool
		lineEditing bool
		ratio       float64
		selected    string
	}{
		{name: "two column", mode: constant.ScreenModeTwoColumn, ratio: 0.30, selected: constant.ModifiedFilesComponentPanel},
		{name: "two column horizontal dual", mode: constant.ScreenModeTwoColumn, showTwo: true, ratio: 0.30, selected: constant.DetailComponentPanel},
		{name: "two column vertical dual", mode: constant.ScreenModeTwoColumn, showTwo: true, ratio: 0.65, selected: constant.DetailComponentPanelTwo},
		{name: "two column line-editing title and dual body", mode: constant.ScreenModeTwoColumn, showTwo: true, lineEditing: true, ratio: 0.30, selected: constant.DetailComponentPanelTwo},
		{name: "single column", mode: constant.ScreenModeSingleColumn, ratio: 0.30, selected: constant.ModifiedFilesComponentPanel},
		{name: "focused dual detail state", mode: constant.ScreenModeFocused, showTwo: true, ratio: 0.30, selected: constant.DetailComponentPanelTwo},
	} {
		t.Run(tt.name, func(t *testing.T) {
			model := initMouseScreenModeModel(t)
			model.ScreenMode = tt.mode
			model.ShowDetailPanelTwo.Store(tt.showTwo)
			model.WindowLeftPanelRatio = tt.ratio
			model.CurrentSelectedComponent = tt.selected
			if tt.selected == constant.DetailComponentPanel || tt.selected == constant.DetailComponentPanelTwo {
				model.DetailPanelParentComponent = constant.ModifiedFilesComponentPanel
			}
			layout.TuiWindowSizing(model)
			model.IsLineEditingState.Store(tt.lineEditing)
			footprints := visiblePanelFootprints(model)
			mainHeight := model.Height - constant.MainPageKeyBindingLayoutPanelHeight

			for y := 0; y < mainHeight; y++ {
				for x := 0; x < model.Width; x++ {
					count := 0
					for _, footprint := range footprints {
						if footprint.rectangle.contains(x, y) {
							count++
						}
					}
					if count != 1 {
						t.Fatalf("cell (%d,%d) belongs to %d footprints, want exactly one", x, y, count)
					}
				}
			}
			for _, point := range [][2]int{{0, mainHeight}, {-1, 0}, {model.Width, 0}} {
				for _, footprint := range footprints {
					if footprint.rectangle.contains(point[0], point[1]) {
						t.Fatalf("footer/out-of-bounds cell (%d,%d) belongs to %+v", point[0], point[1], footprint)
					}
				}
			}
		})
	}
}

func TestClickRejectsLineEditingFooterAndOutOfBounds(t *testing.T) {
	for _, tt := range []struct {
		name string
		x    int
		y    int
		line bool
	}{
		{name: "line editing", x: 0, y: 0, line: true},
		{name: "footer", x: 0, y: 39},
		{name: "negative x", x: -1, y: 0},
		{name: "negative y", x: 0, y: -1},
		{name: "past width", x: 120, y: 0},
		{name: "past height", x: 0, y: 40},
	} {
		t.Run(tt.name, func(t *testing.T) {
			model := initMouseScreenModeModel(t)
			model.ScreenMode = constant.ScreenModeTwoColumn
			model.CurrentSelectedComponent = constant.ModifiedFilesComponentPanel
			layout.TuiWindowSizing(model)
			model.IsLineEditingState.Store(tt.line)

			click(model, tt.x, tt.y)

			if model.CurrentSelectedComponent != constant.ModifiedFilesComponentPanel {
				t.Errorf("rejected click selected %q", model.CurrentSelectedComponent)
			}
		})
	}

	t.Run("terminal warning", func(t *testing.T) {
		model := initMouseScreenModeModel(t)
		model.Width = constant.MinWidth - 1
		model.Height = constant.MinHeight - 1
		model.ScreenMode = constant.ScreenModeTwoColumn
		model.CurrentSelectedComponent = constant.ModifiedFilesComponentPanel
		layout.TuiWindowSizing(model)

		click(model, 0, 0)

		if model.CurrentSelectedComponent != constant.ModifiedFilesComponentPanel {
			t.Errorf("warning-screen click selected %q", model.CurrentSelectedComponent)
		}
	})
}

func click(m *types.GittiModel, x int, y int) {
	handleLeftMouseClick(tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}), m)
}

func initMouseScreenModeModel(t *testing.T) *types.GittiModel {
	originalLanguageMapping := i18n.LANGUAGEMAPPING
	i18n.InitGittiLanguageMapping("EN")
	t.Cleanup(func() { i18n.LANGUAGEMAPPING = originalLanguageMapping })

	newList := func() list.Model {
		componentList := list.New([]list.Item{}, mouseTestDelegate{}, 0, 0)
		componentList.SetShowPagination(false)
		componentList.SetShowStatusBar(false)
		componentList.SetFilteringEnabled(false)
		componentList.KeyMap = list.KeyMap{}
		return componentList
	}
	model := &types.GittiModel{
		Width:                         120,
		Height:                        40,
		WindowLeftPanelRatio:          0.30,
		CurrentSelectedComponent:      constant.ModifiedFilesComponentPanel,
		CurrentSelectedComponentIndex: 2,
		CurrentLocalBranchOrTagOrRemoteOrWorktreeComponentShowing: constant.SHOW_LOCAL_BRANCH,
		CurrentCommitLogOrRefLogComponentShowing:                  constant.SHOW_COMMITLOG,
		CurrentRepoBranchesInfoList:                               newList(),
		CurrentRepoTagInfoList:                                    newList(),
		CurrentRepoModifiedFilesInfoList:                          newList(),
		CurrentRepoCommitLogInfoList:                              newList(),
		CurrentRepoRefLogInfoList:                                 newList(),
		CurrentRepoStashInfoList:                                  newList(),
		CurrentRepoRemoteInfoList:                                 newList(),
		CurrentRepoWorktreeInfoList:                               newList(),
		DetailPanelViewport:                                       viewport.New(),
		DetailPanelTwoViewport:                                    viewport.New(),
		CurrentLogComponentViewport:                               viewport.New(),
		LineEditingIndexCursorViewport:                            viewport.New(),
		LineEditingIndexCursorTwoViewport:                         viewport.New(),
		TuiUpdateChannel:                                          make(chan interface{}, 32),
		PanelFilterQuery:                                          make(map[string]string),
		GittiLogger:                                               logging.InitGittiLogging(10, make(chan string, 1), 3),
	}
	layout.TuiWindowSizing(model)
	t.Cleanup(func() {
		if model.DetailComponentPanelInfoFetchCancelFunc != nil {
			model.DetailComponentPanelInfoFetchCancelFunc()
		}
	})
	return model
}
