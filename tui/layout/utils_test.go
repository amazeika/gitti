package layout

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/viewport"
	"github.com/gohyuhan/gitti/executor"
	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/logging"
	"github.com/gohyuhan/gitti/settings"
	"github.com/gohyuhan/gitti/tui/constant"
	"github.com/gohyuhan/gitti/tui/initialize"
	"github.com/gohyuhan/gitti/tui/types"
)

// ------------------------------------
//
//	Build an initialized model with deterministic terminal geometry for layout tests.
//
// ------------------------------------
func initScreenModeLayoutModel(t *testing.T, width int, height int) *types.GittiModel {
	originalSettings := settings.GITTICONFIGSETTINGS
	originalExecutor := executor.GittiCmdExecutor
	originalLanguageMapping := i18n.LANGUAGEMAPPING
	settings.GITTICONFIGSETTINGS = &settings.GittiConfigSettings{LeftPanelWidthRatio: 0.3}
	executor.InitCmdExecutor(t.TempDir())
	i18n.InitGittiLanguageMapping("EN")
	t.Cleanup(func() {
		settings.GITTICONFIGSETTINGS = originalSettings
		executor.GittiCmdExecutor = originalExecutor
		i18n.LANGUAGEMAPPING = originalLanguageMapping
	})
	model := initialize.InitGittiModel(nil, "/repo", "repo", nil, logging.InitGittiLogging(10, make(chan string, 1), 3), nil, nil)
	model.Width = width
	model.Height = height
	model.WindowLeftPanelRatio = 0.3
	return model
}

func TestTuiWindowSizingUsesModeSpecificWidths(t *testing.T) {
	tests := []struct {
		name            string
		mode            constant.ScreenMode
		selected        string
		wantLeftWidth   int
		wantDetailWidth int
	}{
		{name: "two column", mode: constant.ScreenModeTwoColumn, selected: constant.ModifiedFilesComponentPanel, wantLeftWidth: 36, wantDetailWidth: 84},
		{name: "single column", mode: constant.ScreenModeSingleColumn, selected: constant.ModifiedFilesComponentPanel, wantLeftWidth: 120, wantDetailWidth: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := initScreenModeLayoutModel(t, 120, 40)
			model.ScreenMode = tt.mode
			model.CurrentSelectedComponent = tt.selected

			TuiWindowSizing(model)

			if model.WindowLeftPanelWidth != tt.wantLeftWidth {
				t.Errorf("WindowLeftPanelWidth = %d, want %d", model.WindowLeftPanelWidth, tt.wantLeftWidth)
			}
			if model.DetailComponentPanelWidth != tt.wantDetailWidth {
				t.Errorf("DetailComponentPanelWidth = %d, want %d", model.DetailComponentPanelWidth, tt.wantDetailWidth)
			}
		})
	}
}

func TestSingleColumnUsesTwoColumnStackHeightPolicyAtFullWidth(t *testing.T) {
	twoColumn := initScreenModeLayoutModel(t, 120, 40)
	twoColumn.ScreenMode = constant.ScreenModeTwoColumn
	TuiWindowSizing(twoColumn)

	singleColumn := initScreenModeLayoutModel(t, 120, 40)
	singleColumn.ScreenMode = constant.ScreenModeSingleColumn
	TuiWindowSizing(singleColumn)

	if singleColumn.CurrentRepoModifiedFilesInfoList.Width() != 118 {
		t.Errorf("single-column Modified Files width = %d, want 118", singleColumn.CurrentRepoModifiedFilesInfoList.Width())
	}
	if singleColumn.ModifiedFilesComponentPanelHeight != twoColumn.ModifiedFilesComponentPanelHeight {
		t.Errorf("single-column selected height = %d, want two-column height %d", singleColumn.ModifiedFilesComponentPanelHeight, twoColumn.ModifiedFilesComponentPanelHeight)
	}
	if singleColumn.LocalBranchesComponentPanelHeight != twoColumn.LocalBranchesComponentPanelHeight {
		t.Errorf("single-column unselected height = %d, want two-column height %d", singleColumn.LocalBranchesComponentPanelHeight, twoColumn.LocalBranchesComponentPanelHeight)
	}
}

func TestTuiWindowSizingKeepsSingleColumnPrimaryFocus(t *testing.T) {
	for _, selected := range constant.ComponentPanelNavigationList {
		t.Run(selected, func(t *testing.T) {
			model := initScreenModeLayoutModel(t, 120, 40)
			model.ScreenMode = constant.ScreenModeSingleColumn
			model.CurrentSelectedComponent = selected

			TuiWindowSizing(model)

			if model.ScreenMode != constant.ScreenModeSingleColumn {
				t.Errorf("ScreenMode = %d, want ScreenModeSingleColumn for primary component %q", model.ScreenMode, selected)
			}
			if model.CurrentSelectedComponent != selected {
				t.Errorf("CurrentSelectedComponent = %q, want preserved %q", model.CurrentSelectedComponent, selected)
			}
		})
	}
}

func TestTuiWindowSizingRepairsSingleColumnExtendedFocus(t *testing.T) {
	for _, selected := range []string{
		constant.DetailComponentPanel,
		constant.DetailComponentPanelTwo,
		constant.LogComponentPanel,
	} {
		t.Run(selected, func(t *testing.T) {
			model := initScreenModeLayoutModel(t, 120, 40)
			model.ScreenMode = constant.ScreenModeSingleColumn
			model.CurrentSelectedComponent = selected

			TuiWindowSizing(model)

			if model.ScreenMode != constant.ScreenModeFocused {
				t.Errorf("ScreenMode = %d, want defensive promotion to ScreenModeFocused", model.ScreenMode)
			}
			if model.CurrentSelectedComponent != selected {
				t.Errorf("CurrentSelectedComponent = %q, want preserved %q", model.CurrentSelectedComponent, selected)
			}
		})
	}
}

func TestFocusedModeSizesSelectedPanelToMainArea(t *testing.T) {
	tests := []struct {
		name     string
		selected string
		size     func(*types.GittiModel) (int, int)
	}{
		{name: "branches", selected: constant.LocalBranchOrTagOrRemoteOrWorktreeComponentPanel, size: func(m *types.GittiModel) (int, int) {
			return m.CurrentRepoBranchesInfoList.Width(), m.CurrentRepoBranchesInfoList.Height()
		}},
		{name: "modified files", selected: constant.ModifiedFilesComponentPanel, size: func(m *types.GittiModel) (int, int) {
			return m.CurrentRepoModifiedFilesInfoList.Width(), m.CurrentRepoModifiedFilesInfoList.Height()
		}},
		{name: "commit log", selected: constant.CommitLogOrRefLogComponentPanel, size: func(m *types.GittiModel) (int, int) {
			return m.CurrentRepoCommitLogInfoList.Width(), m.CurrentRepoCommitLogInfoList.Height()
		}},
		{name: "stash", selected: constant.StashComponentPanel, size: func(m *types.GittiModel) (int, int) {
			return m.CurrentRepoStashInfoList.Width(), m.CurrentRepoStashInfoList.Height()
		}},
		{name: "primary detail", selected: constant.DetailComponentPanel, size: func(m *types.GittiModel) (int, int) {
			return m.DetailPanelViewport.Width(), m.DetailPanelViewport.Height()
		}},
		{name: "secondary detail", selected: constant.DetailComponentPanelTwo, size: func(m *types.GittiModel) (int, int) {
			return m.DetailPanelTwoViewport.Width(), m.DetailPanelTwoViewport.Height()
		}},
		{name: "log", selected: constant.LogComponentPanel, size: func(m *types.GittiModel) (int, int) {
			return m.CurrentLogComponentViewport.Width(), m.CurrentLogComponentViewport.Height()
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := initScreenModeLayoutModel(t, 120, 40)
			model.ScreenMode = constant.ScreenModeFocused
			model.CurrentSelectedComponent = tt.selected

			TuiWindowSizing(model)

			width, height := tt.size(model)
			if width != 118 {
				t.Errorf("selected panel inner width = %d, want 118", width)
			}
			if height != model.WindowCoreContentHeight {
				t.Errorf("selected panel height = %d, want main-content height %d", height, model.WindowCoreContentHeight)
			}
		})
	}
}

func TestTwoColumnLogDetailKeepsLogFocusStackDistribution(t *testing.T) {
	logFocused := initScreenModeLayoutModel(t, 120, 40)
	logFocused.ScreenMode = constant.ScreenModeTwoColumn
	logFocused.CurrentSelectedComponent = constant.LogComponentPanel
	TuiWindowSizing(logFocused)
	wantHeights := leftPanelHeights(logFocused)

	for _, selected := range []string{constant.DetailComponentPanel, constant.DetailComponentPanelTwo} {
		t.Run(selected, func(t *testing.T) {
			model := initScreenModeLayoutModel(t, 120, 40)
			model.ScreenMode = constant.ScreenModeTwoColumn
			model.CurrentSelectedComponent = selected
			model.DetailPanelParentComponent = constant.LogComponentPanel

			TuiWindowSizing(model)

			gotHeights := leftPanelHeights(model)
			for index := range wantHeights {
				if gotHeights[index] != wantHeights[index] {
					t.Errorf("left panel height %d = %d, want log-focus height %d", index, gotHeights[index], wantHeights[index])
				}
			}
		})
	}
}

func TestDetailOffsetsSurviveReflowWhileValid(t *testing.T) {
	for _, detail := range detailViewportCases() {
		t.Run(detail.name, func(t *testing.T) {
			model := initScreenModeLayoutModel(t, 100, 30)
			model.ScreenMode = constant.ScreenModeFocused
			model.CurrentSelectedComponent = detail.selected
			selectedViewport := detail.viewport(model)
			selectedViewport.SetContent(strings.Repeat("x", 200) + "\n" + numberedLines(80))
			TuiWindowSizing(model)
			*detail.trackedOffset(model) = 25
			selectedViewport.SetXOffset(25)
			selectedViewport.SetYOffset(20)

			TuiWindowSizing(model)

			if selectedViewport.XOffset() != 25 || *detail.trackedOffset(model) != 25 {
				t.Errorf("horizontal offsets = viewport %d, tracked %d; want both 25", selectedViewport.XOffset(), *detail.trackedOffset(model))
			}
			if selectedViewport.YOffset() != 20 {
				t.Errorf("vertical offset = %d, want 20", selectedViewport.YOffset())
			}
		})
	}
}

func TestLogOffsetsSurviveReflowAndClampPermanently(t *testing.T) {
	model := initScreenModeLayoutModel(t, 80, 24)
	model.ScreenMode = constant.ScreenModeTwoColumn
	model.CurrentSelectedComponent = constant.LogComponentPanel
	logViewport := &model.CurrentLogComponentViewport
	logViewport.SetContent(strings.Repeat("x", 200) + "\n" + numberedLines(80))
	TuiWindowSizing(model)
	logViewport.SetXOffset(120)
	logViewport.SetYOffset(60)

	TuiWindowSizing(model)
	if logViewport.XOffset() != 120 || logViewport.YOffset() != 60 {
		t.Fatalf("valid log offsets after reflow = (%d, %d), want (120, 60)", logViewport.XOffset(), logViewport.YOffset())
	}

	model.Width = 160
	model.Height = 70
	model.ScreenMode = constant.ScreenModeFocused
	TuiWindowSizing(model)

	wantX := 200 - logViewport.Width()
	wantY := logViewport.TotalLineCount() - logViewport.Height()
	if logViewport.XOffset() != wantX || logViewport.YOffset() != wantY {
		t.Errorf("clamped log offsets = (%d, %d), want (%d, %d)", logViewport.XOffset(), logViewport.YOffset(), wantX, wantY)
	}

	model.Width = 80
	model.Height = 24
	model.ScreenMode = constant.ScreenModeTwoColumn
	TuiWindowSizing(model)
	if logViewport.XOffset() != wantX || logViewport.YOffset() != wantY {
		t.Errorf("log offsets after narrowing = (%d, %d), want prior clamp (%d, %d)", logViewport.XOffset(), logViewport.YOffset(), wantX, wantY)
	}
}

func TestDetailOffsetsClampPermanentlyWhenViewportGrows(t *testing.T) {
	for _, detail := range detailViewportCases() {
		t.Run(detail.name, func(t *testing.T) {
			model := initScreenModeLayoutModel(t, 80, 24)
			model.ScreenMode = constant.ScreenModeTwoColumn
			model.CurrentSelectedComponent = detail.selected
			selectedViewport := detail.viewport(model)
			selectedViewport.SetContent(strings.Repeat("x", 200) + "\n" + numberedLines(80))
			TuiWindowSizing(model)
			*detail.trackedOffset(model) = 120
			selectedViewport.SetXOffset(120)
			selectedViewport.SetYOffset(60)

			model.Width = 160
			model.Height = 70
			model.ScreenMode = constant.ScreenModeFocused
			TuiWindowSizing(model)

			wantX := 200 - selectedViewport.Width()
			wantY := selectedViewport.TotalLineCount() - selectedViewport.Height()
			if selectedViewport.XOffset() != wantX || *detail.trackedOffset(model) != wantX {
				t.Errorf("clamped horizontal offsets = viewport %d, tracked %d; want both %d", selectedViewport.XOffset(), *detail.trackedOffset(model), wantX)
			}
			if selectedViewport.YOffset() != wantY {
				t.Errorf("clamped vertical offset = %d, want %d", selectedViewport.YOffset(), wantY)
			}

			model.Width = 80
			model.Height = 24
			model.ScreenMode = constant.ScreenModeTwoColumn
			TuiWindowSizing(model)

			if selectedViewport.XOffset() != wantX || *detail.trackedOffset(model) != wantX {
				t.Errorf("horizontal offsets after narrowing = viewport %d, tracked %d; want prior clamp %d", selectedViewport.XOffset(), *detail.trackedOffset(model), wantX)
			}
			if selectedViewport.YOffset() != wantY {
				t.Errorf("vertical offset after narrowing = %d, want prior clamp %d", selectedViewport.YOffset(), wantY)
			}
		})
	}
}

func leftPanelHeights(m *types.GittiModel) []int {
	return []int{
		m.LocalBranchesComponentPanelHeight,
		m.TagsComponentPanelHeight,
		m.RemoteComponentPanelHeight,
		m.WorktreeComponentPanelHeight,
		m.ModifiedFilesComponentPanelHeight,
		m.CommitLogComponentPanelHeight,
		m.RefLogComponentPanelHeight,
		m.StashComponentPanelHeight,
	}
}

// ------------------------------------
//
//	Describe both detail viewports and their synchronized horizontal-offset fields.
//
// ------------------------------------
func detailViewportCases() []struct {
	name          string
	selected      string
	viewport      func(*types.GittiModel) *viewport.Model
	trackedOffset func(*types.GittiModel) *int
} {
	return []struct {
		name          string
		selected      string
		viewport      func(*types.GittiModel) *viewport.Model
		trackedOffset func(*types.GittiModel) *int
	}{
		{
			name:     "primary detail",
			selected: constant.DetailComponentPanel,
			viewport: func(m *types.GittiModel) *viewport.Model { return &m.DetailPanelViewport },
			trackedOffset: func(m *types.GittiModel) *int {
				return &m.DetailPanelViewportOffset
			},
		},
		{
			name:     "secondary detail",
			selected: constant.DetailComponentPanelTwo,
			viewport: func(m *types.GittiModel) *viewport.Model { return &m.DetailPanelTwoViewport },
			trackedOffset: func(m *types.GittiModel) *int {
				return &m.DetailPanelTwoViewportOffset
			},
		},
	}
}

// ------------------------------------
//
//	Create distinct viewport lines without relying on wrapping behavior.
//
// ------------------------------------
func numberedLines(count int) string {
	var lines strings.Builder
	for index := range count {
		fmt.Fprintf(&lines, "line-%03d\n", index)
	}
	return lines.String()
}
