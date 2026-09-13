package nontyping

import (
	"io"
	"math"
	"testing"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/logging"
	"github.com/gohyuhan/gitti/settings"
	"github.com/gohyuhan/gitti/tui/component/branch"
	"github.com/gohyuhan/gitti/tui/constant"
	"github.com/gohyuhan/gitti/tui/layout"
	"github.com/gohyuhan/gitti/tui/types"
)

type screenModeTestItem string

func (item screenModeTestItem) FilterValue() string { return string(item) }

type screenModeTestDelegate struct{}

func (screenModeTestDelegate) Height() int                                  { return 1 }
func (screenModeTestDelegate) Spacing() int                                 { return 0 }
func (screenModeTestDelegate) Update(tea.Msg, *list.Model) tea.Cmd          { return nil }
func (screenModeTestDelegate) Render(io.Writer, list.Model, int, list.Item) {}

func TestDirectionalCompatibleScreenModeCycles(t *testing.T) {
	primaryCycles := []struct {
		name      string
		key       string
		startMode constant.ScreenMode
		wantMode  constant.ScreenMode
	}{
		{name: "forward two to single", key: "=", startMode: constant.ScreenModeTwoColumn, wantMode: constant.ScreenModeSingleColumn},
		{name: "forward single to focused", key: "=", startMode: constant.ScreenModeSingleColumn, wantMode: constant.ScreenModeFocused},
		{name: "forward focused to two", key: "=", startMode: constant.ScreenModeFocused, wantMode: constant.ScreenModeTwoColumn},
		{name: "backward two to focused", key: "_", startMode: constant.ScreenModeTwoColumn, wantMode: constant.ScreenModeFocused},
		{name: "backward focused to single", key: "_", startMode: constant.ScreenModeFocused, wantMode: constant.ScreenModeSingleColumn},
		{name: "backward single to two", key: "_", startMode: constant.ScreenModeSingleColumn, wantMode: constant.ScreenModeTwoColumn},
	}
	for _, tt := range primaryCycles {
		t.Run(tt.name, func(t *testing.T) {
			model := initScreenModeInteractionModel(t)
			model.CurrentSelectedComponent = constant.ModifiedFilesComponentPanel
			model.ScreenMode = tt.startMode
			layout.TuiWindowSizing(model)

			Handle(keyPress(tt.key), model)

			if model.ScreenMode != tt.wantMode {
				t.Errorf("ScreenMode = %d, want %d", model.ScreenMode, tt.wantMode)
			}
			if model.CurrentSelectedComponent != constant.ModifiedFilesComponentPanel {
				t.Errorf("CurrentSelectedComponent = %q, want unchanged Modified Files", model.CurrentSelectedComponent)
			}
			switch tt.wantMode {
			case constant.ScreenModeTwoColumn:
				if model.DetailComponentPanelWidth != 84 {
					t.Errorf("two-column detail width = %d, want canonical reflow width 84", model.DetailComponentPanelWidth)
				}
			case constant.ScreenModeSingleColumn:
				if model.DetailComponentPanelWidth != 0 || model.CurrentRepoModifiedFilesInfoList.Width() != 118 {
					t.Errorf("single-column widths = detail %d/list %d, want 0/118", model.DetailComponentPanelWidth, model.CurrentRepoModifiedFilesInfoList.Width())
				}
			case constant.ScreenModeFocused:
				if model.CurrentRepoModifiedFilesInfoList.Width() != 118 || model.CurrentRepoModifiedFilesInfoList.Height() != model.WindowCoreContentHeight {
					t.Errorf("focused list size = %dx%d, want 118x%d", model.CurrentRepoModifiedFilesInfoList.Width(), model.CurrentRepoModifiedFilesInfoList.Height(), model.WindowCoreContentHeight)
				}
			}
		})
	}

	for _, selected := range []string{constant.DetailComponentPanel, constant.DetailComponentPanelTwo, constant.LogComponentPanel} {
		for _, tt := range []struct {
			name      string
			key       string
			startMode constant.ScreenMode
			wantMode  constant.ScreenMode
		}{
			{name: "forward from two", key: "=", startMode: constant.ScreenModeTwoColumn, wantMode: constant.ScreenModeFocused},
			{name: "forward from focused", key: "=", startMode: constant.ScreenModeFocused, wantMode: constant.ScreenModeTwoColumn},
			{name: "backward from two", key: "_", startMode: constant.ScreenModeTwoColumn, wantMode: constant.ScreenModeFocused},
			{name: "backward from focused", key: "_", startMode: constant.ScreenModeFocused, wantMode: constant.ScreenModeTwoColumn},
			{name: "repair corrupt single forward", key: "=", startMode: constant.ScreenModeSingleColumn, wantMode: constant.ScreenModeFocused},
			{name: "repair corrupt single backward", key: "_", startMode: constant.ScreenModeSingleColumn, wantMode: constant.ScreenModeFocused},
		} {
			t.Run(selected+" "+tt.name, func(t *testing.T) {
				model := initScreenModeInteractionModel(t)
				model.CurrentSelectedComponent = selected
				model.ScreenMode = tt.startMode

				Handle(keyPress(tt.key), model)

				if model.ScreenMode != tt.wantMode {
					t.Errorf("ScreenMode = %d, want %d", model.ScreenMode, tt.wantMode)
				}
				if model.CurrentSelectedComponent != selected {
					t.Errorf("CurrentSelectedComponent = %q, want unchanged %q", model.CurrentSelectedComponent, selected)
				}
			})
		}
	}
}

func TestScreenModeCycleUsesSignedNormalizedModulo(t *testing.T) {
	for _, tt := range []struct {
		value int
		want  int
	}{
		{value: -4, want: 2},
		{value: -1, want: 2},
		{value: 0, want: 0},
		{value: 4, want: 1},
	} {
		if got := normalizeScreenModeIndex(tt.value, 3); got != tt.want {
			t.Errorf("normalizeScreenModeIndex(%d, 3) = %d, want %d", tt.value, got, tt.want)
		}
	}
}

func TestScreenModeKeysAreBlockedByEveryPopup(t *testing.T) {
	for _, key := range []string{"=", "_"} {
		t.Run(key, func(t *testing.T) {
			model := initScreenModeInteractionModel(t)
			model.ScreenMode = constant.ScreenModeTwoColumn
			model.ShowPopUp.Store(true)
			model.IsTyping.Store(false)
			model.PopUpType = constant.ChoosePushTypePopUp

			Handle(keyPress(key), model)

			if model.ScreenMode != constant.ScreenModeTwoColumn {
				t.Errorf("ScreenMode = %d, want popup to preserve Two-column", model.ScreenMode)
			}
		})
	}
}

func TestRatioKeysOnlyChangeTwoColumnMode(t *testing.T) {
	for _, tt := range []struct {
		name  string
		mode  constant.ScreenMode
		key   string
		start float64
		want  float64
	}{
		{name: "two column plus", mode: constant.ScreenModeTwoColumn, key: "+", start: 0.40, want: 0.41},
		{name: "two column minus", mode: constant.ScreenModeTwoColumn, key: "-", start: 0.40, want: 0.39},
		{name: "two column plus clamps", mode: constant.ScreenModeTwoColumn, key: "+", start: settings.MAXLEFTPANELWIDTHRATIO, want: settings.MAXLEFTPANELWIDTHRATIO},
		{name: "two column minus clamps", mode: constant.ScreenModeTwoColumn, key: "-", start: settings.MINLEFTPANELWIDTHRATIO, want: settings.MINLEFTPANELWIDTHRATIO},
		{name: "single plus", mode: constant.ScreenModeSingleColumn, key: "+", start: 0.30, want: 0.30},
		{name: "single minus", mode: constant.ScreenModeSingleColumn, key: "-", start: 0.30, want: 0.30},
		{name: "focused plus", mode: constant.ScreenModeFocused, key: "+", start: 0.30, want: 0.30},
		{name: "focused minus", mode: constant.ScreenModeFocused, key: "-", start: 0.30, want: 0.30},
	} {
		t.Run(tt.name, func(t *testing.T) {
			model := initScreenModeInteractionModel(t)
			model.ScreenMode = tt.mode
			model.WindowLeftPanelRatio = tt.start
			layout.TuiWindowSizing(model)

			Handle(keyPress(tt.key), model)

			if math.Abs(model.WindowLeftPanelRatio-tt.want) > 0.000001 {
				t.Errorf("WindowLeftPanelRatio = %.2f, want %.2f", model.WindowLeftPanelRatio, tt.want)
			}
		})
	}
}

func TestNonTwoColumnRatioSurvivesForLaterTwoColumnLayout(t *testing.T) {
	for _, mode := range []constant.ScreenMode{constant.ScreenModeSingleColumn, constant.ScreenModeFocused} {
		t.Run(screenModeLabel(mode), func(t *testing.T) {
			model := initScreenModeInteractionModel(t)
			model.ScreenMode = mode
			model.WindowLeftPanelRatio = 0.42
			layout.TuiWindowSizing(model)

			Handle(keyPress("+"), model)
			Handle(keyPress("-"), model)
			model.ScreenMode = constant.ScreenModeTwoColumn
			layout.TuiWindowSizing(model)

			if math.Abs(model.WindowLeftPanelRatio-0.42) > 0.000001 {
				t.Errorf("WindowLeftPanelRatio = %.2f, want preserved 0.42", model.WindowLeftPanelRatio)
			}
			if wantWidth := int(float64(model.Width) * 0.42); model.WindowLeftPanelWidth != wantWidth {
				t.Errorf("WindowLeftPanelWidth = %d, want restored split width %d", model.WindowLeftPanelWidth, wantWidth)
			}
		})
	}
}

func TestSingleColumnEnterPromotionOnlyOnSuccessfulDrillDown(t *testing.T) {
	for _, selected := range []string{
		constant.ModifiedFilesComponentPanel,
		constant.CommitLogOrRefLogComponentPanel,
		constant.StashComponentPanel,
	} {
		t.Run(selected+" succeeds", func(t *testing.T) {
			model := initScreenModeInteractionModel(t)
			model.ScreenMode = constant.ScreenModeSingleColumn
			model.CurrentSelectedComponent = selected
			setDrillDownItems(model, selected, true)
			layout.TuiWindowSizing(model)

			handleNonTypingEnterKeyBindingInteraction(model)

			if model.ScreenMode != constant.ScreenModeFocused {
				t.Errorf("ScreenMode = %d, want Focused", model.ScreenMode)
			}
			if model.CurrentSelectedComponent != constant.DetailComponentPanel {
				t.Errorf("CurrentSelectedComponent = %q, want primary detail", model.CurrentSelectedComponent)
			}
			if model.DetailPanelParentComponent != selected {
				t.Errorf("DetailPanelParentComponent = %q, want %q", model.DetailPanelParentComponent, selected)
			}
		})

		t.Run(selected+" empty list", func(t *testing.T) {
			model := initScreenModeInteractionModel(t)
			model.ScreenMode = constant.ScreenModeSingleColumn
			model.CurrentSelectedComponent = selected
			setDrillDownItems(model, selected, false)
			layout.TuiWindowSizing(model)

			handleNonTypingEnterKeyBindingInteraction(model)

			if model.ScreenMode != constant.ScreenModeSingleColumn || model.CurrentSelectedComponent != selected {
				t.Errorf("empty-list Enter changed mode/component to %d/%q", model.ScreenMode, model.CurrentSelectedComponent)
			}
		})
	}

	t.Run("reflog succeeds", func(t *testing.T) {
		model := initScreenModeInteractionModel(t)
		model.ScreenMode = constant.ScreenModeSingleColumn
		model.CurrentSelectedComponent = constant.CommitLogOrRefLogComponentPanel
		model.CurrentCommitLogOrRefLogComponentShowing = constant.SHOW_REFLOG
		model.CurrentRepoRefLogInfoList.SetItems([]list.Item{screenModeTestItem("reflog")})
		layout.TuiWindowSizing(model)

		handleNonTypingEnterKeyBindingInteraction(model)

		if model.ScreenMode != constant.ScreenModeFocused || model.CurrentSelectedComponent != constant.DetailComponentPanel {
			t.Errorf("reflog Enter changed mode/component to %d/%q, want Focused/detail", model.ScreenMode, model.CurrentSelectedComponent)
		}
		if model.DetailPanelParentComponent != constant.CommitLogOrRefLogComponentPanel {
			t.Errorf("DetailPanelParentComponent = %q, want commit/reflog", model.DetailPanelParentComponent)
		}
	})

	t.Run("branch popup retains single column", func(t *testing.T) {
		model := initScreenModeInteractionModel(t)
		model.ScreenMode = constant.ScreenModeSingleColumn
		model.CurrentSelectedComponent = constant.LocalBranchOrTagOrRemoteOrWorktreeComponentPanel
		model.CurrentLocalBranchOrTagOrRemoteOrWorktreeComponentShowing = constant.SHOW_LOCAL_BRANCH
		model.CurrentRepoBranchesInfoList.SetItems([]list.Item{branch.GitBranchItem{BranchName: "other"}})
		layout.TuiWindowSizing(model)

		handleNonTypingEnterKeyBindingInteraction(model)

		if !model.ShowPopUp.Load() {
			t.Fatal("Enter did not open the branch popup")
		}
		if model.ScreenMode != constant.ScreenModeSingleColumn {
			t.Errorf("ScreenMode = %d, want Single-column beneath popup", model.ScreenMode)
		}
	})
}

func TestSlashPromotesOnlySingleColumnToFocused(t *testing.T) {
	for _, tt := range []struct {
		mode constant.ScreenMode
		want constant.ScreenMode
	}{
		{mode: constant.ScreenModeTwoColumn, want: constant.ScreenModeTwoColumn},
		{mode: constant.ScreenModeSingleColumn, want: constant.ScreenModeFocused},
		{mode: constant.ScreenModeFocused, want: constant.ScreenModeFocused},
	} {
		t.Run(screenModeLabel(tt.mode), func(t *testing.T) {
			model := initScreenModeInteractionModel(t)
			model.ScreenMode = tt.mode
			model.CurrentSelectedComponent = constant.ModifiedFilesComponentPanel
			layout.TuiWindowSizing(model)

			handleNonTypingSlashKeyBindingInteraction(model)

			if model.ScreenMode != tt.want {
				t.Errorf("ScreenMode = %d, want %d", model.ScreenMode, tt.want)
			}
			if model.CurrentSelectedComponent != constant.LogComponentPanel {
				t.Errorf("CurrentSelectedComponent = %q, want log", model.CurrentSelectedComponent)
			}
		})
	}
}

func initScreenModeInteractionModel(t *testing.T) *types.GittiModel {
	originalLanguageMapping := i18n.LANGUAGEMAPPING
	i18n.InitGittiLanguageMapping("EN")
	t.Cleanup(func() { i18n.LANGUAGEMAPPING = originalLanguageMapping })

	newList := func() list.Model {
		componentList := list.New([]list.Item{}, screenModeTestDelegate{}, 0, 0)
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

func setDrillDownItems(m *types.GittiModel, component string, populated bool) {
	items := []list.Item{}
	if populated {
		items = []list.Item{screenModeTestItem("item")}
	}
	switch component {
	case constant.ModifiedFilesComponentPanel:
		m.CurrentRepoModifiedFilesInfoList.SetItems(items)
	case constant.CommitLogOrRefLogComponentPanel:
		m.CurrentRepoCommitLogInfoList.SetItems(items)
	case constant.StashComponentPanel:
		m.CurrentRepoStashInfoList.SetItems(items)
	}
}

func keyPress(key string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Text: key, Code: []rune(key)[0]})
}

func screenModeLabel(mode constant.ScreenMode) string {
	switch mode {
	case constant.ScreenModeSingleColumn:
		return "single column"
	case constant.ScreenModeFocused:
		return "focused"
	default:
		return "two column"
	}
}
