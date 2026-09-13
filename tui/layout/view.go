package layout

import (
	"fmt"

	"charm.land/lipgloss/v2"

	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/tui/constant"
	"github.com/gohyuhan/gitti/tui/popup"
	"github.com/gohyuhan/gitti/tui/style"
	"github.com/gohyuhan/gitti/tui/types"
)

// ------------------------------------
//
//	Assemble and return the full Gitti main page view string. Shows a terminal
//	size warning if the window is too small. Otherwise joins all left-column
//	panels and the right-column detail/log panels, then overlays any active
//	popup centered on screen.
//
// ------------------------------------
func GittiMainPageView(m *types.GittiModel) string {
	if m.Width < constant.MinWidth || m.Height < constant.MinHeight {
		title := style.NewStyle.
			Bold(true).
			Render(i18n.LANGUAGEMAPPING.TerminalSizeWarning)

		// Styles for the metric labels and values
		labelStyle := style.NewStyle
		passStyle := style.NewStyle.Foreground(style.ColorGreenSoft)
		failStyle := style.NewStyle.Foreground(style.ColorError)

		// Height
		heightStatus := passStyle.Render(fmt.Sprintf("%s: %v", i18n.LANGUAGEMAPPING.CurrentTerminalHeight, m.Height))
		if m.Height < constant.MinHeight {
			heightStatus = failStyle.Render(fmt.Sprintf("%s: %v", i18n.LANGUAGEMAPPING.CurrentTerminalWidth, m.Height))
		}

		// Width
		widthStatus := passStyle.Render(fmt.Sprintf("%s: %v", i18n.LANGUAGEMAPPING.CurrentTerminalHeight, m.Width))
		if m.Width < constant.MinWidth {
			widthStatus = failStyle.Render(fmt.Sprintf("%s: %v", i18n.LANGUAGEMAPPING.CurrentTerminalWidth, m.Width))
		}

		// Combine formatted text
		warningLine := lipgloss.JoinVertical(
			lipgloss.Center,
			title,
			fmt.Sprintf(
				"\n%s %d\n%s %d\n%s\n%s",
				labelStyle.Render(fmt.Sprintf("%s: ", i18n.LANGUAGEMAPPING.MinimumTerminalHeight)), constant.MinHeight,
				labelStyle.Render(fmt.Sprintf("%s: ", i18n.LANGUAGEMAPPING.MinimumTerminalWidth)), constant.MinWidth,
				heightStatus,
				widthStatus,
			),
		)

		centered := style.NewStyle.
			Width(m.Width).
			Height(m.Height).
			Align(lipgloss.Center, lipgloss.Center).
			Render(warningLine)

		return centered
	}

	content := renderModeContent(m)
	bottomBar := renderKeyBindingComponentPanel(m.Width, m)
	mainView := lipgloss.JoinVertical(lipgloss.Left, content, bottomBar)

	if m.ShowPopUp.Load() {
		// Render the popup view into a string.
		popUpComponent := popup.RenderPopUpComponent(m)

		// Calculate the X and Y coordinates to center the popup.
		popUpWidth := lipgloss.Width(popUpComponent)
		popUpHeight := lipgloss.Height(popUpComponent)
		x := (m.Width - popUpWidth) / 2
		y := (m.Height - popUpHeight) / 2

		layers := []*lipgloss.Layer{
			lipgloss.NewLayer(mainView),
			lipgloss.NewLayer(popUpComponent).X(x).Y(y).Z(1),
		}

		compositor := lipgloss.NewCompositor(layers...)

		return compositor.Render()
	}

	return mainView
}

func renderModeContent(m *types.GittiModel) string {
	switch m.ScreenMode {
	case constant.ScreenModeSingleColumn:
		return renderPrimaryComponentStack(m.Width, m)
	case constant.ScreenModeFocused:
		return renderFocusedComponentPanel(m)
	default:
		leftPanel := renderPrimaryComponentStack(m.WindowLeftPanelWidth, m)
		rightPanel := renderRightComponentStack(m.DetailComponentPanelWidth, m)
		return lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, rightPanel)
	}
}

func renderPrimaryComponentStack(width int, m *types.GittiModel) string {
	gitStatusPanel := renderGitStatusComponentPanel(width, 1, m)
	localBranchesOrTagOrRemotePanel := renderLocalBranchesOrTagOrRemoteOrWorktreeComponentPanel(width, m.LocalBranchesComponentPanelHeight, m)
	modifiedFilesPanel := renderModifiedFilesComponentPanel(width, m.ModifiedFilesComponentPanelHeight, m)
	commitLogOrRefLogPanel := renderCommitLogOrRefLogComponentPanel(width, m.CommitLogComponentPanelHeight, m)
	stashFilesPanel := renderStashComponentPanel(width, m.StashComponentPanelHeight, m)
	return lipgloss.JoinVertical(lipgloss.Left, gitStatusPanel, localBranchesOrTagOrRemotePanel, modifiedFilesPanel, commitLogOrRefLogPanel, stashFilesPanel)
}

func renderRightComponentStack(width int, m *types.GittiModel) string {
	detailPanel := renderDetailComponentPanel(width, m.DetailComponentPanelHeight, m)
	logPanel := renderLogComponentPanel(width, m.LogComponentPanelHeight, m)
	return lipgloss.JoinVertical(lipgloss.Left, detailPanel, logPanel)
}

func renderFocusedComponentPanel(m *types.GittiModel) string {
	width := m.Width
	height := m.WindowCoreContentHeight
	switch m.CurrentSelectedComponent {
	case constant.GitStatusComponentPanel:
		// Unlike list and viewport views, Git status has only one content line, so
		// its minimum height must include the border to fill the main area.
		return renderGitStatusComponentPanel(width, height+2, m)
	case constant.LocalBranchOrTagOrRemoteOrWorktreeComponentPanel:
		return renderLocalBranchesOrTagOrRemoteOrWorktreeComponentPanel(width, height, m)
	case constant.ModifiedFilesComponentPanel:
		return renderModifiedFilesComponentPanel(width, height, m)
	case constant.CommitLogOrRefLogComponentPanel:
		return renderCommitLogOrRefLogComponentPanel(width, height, m)
	case constant.StashComponentPanel:
		return renderStashComponentPanel(width, height, m)
	case constant.DetailComponentPanel, constant.DetailComponentPanelTwo:
		return renderFocusedDetailComponentPanel(width, height, m)
	case constant.LogComponentPanel:
		return renderLogComponentPanel(width, height, m)
	default:
		return ""
	}
}
