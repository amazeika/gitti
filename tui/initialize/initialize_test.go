package initialize

import (
	"testing"

	"github.com/gohyuhan/gitti/executor"
	"github.com/gohyuhan/gitti/logging"
	"github.com/gohyuhan/gitti/settings"
	"github.com/gohyuhan/gitti/tui/constant"
)

func TestScreenModeEnumOrderDefinesStartupCycle(t *testing.T) {
	var mode constant.ScreenMode = constant.ScreenModeTwoColumn

	if mode != 0 {
		t.Fatalf("ScreenModeTwoColumn = %d, want zero-value startup mode", mode)
	}
	if constant.ScreenModeSingleColumn != mode+1 {
		t.Errorf("ScreenModeSingleColumn = %d, want %d", constant.ScreenModeSingleColumn, mode+1)
	}
	if constant.ScreenModeFocused != mode+2 {
		t.Errorf("ScreenModeFocused = %d, want %d", constant.ScreenModeFocused, mode+2)
	}
}

func TestInitGittiModelStartsInTwoColumnMode(t *testing.T) {
	setScreenModeTestSettings(t)
	model := InitGittiModel(nil, "/repo", "repo", nil, logging.InitGittiLogging(10, make(chan string, 1), 3), nil, nil)

	if model.ScreenMode != constant.ScreenModeTwoColumn {
		t.Errorf("ScreenMode = %d, want ScreenModeTwoColumn", model.ScreenMode)
	}
}

func TestReinitGittiModelPreservesRuntimeScreenState(t *testing.T) {
	setScreenModeTestSettings(t)
	model := InitGittiModel(nil, "/old", "old", nil, logging.InitGittiLogging(10, make(chan string, 1), 3), nil, nil)
	model.Width = 132
	model.Height = 43
	model.ScreenMode = constant.ScreenModeFocused
	model.CurrentSelectedComponent = constant.DetailComponentPanelTwo

	ReinitGittiModel(model, "/new", "new", nil)

	if model.Width != 132 || model.Height != 43 {
		t.Errorf("terminal dimensions = %dx%d, want 132x43", model.Width, model.Height)
	}
	if model.ScreenMode != constant.ScreenModeFocused {
		t.Errorf("ScreenMode = %d, want preserved ScreenModeFocused", model.ScreenMode)
	}
	if model.CurrentSelectedComponent != constant.ModifiedFilesComponentPanel {
		t.Errorf("CurrentSelectedComponent = %q, want reset Modified Files component", model.CurrentSelectedComponent)
	}
}

// ------------------------------------
//
//	Install deterministic process settings for model initialization tests.
//
// ------------------------------------
func setScreenModeTestSettings(t *testing.T) {
	originalSettings := settings.GITTICONFIGSETTINGS
	originalExecutor := executor.GittiCmdExecutor
	settings.GITTICONFIGSETTINGS = &settings.GittiConfigSettings{LeftPanelWidthRatio: 0.3}
	executor.InitCmdExecutor(t.TempDir())
	t.Cleanup(func() {
		settings.GITTICONFIGSETTINGS = originalSettings
		executor.GittiCmdExecutor = originalExecutor
	})
}
