package push

import (
	"strings"
	"sync/atomic"

	"github.com/gohyuhan/gitti/api/git"
	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/tui/constant"
	"github.com/gohyuhan/gitti/tui/style"
	"github.com/gohyuhan/gitti/tui/types"
)

// ------------------------------------
//
//	Clear the viewport and the retained push result so a new attempt (or a
//	closed popup) cannot display command, status, or output from the previous
//	one.
//
// ------------------------------------
func ResetGitRemotePushPopUpDiagnostics(popUp *GitRemotePushPopUpModel) {
	popUp.LastPushResult = git.EmptyGitPushResult()
	popUp.GitRemotePushOutputViewport.SetContent("")
}

// ------------------------------------
//
//	Render the separately retained push progress lines for the live viewport.
//	When both streams carry output, each section is tagged by source so stdout
//	and stderr stay unambiguous while the command is still running.
//
// ------------------------------------
func buildGitRemotePushLiveOutput(stdoutLines, stderrLines []string) string {
	bothStreamsUsed := len(stdoutLines) > 0 && len(stderrLines) > 0
	var output strings.Builder
	if bothStreamsUsed {
		output.WriteString(i18n.LANGUAGEMAPPING.GitPushPopUpStdout)
		output.WriteString(":\n")
	}
	for _, line := range stdoutLines {
		output.WriteString(line)
		output.WriteRune('\n')
	}
	if bothStreamsUsed {
		output.WriteString(i18n.LANGUAGEMAPPING.GitPushPopUpStderr)
		output.WriteString(":\n")
	}
	for _, line := range stderrLines {
		output.WriteString(line)
		output.WriteRune('\n')
	}
	return strings.TrimRight(output.String(), "\n")
}

// ------------------------------------
//
//	Sync the remote push output viewport content and scroll position from the
//	latest git push output lines. Applied only while the popup is still
//	processing and not cancelled, so a queued progress event arriving after
//	the attempt finished (or the popup was closed) cannot replace the
//	completed diagnostics with stale live output.
//
// ------------------------------------
func UpdatePopUpGitRemotePushOutputViewport(m *types.GittiModel) {
	popUp, ok := m.PopUpModel.(*GitRemotePushPopUpModel)
	if !ok || popUp.IsCancelled.Load() || !popUp.IsProcessing.Load() {
		return
	}
	popUp.GitRemotePushOutputViewport.SetWidth(min(constant.MaxGitRemotePushPopUpWidth, int(float64(m.Width)*0.8)) - 4)
	popUp.GitRemotePushOutputViewport.SetYOffset(popUp.GitRemotePushOutputViewport.YOffset())
	stdoutLines, stderrLines := m.GitOperations.GitCommit.GitRemotePushOutput()
	logs := buildGitRemotePushLiveOutput(stdoutLines, stderrLines)
	if logs != "" {
		logs = style.NewStyle.Render(logs)
	}
	popUp.GitRemotePushOutputViewport.SetContent(logs)
	popUp.GitRemotePushOutputViewport.PageDown()
}

// ------------------------------------
//
//	Attempt ids are allocated from this process-wide counter rather than a
//	counter owned by one popup, so a popup reconstructed while a cancelled
//	attempt's result is still in flight can never be handed an id the
//	earlier attempt already used.
//
// ------------------------------------
var pushAttemptIDCounter atomic.Int64

// ------------------------------------
//
//	Begin a new push attempt: allocate the next process-wide attempt id,
//	record it on the popup, and return it so the launching service can
//	attach it to the result event. A result event carrying any other id is
//	rejected by UpdateGitPushResultEvent, including ids allocated to
//	attempts that ran on a different popup instance.
//
// ------------------------------------
func BeginGitRemotePushAttempt(popUp *GitRemotePushPopUpModel) int64 {
	id := pushAttemptIDCounter.Add(1)
	popUp.ActivePushAttemptID.Store(id)
	return id
}

// ------------------------------------
//
//	Handle the async git push result event. Clears the IsProcessing flag and
//	sets ProcessSuccess on success, or sets HasError on failure. Stores the
//	definitive result and renders its deterministic diagnostics. No-ops if
//	the popup is not the active push output popup, the operation was
//	cancelled, or the event belongs to a different attempt, so a late result
//	from a cancelled attempt never overwrites a newer push or reopens a
//	closed popup.
//
// ------------------------------------
func UpdateGitPushResultEvent(m *types.GittiModel, data types.GitPushResultEventDataStructure) {
	popUp, ok := m.PopUpModel.(*GitRemotePushPopUpModel)
	if !ok || popUp.IsCancelled.Load() || popUp.ActivePushAttemptID.Load() != data.Attempt {
		return
	}

	popUp.IsProcessing.Store(false)
	popUp.LastPushResult = data.Result
	if data.Success {
		popUp.HasError.Store(false)
		popUp.ProcessSuccess.Store(true)
	} else {
		popUp.HasError.Store(true)
		popUp.ProcessSuccess.Store(false)
	}
	popUp.GitRemotePushOutputViewport.SetContent(buildGitRemotePushDiagnostics(data.Result))
	popUp.GitRemotePushOutputViewport.PageDown()
}
