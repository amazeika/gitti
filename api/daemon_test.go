package api

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gitapi "github.com/gohyuhan/gitti/api/git"
	"github.com/gohyuhan/gitti/executor"
	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/logging"
	"github.com/gohyuhan/gitti/settings"
)

// ------------------------------------
//
//	fakeDaemonGitScript stands in for git so the daemon tests exercise the
//	coordination deterministically without a repository. It answers every
//	passive read the state passes make, honours the rev-list count/failure
//	environment switches, and records the subcommands it is asked to run.
//
// ------------------------------------
const fakeDaemonGitScript = `#!/bin/sh
if [ -n "$FAKE_GIT_CALL_LOG" ]; then
  for a in "$@"; do
    case "$a" in
      for-each-ref|rev-parse|rev-list|fetch|branch|remote|log)
        echo "$a" >> "$FAKE_GIT_CALL_LOG"
        break
        ;;
    esac
  done
fi
for a in "$@"; do
  case "$a" in
    for-each-ref)
      printf 'master\0001\0000\n'
      exit 0
      ;;
    symbolic-ref)
      echo "refs/heads/master"
      exit 0
      ;;
    rev-parse)
      echo "origin/master"
      exit 0
      ;;
    rev-list)
      if [ -n "$FAKE_REVLIST_FAIL" ]; then
        echo "fatal: ambiguous argument" >&2
        exit 128
      fi
      echo "${FAKE_REVLIST_COUNT:-0 0}"
      exit 0
      ;;
    branch)
      echo "  origin/master"
      exit 0
      ;;
    remote)
      echo "https://github.com/example/repo.git"
      exit 0
      ;;
    log)
      exit 0
      ;;
    fetch)
      exit 0
      ;;
  esac
done
exit 0
`

// ------------------------------------
//
//	eventCollector drains a daemon update channel and records the events for
//	ordered assertions
//
// ------------------------------------
type eventCollector struct {
	mu     sync.Mutex
	events []string
}

func (c *eventCollector) run(ch chan string) {
	for event := range ch {
		c.mu.Lock()
		c.events = append(c.events, event)
		c.mu.Unlock()
	}
}

func (c *eventCollector) count(event string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.events {
		if e == event {
			n++
		}
	}
	return n
}

func (c *eventCollector) last(event string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.events) - 1; i >= 0; i-- {
		if c.events[i] == event {
			return i
		}
	}
	return -1
}

// ------------------------------------
//
//	daemonHarness wires a real GitDaemon against the fake git with captured
//	update events and the call log
//
// ------------------------------------
type daemonHarness struct {
	gd        *GitDaemon
	gitOps    *GitOperations
	logging   *logging.GittiLogging
	events    *eventCollector
	callLog   string
	remoteDur time.Duration
}

// ------------------------------------
//
//	daemonUnderTest builds a daemon under test. The remote-sync timer period
//	can be shortened for the timer policy test; by default it stays at the
//	configured 60s so it never fires mid-test.
//
// ------------------------------------
func daemonUnderTest(t *testing.T, remoteTimerDurationMS int) *daemonHarness {
	t.Helper()

	originalSettings := settings.GITTICONFIGSETTINGS
	cfg := settings.GittiDefaultConfigSettings
	if remoteTimerDurationMS > 0 {
		cfg.GitRemoteSyncStatusDurationMS = remoteTimerDurationMS
		// keep the other periodic work out of the test window
		cfg.GitFilesActiveRefreshDurationMS = 3600000
		cfg.FileWatcherDebounceMS = 3600000
	}
	settings.GITTICONFIGSETTINGS = &cfg
	t.Cleanup(func() { settings.GITTICONFIGSETTINGS = originalSettings })

	i18n.InitGittiLanguageMapping("en")

	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(fakeDaemonGitScript), 0o755); err != nil {
		t.Fatalf("writing the fake git: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GIT_CALL_LOG", filepath.Join(t.TempDir(), "calls.log"))

	originalExecutor := executor.GittiCmdExecutor
	t.Cleanup(func() { executor.GittiCmdExecutor = originalExecutor })
	executor.InitCmdExecutor(t.TempDir())

	gittiLogging := logging.InitGittiLogging(4096, make(chan string, 4096), 3)
	events := &eventCollector{}
	updateChannel := make(chan string, 4096)
	go events.run(updateChannel)

	callLog := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("FAKE_GIT_CALL_LOG", callLog)

	gitOps := InitGitOperations(t.TempDir(), t.TempDir(), make(chan string, 64), gittiLogging)
	InitGitDaemon(t.TempDir(), updateChannel, gitOps, false, make(chan string, 16), gittiLogging)
	t.Cleanup(func() {
		harness := GITDAEMON
		// let any in-flight worker or fetch finish before the executor and
		// fake git are torn down, so a late pass cannot exec against a
		// restored nil executor
		harness.WaitStatePassesIdle(5 * time.Second)
		harness.Stop()
		harness.watcher.Close()
	})

	return &daemonHarness{gd: GITDAEMON, gitOps: gitOps, logging: gittiLogging, events: events, callLog: callLog}
}

// ------------------------------------
//
//	domainIndex maps a state domain to its post-push ticket domain index
//
// ------------------------------------
func domainIndex(d *stateRefreshDomain) int {
	switch d.name {
	case statePassDomainBranch:
		return postPushDomainBranch
	case statePassDomainRemoteUpstream:
		return postPushDomainRemoteUpstream
	case statePassDomainCommitLog:
		return postPushDomainCommitLog
	}
	return -1
}

// ------------------------------------
//
//	waitFor polls cond until it holds or the deadline passes
//
// ------------------------------------
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ------------------------------------
//
//	countCalls counts how many times the fake git recorded a subcommand
//
// ------------------------------------
func (h *daemonHarness) countCalls(t *testing.T, subcommand string) int {
	t.Helper()
	data, err := os.ReadFile(h.callLog)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), subcommand+"\n")
}

// ------------------------------------
//
//	blockingHook returns a pass pre-read hook that records each entry on
//	enter and then waits on the currently installed block channel; swap the
//	channel under swapMu between passes to release only the in-flight one.
//
//	The entry is signalled only after the pass has committed to its block
//	channel, so once a test observes entry n it can install a new block for
//	the next pass without racing the in-flight one.
//
// ------------------------------------
type passGate struct {
	swapMu sync.Mutex
	block  chan struct{}

	entered atomic.Int32
	enter   chan struct{}
}

func newPassGate() *passGate {
	g := &passGate{block: make(chan struct{}), enter: make(chan struct{}, 32)}
	close(g.block) // released by default; tests install their own hold
	return g
}

func (g *passGate) installBlock(block chan struct{}) {
	g.swapMu.Lock()
	g.block = block
	g.swapMu.Unlock()
}

func (g *passGate) hook() {
	g.swapMu.Lock()
	block := g.block
	g.swapMu.Unlock()
	g.entered.Add(1)
	g.enter <- struct{}{}
	<-block
}

func (g *passGate) enteredCount() int32 {
	return g.entered.Load()
}

func (g *passGate) awaitEntries(t *testing.T, n int32) {
	t.Helper()
	waitFor(t, 5*time.Second, func() bool { return g.enteredCount() >= n }, "pass entries")
}

// ------------------------------------
//
//	installHook publishes the gate's hook on the daemon field, atomically
//	with respect to a worker that may already be looping
//
// ------------------------------------
func (h *daemonHarness) installHook(field *atomic.Pointer[func()], gate *passGate) {
	hook := gate.hook
	field.Store(&hook)
}

// ------------------------------------
//
//	TestStatePassRequestIsNeverDroppedByABusyWorker covers a request arriving
//	before, during, and after an active state pass for every affected domain:
//	each one ends with a pass that began after the request, so nothing is
//	lost to a busy guard.
//
// ------------------------------------
func TestStatePassRequestIsNeverDroppedByABusyWorker(t *testing.T) {
	h := daemonUnderTest(t, 0)

	domains := []struct {
		name   string
		domain *stateRefreshDomain
		event  string
		field  *atomic.Pointer[func()]
	}{
		{"branch", &h.gd.branchStateDomain, gitapi.GIT_BRANCH_UPDATE, &h.gd.stateBranchPassPreRead},
		{"remote/upstream", &h.gd.remoteUpstreamStateDomain, gitapi.GIT_REMOTE_SYNC_STATUS_AND_UPSTREAM_UPDATE, &h.gd.stateRemoteUpstreamPassPreRead},
		{"commit log", &h.gd.commitLogStateDomain, gitapi.GIT_COMMITLOG_UPDATE, &h.gd.stateCommitLogPassPreRead},
	}

	for _, tc := range domains {
		t.Run(tc.name, func(t *testing.T) {
			gate := newPassGate()
			h.installHook(tc.field, gate)
			d := tc.domain

			// Request before any active pass: the idle worker runs a pass
			// that begins after the request
			h.gd.requestStatePass(d)
			gate.awaitEntries(t, 1)
			waitFor(t, 5*time.Second, func() bool { return d.completedGeneration() >= 1 }, "the first pass to complete")
			if got := d.completedGeneration(); got != 1 {
				t.Fatalf("completed = %d after the first pass, want 1", got)
			}
			waitFor(t, 5*time.Second, func() bool { return h.events.count(tc.event) >= 1 }, "the first pass event")
			if got := h.events.count(tc.event); got != 1 {
				t.Errorf("emitted %d %s events after the first pass, want 1", got, tc.event)
			}

			// Request during an active pass: the in-flight pass is held, a
			// second request lands while the worker is busy, and releasing
			// the pass must leave a later pass that began after the second
			// request
			hold := make(chan struct{})
			gate.installBlock(hold)
			h.gd.requestStatePass(d)
			gate.awaitEntries(t, 2)
			if got := d.completedGeneration(); got != 1 {
				t.Fatalf("completed = %d while the pass is held, want 1", got)
			}
			h.gd.requestStatePass(d) // the request that must not be dropped
			if got := d.requestedGeneration(); got != 3 {
				t.Fatalf("requested = %d after the busy request, want 3", got)
			}
			// install the released channel before closing the hold so the
			// later pass cannot grab the hold
			released := make(chan struct{})
			close(released)
			gate.installBlock(released)
			close(hold)

			// the later pass runs to completion without any further request
			waitFor(t, 5*time.Second, func() bool { return d.completedGeneration() >= 3 }, "the later pass")
			if got := gate.enteredCount(); got != 3 {
				t.Fatalf("the worker ran %d passes, want exactly 3", got)
			}
			waitFor(t, 5*time.Second, func() bool { return h.events.count(tc.event) >= 3 }, "the three pass events")
			if got := h.events.count(tc.event); got != 3 {
				t.Errorf("emitted %d %s events after three passes, want 3", got, tc.event)
			}

			// Request after the worker retired: a fresh worker serves it
			h.gd.requestStatePass(d)
			waitFor(t, 5*time.Second, func() bool { return d.completedGeneration() >= 4 }, "the pass after worker retirement")
			if got := gate.enteredCount(); got != 4 {
				t.Errorf("the worker ran %d passes after the fourth request, want 4", got)
			}
		})
	}
}

// ------------------------------------
//
//	TestPostPushTicketWaitsOnlyForItsTargetPasses proves a ticket resolves as
//	soon as its own target passes have published and emitted, even while a
//	newer unrelated request still queues work on the same domain, and that
//	the ticket performs no fetch.
//
// ------------------------------------
func TestPostPushTicketWaitsOnlyForItsTargetPasses(t *testing.T) {
	h := daemonUnderTest(t, 0)

	commitLogGate := newPassGate()
	h.installHook(&h.gd.stateCommitLogPassPreRead, commitLogGate)

	// hold the target commit log pass in flight
	hold := make(chan struct{})
	commitLogGate.installBlock(hold)

	ticket := h.gd.RequestPostPushRefresh(h.gitOps)
	waitFor(t, 5*time.Second, func() bool {
		return h.gd.branchStateDomain.completedGeneration() >= 1 && h.gd.remoteUpstreamStateDomain.completedGeneration() >= 1
	}, "the branch and remote target passes")
	commitLogGate.awaitEntries(t, 1)

	if _, finalized := ticket.Result(); finalized {
		t.Fatal("the ticket finished while its commit log target pass was still in flight")
	}

	// a newer unrelated request lands while the target pass is held; its
	// pass is held on a block the test only closes at the end, so if the
	// ticket waited for it the ticket would never complete
	permHold := make(chan struct{})
	var permHoldOnce sync.Once
	t.Cleanup(func() { permHoldOnce.Do(func() { close(permHold) }) })
	h.gd.requestStatePass(&h.gd.commitLogStateDomain)

	// release the target pass, but only after the extra pass's hold is in
	// place so the extra pass cannot steal the release
	commitLogGate.installBlock(permHold)
	close(hold)

	select {
	case <-ticket.Done():
		// the ticket resolved on its target pass, not on the unrelated one
	case <-time.After(5 * time.Second):
		t.Fatal("the ticket waited for the newer unrelated request instead of its target pass")
	}

	result := ticket.Await()
	if !result.Refreshed {
		t.Errorf("the ticket reported a failed reconciliation: %s", result.FailureSummary())
	}

	// the unrelated request still gets its own later pass
	permHoldOnce.Do(func() { close(permHold) })
	waitFor(t, 5*time.Second, func() bool { return h.gd.commitLogStateDomain.completedGeneration() >= 2 }, "the unrelated later pass")

	// the reconciliation performs no fetch
	if got := h.countCalls(t, "fetch"); got != 0 {
		t.Errorf("the post-push reconciliation performed %d fetches, want none", got)
	}
}

// ------------------------------------
//
//	TestPostPushTicketDoesNotWaitForAnActiveFetch proves a post-push request
//	made while a network fetch is active runs its local state passes and
//	completes without waiting for the fetch, and that the fetch's completion
//	still schedules its own later remote-state read.
//
// ------------------------------------
func TestPostPushTicketDoesNotWaitForAnActiveFetch(t *testing.T) {
	h := daemonUnderTest(t, 0)

	remoteGate := newPassGate()
	h.installHook(&h.gd.stateRemoteUpstreamPassPreRead, remoteGate)

	fetchEnter := make(chan struct{}, 1)
	fetchRelease := make(chan struct{})
	fetchHook := func() {
		fetchEnter <- struct{}{}
		<-fetchRelease
	}
	h.gd.stateFetchPreRead.Store(&fetchHook)

	// a background fetch is in flight
	h.gd.requestFetch(false)
	select {
	case <-fetchEnter:
	case <-time.After(5 * time.Second):
		t.Fatal("the background fetch never started")
	}
	if !h.gd.isGitFetchRunning.Load() {
		t.Fatal("the fetch coordinator did not mark the fetch running")
	}

	// the post-push request arrives during the active fetch
	ticket := h.gd.RequestPostPushRefresh(h.gitOps)
	remoteGate.awaitEntries(t, 1) // the remote local read ran while the fetch was held

	select {
	case <-ticket.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the ticket waited for the active fetch instead of its own passes")
	}
	result := ticket.Await()
	if !result.Refreshed {
		t.Errorf("the ticket reported a failed reconciliation: %s", result.FailureSummary())
	}

	// releasing the fetch makes it run, and its completion requests its own
	// later remote-state read
	close(fetchRelease)
	waitFor(t, 5*time.Second, func() bool { return remoteGate.enteredCount() >= 2 }, "the fetch-completion remote state read")
	waitFor(t, 5*time.Second, func() bool { return !h.gd.isGitFetchRunning.Load() }, "the fetch to finish")
	if got := h.countCalls(t, "fetch"); got != 1 {
		t.Errorf("the fetch ran %d times, want exactly 1", got)
	}
}

// ------------------------------------
//
//	TestPostPushTicketRequestedDuringEachActiveStatePass covers a post-push
//	reconciliation request that arrives while a pass of each affected domain
//	is already in flight: the in-flight pass began before the request, so it
//	cannot satisfy the ticket; a later pass that begins after the request
//	must run and resolve the ticket.
//
// ------------------------------------
func TestPostPushTicketRequestedDuringEachActiveStatePass(t *testing.T) {
	h := daemonUnderTest(t, 0)

	domains := []struct {
		name   string
		domain *stateRefreshDomain
		field  *atomic.Pointer[func()]
	}{
		{"branch", &h.gd.branchStateDomain, &h.gd.stateBranchPassPreRead},
		{"remote/upstream", &h.gd.remoteUpstreamStateDomain, &h.gd.stateRemoteUpstreamPassPreRead},
		{"commit log", &h.gd.commitLogStateDomain, &h.gd.stateCommitLogPassPreRead},
	}

	for _, tc := range domains {
		t.Run(tc.name, func(t *testing.T) {
			gate := newPassGate()
			h.installHook(tc.field, gate)

			// the domain is idle, so requested and completed agree
			baseline := tc.domain.completedGeneration()

			// start a pass and hold it in flight
			hold := make(chan struct{})
			gate.installBlock(hold)
			h.gd.requestStatePass(tc.domain)
			gate.awaitEntries(t, 1)

			// the successor pass the ticket will target gets its own hold,
			// installed while the original pass is still held, so it cannot
			// run ahead of the assertions
			targetHold := make(chan struct{})
			var targetHoldOnce sync.Once
			releaseTargetHold := func() { targetHoldOnce.Do(func() { close(targetHold) }) }
			t.Cleanup(releaseTargetHold)
			gate.installBlock(targetHold)

			// the post-push request arrives during the active pass; its
			// target generation is strictly after the in-flight one
			ticket := h.gd.RequestPostPushRefresh(h.gitOps)
			if got := ticket.targets[domainIndex(tc.domain)]; got != baseline+2 {
				t.Fatalf("the ticket target = %d, want %d (the generation after the in-flight pass)", got, baseline+2)
			}

			// release only the original, pre-request pass; it completes
			// without satisfying the ticket, and the target pass enters held
			close(hold)
			gate.awaitEntries(t, 2)
			waitFor(t, 5*time.Second, func() bool { return tc.domain.completedGeneration() == baseline+1 }, "the in-flight pass")
			if _, finalized := ticket.Result(); finalized {
				t.Fatal("the ticket was satisfied by a pass that began before the request")
			}

			// the later pass that began after the request resolves it
			releaseTargetHold()

			// the later pass that began after the request resolves it
			select {
			case <-ticket.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("the later pass did not resolve the ticket")
			}
			if gate.enteredCount() < 2 {
				t.Fatalf("only %d passes ran, want a later pass after the in-flight one", gate.enteredCount())
			}
			result := ticket.Await()
			if !result.Refreshed {
				t.Errorf("the ticket reported a failed reconciliation: %s", result.FailureSummary())
			}
		})
	}
}

// ------------------------------------
//
//	TestPostPushTicketSurvivesATargetPassCompletingDuringRequest covers the
//	registration race a bare append could lose: the request itself starts
//	the branch target pass, which completes against the fast fake git while
//	the request is still scheduling the other domains. The other two
//	domains are held in flight so only the branch pass can finish in that
//	window. The ticket must still observe the branch completion instead of
//	losing it, and finalize once every target pass has run.
//
// ------------------------------------
func TestPostPushTicketSurvivesATargetPassCompletingDuringRequest(t *testing.T) {
	h := daemonUnderTest(t, 0)

	for round := 0; round < 5; round++ {
		if !h.gd.WaitStatePassesIdle(5 * time.Second) {
			t.Fatal("the daemon did not quiesce before the round")
		}

		// hold the two non-branch domains in flight so their workers are
		// already active when the ticket requests them
		remoteGate := newPassGate()
		logGate := newPassGate()
		hold := make(chan struct{})
		var holdOnce sync.Once
		releaseHold := func() { holdOnce.Do(func() { close(hold) }) }
		t.Cleanup(releaseHold)
		remoteGate.installBlock(hold)
		logGate.installBlock(hold)
		h.installHook(&h.gd.stateRemoteUpstreamPassPreRead, remoteGate)
		h.installHook(&h.gd.stateCommitLogPassPreRead, logGate)
		h.gd.requestStatePass(&h.gd.remoteUpstreamStateDomain)
		h.gd.requestStatePass(&h.gd.commitLogStateDomain)
		remoteGate.awaitEntries(t, 1)
		logGate.awaitEntries(t, 1)

		// the request starts the branch target pass, which completes while
		// the request is still registering the ticket
		ticket := h.gd.RequestPostPushRefresh(h.gitOps)

		// releasing the held domains lets every target pass complete; the
		// ticket must finalize rather than lose the branch completion
		releaseHold()
		select {
		case <-ticket.Done():
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d: the ticket lost a target completion that finished during the request", round)
		}
		result := ticket.Await()
		if !result.Refreshed {
			t.Errorf("round %d: the ticket reported a failed reconciliation: %s", round, result.FailureSummary())
		}

		// uninstall the hooks so the next round starts clean
		h.gd.stateRemoteUpstreamPassPreRead.Store(nil)
		h.gd.stateCommitLogPassPreRead.Store(nil)
	}
}

// ------------------------------------
//
//	TestTicketReportsStaleGenerationWhenWorktreeChangesBeforeTicketCreation
//	covers the push-time identity binding: the push completed on one
//	worktree, the worktree switched before the ticket was created, and the
//	ticket still binds to the generation that executed the push. The passes
//	publish the new worktree's own current state, and the ticket reports the
//	stale generation instead of a successful reconciliation for the old
//	push.
//
// ------------------------------------
func TestTicketReportsStaleGenerationWhenWorktreeChangesBeforeTicketCreation(t *testing.T) {
	h := daemonUnderTest(t, 0)

	// establish last-good state on the original worktree
	h.gd.requestStatePass(&h.gd.branchStateDomain)
	h.gd.requestStatePass(&h.gd.remoteUpstreamStateDomain)
	h.gd.requestStatePass(&h.gd.commitLogStateDomain)
	waitFor(t, 5*time.Second, func() bool {
		return h.gd.branchStateDomain.completedGeneration() >= 1 &&
			h.gd.remoteUpstreamStateDomain.completedGeneration() >= 1 &&
			h.gd.commitLogStateDomain.completedGeneration() >= 1
	}, "the baseline state passes")

	// the worktree switches after the push completed but before the ticket
	// is created
	newOps := InitGitOperations(t.TempDir(), t.TempDir(), make(chan string, 64), h.logging)
	h.gd.UpdateGitOperations(newOps)
	t.Cleanup(func() { h.gd.UpdateGitOperations(h.gitOps) })

	// the ticket binds to the generation that executed the push
	ticket := h.gd.RequestPostPushRefresh(h.gitOps)
	result := ticket.Await()

	if result.Refreshed {
		t.Error("the ticket reported success for a push on a worktree that is no longer active")
	}
	if !result.WorktreeGenerationChanged {
		t.Error("the ticket did not report the changed worktree generation")
	}
}

// ------------------------------------
//
//	TestFetchCompletionRequestsItsOwnRemoteStateRead covers the same contract
//	for the non-ticket callers: startup, timer, and manual fetches keep their
//	own policy, and every completed fetch schedules a later remote-state read
//	without being merged into or satisfied by one.
//
// ------------------------------------
func TestFetchCompletionRequestsItsOwnRemoteStateRead(t *testing.T) {
	h := daemonUnderTest(t, 0)

	remoteGate := newPassGate()
	h.installHook(&h.gd.stateRemoteUpstreamPassPreRead, remoteGate)

	h.gd.requestFetch(true)
	waitFor(t, 5*time.Second, func() bool { return h.gd.remoteUpstreamStateDomain.completedGeneration() >= 1 }, "the fetch-completion remote state read")
	waitFor(t, 5*time.Second, func() bool { return !h.gd.isGitFetchRunning.Load() }, "the fetch to finish")
	if got := h.countCalls(t, "fetch"); got != 1 {
		t.Fatalf("the fetch ran %d times, want exactly 1", got)
	}
}

// ------------------------------------
//
//	TestManualFetchSkipWarnsOnlyWhenUserTriggered preserves the existing
//	fetch logging policy: a skipped user-triggered fetch warns, a skipped
//	background fetch stays quiet.
//
// ------------------------------------
func TestManualFetchSkipWarnsOnlyWhenUserTriggered(t *testing.T) {
	h := daemonUnderTest(t, 0)

	fetchRelease := make(chan struct{})
	fetchHook := func() { <-fetchRelease }
	h.gd.stateFetchPreRead.Store(&fetchHook)

	h.gd.requestFetch(false)
	waitFor(t, 5*time.Second, func() bool { return h.gd.isGitFetchRunning.Load() }, "the in-flight fetch")

	h.gd.requestFetch(true)  // user-triggered skip: warns
	h.gd.requestFetch(false) // background skip: stays quiet

	warnCount := 0
	for _, item := range h.logging.GetFullLogs() {
		if strings.Contains(item.OpsDescription, "A background process to fetch is already running") {
			warnCount++
		}
	}
	if warnCount != 1 {
		t.Errorf("the fetch skip produced %d warnings, want 1 (user-triggered only)", warnCount)
	}

	close(fetchRelease)
	waitFor(t, 5*time.Second, func() bool { return !h.gd.isGitFetchRunning.Load() }, "the fetch to finish")
}

// ------------------------------------
//
//	TestStartupAndTimerFetchPolicy preserves the existing startup and timer
//	fetch policy through the fetch coordinator: startup performs a
//	background fetch plus local reads, and each timer tick repeats the same
//	background fetch plus local read.
//
// ------------------------------------
func TestStartupAndTimerFetchPolicy(t *testing.T) {
	h := daemonUnderTest(t, 50)

	h.gd.Start()
	defer h.gd.Stop()

	// startup: one background fetch plus the three state domains
	waitFor(t, 5*time.Second, func() bool { return h.countCalls(t, "fetch") >= 1 }, "the startup fetch")
	waitFor(t, 5*time.Second, func() bool {
		return h.events.count(gitapi.GIT_BRANCH_UPDATE) >= 1 &&
			h.events.count(gitapi.GIT_REMOTE_SYNC_STATUS_AND_UPSTREAM_UPDATE) >= 1 &&
			h.events.count(gitapi.GIT_COMMITLOG_UPDATE) >= 1
	}, "the startup state events")

	// at least one 50ms timer tick has repeated the background fetch
	waitFor(t, 5*time.Second, func() bool { return h.countCalls(t, "fetch") >= 2 }, "a timer-tick fetch")

	// the background (non-user) fetches never warn
	for _, item := range h.logging.GetFullLogs() {
		if item.OpsSeverityLevel == logging.WARN && strings.Contains(item.OpsDescription, "fetch is already running") {
			t.Errorf("a background fetch warned: %q", item.OpsDescription)
		}
	}
}

// ------------------------------------
//
//	TestWatcherRefreshDoesNotFetch covers the watcher path: a debounced file
//	event refresh performs the local reads but no network fetch.
//
// ------------------------------------
func TestWatcherRefreshDoesNotFetch(t *testing.T) {
	h := daemonUnderTest(t, 0)

	h.gd.gitLatestInfoFetch(false)
	waitFor(t, 5*time.Second, func() bool {
		return h.gd.branchStateDomain.completedGeneration() >= 1 &&
			h.gd.remoteUpstreamStateDomain.completedGeneration() >= 1 &&
			h.gd.commitLogStateDomain.completedGeneration() >= 1
	}, "the watcher state passes")

	if got := h.countCalls(t, "fetch"); got != 0 {
		t.Errorf("the watcher refresh performed %d fetches, want none", got)
	}
}

// ------------------------------------
//
//	TestStaleWorktreeGenerationIsRejectedAtPublication proves a worktree
//	switch mid-pass rejects the publication and the event emission: the old
//	worktree's snapshots are not republished, the new worktree's are never
//	touched, and the ticket reports the failure instead of success.
//
// ------------------------------------
func TestStaleWorktreeGenerationIsRejectedAtPublication(t *testing.T) {
	h := daemonUnderTest(t, 0)

	// establish the last good state for the current worktree
	h.gd.requestStatePass(&h.gd.branchStateDomain)
	h.gd.requestStatePass(&h.gd.remoteUpstreamStateDomain)
	h.gd.requestStatePass(&h.gd.commitLogStateDomain)
	waitFor(t, 5*time.Second, func() bool {
		return h.gd.branchStateDomain.completedGeneration() >= 1 &&
			h.gd.remoteUpstreamStateDomain.completedGeneration() >= 1 &&
			h.gd.commitLogStateDomain.completedGeneration() >= 1 &&
			h.events.count(gitapi.GIT_BRANCH_UPDATE) >= 1 &&
			h.events.count(gitapi.GIT_REMOTE_SYNC_STATUS_AND_UPSTREAM_UPDATE) >= 1 &&
			h.events.count(gitapi.GIT_COMMITLOG_UPDATE) >= 1
	}, "the baseline state passes")
	branchEventsBefore := h.events.count(gitapi.GIT_BRANCH_UPDATE)
	remoteEventsBefore := h.events.count(gitapi.GIT_REMOTE_SYNC_STATUS_AND_UPSTREAM_UPDATE)
	logEventsBefore := h.events.count(gitapi.GIT_COMMITLOG_UPDATE)

	beforeBranch := h.gitOps.GitBranch.CurrentCheckOut()
	beforeStatus := h.gitOps.GitRemote.RemoteSyncStatus()

	// the ticket's passes all enter while held
	branchGate := newPassGate()
	remoteGate := newPassGate()
	logGate := newPassGate()
	hold := make(chan struct{})
	for _, g := range []*passGate{branchGate, remoteGate, logGate} {
		g.installBlock(hold)
	}
	h.installHook(&h.gd.stateBranchPassPreRead, branchGate)
	h.installHook(&h.gd.stateRemoteUpstreamPassPreRead, remoteGate)
	h.installHook(&h.gd.stateCommitLogPassPreRead, logGate)

	ticket := h.gd.RequestPostPushRefresh(h.gitOps)
	branchGate.awaitEntries(t, 1)
	remoteGate.awaitEntries(t, 1)
	logGate.awaitEntries(t, 1)

	// the worktree switches while the passes are in flight
	newOps := InitGitOperations(t.TempDir(), t.TempDir(), make(chan string, 64), h.logging)
	h.gd.UpdateGitOperations(newOps)
	t.Cleanup(func() { h.gd.UpdateGitOperations(h.gitOps) })

	// releasing the passes drives every publication guard into the stale
	// generation
	close(hold)
	result := ticket.Await()
	if result.Refreshed {
		t.Error("the ticket reported success after the worktree generation changed")
	}
	if !result.WorktreeGenerationChanged {
		t.Error("the ticket did not report the changed worktree generation")
	}
	if h.events.count(gitapi.GIT_BRANCH_UPDATE) != branchEventsBefore {
		t.Error("a stale branch pass emitted GIT_BRANCH_UPDATE")
	}
	if h.events.count(gitapi.GIT_REMOTE_SYNC_STATUS_AND_UPSTREAM_UPDATE) != remoteEventsBefore {
		t.Error("a stale remote pass emitted GIT_REMOTE_SYNC_STATUS_AND_UPSTREAM_UPDATE")
	}
	if h.events.count(gitapi.GIT_COMMITLOG_UPDATE) != logEventsBefore {
		t.Error("a stale commit log pass emitted GIT_COMMITLOG_UPDATE")
	}
	if got := h.gitOps.GitBranch.CurrentCheckOut(); got != beforeBranch {
		t.Errorf("the old worktree's branch snapshot changed: %v -> %v", beforeBranch, got)
	}
	if got := h.gitOps.GitRemote.RemoteSyncStatus(); got != beforeStatus {
		t.Errorf("the old worktree's sync status changed: %v -> %v", beforeStatus, got)
	}
	if got := newOps.GitBranch.CurrentCheckOut(); got.BranchName != "" {
		t.Errorf("the newly active worktree was published a stale branch snapshot: %v", got)
	}
}

// ------------------------------------
//
//	TestFailedRemoteReadPreservesLastGoodState proves a failed remote/upstream
//	read keeps the last good upstream and ahead/behind snapshots, emits no
//	event, and the ticket names the failed domain without relabeling the
//	other domains.
//
// ------------------------------------
func TestFailedRemoteReadPreservesLastGoodState(t *testing.T) {
	h := daemonUnderTest(t, 0)

	t.Setenv("FAKE_REVLIST_COUNT", "2 0")
	h.gd.requestStatePass(&h.gd.remoteUpstreamStateDomain)
	waitFor(t, 5*time.Second, func() bool {
		return h.gd.remoteUpstreamStateDomain.completedGeneration() >= 1 &&
			h.events.count(gitapi.GIT_REMOTE_SYNC_STATUS_AND_UPSTREAM_UPDATE) >= 1
	}, "the baseline remote pass")
	if status := h.gitOps.GitRemote.RemoteSyncStatus(); status != (gitapi.RemoteSyncStatus{Local: "2", Remote: "0"}) {
		t.Fatalf("baseline sync status = %v, want 2 0", status)
	}
	upstreamBefore := h.gitOps.GitRemote.CurrentBranchUpStream()
	if upstreamBefore != "origin/master" {
		t.Fatalf("baseline upstream = %q, want origin/master", upstreamBefore)
	}
	iconBefore := h.gitOps.GitRemote.UpStreamRemoteIcon()
	remoteEventsBefore := h.events.count(gitapi.GIT_REMOTE_SYNC_STATUS_AND_UPSTREAM_UPDATE)

	t.Setenv("FAKE_REVLIST_FAIL", "1")
	ticket := h.gd.RequestPostPushRefresh(h.gitOps)
	result := ticket.Await()

	if result.Refreshed {
		t.Error("the ticket reported success even though the remote read failed")
	}
	if len(result.FailedDomains) != 1 || result.FailedDomains[0] != statePassDomainRemoteUpstream {
		t.Errorf("failed domains = %v, want only the remote/upstream domain", result.FailedDomains)
	}
	if status := h.gitOps.GitRemote.RemoteSyncStatus(); status != (gitapi.RemoteSyncStatus{Local: "2", Remote: "0"}) {
		t.Errorf("the failed read overwrote the last good sync status: %v", status)
	}
	if got := h.gitOps.GitRemote.CurrentBranchUpStream(); got != upstreamBefore {
		t.Errorf("the failed read overwrote the last good upstream: %q -> %q", upstreamBefore, got)
	}
	if got := h.gitOps.GitRemote.UpStreamRemoteIcon(); got != iconBefore {
		t.Error("the failed read overwrote the last good upstream icon")
	}
	if got := h.events.count(gitapi.GIT_REMOTE_SYNC_STATUS_AND_UPSTREAM_UPDATE); got != remoteEventsBefore {
		t.Error("the failed remote pass emitted an update event")
	}
	// the healthy domains still published their newer state
	if h.events.count(gitapi.GIT_BRANCH_UPDATE) == 0 || h.events.count(gitapi.GIT_COMMITLOG_UPDATE) == 0 {
		t.Error("the healthy domains did not publish their state")
	}
}

// ------------------------------------
//
//	TestStatePassRequestDuringAnActiveFetchStillRuns proves a local state
//	request is never queued behind an active or pending network fetch: the
//	pass runs while the fetch is held, and the ticket's remote target is
//	served by that pass.
//
// ------------------------------------
func TestStatePassRequestDuringAnActiveFetchStillRuns(t *testing.T) {
	h := daemonUnderTest(t, 0)

	fetchRelease := make(chan struct{})
	fetchHook := func() { <-fetchRelease }
	h.gd.stateFetchPreRead.Store(&fetchHook)
	h.gd.requestFetch(false)
	waitFor(t, 5*time.Second, func() bool { return h.gd.isGitFetchRunning.Load() }, "the in-flight fetch")

	// the post-push remote-state request runs while the fetch is active
	before := h.gd.remoteUpstreamStateDomain.completedGeneration()
	h.gd.requestStatePass(&h.gd.remoteUpstreamStateDomain)
	waitFor(t, 5*time.Second, func() bool { return h.gd.remoteUpstreamStateDomain.completedGeneration() == before+1 }, "the remote pass during the active fetch")

	close(fetchRelease)
}
