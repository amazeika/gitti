package nontyping

import (
	tea "charm.land/bubbletea/v2"
	"github.com/gohyuhan/gitti/tui/types"
)

// ------------------------------------
//
//	Handle '_' by moving to the previous screen mode compatible with the
//	currently selected component. Popups keep precedence over mode changes.
//
// ------------------------------------
func handleNonTypingUnderscoreKeyBindingInteraction(m *types.GittiModel) (*types.GittiModel, tea.Cmd) {
	if m.ShowPopUp.Load() {
		return m, nil
	}
	cycleCompatibleScreenMode(m, screenModeCycleBackward)
	return m, nil
}
