package git

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/gohyuhan/gitti/executor"
	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/logging"
)

// remoteSyncSnapshot is one immutable generation of the remote/upstream
// local-read state: the typed observation state, the observed symbolic
// branch, the ahead/behind counts, the upstream identity, and its icon.
// GetLatestRemoteSyncStatusAndUpstream assembles the fresh state in local
// variables and publishes it with one store, so a reader sees either the
// whole prior generation or the whole new one. Counts and upstream identity
// are only meaningful while the observation state is tracked.
type remoteSyncSnapshot struct {
	remoteSyncStatus      RemoteSyncStatus
	upStreamRemoteIcon    string
	currentBranchUpStream string
	observationState      UpstreamObservationState
	observedBranch        string
}

// RemoteSyncUpstreamSnapshot is one published generation of the
// remote/upstream state, returned by the combined getter so a reader never
// mixes the observation state, observed branch, upstream identity, icon, and
// counts from two passes.
type RemoteSyncUpstreamSnapshot struct {
	RemoteSyncStatus      RemoteSyncStatus
	UpStreamRemoteIcon    string
	CurrentBranchUpStream string
	ObservationState      UpstreamObservationState
	ObservedBranch        string
}

type GitRemote struct {
	updateChannel   chan string
	gitProcessLock  *GitProcessLock
	cmdExecutor     *executor.CmdExecutor // generation executor bound to the captured worktree
	remoteInventory atomic.Pointer[GitRemoteInventory]
	remoteSync      atomic.Pointer[remoteSyncSnapshot]
	// lastGoodRemoteSync is the last published tracked generation whose
	// upstream names a remote-tracking ref. A failed read publishes
	// unavailable health with this payload, so an intermittent probe
	// failure restores genuine remote identity, icon, and counts even when
	// newer published generations (a valid unpublished absence, or a
	// local-dot upstream naming the branch itself) carry no remote state.
	lastGoodRemoteSync atomic.Pointer[remoteSyncSnapshot]
	logging            *logging.GittiLogging
}

// GitRemoteInventoryEntry holds one configured remote name with its complete
// immutable fetch and push URL sets exactly as git reported them.
type GitRemoteInventoryEntry struct {
	Name      string
	FetchURLs []string
	PushURLs  []string
}

// GitRemoteInventory is one immutable, atomically published generation of
// the configured-remote inventory: one entry per remote name, in sorted
// order. A generation is only produced by a successful read, so a failed
// read can never be mistaken for "no remotes": the previous generation
// stays published until a successful read replaces it. The zero value is
// the initial "not yet read" generation and reads as empty.
type GitRemoteInventory struct {
	entries []GitRemoteInventoryEntry
}

// Entries returns a defensive deep copy of the inventory's entries in its
// deterministic order.
func (inv GitRemoteInventory) Entries() []GitRemoteInventoryEntry {
	return inv.defensiveCopy().entries
}

// defensiveCopy returns a deep copy of the generation: a fresh entries
// slice with fresh fetch and push URL sets, so a caller can mutate its
// copy without affecting the stored generation or another reader's copy.
func (inv GitRemoteInventory) defensiveCopy() GitRemoteInventory {
	if len(inv.entries) == 0 {
		return GitRemoteInventory{}
	}
	entries := make([]GitRemoteInventoryEntry, len(inv.entries))
	for i, entry := range inv.entries {
		entries[i] = GitRemoteInventoryEntry{
			Name:      entry.Name,
			FetchURLs: append([]string(nil), entry.FetchURLs...),
			PushURLs:  append([]string(nil), entry.PushURLs...),
		}
	}
	return GitRemoteInventory{entries: entries}
}

// PushCapableNames returns one name per entry that has at least one push
// URL, in the inventory's deterministic order. A fetch-only configured
// remote is not a publish target.
func (inv GitRemoteInventory) PushCapableNames() []string {
	names := []string{}
	for _, entry := range inv.entries {
		if len(entry.PushURLs) > 0 {
			names = append(names, entry.Name)
		}
	}
	return names
}

// Len returns the number of configured remote names.
func (inv GitRemoteInventory) Len() int {
	return len(inv.entries)
}

// parseRemoteInventory builds one immutable inventory generation from
// `git remote -v` output. Each non-empty line is a tab-separated remote
// name, then a URL, then a space-separated final ` (fetch)` or ` (push)`
// purpose suffix. The name is cut on the first tab and the purpose is cut
// from the final suffix, so the URL in between is preserved verbatim and
// a URL that contains spaces survives the parse. Anything else is a parse
// failure rather than a silently dropped remote, so a malformed read is
// reported instead of being treated as an absence.
func parseRemoteInventory(raw []byte) (*GitRemoteInventory, error) {
	byName := make(map[string]*GitRemoteInventoryEntry)
	order := []string{}

	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, remainder, found := strings.Cut(line, "\t")
		if !found || name == "" {
			return nil, fmt.Errorf("remote inventory: invalid remote line %q", line)
		}
		isFetch := strings.HasSuffix(remainder, " (fetch)")
		isPush := strings.HasSuffix(remainder, " (push)")
		if !isFetch && !isPush {
			return nil, fmt.Errorf("remote inventory: invalid remote purpose in line %q", line)
		}
		url := strings.TrimSuffix(remainder, " (fetch)")
		if isPush {
			url = strings.TrimSuffix(remainder, " (push)")
		}
		if url == "" {
			return nil, fmt.Errorf("remote inventory: invalid remote url in line %q", line)
		}
		entry, ok := byName[name]
		if !ok {
			entry = &GitRemoteInventoryEntry{Name: name}
			byName[name] = entry
			order = append(order, name)
		}
		if isFetch {
			entry.FetchURLs = append(entry.FetchURLs, url)
		} else {
			entry.PushURLs = append(entry.PushURLs, url)
		}
	}

	sort.Strings(order)
	entries := make([]GitRemoteInventoryEntry, 0, len(order))
	for _, name := range order {
		entries = append(entries, *byName[name])
	}
	return &GitRemoteInventory{entries: entries}, nil
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
//	Initialize the git remote handler with shared dependencies. The command
//	executor is the generation's scoped executor so the remote inventory
//	read and the upstream observation run in the captured worktree.
//
// ------------------------------------
func InitGitRemote(updateChannel chan string, gitProcessLock *GitProcessLock, cmdExecutor *executor.CmdExecutor, logging *logging.GittiLogging) *GitRemote {
	gitRemote := GitRemote{
		updateChannel:  updateChannel,
		gitProcessLock: gitProcessLock,
		cmdExecutor:    cmdExecutor,
		logging:        logging,
	}
	// until the first state pass completes, the read is pending rather than
	// a failure or an absence
	gitRemote.remoteSync.Store(&remoteSyncSnapshot{observationState: UpstreamStatePending})

	return &gitRemote
}

// ------------------------------------
//
//	Return the current immutable remote inventory generation as a defensive
//	deep copy; the zero inventory until the first successful read. A caller
//	can mutate the returned inventory or any of its URL sets without
//	affecting the stored generation.
//
// ------------------------------------
func (gr *GitRemote) RemoteInventory() GitRemoteInventory {
	if inv := gr.remoteInventory.Load(); inv != nil {
		return inv.defensiveCopy()
	}
	return GitRemoteInventory{}
}

// inventory returns the current generation or the zero inventory before the
// first successful read
func (gr *GitRemote) inventory() GitRemoteInventory {
	return gr.RemoteInventory()
}

// ------------------------------------
//
//	Return remote (derived from the current inventory generation: one info
//	per unique name-url pair, preserving each url's fetch and push roles)
//
// ------------------------------------
func (gr *GitRemote) Remote() []GitRemoteInfo {
	infos := []GitRemoteInfo{}
	for _, entry := range gr.inventory().entries {
		seen := make(map[string]bool)
		for _, url := range entry.FetchURLs {
			if seen[url] {
				continue
			}
			seen[url] = true
			infos = append(infos, GitRemoteInfo{Name: entry.Name, Url: url, Fetch: true, Push: containsString(entry.PushURLs, url)})
		}
		for _, url := range entry.PushURLs {
			if seen[url] {
				continue
			}
			seen[url] = true
			infos = append(infos, GitRemoteInfo{Name: entry.Name, Url: url, Push: true})
		}
	}
	return infos
}

// ------------------------------------
//
//	Return fetch related remote only (derived from the inventory generation)
//
// ------------------------------------
func (gr *GitRemote) FetchRemote() []GitRemoteInfo {
	infos := []GitRemoteInfo{}
	for _, entry := range gr.inventory().entries {
		for _, url := range entry.FetchURLs {
			infos = append(infos, GitRemoteInfo{Name: entry.Name, Url: url, Fetch: true})
		}
	}
	return infos
}

// ------------------------------------
//
//	Return push related remote only (derived from the inventory generation)
//
// ------------------------------------
func (gr *GitRemote) PushRemote() []GitRemoteInfo {
	infos := []GitRemoteInfo{}
	for _, entry := range gr.inventory().entries {
		for _, url := range entry.PushURLs {
			infos = append(infos, GitRemoteInfo{Name: entry.Name, Url: url, Push: true})
		}
	}
	return infos
}

// ------------------------------------
//
//	PushCapableRemoteInfos returns one info per push-capable entry in the
//	inventory generation (the remote name with its first push URL), in the
//	inventory's deterministic order. It is the publish and push target list:
//	a fetch-only configured remote is never offered as a target.
//
// ------------------------------------
func (gr *GitRemote) PushCapableRemoteInfos() []GitRemoteInfo {
	infos := []GitRemoteInfo{}
	for _, entry := range gr.inventory().entries {
		if len(entry.PushURLs) > 0 {
			infos = append(infos, GitRemoteInfo{Name: entry.Name, Url: entry.PushURLs[0], Push: true})
		}
	}
	return infos
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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
		ObservationState:      snapshot.observationState,
		ObservedBranch:        snapshot.observedBranch,
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
//	CheckRemoteExist re-reads the configured remotes through the generation's
//	command executor and publishes one new immutable inventory generation on
//	success. A command or parse failure returns an error and leaves the last
//	good inventory untouched, so a failed read is never treated as "no
//	remotes". Passive (daemon-driven) reads log only their failure; user-
//	triggered reads also log their start.
//
// ------------------------------------
func (gr *GitRemote) CheckRemoteExist(passiveRunning bool) error {
	gitArgs := []string{"remote", "-v"}
	cmd := gr.cmdExecutor.RunGitCmd(gitArgs, false)
	if !passiveRunning {
		gr.logging.RegisterNewLog(logging.CHECK_REMOTE_OPS, strings.Join(gitArgs, " "), logging.INFO, "", true)
	}
	gitOutput, err := cmd.Output()
	if err != nil {
		if !passiveRunning {
			gr.logging.RegisterNewLog(logging.CHECK_REMOTE_OPS, strings.Join(gitArgs, " "), logging.ERROR, fmt.Sprintf("[%s ERROR]: %s", logging.CHECK_REMOTE_OPS, err.Error()), true)
		}
		return fmt.Errorf("reading the remote inventory: %w", err)
	}

	inventory, parseErr := parseRemoteInventory(gitOutput)
	if parseErr != nil {
		if !passiveRunning {
			gr.logging.RegisterNewLog(logging.CHECK_REMOTE_OPS, strings.Join(gitArgs, " "), logging.ERROR, fmt.Sprintf("[%s ERROR]: %s", logging.CHECK_REMOTE_OPS, parseErr.Error()), true)
		}
		return parseErr
	}

	gr.remoteInventory.Store(inventory)
	return nil
}

// ------------------------------------
//
//	Related to Git Remote sync status and upstream, will be call by system.
//	This is the local read of the remote/upstream state domain: the typed
//	upstream observation plus its matching payload (observed branch,
//	upstream identity, ahead/behind counts). It performs no network I/O; the
//	fetch coordinator in the daemon schedules fetches separately, and a
//	completed fetch requests its own later pass of this read.
//
//	The fresh observation is assembled in local variables and stored in one
//	publication after publishGuard passes, so a reader never mixes the state
//	and payload from two generations. A successful read publishes the
//	classified state with its matching payload; a tracked read whose
//	upstream names a remote-tracking ref also refreshes the last-good
//	remote snapshot. A failed current-generation read changes only the
//	observation health to unavailable while retaining the last-good remote
//	payload (the previous payload when no tracked remote generation was ever
//	published), and still returns the error so a caller can report the failed
//	read. A stale generation publishes neither data nor health and returns
//	the guard's error.
//
// ------------------------------------
func (gr *GitRemote) GetLatestRemoteSyncStatusAndUpstream(publishGuard PublishGuard) error {
	observation, observationErr := resolveUpstreamObservation(gr.cmdExecutor, gr.logging)

	if publishGuard != nil {
		if err := publishGuard(); err != nil {
			return err
		}
	}

	if observationErr != nil {
		// only the health changes; the last-good remote payload stays, so
		// a valid absence or local-dot generation published since the
		// last tracked remote read is not mistaken for restorable state
		previous := gr.remoteSync.Load()
		retain := previous
		if lastGood := gr.lastGoodRemoteSync.Load(); lastGood != nil {
			retain = lastGood
		}
		gr.remoteSync.Store(&remoteSyncSnapshot{
			remoteSyncStatus:      retain.remoteSyncStatus,
			upStreamRemoteIcon:    retain.upStreamRemoteIcon,
			currentBranchUpStream: retain.currentBranchUpStream,
			observationState:      UpstreamStateUnavailable,
			observedBranch:        previous.observedBranch,
		})
		return observationErr
	}

	upStreamIcon := observation.UpStreamIcon
	if upStreamIcon == "" {
		upStreamIcon = DefaultUpStreamRemoteIcon
	}
	published := &remoteSyncSnapshot{
		remoteSyncStatus:      observation.RemoteSync,
		upStreamRemoteIcon:    upStreamIcon,
		currentBranchUpStream: observation.UpStream,
		observationState:      observation.State,
		observedBranch:        observation.Branch,
	}
	gr.remoteSync.Store(published)
	if observation.State == UpstreamStateTracked && observation.UpStream != observation.Branch {
		// a resolved remote-tracking upstream is restorable remote
		// state; a local-dot upstream names the branch itself and
		// carries no remote identity worth restoring later
		gr.lastGoodRemoteSync.Store(published)
	}
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
