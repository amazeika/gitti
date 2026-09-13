package interaction

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/gohyuhan/gitti/tui/constant"
	"github.com/gohyuhan/gitti/tui/layout"
	"github.com/gohyuhan/gitti/tui/types"
)

func TestWheelRoutesOnlyToVisibleViewport(t *testing.T) {
	for _, tt := range []struct {
		name       string
		mode       constant.ScreenMode
		selected   string
		showTwo    bool
		point      func(*types.GittiModel) (int, int)
		button     tea.MouseButton
		wantTarget string
	}{
		{
			name: "two column detail uses selected primary detail",
			mode: constant.ScreenModeTwoColumn, selected: constant.ModifiedFilesComponentPanel,
			point: detailWheelPoint, button: tea.MouseWheelDown, wantTarget: constant.DetailComponentPanel,
		},
		{
			name: "two column dual detail hit uses selected secondary not pointer subpanel",
			mode: constant.ScreenModeTwoColumn, selected: constant.DetailComponentPanelTwo, showTwo: true,
			point:  func(m *types.GittiModel) (int, int) { return m.WindowLeftPanelWidth, 0 },
			button: tea.MouseWheelDown, wantTarget: constant.DetailComponentPanelTwo,
		},
		{
			name: "two column log uses current log viewport",
			mode: constant.ScreenModeTwoColumn, selected: constant.ModifiedFilesComponentPanel,
			point: logWheelPoint, button: tea.MouseWheelDown, wantTarget: constant.LogComponentPanel,
		},
		{
			name: "two column primary stack is no op",
			mode: constant.ScreenModeTwoColumn, selected: constant.ModifiedFilesComponentPanel,
			point:  func(*types.GittiModel) (int, int) { return 0, 5 },
			button: tea.MouseWheelDown,
		},
		{
			name: "single column is no op",
			mode: constant.ScreenModeSingleColumn, selected: constant.ModifiedFilesComponentPanel,
			point:  func(m *types.GittiModel) (int, int) { return m.Width - 1, 5 },
			button: tea.MouseWheelDown,
		},
		{
			name: "focused primary detail",
			mode: constant.ScreenModeFocused, selected: constant.DetailComponentPanel,
			point:  func(m *types.GittiModel) (int, int) { return m.Width - 1, m.Height - 2 },
			button: tea.MouseWheelDown, wantTarget: constant.DetailComponentPanel,
		},
		{
			name: "focused secondary detail is one rectangle despite dual state",
			mode: constant.ScreenModeFocused, selected: constant.DetailComponentPanelTwo, showTwo: true,
			point:  func(m *types.GittiModel) (int, int) { return 0, m.Height - 2 },
			button: tea.MouseWheelDown, wantTarget: constant.DetailComponentPanelTwo,
		},
		{
			name: "focused log uses current log viewport",
			mode: constant.ScreenModeFocused, selected: constant.LogComponentPanel,
			point:  func(m *types.GittiModel) (int, int) { return m.Width / 2, m.Height / 2 },
			button: tea.MouseWheelDown, wantTarget: constant.LogComponentPanel,
		},
		{
			name: "focused primary panel is no op",
			mode: constant.ScreenModeFocused, selected: constant.CommitLogOrRefLogComponentPanel,
			point:  func(m *types.GittiModel) (int, int) { return m.Width / 2, m.Height / 2 },
			button: tea.MouseWheelDown,
		},
		{
			name: "footer is no op",
			mode: constant.ScreenModeFocused, selected: constant.DetailComponentPanel,
			point:  func(m *types.GittiModel) (int, int) { return 0, m.Height - 1 },
			button: tea.MouseWheelDown,
		},
		{
			name: "outside terminal is no op",
			mode: constant.ScreenModeFocused, selected: constant.DetailComponentPanel,
			point:  func(m *types.GittiModel) (int, int) { return m.Width, 0 },
			button: tea.MouseWheelDown,
		},
		{
			name: "horizontal wheel uses same visible resolver",
			mode: constant.ScreenModeTwoColumn, selected: constant.LogComponentPanel,
			point: logWheelPoint, button: tea.MouseWheelRight, wantTarget: constant.LogComponentPanel,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			model := initMouseScreenModeModel(t)
			model.ScreenMode = tt.mode
			model.CurrentSelectedComponent = tt.selected
			model.ShowDetailPanelTwo.Store(tt.showTwo)
			layout.TuiWindowSizing(model)
			prepareScrollableViewports(model)
			x, y := tt.point(model)
			before := wheelOffsets(model)

			GittiMouseInteraction(wheelMessage(x, y, tt.button), model)

			after := wheelOffsets(model)
			assertOnlyWheelTargetChanged(t, before, after, tt.wantTarget, tt.button)
		})
	}
}

func TestWheelIgnoresTerminalWarning(t *testing.T) {
	model := initMouseScreenModeModel(t)
	model.Width = constant.MinWidth - 1
	model.Height = constant.MinHeight - 1
	model.ScreenMode = constant.ScreenModeFocused
	model.CurrentSelectedComponent = constant.DetailComponentPanel
	layout.TuiWindowSizing(model)
	prepareScrollableViewports(model)
	before := wheelOffsets(model)

	GittiMouseInteraction(wheelMessage(0, 0, tea.MouseWheelDown), model)

	if after := wheelOffsets(model); after != before {
		t.Errorf("warning-screen wheel changed offsets from %+v to %+v", before, after)
	}
}

func TestLineEditingWheelDirectionAndTitleRegion(t *testing.T) {
	t.Run("vertical wheel over title is blocked", func(t *testing.T) {
		model := initMouseScreenModeModel(t)
		model.ScreenMode = constant.ScreenModeTwoColumn
		model.CurrentSelectedComponent = constant.DetailComponentPanelTwo
		model.DetailPanelParentComponent = constant.ModifiedFilesComponentPanel
		model.ShowDetailPanelTwo.Store(true)
		layout.TuiWindowSizing(model)
		prepareScrollableViewports(model)
		model.IsLineEditingState.Store(true)
		before := wheelOffsets(model)

		GittiMouseInteraction(wheelMessage(model.WindowLeftPanelWidth, 0, tea.MouseWheelDown), model)

		if after := wheelOffsets(model); after != before {
			t.Errorf("vertical line-editing wheel changed offsets from %+v to %+v", before, after)
		}
	})

	t.Run("horizontal wheel over title scrolls selected visible detail", func(t *testing.T) {
		model := initMouseScreenModeModel(t)
		model.ScreenMode = constant.ScreenModeTwoColumn
		model.CurrentSelectedComponent = constant.DetailComponentPanelTwo
		model.DetailPanelParentComponent = constant.ModifiedFilesComponentPanel
		model.ShowDetailPanelTwo.Store(true)
		layout.TuiWindowSizing(model)
		prepareScrollableViewports(model)
		model.IsLineEditingState.Store(true)
		before := wheelOffsets(model)

		GittiMouseInteraction(wheelMessage(model.WindowLeftPanelWidth, 0, tea.MouseWheelRight), model)

		after := wheelOffsets(model)
		assertOnlyWheelTargetChanged(t, before, after, constant.DetailComponentPanelTwo, tea.MouseWheelRight)
	})
}

type viewportOffsets struct {
	detailX    int
	detailY    int
	detailTwoX int
	detailTwoY int
	logX       int
	logY       int
}

func prepareScrollableViewports(m *types.GittiModel) {
	var lines strings.Builder
	lines.WriteString(strings.Repeat("x", 300))
	for index := 0; index < 100; index++ {
		fmt.Fprintf(&lines, "\nline %03d", index)
	}
	content := lines.String()
	m.DetailPanelViewport.SetContent(content)
	m.DetailPanelTwoViewport.SetContent(content)
	m.CurrentLogComponentViewport.SetContent(content)
	m.DetailPanelViewport.SetXOffset(0)
	m.DetailPanelViewport.SetYOffset(0)
	m.DetailPanelTwoViewport.SetXOffset(0)
	m.DetailPanelTwoViewport.SetYOffset(0)
	m.CurrentLogComponentViewport.SetXOffset(0)
	m.CurrentLogComponentViewport.SetYOffset(0)
}

func wheelOffsets(m *types.GittiModel) viewportOffsets {
	return viewportOffsets{
		detailX: m.DetailPanelViewport.XOffset(), detailY: m.DetailPanelViewport.YOffset(),
		detailTwoX: m.DetailPanelTwoViewport.XOffset(), detailTwoY: m.DetailPanelTwoViewport.YOffset(),
		logX: m.CurrentLogComponentViewport.XOffset(), logY: m.CurrentLogComponentViewport.YOffset(),
	}
}

func assertOnlyWheelTargetChanged(t *testing.T, before viewportOffsets, after viewportOffsets, target string, button tea.MouseButton) {
	t.Helper()
	want := before
	switch target {
	case constant.DetailComponentPanel:
		if button == tea.MouseWheelRight {
			want.detailX++
		} else {
			want.detailY++
		}
	case constant.DetailComponentPanelTwo:
		if button == tea.MouseWheelRight {
			want.detailTwoX++
		} else {
			want.detailTwoY++
		}
	case constant.LogComponentPanel:
		if button == tea.MouseWheelRight {
			want.logX++
		} else {
			want.logY++
		}
	}
	if after != want {
		t.Errorf("wheel offsets = %+v, want %+v", after, want)
	}
}

func detailWheelPoint(m *types.GittiModel) (int, int) {
	return m.WindowLeftPanelWidth, m.DetailComponentPanelHeight / 2
}

func logWheelPoint(m *types.GittiModel) (int, int) {
	return m.WindowLeftPanelWidth, m.DetailComponentPanelHeight + 2
}

func wheelMessage(x int, y int, button tea.MouseButton) tea.MouseMsg {
	return tea.MouseWheelMsg(tea.Mouse{X: x, Y: y, Button: button})
}
