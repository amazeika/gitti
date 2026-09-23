package api

import (
	"testing"
	"time"

	gitapi "github.com/gohyuhan/gitti/api/git"
)

// ------------------------------------
//
//	TestRemoteSyncStatusRequestFromUIRunsAFreshPass proves the UI can demand
//	a fresh remote/upstream pass without waiting for the daemon timer: the
//	observation request signal on the daemon receiver channel completes a
//	new generation of the remote/upstream domain and emits the update event,
//	repeatedly, so a request is never lost to coalescing.
//
// ------------------------------------
func TestRemoteSyncStatusRequestFromUIRunsAFreshPass(t *testing.T) {
	h := daemonUnderTest(t, 0)
	const event = gitapi.GIT_REMOTE_SYNC_STATUS_AND_UPSTREAM_UPDATE

	h.gd.Start()
	defer h.gd.Stop()

	// the startup fetch performs its own remote/upstream pass first
	waitFor(t, 5*time.Second, func() bool {
		return h.gd.remoteUpstreamStateDomain.completedGeneration() >= 1
	}, "the startup remote/upstream pass to complete")

	// the UI demands a fresh pass while the next timer is still far away
	for generation := int64(2); generation <= 3; generation++ {
		h.gd.daemonReceiverChannel <- gitapi.GIT_REMOTE_SYNC_STATUS_REQUEST
		want := generation
		waitFor(t, 5*time.Second, func() bool {
			return h.gd.remoteUpstreamStateDomain.completedGeneration() >= want
		}, "the requested remote/upstream pass to complete")
	}
	if got := h.events.count(event); got < 3 {
		t.Fatalf("emitted %d remote/upstream events for the startup pass plus two requests, want at least 3", got)
	}
}
