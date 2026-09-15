package services

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/gohyuhan/gitti/api"
	"github.com/gohyuhan/gitti/logging"
	"github.com/gohyuhan/gitti/tui/constant"
	pushPopUp "github.com/gohyuhan/gitti/tui/popup/push"
	"github.com/gohyuhan/gitti/tui/types"
)

// services was to bridge api and the needs of the terminal interface logic so that it can be compatible and feels smooth and not clunky
// ------------------------------------
//
//	For Git Remote Push
//
// ------------------------------------
func GitRemotePushService(m *types.GittiModel, remoteName string, pushType string) {
	popUp, ok := m.PopUpModel.(*pushPopUp.GitRemotePushPopUpModel)
	checkoutBranch := m.CheckOutBranch
	if ok {
		ctx, cancel := context.WithCancel(context.Background())
		popUp.CancelFunc = cancel
		// Capture this attempt's identity before the push starts so the
		// result event can be matched against the active popup attempt
		attemptID := pushPopUp.BeginGitRemotePushAttempt(popUp)
		popUp.HasError.Store(false)
		popUp.ProcessSuccess.Store(false)
		popUp.IsProcessing.Store(true)
		popUp.IsCancelled.Store(false)
		// a new attempt must never display the command, status, or output
		// retained from the previous one
		pushPopUp.ResetGitRemotePushPopUpDiagnostics(popUp)

		// Capture this attempt's worktree identity before the push starts so
		// the reconciliation ticket binds to the generation that executed the
		// push, not to one selected after it finished
		gitOperations := m.GitOperations

		go func(ctx context.Context) {
			defer cancel()

			result := gitOperations.GitCommit.GitPush(ctx, remoteName, pushType, checkoutBranch)
			if !result.Success() {
				// A failed, unstarted, or cancelled push publishes its final
				// result and never requests the success-only reconciliation
				// ticket
				m.TuiUpdateChannel <- types.GittiTuiUpdateMsg{
					Event: constant.GIT_PUSH_RESULT_EVENT,
					Data: types.GitPushResultEventDataStructure{
						Success: result.Success(),
						Result:  result,
						Attempt: attemptID,
					},
				}
				return
			}

			// A zero-status push enters the visible reconciliation stage; its
			// final success is published only after the post-push refresh
			// ticket completes. The reconciliation performs no fetch and never
			// waits for one
			pushPopUp.MarkGitRemotePushPopUpReconciling(popUp)
			var refresh *api.PostPushRefreshResult
			if api.GITDAEMON != nil {
				ticket := api.GITDAEMON.RequestPostPushRefresh(gitOperations)
				refreshed := ticket.Await()
				refresh = &refreshed
			}
			m.TuiUpdateChannel <- types.GittiTuiUpdateMsg{
				Event: constant.GIT_PUSH_RESULT_EVENT,
				Data: types.GitPushResultEventDataStructure{
					Success: true,
					Result:  result,
					Attempt: attemptID,
					Refresh: refresh,
				},
			}
		}(ctx)
	}
}

// ------------------------------------
//
//	Cancel the current git push operation and clean up pop-up state
//
// ------------------------------------
func GitRemotePushCancelService(m *types.GittiModel) {
	popUp, ok := m.PopUpModel.(*pushPopUp.GitRemotePushPopUpModel)
	if ok {
		popUp.IsCancelled.Store(true) // set cancellation flag first to prevent race condition
		if popUp.CancelFunc != nil {
			popUp.CancelFunc() // Cancel the context, which terminates the command and goroutine
		}
	}
	m.GitOperations.GitCommit.ClearGitRemotePushOutput() // clear the git commit output log
	m.ShowPopUp.Store(false)                             // close the pop up
	m.IsTyping.Store(false)                              // and reset typing mode
	m.PopUpType = constant.NoPopUp
	if ok {
		// a late result must not re-populate a popup the user already closed
		pushPopUp.ResetGitRemotePushPopUpDiagnostics(popUp)
		popUp.IsProcessing.Store(false)
		popUp.HasError.Store(false)
		popUp.ProcessSuccess.Store(false)
	}
}

// ------------------------------------
//
//	Initialize the push pop-up model and start the push operation
//
// ------------------------------------
func InitGitRemotePushPopUpModelAndStartGitRemotePushService(m *types.GittiModel, remoteName string, pushType string) (*types.GittiModel, tea.Cmd) {
	m.GitOperations.GitCommit.ClearGitRemotePushOutput()
	if popUp, ok := m.PopUpModel.(*pushPopUp.GitRemotePushPopUpModel); !ok {
		pushPopUp.InitGitRemotePushPopUpModel(m)
	} else {
		pushPopUp.ResetGitRemotePushPopUpDiagnostics(popUp)
	}
	// then push it after init the git remote push pop up model
	GitRemotePushService(m, remoteName, pushType)
	// Start spinner ticking
	if pushPopup, ok := m.PopUpModel.(*pushPopUp.GitRemotePushPopUpModel); ok {
		return m, pushPopup.Spinner.Tick
	}
	return m, nil
}

// ------------------------------------
//
//	RequestPostPushRefreshForSignedPush requests the same post-push
//	reconciliation a zero-status background push gets, for the successful
//	terminal-interactive signing push route. The completion message carries
//	the operation identity and success information: only a successful signing
//	push requests the ticket. Any reconciliation failure is logged because no
//	push popup remains after the terminal resumes.
//
// ------------------------------------
func RequestPostPushRefreshForSignedPush(m *types.GittiModel, msg types.GitOperationRequiredSigningFinishedMsg) {
	if msg.GitOperationOpsTypeForLogging != logging.GIT_PUSH_WITH_SIGNING_OPS || msg.Err != nil {
		return
	}
	if api.GITDAEMON == nil {
		return
	}
	// The completion message carries the identity of the worktree that ran
	// the signing command, captured when the suspension was built; fall back
	// to the model's current operations when it does not
	gitOperations := m.GitOperations
	if msg.GitOperations != nil {
		gitOperations = msg.GitOperations
	}
	ticket := api.GITDAEMON.RequestPostPushRefresh(gitOperations)
	go func() {
		result := ticket.Await()
		if !result.Refreshed {
			m.GittiLogger.RegisterNewLog(logging.GIT_PUSH_WITH_SIGNING_OPS, "", logging.WARN,
				fmt.Sprintf("[WARN]: post-push state refresh failed after signing push: %s", result.FailureSummary()), false)
		}
	}()
}
