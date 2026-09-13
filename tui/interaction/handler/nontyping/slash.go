package nontyping

import (
	tea "charm.land/bubbletea/v2"
	"github.com/gohyuhan/gitti/tui/constant"
	"github.com/gohyuhan/gitti/tui/layout"
	"github.com/gohyuhan/gitti/tui/services"
	"github.com/gohyuhan/gitti/tui/types"
)

// ------------------------------------
//
//	Handle '/' key interaction.
//	Responsibility: Focuses the Log Component Panel.
//	Used as a quick jumping shortcut to view the internal console/error logs of Gitti.
//
// ------------------------------------
func handleNonTypingSlashKeyBindingInteraction(m *types.GittiModel) (*types.GittiModel, tea.Cmd) {
	if m.ShowPopUp.Load() {
		return m, nil
	}

	componentChanged := m.CurrentSelectedComponent != constant.LogComponentPanel
	modeChanged := m.ScreenMode == constant.ScreenModeSingleColumn
	if componentChanged {
		m.CurrentSelectedComponent = constant.LogComponentPanel
		m.DetailPanelParentComponent = ""
	}
	if modeChanged {
		m.ScreenMode = constant.ScreenModeFocused
	}
	if componentChanged || modeChanged {
		layout.TuiWindowSizing(m)
	}
	if componentChanged {
		services.FetchDetailComponentPanelInfoService(m, true)
	}
	return m, nil
}
