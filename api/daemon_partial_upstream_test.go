package api

import (
	"os"
	"path/filepath"
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
//	partialUpstreamFakeGitScript stands in for git so the phase-2 daemon
//	tests exercise partial upstream configuration deterministically. It
//	answers every passive read the state passes make; FAKE_REMOTE_MODE and
//	FAKE_MERGE_MODE select each upstream config key's answer (unset for the
//	verified missing-key result, fail for a genuine read failure, dot for a
//	local-dot remote value, anything else for the configured value), while
//	FAKE_UPSTREAM_RESOLVE_FAIL breaks the configured-ref probe with both
//	keys set.
//
// ------------------------------------
const partialUpstreamFakeGitScript = `#!/bin/sh
for a in "$@"; do
  case "$a" in
    for-each-ref|rev-parse|rev-list|config|fetch|branch|remote|log)
      break
      ;;
  esac
done
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
      case "$*" in
        *"--abbrev-ref HEAD"*)
          echo "master"
          exit 0
          ;;
        *"--verify --quiet HEAD"*)
          exit 0
          ;;
        *--abbrev-ref*)
          if [ -n "$FAKE_UPSTREAM_RESOLVE_FAIL" ]; then
            echo "fatal: no upstream configured for branch 'master'" >&2
            exit 128
          fi
          echo "origin/master"
          exit 0
          ;;
      esac
      echo "origin/master"
      exit 0
      ;;
    config)
      key=""
      for a in "$@"; do
        case "$a" in
          *.remote) key="remote" ;;
          *.merge) key="merge" ;;
        esac
      done
      if [ "$key" = "remote" ]; then
        case "$FAKE_REMOTE_MODE" in
          unset) exit 1 ;;
          fail)
            echo "fatal: unable to read config" >&2
            exit 128
            ;;
          dot)
            echo "."
            exit 0
            ;;
          *)
            echo "origin"
            exit 0
            ;;
        esac
      fi
      if [ "$key" = "merge" ]; then
        case "$FAKE_MERGE_MODE" in
          unset) exit 1 ;;
          fail)
            echo "fatal: unable to read config" >&2
            exit 128
            ;;
          *)
            echo "refs/heads/master"
            exit 0
            ;;
        esac
      fi
      echo "origin"
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
      case "$*" in
        *"remote -v"*)
          printf 'origin\thttps://github.com/example/repo.git (fetch)\norigin\thttps://github.com/example/repo.git (push)\n'
          exit 0
          ;;
      esac
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
//	partialUpstreamDaemonUnderTest wires a real GitDaemon against the
//	partial-upstream fake git. The periodic timers stay out of the test
//	window so only the requested passes and tickets run.
//
// ------------------------------------
func partialUpstreamDaemonUnderTest(t *testing.T) *daemonHarness {
	t.Helper()

	originalSettings := settings.GITTICONFIGSETTINGS
	cfg := settings.GittiDefaultConfigSettings
	cfg.GitRemoteSyncStatusDurationMS = 3600000
	cfg.GitFilesActiveRefreshDurationMS = 3600000
	cfg.FileWatcherDebounceMS = 3600000
	settings.GITTICONFIGSETTINGS = &cfg
	t.Cleanup(func() { settings.GITTICONFIGSETTINGS = originalSettings })

	i18n.InitGittiLanguageMapping("en")

	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(partialUpstreamFakeGitScript), 0o755); err != nil {
		t.Fatalf("writing the fake git: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	originalExecutor := executor.GittiCmdExecutor
	t.Cleanup(func() { executor.GittiCmdExecutor = originalExecutor })
	executor.InitCmdExecutor(t.TempDir())

	gittiLogging := logging.InitGittiLogging(4096, make(chan string, 4096), 3)
	events := &eventCollector{}
	updateChannel := make(chan string, 4096)
	go events.run(updateChannel)

	gitOps := InitGitOperations(t.TempDir(), t.TempDir(), make(chan string, 64), gittiLogging)
	InitGitDaemon(t.TempDir(), updateChannel, gitOps, false, make(chan string, 16), gittiLogging)
	t.Cleanup(func() {
		harness := GITDAEMON
		harness.WaitStatePassesIdle(5 * time.Second)
		harness.Stop()
		harness.watcher.Close()
	})

	return &daemonHarness{gd: GITDAEMON, gitOps: gitOps, logging: gittiLogging, events: events}
}

// ------------------------------------
//
//	TestPartialKeyPostPushTicketRefreshesUnpublished proves a post-push
//	ticket on a partially configured branch reconciles cleanly: no failed
//	domains, and the remote/upstream state publishes unpublished with the
//	observed branch, cleared upstream and counts, and the default
//	no-upstream icon.
//
// ------------------------------------
func TestPartialKeyPostPushTicketRefreshesUnpublished(t *testing.T) {
	for _, tc := range []struct {
		name       string
		remoteMode string
		mergeMode  string
	}{
		{name: "remote-only", remoteMode: "", mergeMode: "unset"},
		{name: "merge-only", remoteMode: "unset", mergeMode: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := partialUpstreamDaemonUnderTest(t)
			t.Setenv("FAKE_REMOTE_MODE", tc.remoteMode)
			t.Setenv("FAKE_MERGE_MODE", tc.mergeMode)

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
		})
	}
}

// ------------------------------------
//
//	TestCompleteConfigProbeFailureNamesRemoteUpstreamDomain proves a genuine
//	complete-config probe failure keeps the push distinct from its refresh
//	failure: the ticket names the remote/upstream domain, the observation
//	publishes unavailable health, and the last-good upstream, icon, and
//	counts survive.
//
// ------------------------------------
func TestCompleteConfigProbeFailureNamesRemoteUpstreamDomain(t *testing.T) {
	h := partialUpstreamDaemonUnderTest(t)

	t.Setenv("FAKE_REVLIST_COUNT", "2 0")
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
	if snapshot.RemoteSyncStatus != (gitapi.RemoteSyncStatus{Local: "2", Remote: "0"}) {
		t.Errorf("the failed probe overwrote the last-good counts: %v", snapshot.RemoteSyncStatus)
	}
	if snapshot.UpStreamRemoteIcon != iconBefore {
		t.Error("the failed probe overwrote the last-good upstream icon")
	}
}
