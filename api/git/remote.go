package git

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"

	"github.com/gohyuhan/gitti/executor"
	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/logging"
)

// remoteSyncSnapshot is one immutable generation of the remote/upstream
// local-read state: the ahead/behind counts, the upstream identity, and its
// icon. GetLatestRemoteSyncStatusAndUpstream assembles the fresh state in
// local variables and publishes it with one store, so a reader sees either
// the whole prior generation or the whole new one.
type remoteSyncSnapshot struct {
	remoteSyncStatus              RemoteSyncStatus
	upStreamRemoteIcon            string
	currentBranchUpStream         string
	currentBranchUpStreamWithIcon string
}

// RemoteSyncUpstreamSnapshot is one published generation of the
// remote/upstream state, returned by the combined getter so a reader never
// mixes the upstream identity, icon, and counts from two passes.
type RemoteSyncUpstreamSnapshot struct {
	RemoteSyncStatus      RemoteSyncStatus
	UpStreamRemoteIcon    string
	CurrentBranchUpStream string
}

type GitRemote struct {
	updateChannel  chan string
	gitProcessLock *GitProcessLock
	remote         []GitRemoteInfo // all remote info
	fetchRemote    []GitRemoteInfo // if the url is for fetch
	pushRemote     []GitRemoteInfo // if the url is for push
	remoteSync     atomic.Pointer[remoteSyncSnapshot]
	logging        *logging.GittiLogging
}

type GitRemoteInfo struct {
	Name  string
	Url   string
	Fetch bool
	Push  bool
}

type RemoteSyncStatus struct {
	Local  string
	Remote string
}

// ------------------------------------
//
//	Initialize the git remote handler with shared dependencies
//
// ------------------------------------
func InitGitRemote(updateChannel chan string, gitProcessLock *GitProcessLock, logging *logging.GittiLogging) *GitRemote {
	gitRemote := GitRemote{
		updateChannel:  updateChannel,
		gitProcessLock: gitProcessLock,
		remote:         []GitRemoteInfo{},
		logging:        logging,
	}
	gitRemote.remoteSync.Store(&remoteSyncSnapshot{})

	return &gitRemote
}

// ------------------------------------
//
//	Return remote
//
// ------------------------------------
func (gr *GitRemote) Remote() []GitRemoteInfo {
	return gr.remote
}

// ------------------------------------
//
//	Return fetch related remote only
//
// ------------------------------------
func (gr *GitRemote) FetchRemote() []GitRemoteInfo {
	return gr.fetchRemote
}

// ------------------------------------
//
//	Return push related remote only
//
// ------------------------------------
func (gr *GitRemote) PushRemote() []GitRemoteInfo {
	return gr.pushRemote
}

// ------------------------------------
//
//	Return remote sync status
//
// ------------------------------------
func (gr *GitRemote) RemoteSyncStatus() RemoteSyncStatus {
	return gr.remoteSync.Load().remoteSyncStatus
}

// ------------------------------------
//
//	Return current upstream icon
//
// ------------------------------------
func (gr *GitRemote) UpStreamRemoteIcon() string {
	return gr.remoteSync.Load().upStreamRemoteIcon
}

// ------------------------------------
//
//	Return current branch upstream
//
// ------------------------------------
func (gr *GitRemote) CurrentBranchUpStream() string {
	return gr.remoteSync.Load().currentBranchUpStream
}

// ------------------------------------
//
//	Return the remote sync status, upstream icon, and upstream name from one
//	published generation
//
// ------------------------------------
func (gr *GitRemote) RemoteSyncStatusAndUpstream() RemoteSyncUpstreamSnapshot {
	snapshot := gr.remoteSync.Load()
	return RemoteSyncUpstreamSnapshot{
		RemoteSyncStatus:      snapshot.remoteSyncStatus,
		UpStreamRemoteIcon:    snapshot.upStreamRemoteIcon,
		CurrentBranchUpStream: snapshot.currentBranchUpStream,
	}
}

// ------------------------------------
//
//	Related to add Remote
//
// ------------------------------------
func (gr *GitRemote) GitAddRemote(ctx context.Context, originName string, url string) ([]string, int) {
	if !gr.gitProcessLock.CanProceedWithGitOps() {
		return []string{gr.gitProcessLock.OtherProcessRunningWarning()}, -1
	}
	defer func() {
		gr.gitProcessLock.ReleaseGitOpsLock()
	}()

	if !isValidGitRemoteURL(url) {
		errMsg := "Invalid remote URL format"
		if i18n.LANGUAGEMAPPING != nil {
			errMsg = i18n.LANGUAGEMAPPING.AddRemotePopUpInvalidRemoteUrlFormat
		}
		return []string{errMsg}, -1
	}

	gitArgs := []string{"remote", "add", originName, url}
	cmd := executor.GittiCmdExecutor.RunGitCmdWithContext(ctx, gitArgs, false)

	// CombinedOutput starts and waits for the command
	gitOutput, err := cmd.CombinedOutput()
	gr.logging.RegisterNewLog(logging.ADD_REMOTE_OPS, strings.Join(gitArgs, " "), logging.INFO, "", true)

	gitAddRemoteOutput := processGeneralGitOpsOutputIntoStringArray(gitOutput)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			status := exitErr.ExitCode()
			gr.logging.RegisterNewLog(logging.ADD_REMOTE_OPS, strings.Join(gitArgs, " "), logging.ERROR, fmt.Sprintf("[%s ERROR]: %s", logging.ADD_REMOTE_OPS, err.Error()), true)
			return gitAddRemoteOutput, status
		}
		gr.logging.RegisterNewLog(logging.ADD_REMOTE_OPS, strings.Join(gitArgs, " "), logging.ERROR, fmt.Sprintf("[%s ERROR]: %s", logging.ADD_REMOTE_OPS, err.Error()), true)
		return gitAddRemoteOutput, -1

	}
	return gitAddRemoteOutput, 0
}

// ------------------------------------
//
//	Related to delete Remote
//
// ------------------------------------
func (gr *GitRemote) GitRemoveRemote(remoteName string) {
	if !gr.gitProcessLock.CanProceedWithGitOps() {
		gr.logging.RegisterNewLog(logging.REMOVE_REMOTE_OPS, "", logging.WARN, fmt.Sprintf("[WARN]: %s", gr.gitProcessLock.OtherProcessRunningWarning()), false)
		return
	}
	defer func() {
		gr.gitProcessLock.ReleaseGitOpsLock()
	}()

	gitArgs := []string{"remote", "remove", remoteName}
	cmd := executor.GittiCmdExecutor.RunGitCmd(gitArgs, false)
	gr.logging.RegisterNewLog(logging.REMOVE_REMOTE_OPS, strings.Join(gitArgs, " "), logging.INFO, "", true)

	err := cmd.Run()
	if err != nil {
		gr.logging.RegisterNewLog(logging.REMOVE_REMOTE_OPS, strings.Join(gitArgs, " "), logging.ERROR, fmt.Sprintf("[%s ERROR]: %s", logging.REMOVE_REMOTE_OPS, err.Error()), true)
	}
}

// ------------------------------------
//
//		Related to set remote as tracking upStream for current branch
//	   * currently we always assume the local branch and remote branch will be the same identical name
//	     so it will be something like git branch --set-upstream-to=origin/<main> <main>
//
// ------------------------------------
func (gr *GitRemote) GitSetRemoteAsTrackingUpstream(remoteName string, branchName string) {
	if !gr.gitProcessLock.CanProceedWithGitOps() {
		gr.logging.RegisterNewLog(logging.SET_REMOTE_AS_TRACKING_UPSTREAM_OPS, "", logging.WARN, fmt.Sprintf("[WARN]: %s", gr.gitProcessLock.OtherProcessRunningWarning()), false)
		return
	}
	defer func() {
		gr.gitProcessLock.ReleaseGitOpsLock()
	}()

	upstream := fmt.Sprintf("--set-upstream-to=%s/%s", remoteName, branchName)
	gitArgs := []string{"branch", upstream, branchName}
	cmd := executor.GittiCmdExecutor.RunGitCmd(gitArgs, false)
	gr.logging.RegisterNewLog(logging.SET_REMOTE_AS_TRACKING_UPSTREAM_OPS, strings.Join(gitArgs, " "), logging.INFO, "", true)

	err := cmd.Run()
	if err != nil {
		gr.logging.RegisterNewLog(logging.SET_REMOTE_AS_TRACKING_UPSTREAM_OPS, strings.Join(gitArgs, " "), logging.ERROR, fmt.Sprintf("[%s ERROR]: %s", logging.SET_REMOTE_AS_TRACKING_UPSTREAM_OPS, err.Error()), true)
	}
}

// ------------------------------------
//
//	Related to change remote name
//
// ------------------------------------
func (gr *GitRemote) GitChangeRemoteName(oldRemoteName string, newRemoteName string) {
	if !gr.gitProcessLock.CanProceedWithGitOps() {
		gr.logging.RegisterNewLog(logging.CHANGE_REMOTE_NAME_OPS, "", logging.WARN, fmt.Sprintf("[WARN]: %s", gr.gitProcessLock.OtherProcessRunningWarning()), false)
		return
	}
	defer func() {
		gr.gitProcessLock.ReleaseGitOpsLock()
	}()

	gitArgs := []string{"remote", "rename", oldRemoteName, newRemoteName}
	cmd := executor.GittiCmdExecutor.RunGitCmd(gitArgs, false)
	gr.logging.RegisterNewLog(logging.CHANGE_REMOTE_NAME_OPS, strings.Join(gitArgs, " "), logging.INFO, "", true)

	err := cmd.Run()
	if err != nil {
		gr.logging.RegisterNewLog(logging.CHANGE_REMOTE_NAME_OPS, strings.Join(gitArgs, " "), logging.ERROR, fmt.Sprintf("[%s ERROR]: %s", logging.CHANGE_REMOTE_NAME_OPS, err.Error()), true)
	}
}

// ------------------------------------
//
//	Related to change remote url
//
// ------------------------------------
func (gr *GitRemote) GitChangeRemoteUrl(remoteName string, newRemoteUrl string) {
	if !gr.gitProcessLock.CanProceedWithGitOps() {
		gr.logging.RegisterNewLog(logging.CHANGE_REMOTE_URL_OPS, "", logging.WARN, fmt.Sprintf("[WARN]: %s", gr.gitProcessLock.OtherProcessRunningWarning()), false)
		return
	}
	defer func() {
		gr.gitProcessLock.ReleaseGitOpsLock()
	}()

	if !isValidGitRemoteURL(newRemoteUrl) {
		errMsg := "Invalid remote URL format"
		if i18n.LANGUAGEMAPPING != nil {
			errMsg = i18n.LANGUAGEMAPPING.AddRemotePopUpInvalidRemoteUrlFormat
		}
		gr.logging.RegisterNewLog(logging.CHANGE_REMOTE_URL_OPS, "", logging.ERROR, fmt.Sprintf("[%s ERROR]: %s", logging.CHANGE_REMOTE_URL_OPS, errMsg), false)
		return
	}

	gitArgs := []string{"remote", "set-url", remoteName, newRemoteUrl}
	cmd := executor.GittiCmdExecutor.RunGitCmd(gitArgs, false)
	gr.logging.RegisterNewLog(logging.CHANGE_REMOTE_URL_OPS, strings.Join(gitArgs, " "), logging.INFO, "", true)

	err := cmd.Run()
	if err != nil {
		gr.logging.RegisterNewLog(logging.CHANGE_REMOTE_URL_OPS, strings.Join(gitArgs, " "), logging.ERROR, fmt.Sprintf("[%s ERROR]: %s", logging.CHANGE_REMOTE_URL_OPS, err.Error()), true)
	}
}

// ------------------------------------
//
//	CheckRemoteExist checks for existing remotes by running 'git remote -v'.
//	It parses the output to identify unique remote name-URL combinations and
//	determines if they are intended for fetching, pushing, or both.
//	It populates the gr.remote, gr.fetchRemote, and gr.pushRemote slices accordingly.
//
// ------------------------------------
func (gr *GitRemote) CheckRemoteExist(passiveRunning bool) bool {
	gitArgs := []string{"remote", "-v"}
	cmd := executor.GittiCmdExecutor.RunGitCmd(gitArgs, false)
	gitOutput, err := cmd.Output()
	if !passiveRunning {
		gr.logging.RegisterNewLog(logging.CHECK_REMOTE_OPS, strings.Join(gitArgs, " "), logging.INFO, "", true)
	}
	if err != nil {
		if !passiveRunning {
			gr.logging.RegisterNewLog(logging.CHECK_REMOTE_OPS, strings.Join(gitArgs, " "), logging.ERROR, fmt.Sprintf("[%s ERROR]: %s", logging.CHECK_REMOTE_OPS, err.Error()), true)
		}
		return false
	}

	remotes := strings.SplitSeq(strings.TrimSpace(string(gitOutput)), "\n")
	var remoteStruct []GitRemoteInfo
	var fetchRemoteStruct []GitRemoteInfo
	var pushRemoteStruct []GitRemoteInfo

	uniqueRemoteMap := make(map[string]GitRemoteInfo)

	for remote := range remotes {
		remoteLinePart := strings.Fields(remote)
		if len(remoteLinePart) < 3 {
			continue
		}

		// check if the remote unique combination (remote name + url) already exist in the map
		// if not create one
		key := fmt.Sprintf("%s-%s", remoteLinePart[0], remoteLinePart[1])
		r, ok := uniqueRemoteMap[key]
		if !ok {
			r = GitRemoteInfo{
				Name:  remoteLinePart[0],
				Url:   remoteLinePart[1],
				Fetch: false,
				Push:  false,
			}
		}

		// check if the remote is fetch or push and update the info
		typePart := strings.TrimSpace(remoteLinePart[2])
		if typePart == "(fetch)" {
			r.Fetch = true
		}
		if typePart == "(push)" {
			r.Push = true
		}
		uniqueRemoteMap[key] = r
	}

	for _, r := range uniqueRemoteMap {
		remoteStruct = append(remoteStruct, r)
		if r.Fetch {
			fetchRemoteStruct = append(fetchRemoteStruct, r)
		}
		if r.Push {
			pushRemoteStruct = append(pushRemoteStruct, r)
		}
	}
	gr.remote = remoteStruct
	gr.fetchRemote = fetchRemoteStruct
	gr.pushRemote = pushRemoteStruct
	return len(gr.remote) > 0
}

// ------------------------------------
//
//	Related to Git Remote sync status and upstream, will be call by system.
//	This is the local read of the remote/upstream state domain: upstream
//	identity plus the ahead/behind counts. It performs no network I/O; the
//	fetch coordinator in the daemon schedules fetches separately, and a
//	completed fetch requests its own later pass of this read.
//
//	The fresh state is assembled in local variables and stored in one
//	publication after publishGuard passes, so the upstream identity is never
//	stored before the ahead/behind parse succeeds. A verified absent-upstream
//	state publishes cleared upstream and counts. A failed read returns an
//	error and keeps the last good snapshot.
//
// ------------------------------------
func (gr *GitRemote) GetLatestRemoteSyncStatusAndUpstream(publishGuard PublishGuard) error {
	upstreamIcon, upstream, upstreamExists := hasUpstreamWithIcon()

	var syncStatus RemoteSyncStatus
	if upstreamExists {
		gitArgs := []string{"rev-list", "--left-right", "--count", "HEAD...@{upstream}"}

		remoteSyncStatusCmd := executor.GittiCmdExecutor.RunGitCmd(gitArgs, false)
		remoteSyncStatusOutput, remoteSyncStatusErr := remoteSyncStatusCmd.Output()
		if remoteSyncStatusErr != nil {
			gr.logging.RegisterNewLog(logging.CHECK_REMOTE_SYNC_STATUS_OPS, strings.Join(gitArgs, " "), logging.ERROR, fmt.Sprintf("[%s ERROR]: %s", logging.CHECK_REMOTE_SYNC_STATUS_OPS, remoteSyncStatusErr.Error()), true)
			return remoteSyncStatusErr
		}

		parsedOutput := strings.TrimSpace(string(remoteSyncStatusOutput))
		parts := strings.Fields(parsedOutput)

		if len(parts) < 2 {
			gr.logging.RegisterNewLog(logging.CHECK_REMOTE_SYNC_STATUS_OPS, strings.Join(gitArgs, " "), logging.ERROR, fmt.Sprintf("[%s ERROR]: Invalid output format", logging.CHECK_REMOTE_SYNC_STATUS_OPS), true)
			return fmt.Errorf("remote sync status: invalid output format")
		}

		syncStatus = RemoteSyncStatus{
			Local:  parts[0],
			Remote: parts[1],
		}
	}

	if publishGuard != nil {
		if err := publishGuard(); err != nil {
			return err
		}
	}
	gr.remoteSync.Store(&remoteSyncSnapshot{
		remoteSyncStatus:              syncStatus,
		upStreamRemoteIcon:            upstreamIcon,
		currentBranchUpStream:         upstream,
		currentBranchUpStreamWithIcon: "",
	})
	return nil
}

// ------------------------------------
//
//	Related to Git Fetch. This is the network fetch of the fetch coordinator.
//	It fetches and prunes all remotes with the existing fetch policy.
//
//	Only a user-triggered fetch logs its start and its failure. The fetch
//	publishes no remote state itself; the caller decides what to refresh
//	after it finishes.
//
// ------------------------------------
func (gr *GitRemote) GitFetch(userTriggered bool) {
	gitFetch(gr.logging, userTriggered)
}
