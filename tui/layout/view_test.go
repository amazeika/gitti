package layout

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	gitticonst "github.com/gohyuhan/gitti/constant"
	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/tui/constant"
	keybindingPopUp "github.com/gohyuhan/gitti/tui/popup/keybinding"
)

const (
	primaryDetailSentinel   = "PRIMARY_DETAIL_SENTINEL"
	secondaryDetailSentinel = "SECONDARY_DETAIL_SENTINEL"
	logSentinel             = "LOG_SENTINEL"
)

func TestRenderModeContentShowsOnlyPanelsOwnedByMode(t *testing.T) {
	tests := []struct {
		name       string
		mode       constant.ScreenMode
		selected   string
		want       []string
		doNotWant  []string
		showSecond bool
	}{
		{
			name:       "two column composes both stacks",
			mode:       constant.ScreenModeTwoColumn,
			selected:   constant.ModifiedFilesComponentPanel,
			want:       []string{primaryDetailSentinel, secondaryDetailSentinel, logSentinel},
			showSecond: true,
		},
		{
			name:      "single column omits extended panels",
			mode:      constant.ScreenModeSingleColumn,
			selected:  constant.ModifiedFilesComponentPanel,
			doNotWant: []string{primaryDetailSentinel, secondaryDetailSentinel, logSentinel},
		},
		{
			name:       "focused primary detail omits sibling and log",
			mode:       constant.ScreenModeFocused,
			selected:   constant.DetailComponentPanel,
			want:       []string{primaryDetailSentinel},
			doNotWant:  []string{secondaryDetailSentinel, logSentinel},
			showSecond: true,
		},
		{
			name:       "focused secondary detail omits sibling and log",
			mode:       constant.ScreenModeFocused,
			selected:   constant.DetailComponentPanelTwo,
			want:       []string{secondaryDetailSentinel},
			doNotWant:  []string{primaryDetailSentinel, logSentinel},
			showSecond: true,
		},
		{
			name:      "focused log omits detail panels",
			mode:      constant.ScreenModeFocused,
			selected:  constant.LogComponentPanel,
			want:      []string{logSentinel},
			doNotWant: []string{primaryDetailSentinel, secondaryDetailSentinel},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := initScreenModeLayoutModel(t, 120, 40)
			model.ScreenMode = tt.mode
			model.CurrentSelectedComponent = tt.selected
			model.ShowDetailPanelTwo.Store(tt.showSecond)
			model.DetailPanelViewport.SetContent(primaryDetailSentinel)
			model.DetailPanelTwoViewport.SetContent(secondaryDetailSentinel)
			model.CurrentLogComponentViewport.SetContent(logSentinel)
			TuiWindowSizing(model)

			view := renderModeContent(model)

			for _, marker := range tt.want {
				if !strings.Contains(view, marker) {
					t.Errorf("mode content does not contain visible panel marker %q", marker)
				}
			}
			for _, marker := range tt.doNotWant {
				if strings.Contains(view, marker) {
					t.Errorf("mode content contains hidden panel marker %q", marker)
				}
			}
			if got := lipgloss.Width(view); got != model.Width {
				t.Errorf("mode content width = %d, want terminal width %d", got, model.Width)
			}
			if got := lipgloss.Height(view); got != model.Height-constant.MainPageKeyBindingLayoutPanelHeight {
				t.Errorf("mode content height = %d, want main area height %d", got, model.Height-constant.MainPageKeyBindingLayoutPanelHeight)
			}
		})
	}
}

func TestRenderBorderedPanelTruncatesStyledContentByDisplayWidth(t *testing.T) {
	styledContent := lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("123456789")

	view := renderBorderedPanel(lipgloss.NewStyle().Border(lipgloss.NormalBorder()), 8, 1, styledContent)
	plainView := ansi.Strip(view)

	if !strings.Contains(plainView, "12345…") {
		t.Errorf("truncated styled panel = %q, want visible content %q", plainView, "12345…")
	}
	for _, line := range strings.Split(view, "\n") {
		if got := ansi.StringWidth(line); got > 8 {
			t.Errorf("styled panel line width = %d, want at most 8", got)
		}
	}
}

func TestFocusedGitStatusFillsMainArea(t *testing.T) {
	model := initScreenModeLayoutModel(t, 100, 30)
	model.ScreenMode = constant.ScreenModeFocused
	model.CurrentSelectedComponent = constant.GitStatusComponentPanel
	model.RepoName = "focused-repository"
	TuiWindowSizing(model)

	view := renderModeContent(model)

	if !strings.Contains(view, "focused-repository") {
		t.Error("focused Git status does not retain its status content")
	}
	if got := lipgloss.Width(view); got != model.Width {
		t.Errorf("focused Git status width = %d, want %d", got, model.Width)
	}
	if got := lipgloss.Height(view); got != model.Height-constant.MainPageKeyBindingLayoutPanelHeight {
		t.Errorf("focused Git status height = %d, want %d", got, model.Height-constant.MainPageKeyBindingLayoutPanelHeight)
	}
}

func TestMainPageSharedFrameFitsEveryMode(t *testing.T) {
	for _, size := range []struct {
		width  int
		height int
	}{{width: 80, height: 24}, {width: 140, height: 50}} {
		for _, mode := range []constant.ScreenMode{
			constant.ScreenModeTwoColumn,
			constant.ScreenModeSingleColumn,
			constant.ScreenModeFocused,
		} {
			t.Run(screenModeTestName(mode, size.width, size.height), func(t *testing.T) {
				model := initScreenModeLayoutModel(t, size.width, size.height)
				model.ScreenMode = mode
				model.CurrentSelectedComponent = constant.ModifiedFilesComponentPanel
				TuiWindowSizing(model)

				view := GittiMainPageView(model)

				if got := lipgloss.Width(view); got != size.width {
					t.Errorf("main page width = %d, want %d", got, size.width)
				}
				if got := lipgloss.Height(view); got != size.height {
					t.Errorf("main page height = %d, want %d", got, size.height)
				}
				if !strings.Contains(view, gitticonst.APPVERSION) {
					t.Error("shared footer does not contain the application version")
				}
			})
		}
	}
}

func TestTerminalWarningTakesPrecedenceOverEveryMode(t *testing.T) {
	for _, mode := range []constant.ScreenMode{
		constant.ScreenModeTwoColumn,
		constant.ScreenModeSingleColumn,
		constant.ScreenModeFocused,
	} {
		t.Run(screenModeTestName(mode, constant.MinWidth-1, constant.MinHeight-1), func(t *testing.T) {
			model := initScreenModeLayoutModel(t, constant.MinWidth-1, constant.MinHeight-1)
			model.ScreenMode = mode
			model.CurrentSelectedComponent = constant.DetailComponentPanel
			model.DetailPanelViewport.SetContent(primaryDetailSentinel)

			view := GittiMainPageView(model)

			if !strings.Contains(view, i18n.LANGUAGEMAPPING.TerminalSizeWarning) {
				t.Error("terminal warning is absent")
			}
			if strings.Contains(view, primaryDetailSentinel) {
				t.Error("mode content rendered ahead of the terminal warning")
			}
		})
	}
}

func TestPopupOverlaysEveryBaseModeWithoutChangingIt(t *testing.T) {
	for _, mode := range []constant.ScreenMode{
		constant.ScreenModeTwoColumn,
		constant.ScreenModeSingleColumn,
		constant.ScreenModeFocused,
	} {
		t.Run(screenModeTestName(mode, 120, 50), func(t *testing.T) {
			model := initScreenModeLayoutModel(t, 120, 50)
			model.ScreenMode = mode
			model.CurrentSelectedComponent = constant.ModifiedFilesComponentPanel
			TuiWindowSizing(model)
			baseView := GittiMainPageView(model)

			keybindingPopUp.InitKeybindingAndFeatureInstructionsPopUpModel(model)
			model.PopUpType = constant.KeybindingAndFeatureInstructionsPopUp
			model.ShowPopUp.Store(true)
			overlaidView := GittiMainPageView(model)

			if overlaidView == baseView {
				t.Error("opening a popup did not change the composed view")
			}
			if model.ScreenMode != mode {
				t.Errorf("ScreenMode = %d after popup render, want %d", model.ScreenMode, mode)
			}
			if got := lipgloss.Width(overlaidView); got != model.Width {
				t.Errorf("popup composition width = %d, want %d", got, model.Width)
			}
			if got := lipgloss.Height(overlaidView); got != model.Height {
				t.Errorf("popup composition height = %d, want %d", got, model.Height)
			}
		})
	}
}

func TestKeybindingBarAdvertisesScreenModesOnlyOnMainPage(t *testing.T) {
	model := initScreenModeLayoutModel(t, 160, 40)
	model.CurrentSelectedComponent = constant.ModifiedFilesComponentPanel

	mainBar := ansi.Strip(renderKeyBindingComponentPanel(model.Width, model))
	screenModeHelp := fmt.Sprintf("[%s] %s", i18n.LANGUAGEMAPPING.ScreenModeNavigationKey, i18n.LANGUAGEMAPPING.ScreenModeNavigationDescription)
	pageNavigationHelp := fmt.Sprintf("[%s] %s", i18n.LANGUAGEMAPPING.PageNavigationKey, i18n.LANGUAGEMAPPING.PageNavigationDescription)
	if !strings.Contains(mainBar, screenModeHelp) {
		t.Errorf("normal keybinding bar = %q, want screen mode help %q", mainBar, screenModeHelp)
	}
	if strings.Index(mainBar, screenModeHelp) > strings.Index(mainBar, pageNavigationHelp) {
		t.Errorf("normal keybinding bar = %q, want screen mode help before page navigation", mainBar)
	}

	model.ShowPopUp.Store(true)
	model.PopUpType = constant.CommitPopUp
	popupBar := ansi.Strip(renderKeyBindingComponentPanel(model.Width, model))
	if strings.Contains(popupBar, screenModeHelp) {
		t.Errorf("popup keybinding bar = %q, must omit screen mode help", popupBar)
	}
}

func screenModeTestName(mode constant.ScreenMode, width int, height int) string {
	return strings.ReplaceAll(fmt.Sprintf("%s at %dx%d", screenModeName(mode), width, height), " ", "_")
}

func screenModeName(mode constant.ScreenMode) string {
	switch mode {
	case constant.ScreenModeSingleColumn:
		return "single column"
	case constant.ScreenModeFocused:
		return "focused"
	default:
		return "two column"
	}
}
