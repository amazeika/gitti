package nontyping

import (
	tea "charm.land/bubbletea/v2"
	"github.com/gohyuhan/gitti/tui/types"
)

// ------------------------------------
//
//	Handle '=' by advancing to the next screen mode compatible with the
//	currently selected component. Popups keep precedence over mode changes.
//
// ------------------------------------
func handleNonTypingEqualKeyBindingInteraction(m *types.GittiModel) (*types.GittiModel, tea.Cmd) {
	if m.ShowPopUp.Load() {
		return m, nil
	}
	cycleCompatibleScreenMode(m, screenModeCycleForward)
	return m, nil
}
