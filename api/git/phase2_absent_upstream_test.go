package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gohyuhan/gitti/executor"
	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/logging"
)

// ------------------------------------
//
//	phase2TrackedMaster builds a real repository with a bare local origin
//	and a tracked master, the baseline every absent-upstream transition
//	starts from
//
// ------------------------------------
func phase2TrackedMaster(t *testing.T) (string, func(gitArgs ...string)) {
	t.Helper()
	root, run := repositoryUnderTest(t)
	run("init", "--bare", "-q", "origin.git")
	run("remote", "add", "origin", "origin.git")
	run("commit", "--allow-empty", "-q", "-m", "base")
	run("push", "-q", "-u", "origin", "master")
	return root, run
}

// ------------------------------------
//
//	phase2RemoteUnderTest builds a remote handler scoped to the repository
//	root with its logging exposed for command-identity assertions
//
// ------------------------------------
func phase2RemoteUnderTest(t *testing.T, root string) (*GitRemote, *logging.GittiLogging) {
	t.Helper()
	i18n.InitGittiLanguageMapping("en")
	gittiLogging := logging.InitGittiLogging(256, make(chan string, 256), 3)
	return InitGitRemote(make(chan string, 16), nil, executor.InitScopedCmdExecutor(root), gittiLogging), gittiLogging
}

// ------------------------------------
//
//	phase2CommitUnderTest builds a commit (push) handler scoped to the
//	repository root with its logging exposed for refusal assertions
//
// ------------------------------------
func phase2CommitUnderTest(t *testing.T, root string) (*GitCommit, *logging.GittiLogging) {
	t.Helper()
	i18n.InitGittiLanguageMapping("en")
	gittiLogging := logging.InitGittiLogging(256, make(chan string, 256), 3)
	return InitGitCommit(make(chan string, 16), InitGitProcessLock(gittiLogging), executor.InitScopedCmdExecutor(root), gittiLogging), gittiLogging
}

// ------------------------------------
//
//	phase2AssertUnpublished proves one published generation carries the
//	complete valid absent state: unpublished health, the observed branch,
//	and cleared upstream identity, no-upstream icon, and ahead/behind
//	counts
//
// ------------------------------------
func phase2AssertUnpublished(t *testing.T, snapshot RemoteSyncUpstreamSnapshot, branch string) {
	t.Helper()
	if snapshot.ObservationState != UpstreamStateUnpublished {
		t.Errorf("observation state = %s, want unpublished", snapshot.ObservationState)
	}
	if snapshot.ObservedBranch != branch {
		t.Errorf("observed branch = %q, want %s", snapshot.ObservedBranch, branch)
	}
	if snapshot.CurrentBranchUpStream != "" {
		t.Errorf("the unpublished observation carries an upstream: %q", snapshot.CurrentBranchUpStream)
	}
	if snapshot.UpStreamRemoteIcon != DefaultUpStreamRemoteIcon {
		t.Errorf("the unpublished icon = %q, want the no-upstream default %q", snapshot.UpStreamRemoteIcon, DefaultUpStreamRemoteIcon)
	}
	if snapshot.RemoteSyncStatus != (RemoteSyncStatus{}) {
		t.Errorf("the unpublished observation carries counts: %v", snapshot.RemoteSyncStatus)
	}
}

// ------------------------------------
//
//	phase2LogNamesKey reports whether the logging recorded the failing
//	upstream config command for the given key after the given log length
//
// ------------------------------------
func phase2LogNamesKey(t *testing.T, gittiLogging *logging.GittiLogging, before int, key string) bool {
	t.Helper()
	for _, entry := range gittiLogging.GetFullLogs()[before:] {
		if strings.Contains(entry.OpsCommand, "branch.master."+key) {
			return true
		}
	}
	return false
}

// ------------------------------------
//
//	phase2ConfigWrapperScript answers `git config --get
//	branch.<name>.remote` and `.merge` from PHASE2_REMOTE_MODE /
//	PHASE2_MERGE_MODE and defers every other invocation to the real git.
//	The "real" mode leaves the key to the repository so the untouched key
//	keeps its configured value.
//
// ------------------------------------
const phase2ConfigWrapperScript = `#!/bin/sh
key=""
want_get=0
for a in "$@"; do
  case "$a" in
    --get) want_get=1 ;;
    *.remote) key="remote" ;;
    *.merge) key="merge" ;;
  esac
done
if [ "$want_get" = "1" ] && [ -n "$key" ]; then
  if [ "$key" = "remote" ]; then
    mode="$PHASE2_REMOTE_MODE"
    valid="origin"
  else
    mode="$PHASE2_MERGE_MODE"
    valid="refs/heads/master"
  fi
  case "$mode" in
    ""|real) ;;
    absent) exit 1 ;;
    value) printf '%%s\n' "$valid"; exit 0 ;;
    dot) printf '.\n'; exit 0 ;;
    exit1-stdout) printf 'stale-value\n'; exit 1 ;;
    exit1-stderr) printf 'fatal: unable to read config\n' >&2; exit 1 ;;
    exit128) printf 'fatal: unable to read config\n' >&2; exit 128 ;;
    exit2) exit 2 ;;
    stderr-success) printf '%%s\n' "$valid"; printf 'warning: dubious ownership\n' >&2; exit 0 ;;
    whitespace) printf '   \n'; exit 0 ;;
    empty) printf ''; exit 0 ;;
    multiline) printf '%%s\nextra-line\n' "$valid"; exit 0 ;;
    *) printf 'unknown phase2 mode %%s\n' "$mode" >&2; exit 99 ;;
  esac
fi
exec %s "$@"
`

// ------------------------------------
//
//	phase2CallLogWrapperScript records every git invocation's full command
//	line before deferring to the real git, so tests can prove which
//	upstream-domain commands ran
//
// ------------------------------------
const phase2CallLogWrapperScript = `#!/bin/sh
echo "$*" >> "$PHASE2_CALL_LOG"
exec %s "$@"
`

// ------------------------------------
//
//	phase2PushFakeGitScript stands in for git so a successful push and its
//	later refresh failure stay distinct without a network. It answers the
//	upstream observation as tracked until PHASE2_UPSTREAM_FAIL breaks the
//	configured-ref probe.
//
// ------------------------------------
const phase2PushFakeGitScript = `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    config)
      key=""
      for b in "$@"; do
        case "$b" in
          *.remote) key="remote" ;;
          *.merge) key="merge" ;;
        esac
      done
      if [ "$key" = "merge" ]; then
        echo "refs/heads/master"
      else
        echo "origin"
      fi
      exit 0
      ;;
    rev-parse)
      if [ -n "$PHASE2_UPSTREAM_FAIL" ]; then
        echo "fatal: no upstream configured for branch 'master'" >&2
        exit 128
      fi
      echo "origin/master"
      exit 0
      ;;
    rev-list) echo "0 0"; exit 0 ;;
    push) echo "Everything up-to-date"; exit 0 ;;
    remote) echo "https://github.com/example/repo.git"; exit 0 ;;
  esac
done
exit 0
`

func TestPhase2AbsentUpstreamRefreshClearsSnapshot(t *testing.T) {
	_, run := phase2TrackedMaster(t)
	gr := remoteSyncHandlerUnderTest(t)

	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the baseline tracked-branch read failed: %v", err)
	}
	if got := gr.RemoteSyncStatusAndUpstream(); got.ObservationState != UpstreamStateTracked {
		t.Fatalf("baseline observation = %s, want tracked", got.ObservationState)
	}

	// remote-only: the merge key alone is removed
	run("branch", "--unset-upstream")
	run("config", "branch.master.remote", "origin")
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("remote-only refresh failed: %v, want unpublished success", err)
	}
	phase2AssertUnpublished(t, gr.RemoteSyncStatusAndUpstream(), "master")

	// merge-only: the remote key alone is removed
	run("config", "--unset", "branch.master.remote")
	run("config", "branch.master.merge", "refs/heads/master")
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("merge-only refresh failed: %v, want unpublished success", err)
	}
	phase2AssertUnpublished(t, gr.RemoteSyncStatusAndUpstream(), "master")

	// neither key: no upstream configuration at all
	run("config", "--unset", "branch.master.merge")
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("neither-key refresh failed: %v, want unpublished success", err)
	}
	phase2AssertUnpublished(t, gr.RemoteSyncStatusAndUpstream(), "master")
}

func TestPhase2UpstreamConfigKeyOutcomes(t *testing.T) {
	for _, key := range []string{"remote", "merge"} {
		t.Run(key, func(t *testing.T) {
			root, _ := phase2TrackedMaster(t)
			installGitWrapper(t, phase2ConfigWrapperScript, realGitPath(t))
			gr, gittiLogging := phase2RemoteUnderTest(t, root)

			setModes := func(target, other string) {
				t.Helper()
				if key == "remote" {
					t.Setenv("PHASE2_REMOTE_MODE", target)
					t.Setenv("PHASE2_MERGE_MODE", other)
				} else {
					t.Setenv("PHASE2_MERGE_MODE", target)
					t.Setenv("PHASE2_REMOTE_MODE", other)
				}
			}

			// the untouched key stays real, so the re-established tracked
			// baseline is the latest published payload every error case
			// must retain
			setModes("value", "real")
			if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
				t.Fatalf("the baseline tracked-branch read failed: %v", err)
			}
			if got := gr.RemoteSyncStatusAndUpstream(); got.ObservationState != UpstreamStateTracked {
				t.Fatalf("baseline observation = %s, want tracked", got.ObservationState)
			}

			t.Run("usable single-line value", func(t *testing.T) {
				setModes("value", "real")
				if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
					t.Fatalf("%s value refresh failed: %v, want tracked success", key, err)
				}
				if got := gr.RemoteSyncStatusAndUpstream(); got.ObservationState != UpstreamStateTracked {
					t.Errorf("%s value observation = %s, want tracked", key, got.ObservationState)
				}
			})

			t.Run("dot value is usable", func(t *testing.T) {
				setModes("dot", "real")
				if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
					t.Fatalf("%s dot refresh failed: %v, want tracked success", key, err)
				}
				got := gr.RemoteSyncStatusAndUpstream()
				if got.ObservationState != UpstreamStateTracked {
					t.Fatalf("%s dot observation = %s, want tracked", key, got.ObservationState)
				}
				if key == "remote" && got.CurrentBranchUpStream != "master" {
					t.Errorf("dot remote upstream = %q, want the local master ref", got.CurrentBranchUpStream)
				}
			})

			t.Run("verified absence is unpublished", func(t *testing.T) {
				setModes("absent", "real")
				if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
					t.Fatalf("%s absence refresh failed: %v, want unpublished success", key, err)
				}
				phase2AssertUnpublished(t, gr.RemoteSyncStatusAndUpstream(), "master")
			})

			for _, mode := range []string{"exit1-stdout", "exit1-stderr", "exit128", "exit2", "stderr-success", "whitespace", "empty", "multiline"} {
				t.Run(mode, func(t *testing.T) {
					// re-establish the tracked baseline: the earlier absence
					// case published unpublished, and failures retain the
					// latest published payload
					setModes("value", "real")
					if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
						t.Fatalf("%s mode %s baseline refresh failed: %v, want tracked success", key, mode, err)
					}
					setModes(mode, "real")
					before := len(gittiLogging.GetFullLogs())
					if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
						t.Fatalf("%s mode %s refresh succeeded, want the config read error", key, mode)
					}
					got := gr.RemoteSyncStatusAndUpstream()
					if got.ObservationState != UpstreamStateUnavailable {
						t.Errorf("%s mode %s observation = %s, want unavailable", key, mode, got.ObservationState)
					}
					if got.CurrentBranchUpStream != "origin/master" {
						t.Errorf("%s mode %s overwrote the last-good upstream: %q", key, mode, got.CurrentBranchUpStream)
					}
					if got.RemoteSyncStatus != (RemoteSyncStatus{Local: "0", Remote: "0"}) {
						t.Errorf("%s mode %s overwrote the last-good counts: %v", key, mode, got.RemoteSyncStatus)
					}
					if !phase2LogNamesKey(t, gittiLogging, before, key) {
						t.Errorf("%s mode %s did not identify the failing branch.master.%s command in the logs", key, mode, key)
					}
				})
			}
		})
	}
}

func TestPhase2UpstreamConfigStartFailureIsUnavailable(t *testing.T) {
	root, _ := phase2TrackedMaster(t)
	gr, _ := phase2RemoteUnderTest(t, root)

	t.Setenv("PATH", t.TempDir())
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
		t.Fatal("refresh with no git on PATH succeeded, want the start failure")
	}
	if got := gr.RemoteSyncStatusAndUpstream().ObservationState; got != UpstreamStateUnavailable {
		t.Errorf("start-failure observation = %s, want unavailable rather than a verified absence", got)
	}
}

func TestPhase2UpstreamConfigErrorBeatsOtherKeyAbsence(t *testing.T) {
	root, _ := phase2TrackedMaster(t)
	installGitWrapper(t, phase2ConfigWrapperScript, realGitPath(t))
	gr, gittiLogging := phase2RemoteUnderTest(t, root)

	for _, tc := range []struct {
		name       string
		remoteMode string
		mergeMode  string
		wantKeys   []string
	}{
		{name: "merge failure with remote absent", remoteMode: "absent", mergeMode: "exit128", wantKeys: []string{"merge"}},
		{name: "remote failure with merge absent", remoteMode: "exit128", mergeMode: "absent", wantKeys: []string{"remote"}},
		{name: "both keys fail", remoteMode: "exit2", mergeMode: "exit128", wantKeys: []string{"remote", "merge"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PHASE2_REMOTE_MODE", tc.remoteMode)
			t.Setenv("PHASE2_MERGE_MODE", tc.mergeMode)
			before := len(gittiLogging.GetFullLogs())
			if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
				t.Fatalf("%s refresh succeeded, want the config read error to win over the other key's absence", tc.name)
			}
			if got := gr.RemoteSyncStatusAndUpstream().ObservationState; got != UpstreamStateUnavailable {
				t.Errorf("%s observation = %s, want unavailable", tc.name, got)
			}
			for _, key := range tc.wantKeys {
				if !phase2LogNamesKey(t, gittiLogging, before, key) {
					t.Errorf("%s did not identify the failing branch.master.%s command in the logs", tc.name, key)
				}
			}
		})
	}
}

func TestPhase2CompleteConfigFailuresRetainLastGood(t *testing.T) {
	t.Run("failed upstream ref probe", func(t *testing.T) {
		_, run := phase2TrackedMaster(t)
		run("remote", "set-url", "origin", "https://github.com/example/repo.git")
		gr := remoteSyncHandlerUnderTest(t)

		if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
			t.Fatalf("the baseline tracked-branch read failed: %v", err)
		}
		iconBefore := gr.UpStreamRemoteIcon()
		if iconBefore == DefaultUpStreamRemoteIcon {
			t.Fatal("the baseline icon is the generic glyph, want the github remote-kind glyph for a meaningful retention oracle")
		}

		run("config", "branch.master.merge", "refs/heads/does-not-exist")
		if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
			t.Fatal("the failed ref probe returned nil, want the resolution error")
		}
		snapshot := gr.RemoteSyncStatusAndUpstream()
		if snapshot.ObservationState != UpstreamStateUnavailable {
			t.Errorf("observation state = %s, want unavailable", snapshot.ObservationState)
		}
		if snapshot.CurrentBranchUpStream != "origin/master" {
			t.Errorf("the failed ref probe overwrote the last-good upstream: %q", snapshot.CurrentBranchUpStream)
		}
		if snapshot.RemoteSyncStatus != (RemoteSyncStatus{Local: "0", Remote: "0"}) {
			t.Errorf("the failed ref probe overwrote the last-good counts: %v", snapshot.RemoteSyncStatus)
		}
		if got := gr.UpStreamRemoteIcon(); got != iconBefore {
			t.Errorf("the failed ref probe overwrote the last-good icon: %q -> %q", iconBefore, got)
		}
	})

	t.Run("failed count probe", func(t *testing.T) {
		_, run := phase2TrackedMaster(t)
		run("remote", "set-url", "origin", "https://github.com/example/repo.git")
		gr := remoteSyncHandlerUnderTest(t)

		if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
			t.Fatalf("the baseline tracked-branch read failed: %v", err)
		}
		iconBefore := gr.UpStreamRemoteIcon()
		if iconBefore == DefaultUpStreamRemoteIcon {
			t.Fatal("the baseline icon is the generic glyph, want the github remote-kind glyph for a meaningful retention oracle")
		}

		// only the count probe fails from here on; both config keys stay set
		installGitWrapper(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-list)
      echo "fatal: ambiguous argument" >&2
      exit 128
      ;;
  esac
done
exec %s "$@"
`, realGitPath(t))

		if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
			t.Fatal("the failed count probe returned nil, want the rev-list error")
		}
		snapshot := gr.RemoteSyncStatusAndUpstream()
		if snapshot.ObservationState != UpstreamStateUnavailable {
			t.Errorf("observation state = %s, want unavailable", snapshot.ObservationState)
		}
		if snapshot.ObservedBranch != "master" {
			t.Errorf("the failed count probe overwrote the last-good observed branch: %q", snapshot.ObservedBranch)
		}
		if snapshot.CurrentBranchUpStream != "origin/master" {
			t.Errorf("the failed count probe overwrote the last-good upstream: %q", snapshot.CurrentBranchUpStream)
		}
		if snapshot.RemoteSyncStatus != (RemoteSyncStatus{Local: "0", Remote: "0"}) {
			t.Errorf("the failed count probe overwrote the last-good counts: %v", snapshot.RemoteSyncStatus)
		}
		if got := gr.UpStreamRemoteIcon(); got != iconBefore {
			t.Errorf("the failed count probe overwrote the last-good icon: %q -> %q", iconBefore, got)
		}
	})
}

func TestPhase2PartialConfigSkipsProbes(t *testing.T) {
	root, run := phase2TrackedMaster(t)
	installGitWrapper(t, phase2CallLogWrapperScript, realGitPath(t))
	callLog := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("PHASE2_CALL_LOG", callLog)
	gr, _ := phase2RemoteUnderTest(t, root)

	run("branch", "--unset-upstream")
	run("config", "branch.master.remote", "origin")
	transitions := []struct {
		name string
		next func()
	}{
		{name: "remote-only", next: func() {}},
		{name: "merge-only", next: func() {
			run("config", "--unset", "branch.master.remote")
			run("config", "branch.master.merge", "refs/heads/master")
		}},
		{name: "neither-key", next: func() {
			run("config", "--unset", "branch.master.merge")
		}},
	}
	for _, tc := range transitions {
		t.Run(tc.name, func(t *testing.T) {
			tc.next()
			if err := os.WriteFile(callLog, nil, 0o644); err != nil {
				t.Fatalf("clearing the call log: %v", err)
			}
			if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
				t.Fatalf("%s refresh failed: %v, want unpublished success", tc.name, err)
			}
			phase2AssertUnpublished(t, gr.RemoteSyncStatusAndUpstream(), "master")
			data, err := os.ReadFile(callLog)
			if err != nil {
				t.Fatalf("reading the call log: %v", err)
			}
			logged := string(data)
			if !strings.Contains(logged, "config --get branch.master.remote") {
				t.Errorf("%s never read the remote key:\n%s", tc.name, logged)
			}
			if !strings.Contains(logged, "config --get branch.master.merge") {
				t.Errorf("%s never read the merge key:\n%s", tc.name, logged)
			}
			if strings.Contains(logged, "@{upstream}") {
				t.Errorf("%s resolved the upstream ref for valid partial configuration:\n%s", tc.name, logged)
			}
			if strings.Contains(logged, "rev-list") {
				t.Errorf("%s counted ahead/behind for valid partial configuration:\n%s", tc.name, logged)
			}
		})
	}
}

func TestPhase2PartialConfigPushPreparation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(run func(gitArgs ...string))
	}{
		{name: "remote-only", setup: func(run func(gitArgs ...string)) {
			run("branch", "--unset-upstream")
			run("config", "branch.master.remote", "origin")
		}},
		{name: "merge-only", setup: func(run func(gitArgs ...string)) {
			run("branch", "--unset-upstream")
			run("config", "branch.master.merge", "refs/heads/master")
		}},
		{name: "neither-key", setup: func(run func(gitArgs ...string)) {
			run("branch", "--unset-upstream")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, run := phase2TrackedMaster(t)
			tc.setup(run)
			gc, _ := phase2CommitUnderTest(t, root)

			publishRoute := GitPushRoute{RemoteName: "origin", Branch: "master", Intent: PushIntentPublish}
			args, err := gc.prepareGitPush(publishRoute)
			if err != nil {
				t.Fatalf("%s publish preparation failed: %v, want the unpublished push arguments", tc.name, err)
			}
			wantPublish := buildPublishGitArgs("origin")
			if strings.Join(args, " ") != strings.Join(wantPublish, " ") {
				t.Errorf("%s publish argv = %v, want %v", tc.name, args, wantPublish)
			}
			if signingArgs, err := gc.GitPushWithSigning(publishRoute); err != nil || strings.Join(signingArgs, " ") != strings.Join(wantPublish, " ") {
				t.Errorf("%s signing publish argv = %v, err %v, want %v", tc.name, signingArgs, err, wantPublish)
			}

			pushRoute := GitPushRoute{RemoteName: "origin", PushType: PUSH, Branch: "master", Intent: PushIntentPush}
			pushArgs, err := gc.prepareGitPush(pushRoute)
			if err != nil {
				t.Fatalf("%s push preparation failed: %v, want the unpublished push arguments", tc.name, err)
			}
			wantPush := buildPushGitArgs("origin", PUSH, "master", false)
			if strings.Join(pushArgs, " ") != strings.Join(wantPush, " ") {
				t.Errorf("%s push argv = %v, want %v", tc.name, pushArgs, wantPush)
			}
		})
	}
}

func TestPhase2UpstreamConfigReadFailureRefusesPush(t *testing.T) {
	for _, tc := range []struct {
		name       string
		remoteMode string
		mergeMode  string
	}{
		{name: "remote key fails", remoteMode: "exit128", mergeMode: "real"},
		{name: "merge key fails", remoteMode: "real", mergeMode: "exit128"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := phase2TrackedMaster(t)
			installGitWrapper(t, phase2ConfigWrapperScript, realGitPath(t))
			t.Setenv("PHASE2_REMOTE_MODE", tc.remoteMode)
			t.Setenv("PHASE2_MERGE_MODE", tc.mergeMode)
			gc, gittiLogging := phase2CommitUnderTest(t, root)

			route := GitPushRoute{RemoteName: "origin", Branch: "master", Intent: PushIntentPublish}
			if _, err := gc.prepareGitPush(route); err == nil {
				t.Fatalf("%s preparation succeeded, want the config-read refusal", tc.name)
			}

			result := gc.GitPush(context.Background(), route)
			if result.Started() {
				t.Error("a config-read failure started a push process, want refusal before process start")
			}
			if result.ExitCode() != -1 {
				t.Errorf("ExitCode() = %d, want -1 for the absent process status", result.ExitCode())
			}
			if result.Err() == nil {
				t.Error("Err() = nil, want the refusal error so the popup can act on it")
			}
			if result.Success() {
				t.Error("a refused push must not be reported as a success")
			}
			var notStartedLogged bool
			for _, entry := range gittiLogging.GetFullLogs() {
				if entry.OpsSeverityLevel == logging.WARN && strings.Contains(entry.OpsDescription, "NOT STARTED") {
					notStartedLogged = true
				}
			}
			if !notStartedLogged {
				t.Error("a refusal before start left no NOT STARTED log entry")
			}
		})
	}
}

func TestPhase2SuccessfulPushSurvivesLaterRefreshFailure(t *testing.T) {
	gitCommit, _, _, _, _ := gitCommitUnderTestWithFakeGit(t, phase2PushFakeGitScript)

	result := gitCommit.GitPush(context.Background(), pushRouteUnderTest("master"))
	if !result.Success() {
		t.Fatalf("the push did not succeed: exit %d, cancelled %v, err %v", result.ExitCode(), result.Cancelled(), result.Err())
	}

	gr := remoteSyncHandlerUnderTest(t)
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the baseline tracked-branch read failed: %v", err)
	}

	t.Setenv("PHASE2_UPSTREAM_FAIL", "1")
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
		t.Fatal("the failed probe refresh returned nil, want the upstream resolution error")
	}
	if got := gr.RemoteSyncStatusAndUpstream().ObservationState; got != UpstreamStateUnavailable {
		t.Errorf("the failed probe observation = %s, want unavailable", got)
	}
	if got := gr.CurrentBranchUpStream(); got != "origin/master" {
		t.Errorf("the failed probe overwrote the last-good upstream: %q", got)
	}
	if !result.Success() {
		t.Error("the later refresh failure relabelled the successful push")
	}
}

func TestPhase2TrackedDetachedUnbornPushIntact(t *testing.T) {
	root, run := phase2TrackedMaster(t)
	gc, _ := phase2CommitUnderTest(t, root)

	args, err := gc.prepareGitPush(GitPushRoute{RemoteName: "origin", PushType: PUSH, Branch: "master", Intent: PushIntentPush})
	if err != nil {
		t.Fatalf("the tracked push preparation failed: %v", err)
	}
	wantTracked := buildPushGitArgs("origin", PUSH, "master", true)
	if strings.Join(args, " ") != strings.Join(wantTracked, " ") {
		t.Errorf("tracked push argv = %v, want %v", args, wantTracked)
	}

	run("checkout", "-q", "--detach")
	if _, err := gc.prepareGitPush(GitPushRoute{RemoteName: "origin", Branch: "", Intent: PushIntentPublish}); err == nil || !strings.Contains(err.Error(), "no publishable branch") {
		t.Errorf("a detached head was not refused for publish: %v", err)
	}
	if _, err := gc.prepareGitPush(GitPushRoute{RemoteName: "origin", PushType: PUSH, Branch: "", Intent: PushIntentPush}); err == nil || !strings.Contains(err.Error(), "no pushable branch") {
		t.Errorf("a detached head was not refused for push: %v", err)
	}

	run("checkout", "-q", "master")
	run("checkout", "-q", "--orphan", "unborn")
	if _, err := gc.prepareGitPush(GitPushRoute{RemoteName: "origin", Branch: "unborn", Intent: PushIntentPublish}); err == nil || !strings.Contains(err.Error(), "no publishable branch") {
		t.Errorf("an unborn branch was not refused for publish: %v", err)
	}
	if _, err := gc.prepareGitPush(GitPushRoute{RemoteName: "origin", PushType: PUSH, Branch: "unborn", Intent: PushIntentPush}); err == nil || !strings.Contains(err.Error(), "no pushable branch") {
		t.Errorf("an unborn branch was not refused for push: %v", err)
	}
}
