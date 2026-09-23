package nontyping

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/gohyuhan/gitti/api/git"
	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/logging"
	"github.com/gohyuhan/gitti/tui/constant"
	pushPopUp "github.com/gohyuhan/gitti/tui/popup/push"
	remotePopUp "github.com/gohyuhan/gitti/tui/popup/remote"
	"github.com/gohyuhan/gitti/tui/types"
)

// ------------------------------------
//
//	Handle 'p' key interaction.
//	Responsibility: Initiates the "push" or "publish" operation, driven by the
//	latest published remote/upstream observation state.
//
//	An unpublished branch enters the publish flow: the observation must name
//	the checked-out branch (a mismatch requests a new observation instead of
//	publishing), the remote inventory is re-read, and the user confirms
//	publishing to one push-capable remote (a chooser is offered when several
//	exist). A tracked branch keeps the existing push-type flow unchanged.
//	Any unsettled, inapplicable, or unreadable state logs a localized
//	actionable reason and shows no remote chooser.
//
// ------------------------------------
func handleNonTypingpKeyBindingInteraction(m *types.GittiModel) (*types.GittiModel, tea.Cmd) {
	if m.ShowPopUp.Load() {
		return m, nil
	}

	// The push/publish decision is driven by the remote/upstream local-read
	// state the daemon last published; until it has settled no chooser may be
	// shown.
	snapshot := m.GitOperations.GitRemote.RemoteSyncStatusAndUpstream()
	switch snapshot.ObservationState {
	case git.UpstreamStatePending, git.UpstreamStateNotApplicable, git.UpstreamStateUnavailable:
		m.GittiLogger.RegisterNewLog(logging.GIT_PUSH_OPS, "", logging.WARN,
			fmt.Sprintf("[GIT_PUSH_OPS WARN]: %s", publishBlockedReason(snapshot.ObservationState)), false)
		return m, nil
	}

	// The publish/push decision must reflect the remotes configured right
	// now, so the inventory is re-read. A failed read keeps the last good
	// inventory and is reported instead of being treated as "no remotes";
	// the chooser is not opened on a failed read.
	if inventoryErr := m.GitOperations.GitRemote.CheckRemoteExist(false); inventoryErr != nil {
		m.GittiLogger.RegisterNewLog(logging.GIT_PUSH_OPS, "", logging.WARN,
			fmt.Sprintf("[GIT_PUSH_OPS WARN]: %s", i18n.LANGUAGEMAPPING.PushInventoryReadFailedWarning), false)
		return m, nil
	}

	switch snapshot.ObservationState {
	case git.UpstreamStateUnpublished:
		// The observation must name the branch the user is trying to
		// publish; a checked-out branch that changed since the observation
		// must never be published, so a new observation is requested
		// instead.
		if snapshot.ObservedBranch != m.CheckOutBranch {
			m.GittiLogger.RegisterNewLog(logging.GIT_PUSH_OPS, "", logging.WARN,
				fmt.Sprintf("[GIT_PUSH_OPS WARN]: %s", fmt.Sprintf(i18n.LANGUAGEMAPPING.PublishBranchMismatchWarning, snapshot.ObservedBranch, m.CheckOutBranch)), false)
			m.DaemonUpdateChannel <- git.GIT_REMOTE_SYNC_STATUS_REQUEST
			return m, nil
		}

		pushCapableRemotes := m.GitOperations.GitRemote.PushCapableRemoteInfos()
		m.ShowPopUp.Store(true)
		m.IsTyping.Store(false)
		switch {
		case len(pushCapableRemotes) == 0:
			if m.GitOperations.GitRemote.RemoteInventory().Len() > 0 {
				// configured remotes exist but none is push-capable: report
				// that instead of prompting for a remote the user already has
				m.GittiLogger.RegisterNewLog(logging.GIT_PUSH_OPS, "", logging.WARN,
					fmt.Sprintf("[GIT_PUSH_OPS WARN]: %s", i18n.LANGUAGEMAPPING.PublishNoPushCapableRemoteWarning), false)
				return m, nil
			}
			// no push-capable remote configured yet: prompt to add one
			showAddRemotePromptPopUp(m)
		case len(pushCapableRemotes) == 1:
			// exactly one push-capable remote: confirm publishing to it, from
			// the branch the observation named and the generation it ran in
			m.PopUpType = constant.PublishBranchConfirmationPopUp
			pushPopUp.InitPublishBranchConfirmationPopUpModel(m, pushCapableRemotes[0].Name, snapshot.ObservedBranch, m.GitOperations)
		default:
			// several push-capable remotes: choose the target first, carrying
			// the observed branch and generation into the confirmation popup
			m.PopUpType = constant.ChooseRemotePopUp
			if _, ok := m.PopUpModel.(*remotePopUp.ChooseRemotePopUpModel); !ok {
				remotePopUp.InitChooseRemotePopUpModelForPublish(m, pushCapableRemotes, snapshot.ObservedBranch, m.GitOperations)
			}
		}
	case git.UpstreamStateTracked:
		// a tracked branch keeps the existing push-type flow unchanged
		m.ShowPopUp.Store(true)
		m.IsTyping.Store(false)
		remotes := m.GitOperations.GitRemote.PushRemote()
		if len(remotes) == 1 {
			m.PopUpType = constant.ChoosePushTypePopUp
			// if the current pop up model is not commit pop up model, then init it and start git push service
			pushPopUp.InitChoosePushTypePopUpModel(m, remotes[0].Name)
		} else if len(remotes) > 1 {
			// if remote is more than 1 let user choose which remote to push to first before pushing
			m.PopUpType = constant.ChooseRemotePopUp
			if _, ok := m.PopUpModel.(*remotePopUp.ChooseRemotePopUpModel); !ok {
				remotePopUp.InitChooseRemotePopUpModel(m, remotes, constant.PUSHACTION)
			}
		}
	}
	return m, nil
}

// ------------------------------------
//
//	publishBlockedReason returns the localized actionable reason a 'p'
//	request is refused for one unsettled, inapplicable, or unreadable
//	observation state.
//
// ------------------------------------
func publishBlockedReason(state git.UpstreamObservationState) string {
	switch state {
	case git.UpstreamStatePending:
		return i18n.LANGUAGEMAPPING.PublishBlockedStatePending
	case git.UpstreamStateNotApplicable:
		return i18n.LANGUAGEMAPPING.PublishBlockedStateNotApplicable
	default:
		return i18n.LANGUAGEMAPPING.PublishBlockedStateUnavailable
	}
}
