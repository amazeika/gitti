package api

import (
	"testing"
	"time"

	gitapi "github.com/gohyuhan/gitti/api/git"
)

func TestPhase2PostPushTicketWithAbsentUpstreamRefreshesCleanly(t *testing.T) {
	h := daemonUnderTest(t, 0)
	t.Setenv("FAKE_CONFIG_UNSET", "1")

	ticket := h.gd.RequestPostPushRefresh(h.gitOps)
	result := ticket.Await()

	if !result.Refreshed {
		t.Fatalf("the partial-key reconciliation failed: %s", result.FailureSummary())
	}
	if len(result.FailedDomains) != 0 {
		t.Errorf("failed domains = %v, want empty for a valid partial configuration", result.FailedDomains)
	}
	snapshot := h.gitOps.GitRemote.RemoteSyncStatusAndUpstream()
	if snapshot.ObservationState != gitapi.UpstreamStateUnpublished {
		t.Errorf("observation state = %s, want unpublished", snapshot.ObservationState)
	}
	if snapshot.ObservedBranch != "master" {
		t.Errorf("observed branch = %q, want master", snapshot.ObservedBranch)
	}
	if snapshot.CurrentBranchUpStream != "" {
		t.Errorf("the partial-key ticket carries an upstream: %q", snapshot.CurrentBranchUpStream)
	}
	if snapshot.UpStreamRemoteIcon != gitapi.DefaultUpStreamRemoteIcon {
		t.Errorf("the partial-key icon = %q, want the default no-upstream icon", snapshot.UpStreamRemoteIcon)
	}
	if snapshot.RemoteSyncStatus != (gitapi.RemoteSyncStatus{}) {
		t.Errorf("the partial-key ticket carries counts: %v", snapshot.RemoteSyncStatus)
	}
	waitFor(t, 5*time.Second, func() bool {
		return h.events.count(gitapi.GIT_REMOTE_SYNC_STATUS_AND_UPSTREAM_UPDATE) >= 1
	}, "the unpublished health update")
}

func TestPhase2PostPushTicketWithFailingProbeKeepsLastGood(t *testing.T) {
	h := daemonUnderTest(t, 0)

	t.Setenv("FAKE_REVLIST_COUNT", "3 1")
	h.gd.requestStatePass(&h.gd.remoteUpstreamStateDomain)
	waitFor(t, 5*time.Second, func() bool {
		return h.gd.remoteUpstreamStateDomain.completedGeneration() >= 1
	}, "the baseline remote pass")
	baseline := h.gitOps.GitRemote.RemoteSyncStatusAndUpstream()
	if baseline.ObservationState != gitapi.UpstreamStateTracked {
		t.Fatalf("baseline observation = %s, want tracked", baseline.ObservationState)
	}
	upstreamBefore := h.gitOps.GitRemote.CurrentBranchUpStream()
	iconBefore := h.gitOps.GitRemote.UpStreamRemoteIcon()

	t.Setenv("FAKE_UPSTREAM_RESOLVE_FAIL", "1")
	ticket := h.gd.RequestPostPushRefresh(h.gitOps)
	result := ticket.Await()

	if result.Refreshed {
		t.Error("the ticket reported success even though the upstream probe failed")
	}
	if len(result.FailedDomains) != 1 || result.FailedDomains[0] != statePassDomainRemoteUpstream {
		t.Errorf("failed domains = %v, want only the remote/upstream domain", result.FailedDomains)
	}
	snapshot := h.gitOps.GitRemote.RemoteSyncStatusAndUpstream()
	if snapshot.ObservationState != gitapi.UpstreamStateUnavailable {
		t.Errorf("the failed probe did not publish the unavailable health: %s", snapshot.ObservationState)
	}
	if snapshot.CurrentBranchUpStream != upstreamBefore {
		t.Errorf("the failed probe overwrote the last-good upstream: %q -> %q", upstreamBefore, snapshot.CurrentBranchUpStream)
	}
	if snapshot.RemoteSyncStatus != (gitapi.RemoteSyncStatus{Local: "3", Remote: "1"}) {
		t.Errorf("the failed probe overwrote the last-good counts: %v", snapshot.RemoteSyncStatus)
	}
	if snapshot.UpStreamRemoteIcon != iconBefore {
		t.Error("the failed probe overwrote the last-good upstream icon")
	}
}
