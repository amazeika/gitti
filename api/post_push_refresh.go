package api

import (
	"errors"
	"fmt"
	"sync"

	"github.com/gohyuhan/gitti/api/git"
	"github.com/gohyuhan/gitti/logging"
)

// ------------------------------------
//
//	Names of the three independently runnable state domains affected by a
//	push. The names identify a failed domain in the push popup warning and
//	in the daemon log.
//
// ------------------------------------
const (
	statePassDomainBranch         = "branch"
	statePassDomainRemoteUpstream = "remote/upstream"
	statePassDomainCommitLog      = "commit log"
)

const (
	postPushDomainBranch = iota
	postPushDomainRemoteUpstream
	postPushDomainCommitLog
	postPushDomainCount
)

// errStaleWorktreeGeneration is returned by a state pass's publish guard when
// the daemon's GitOperations changed while the pass was running, so the pass
// aborts without publishing results into a newly selected worktree.
var errStaleWorktreeGeneration = errors.New("worktree generation changed during refresh")

// ------------------------------------
//
//	stateRefreshDomain is one of the generation-aware state domains. It
//	carries monotonically increasing requested and completed generations and
//	at most one state-publication worker.
//
//	A request made while the worker is active advances the requested
//	generation. Before the worker retires, it must run again until completed
//	catches up to requested. This replaces the old skip-on-busy guard, which
//	silently dropped a refresh request that arrived while a pass was already
//	running.
//
// ------------------------------------
type stateRefreshDomain struct {
	name      string
	mu        sync.Mutex
	requested int64
	completed int64
	active    bool
}

// ------------------------------------
//
//	isActive reports whether the domain's worker is currently running
//
// ------------------------------------
func (d *stateRefreshDomain) isActive() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.active
}

// ------------------------------------
//
//	requestedGeneration returns the current requested generation
//
// ------------------------------------
func (d *stateRefreshDomain) requestedGeneration() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.requested
}

// ------------------------------------
//
//	completedGeneration returns the current completed generation
//
// ------------------------------------
func (d *stateRefreshDomain) completedGeneration() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.completed
}

// ------------------------------------
//
//	requestStatePass advances the domain's requested generation and ensures a
//	worker exists to run passes until the completed generation catches up. A
//	request arriving while a pass is active is never lost: it advances the
//	requested generation, and the worker (or its successor) runs a later pass
//	begun after the request. It returns the generation the request advanced
//	to.
//
//	Lock order: callers that also take postPushTicketsMu must take that
//	mutex first and this domain's mu second. The state-pass workers never
//	hold a domain mu while taking postPushTicketsMu, so the order is
//	deadlock-free.
//
// ------------------------------------
func (gd *GitDaemon) requestStatePass(d *stateRefreshDomain) int64 {
	d.mu.Lock()
	d.requested++
	generation := d.requested
	if !d.active {
		d.active = true
		d.mu.Unlock()
		go gd.runStatePassLoop(d)
		return generation
	}
	d.mu.Unlock()
	return generation
}

// ------------------------------------
//
//	runStatePassLoop is the domain's single state-publication worker. It
//	keeps running passes until the completed generation catches up to the
//	requested one, so a request that lands mid-pass is served by the next
//	pass instead of being dropped.
//
// ------------------------------------
func (gd *GitDaemon) runStatePassLoop(d *stateRefreshDomain) {
	for {
		d.mu.Lock()
		if d.completed >= d.requested {
			d.active = false
			d.mu.Unlock()
			return
		}
		target := d.requested
		d.mu.Unlock()

		gd.runStatePass(d, target)
	}
}

// ------------------------------------
//
//	runStatePass executes one state pass at the given target generation. It
//	captures the daemon's current GitOperations generation. It runs the
//	domain's local reads, which publish their complete snapshots only when
//	heir guard confirms the generation is still current. It re-checks the
//	generation before emitting the TUI update. It then records the completed
//	generation and resolves any post-push ticket whose target this pass
//	satisfies.
//
// ------------------------------------
func (gd *GitDaemon) runStatePass(d *stateRefreshDomain, target int64) {
	ops := gd.gitOperations.Load()
	var passErr error
	switch d.name {
	case statePassDomainBranch:
		passErr = gd.runBranchStatePass(ops)
	case statePassDomainRemoteUpstream:
		passErr = gd.runRemoteUpstreamStatePass(ops)
	case statePassDomainCommitLog:
		passErr = gd.runCommitLogStatePass(ops)
	}

	d.mu.Lock()
	d.completed = target
	d.mu.Unlock()

	gd.resolvePostPushTicketsForDomain(d, target, passErr)
}

// ------------------------------------
//
//	runBranchStatePass reads the canonical local branches and publishes them,
//	then emits GIT_BRANCH_UPDATE so m.CheckOutBranch is reloaded from the
//	current worktree. A failed or stale pass publishes nothing and emits
//	nothing.
//
// ------------------------------------
func (gd *GitDaemon) runBranchStatePass(ops *GitOperations) error {
	if hook := gd.stateBranchPassPreRead.Load(); hook != nil {
		(*hook)()
	}
	if err := ops.GitBranch.GetLatestBranchesInfo(gd.worktreeGenerationGuard(ops)); err != nil {
		gd.logStatePassFailure(statePassDomainBranch, err)
		return err
	}
	if err := gd.assertWorktreeGeneration(ops); err != nil {
		gd.logStatePassFailure(statePassDomainBranch, err)
		return err
	}
	gd.updateChannel <- git.GIT_BRANCH_UPDATE
	return nil
}

// ------------------------------------
//
//	runRemoteUpstreamStatePass performs the remote/upstream local read:
//	upstream identity plus ahead/behind counts, then the remote branch list.
//	It performs no network I/O and never waits for one. Each sub-read
//	publishes its own complete snapshot, so a sub-read failure preserves that
//	sub-read's last good state while the successful sub-read may still
//	publish; the pass as a whole reports the joined failure.
//
// ------------------------------------
func (gd *GitDaemon) runRemoteUpstreamStatePass(ops *GitOperations) error {
	if hook := gd.stateRemoteUpstreamPassPreRead.Load(); hook != nil {
		(*hook)()
	}
	var readErrs []error
	if err := ops.GitRemote.GetLatestRemoteSyncStatusAndUpstream(gd.worktreeGenerationGuard(ops)); err != nil {
		readErrs = append(readErrs, err)
	}
	if err := ops.GitBranch.GetLatestRemoteBranchesInfo(gd.worktreeGenerationGuard(ops)); err != nil {
		readErrs = append(readErrs, err)
	}
	if len(readErrs) > 0 {
		passErr := errors.Join(readErrs...)
		gd.logStatePassFailure(statePassDomainRemoteUpstream, passErr)
		return passErr
	}
	if err := gd.assertWorktreeGeneration(ops); err != nil {
		gd.logStatePassFailure(statePassDomainRemoteUpstream, err)
		return err
	}
	gd.updateChannel <- git.GIT_REMOTE_SYNC_STATUS_AND_UPSTREAM_UPDATE
	return nil
}

// ------------------------------------
//
//	runCommitLogStatePass re-reads the commit log so %D is read again after
//	the remote-tracking ref moved, then emits GIT_COMMITLOG_UPDATE. A failed
//	or stale pass keeps the last good graph and emits nothing.
//
// ------------------------------------
func (gd *GitDaemon) runCommitLogStatePass(ops *GitOperations) error {
	if hook := gd.stateCommitLogPassPreRead.Load(); hook != nil {
		(*hook)()
	}
	if err := ops.GitCommitLog.GetCommitLogs(gd.worktreeGenerationGuard(ops)); err != nil {
		gd.logStatePassFailure(statePassDomainCommitLog, err)
		return err
	}
	if err := gd.assertWorktreeGeneration(ops); err != nil {
		gd.logStatePassFailure(statePassDomainCommitLog, err)
		return err
	}
	gd.updateChannel <- git.GIT_COMMITLOG_UPDATE
	return nil
}

// ------------------------------------
//
//	worktreeGenerationGuard returns the publish guard a state pass hands to
//	its reads. It fails the publication when the daemon's GitOperations
//	generation is no longer the one the pass captured. A stale worktree's
//	results are therefore reported rather than published into a newly
//	selected worktree.
//
// ------------------------------------
func (gd *GitDaemon) worktreeGenerationGuard(ops *GitOperations) git.PublishGuard {
	return func() error {
		if gd.gitOperations.Load() != ops {
			return errStaleWorktreeGeneration
		}
		return nil
	}
}

// ------------------------------------
//
//	assertWorktreeGeneration re-checks the daemon's GitOperations generation
//	after a pass's reads succeeded and immediately before its TUI update is
//	emitted.
//
//	An event that would make the UI re-read a worktree the app is no longer
//	showing is therefore suppressed and reported.
//
// ------------------------------------
func (gd *GitDaemon) assertWorktreeGeneration(ops *GitOperations) error {
	if gd.gitOperations.Load() != ops {
		return errStaleWorktreeGeneration
	}
	return nil
}

// ------------------------------------
//
//	logStatePassFailure records a failed state pass in the existing log style
//	so a dropped publication is inspectable rather than silent.
//
// ------------------------------------
func (gd *GitDaemon) logStatePassFailure(domain string, err error) {
	gd.gittiLogger.RegisterNewLog(logging.CHECK_REMOTE_SYNC_STATUS_OPS, "", logging.ERROR,
		fmt.Sprintf("[%s ERROR]: state refresh for %s failed: %s", logging.CHECK_REMOTE_SYNC_STATUS_OPS, domain, err.Error()), false)
}

// ------------------------------------
//
//	requestFetch schedules one network fetch of all remotes with the existing
//	fetch policy and logging: a fetch already in flight is not duplicated, and
//	only a user-triggered request warns about the skip.
//
//	The fetch never blocks or delays a state pass, and a post-push local-read
//	request can never merge into, be satisfied by, or queue behind it. When
//	the fetch finishes, it requests its own later remote/upstream local-read
//	pass.
//
// ------------------------------------
func (gd *GitDaemon) requestFetch(userTriggered bool) {
	if !gd.isGitFetchRunning.CompareAndSwap(false, true) {
		if userTriggered {
			gd.gittiLogger.RegisterNewLog(logging.FETCH_OPS, "", logging.WARN, "[WARN]: A background process to fetch is already running", false)
		}
		return
	}
	go func() {
		defer gd.isGitFetchRunning.Store(false)
		if hook := gd.stateFetchPreRead.Load(); hook != nil {
			(*hook)()
		}
		gitOps := gd.gitOperations.Load()
		gitOps.GitRemote.GitFetch(userTriggered)
		// a fetch that completes schedules its own later remote-state read
		gd.requestStatePass(&gd.remoteUpstreamStateDomain)
	}()
}

// ------------------------------------
//
//	PostPushRefreshTicket is the no-drop post-push reconciliation handle. It
//	is tied to the GitOperations/worktree generation that executed the push
//	and to one target generation in each of the three state domains. It
//	completes when each target is satisfied by a pass that began after the
//	request and emitted its TUI update.
//
//	It does not wait for a worker to become globally idle, for newer
//	unrelated requests, or for a network fetch.
//
// ------------------------------------
type PostPushRefreshTicket struct {
	gd            *GitDaemon
	mu            sync.Mutex
	gitOperations *GitOperations
	targets       [postPushDomainCount]int64
	outcomes      [postPushDomainCount]postPushDomainOutcome
	done          chan struct{}
	finalized     bool
	result        PostPushRefreshResult
}

type postPushDomainOutcome struct {
	resolved bool
	err      error
}

// ------------------------------------
//
//	PostPushRefreshResult is the terminal outcome of a post-push
//	reconciliation. Refreshed is true only when every affected domain
//	reconciled. FailedDomains and Errors (same order and index) identify the
//	domains that did not reconcile, and WorktreeGenerationChanged reports that
//	the daemon's GitOperations moved to another worktree during the refresh.
//	A failure here never relabels the Git push: the push already succeeded.
//
// ------------------------------------
type PostPushRefreshResult struct {
	Refreshed                 bool
	FailedDomains             []string
	Errors                    []error
	WorktreeGenerationChanged bool
}

// ------------------------------------
//
//	FailureSummary renders the failed domains and their errors for the push
//	popup warning and the log entry; empty when the reconciliation
//	succeeded.
//
// ------------------------------------
func (r PostPushRefreshResult) FailureSummary() string {
	if r.Refreshed {
		return ""
	}
	var parts []string
	if r.WorktreeGenerationChanged {
		parts = append(parts, "the active worktree changed during the refresh")
	}
	for i, domain := range r.FailedDomains {
		if i < len(r.Errors) && r.Errors[i] != nil {
			parts = append(parts, fmt.Sprintf("%s (%s)", domain, r.Errors[i].Error()))
			continue
		}
		parts = append(parts, domain)
	}
	return joinParts(parts)
}

func joinParts(parts []string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += "; "
		}
		out += part
	}
	return out
}

// ------------------------------------
//
//	RequestPostPushRefresh requests the post-push state reconciliation: one
//	target generation in each of the three state domains, bound to the
//	GitOperations generation that executed the push. The request never
//	performs a fetch and never waits for one. The returned ticket is complete
//	when every target domain pass has run to its publication and TUI update,
//	or has failed (including a stale worktree generation).
//
//	The target allocation and the ticket registration run while holding
//	postPushTicketsMu, which resolvePostPushTicketsForDomain also takes. A
//	target pass therefore cannot finish and resolve the registered tickets
//	before the ticket is registered: its resolution blocks until the
//	registration completes, so a completion is never lost.
//
// ------------------------------------
func (gd *GitDaemon) RequestPostPushRefresh(expectedGitOperations *GitOperations) *PostPushRefreshTicket {
	ticket := &PostPushRefreshTicket{
		gd:            gd,
		gitOperations: expectedGitOperations,
		done:          make(chan struct{}),
	}

	gd.postPushTicketsMu.Lock()
	ticket.targets[postPushDomainBranch] = gd.requestStatePass(&gd.branchStateDomain)
	ticket.targets[postPushDomainRemoteUpstream] = gd.requestStatePass(&gd.remoteUpstreamStateDomain)
	ticket.targets[postPushDomainCommitLog] = gd.requestStatePass(&gd.commitLogStateDomain)
	gd.postPushTickets = append(gd.postPushTickets, ticket)
	gd.postPushTicketsMu.Unlock()

	return ticket
}

// ------------------------------------
//
//	Done is closed exactly once when the ticket's result is final.
//
// ------------------------------------
func (t *PostPushRefreshTicket) Done() <-chan struct{} {
	return t.done
}

// ------------------------------------
//
//	Await blocks until the reconciliation outcome is final and returns it.
//
// ------------------------------------
func (t *PostPushRefreshTicket) Await() PostPushRefreshResult {
	<-t.done
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.result
}

// ------------------------------------
//
//	Result reports the final outcome without blocking, or false when the
//	ticket has not finished yet.
//
// ------------------------------------
func (t *PostPushRefreshTicket) Result() (PostPushRefreshResult, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.finalized {
		return PostPushRefreshResult{}, false
	}
	return t.result, true
}

// ------------------------------------
//
//	resolveDomain records the outcome of the pass at passGeneration for one
//	ticket domain. A pass satisfies the ticket's target when it ran at a
//	generation at or after the target: the generation is assigned from the
//	requested counter, so such a pass necessarily began after the ticket's
//	request. A failed pass also resolves the target, with the failure, so the
//	ticket completes and names the domain instead of waiting forever.
//
// ------------------------------------
func (t *PostPushRefreshTicket) resolveDomain(domain int, passGeneration int64, passErr error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.outcomes[domain].resolved || passGeneration < t.targets[domain] {
		return
	}
	t.outcomes[domain] = postPushDomainOutcome{resolved: true, err: passErr}
	t.finalizeLocked()
}

// ------------------------------------
//
//	finalizeLocked computes the final result, records a stale worktree
//	generation when the daemon's GitOperations no longer matches the one that
//	executed the push, and closes the ticket. Returns true once finalized.
//
// ------------------------------------
func (t *PostPushRefreshTicket) finalizeLocked() {
	for _, outcome := range t.outcomes {
		if !outcome.resolved {
			return
		}
	}
	if t.finalized {
		return
	}
	t.finalized = true
	t.result = t.buildResultLocked()
	close(t.done)
}

// ------------------------------------
//
//	buildResultLocked assembles the final result from the per-domain
//	outcomes and the worktree generation check.
//
// ------------------------------------
func (t *PostPushRefreshTicket) buildResultLocked() PostPushRefreshResult {
	result := PostPushRefreshResult{Refreshed: true}
	for domain, outcome := range t.outcomes {
		if outcome.err == nil {
			continue
		}
		result.Refreshed = false
		result.FailedDomains = append(result.FailedDomains, postPushDomainNames[domain])
		result.Errors = append(result.Errors, outcome.err)
	}
	if t.gd.gitOperations.Load() != t.gitOperations {
		result.WorktreeGenerationChanged = true
		if result.Refreshed {
			result.Refreshed = false
			result.FailedDomains = append(result.FailedDomains, "worktree generation")
			result.Errors = append(result.Errors, errStaleWorktreeGeneration)
		}
	}
	return result
}

var postPushDomainNames = [postPushDomainCount]string{
	statePassDomainBranch,
	statePassDomainRemoteUpstream,
	statePassDomainCommitLog,
}

// ------------------------------------
//
//	resolvePostPushTicketsForDomain resolves, for one completed pass
//	generation, every registered ticket whose target in the domain that pass
//	served is at or before the generation, then drops the tickets that
//	finalized.
//
// ------------------------------------
func (gd *GitDaemon) resolvePostPushTicketsForDomain(d *stateRefreshDomain, passGeneration int64, passErr error) {
	domainIndex := -1
	switch d.name {
	case statePassDomainBranch:
		domainIndex = postPushDomainBranch
	case statePassDomainRemoteUpstream:
		domainIndex = postPushDomainRemoteUpstream
	case statePassDomainCommitLog:
		domainIndex = postPushDomainCommitLog
	}

	gd.postPushTicketsMu.Lock()
	remaining := gd.postPushTickets[:0]
	for _, ticket := range gd.postPushTickets {
		ticket.resolveDomain(domainIndex, passGeneration, passErr)
		if _, finalized := ticket.Result(); !finalized {
			remaining = append(remaining, ticket)
		}
	}
	gd.postPushTickets = remaining
	gd.postPushTicketsMu.Unlock()
}
