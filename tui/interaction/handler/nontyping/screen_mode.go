package nontyping

import (
	"github.com/gohyuhan/gitti/tui/constant"
	"github.com/gohyuhan/gitti/tui/layout"
	"github.com/gohyuhan/gitti/tui/types"
)

const (
	screenModeCycleBackward = -1
	screenModeCycleForward  = 1
)

func cycleCompatibleScreenMode(m *types.GittiModel, direction int) {
	modeCount := int(constant.ScreenModeFocused) + 1
	currentMode := normalizeScreenModeIndex(int(m.ScreenMode), modeCount)

	if isPrimaryScreenModeComponent(m.CurrentSelectedComponent) {
		m.ScreenMode = constant.ScreenMode(normalizeScreenModeIndex(currentMode+direction, modeCount))
	} else if m.ScreenMode == constant.ScreenModeSingleColumn {
		m.ScreenMode = constant.ScreenModeFocused
	} else {
		for step := 1; step < modeCount; step++ {
			candidate := constant.ScreenMode(normalizeScreenModeIndex(currentMode+direction*step, modeCount))
			if candidate != constant.ScreenModeSingleColumn {
				m.ScreenMode = candidate
				break
			}
		}
	}

	layout.TuiWindowSizing(m)
}

func normalizeScreenModeIndex(value int, modeCount int) int {
	return ((value % modeCount) + modeCount) % modeCount
}

func isPrimaryScreenModeComponent(component string) bool {
	for _, primaryComponent := range constant.ComponentPanelNavigationList {
		if component == primaryComponent {
			return true
		}
	}
	return false
}
