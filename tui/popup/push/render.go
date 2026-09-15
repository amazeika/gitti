package push

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gohyuhan/gitti/api"
	"github.com/gohyuhan/gitti/api/git"
	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/tui/constant"
	"github.com/gohyuhan/gitti/tui/style"
	"github.com/gohyuhan/gitti/tui/types"

	"charm.land/lipgloss/v2"
)

// ------------------------------------
//
//	Render the deterministic final diagnostics for a finished push attempt.
//	The sections are: the working directory, one quoted argv element per
//	line, an explicit exit status (integer, not started, or cancelled), the
//	separately retained stdout and stderr sections, and the post-push
//	reconciliation outcome for a successful push.
//
//	Failed attempts additionally carry an actionable explanation. Empty
//	streams show an explicit marker rather than blank ambiguity.
//
// ------------------------------------
func buildGitRemotePushDiagnostics(result git.GitPushResult, refresh *api.PostPushRefreshResult) string {
	var diagnostics strings.Builder

	diagnostics.WriteString(i18n.LANGUAGEMAPPING.GitPushPopUpWorkingDirectory)
	diagnostics.WriteString(": ")
	diagnostics.WriteString(result.WorkingDirectory())
	diagnostics.WriteRune('\n')

	// One quoted argument per line keeps spaces, empty arguments, and option
	// boundaries unambiguous without suggesting that a shell was invoked.
	for index, argument := range result.Argv() {
		fmt.Fprintf(&diagnostics, "%s[%d]: %q\n", i18n.LANGUAGEMAPPING.GitPushPopUpArgv, index, argument)
	}

	diagnostics.WriteString(i18n.LANGUAGEMAPPING.GitPushPopUpExitStatus)
	diagnostics.WriteString(": ")
	switch {
	// Cancellation is reported before the not-started check so a push
	// cancelled before it started shows its own status
	case result.Cancelled():
		diagnostics.WriteString(i18n.LANGUAGEMAPPING.GitPushPopUpExitStatusCancelled)
	case !result.Started():
		diagnostics.WriteString(i18n.LANGUAGEMAPPING.GitPushPopUpExitStatusNotStarted)
	default:
		diagnostics.WriteString(strconv.Itoa(result.ExitCode()))
	}
	diagnostics.WriteRune('\n')

	// Nonzero and setup failures show actionable text in addition to the red
	// border; a clean finish shows nothing extra.
	switch {
	case !result.Started() && result.Err() != nil:
		fmt.Fprintf(&diagnostics, i18n.LANGUAGEMAPPING.GitPushPopUpCouldNotStart+"\n", result.Err().Error())
	case result.Started() && !result.Cancelled() && result.ExitCode() != 0:
		fmt.Fprintf(&diagnostics, i18n.LANGUAGEMAPPING.GitPushPopUpNonZeroExit+"\n", result.ExitCode())
	case result.Started() && !result.Cancelled() && result.Err() != nil:
		fmt.Fprintf(&diagnostics, i18n.LANGUAGEMAPPING.GitPushPopUpReadFailure+"\n", result.Err().Error())
	}

	diagnostics.WriteRune('\n')
	writeGitPushOutputSection(&diagnostics, i18n.LANGUAGEMAPPING.GitPushPopUpStdout, result.Stdout())
	diagnostics.WriteRune('\n')
	writeGitPushOutputSection(&diagnostics, i18n.LANGUAGEMAPPING.GitPushPopUpStderr, result.Stderr())

	// The post-push reconciliation outcome appears only when a ticket was
	// requested, which happens only for a zero-status push. A refresh failure
	// keeps the push-only success statement and adds a visible warning naming
	// the failed domains; it never claims the state was reconciled and never
	// relabels the successful Git push as failed.
	if refresh != nil {
		diagnostics.WriteRune('\n')
		if refresh.Refreshed {
			diagnostics.WriteString(i18n.LANGUAGEMAPPING.GitPushPopUpRefreshSucceeded)
			diagnostics.WriteRune('\n')
		} else {
			diagnostics.WriteString(i18n.LANGUAGEMAPPING.GitPushPopUpPushSucceeded)
			diagnostics.WriteRune('\n')
			diagnostics.WriteString(fmt.Sprintf(i18n.LANGUAGEMAPPING.GitPushPopUpRefreshFailed, refresh.FailureSummary()))
			diagnostics.WriteRune('\n')
		}
	}

	return diagnostics.String()
}

// ------------------------------------
//
//	Append one retained stream as a labelled section, replacing empty content
//	with an explicit marker so an empty stream is never mistaken for a missing
//	section.
//
// ------------------------------------
func writeGitPushOutputSection(diagnostics *strings.Builder, label string, rawStream []byte) {
	diagnostics.WriteString(label)
	diagnostics.WriteString(":\n")

	content := string(rawStream)
	if strings.TrimSpace(content) == "" {
		diagnostics.WriteString(i18n.LANGUAGEMAPPING.GitPushPopUpStreamEmpty)
		diagnostics.WriteRune('\n')
		return
	}

	diagnostics.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		diagnostics.WriteRune('\n')
	}
}

// ------------------------------------
//
//	Render the push type selection popup. Shows a titled bordered list where the
//	user chooses between a normal push, safe force-push (--force-with-lease), or
//	dangerous force-push (--force).
//
// ------------------------------------
func RenderChoosePushTypePopUp(m *types.GittiModel) string {
	popUp, ok := m.PopUpModel.(*ChoosePushTypePopUpModel)
	if ok {
		popUpWidth := min(constant.MaxChoosePushTypePopUpWidth, int(float64(m.Width)*0.8))
		title := style.TitleStyle.Width(popUpWidth).Render(i18n.LANGUAGEMAPPING.GitRemotePushOptionTitle)
		popUp.PushOptionList.SetWidth(popUpWidth - 4)
		content := lipgloss.JoinVertical(
			lipgloss.Left,
			title,
			popUp.PushOptionList.View(),
		)
		return style.PopUpBorderStyle.Width(popUpWidth).Render(content)
	}
	return ""
}

// ------------------------------------
//
//	Render the git push progress popup. Shows a scrollable output viewport with
//	a border that turns red on error or green on success. Displays a spinner
//	above the viewport while IsProcessing is true.
//
// ------------------------------------
func RenderGitRemotePushPopUp(m *types.GittiModel) string {
	popUp, ok := m.PopUpModel.(*GitRemotePushPopUpModel)
	if ok {
		popUpWidth := min(constant.MaxGitRemotePushPopUpWidth, int(float64(m.Width)*0.8))
		title := style.TitleStyle.Render(i18n.LANGUAGEMAPPING.GitRemotePushPopUpTitle)
		logViewPortStyle := style.PanelBorderStyle.
			Width(popUpWidth - 2).
			Height(constant.PopUpGitCommitOutputViewPortHeight + 2)
		if popUp.HasError.Load() {
			logViewPortStyle = style.PanelBorderStyle.
				BorderForeground(style.ColorError)
		} else if popUp.ProcessSuccess.Load() {
			logViewPortStyle = style.PanelBorderStyle.
				BorderForeground(style.ColorGreenSoft)
		}
		popUp.GitRemotePushOutputViewport.SetWidth(popUpWidth - 4)
		popUp.GitRemotePushOutputViewport.SetYOffset(popUp.GitRemotePushOutputViewport.YOffset())
		logViewPort := logViewPortStyle.Render(popUp.GitRemotePushOutputViewport.View())

		var content string
		// Show spinner above viewport when processing; while the zero-status
		// push waits for its post-push refresh ticket the popup shows the
		// visible reconciliation stage instead of the plain processing text
		if popUp.IsProcessing.Load() {
			stageText := i18n.LANGUAGEMAPPING.GitRemotePushPopUpProcessing
			if popUp.IsReconciling.Load() {
				stageText = i18n.LANGUAGEMAPPING.GitPushPopUpReconciling
			}
			processingText := style.SpinnerStyle.Render(popUp.Spinner.View() + " " + stageText)
			content = lipgloss.JoinVertical(
				lipgloss.Left,
				title,
				"",
				processingText,
				logViewPort,
			)
		} else {
			content = lipgloss.JoinVertical(
				lipgloss.Left,
				title,
				logViewPort,
			)
		}
		return style.PopUpBorderStyle.Width(popUpWidth).Render(content)
	}
	return ""
}
