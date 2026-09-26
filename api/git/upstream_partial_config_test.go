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
//	Helpers for the partial-upstream (phase 2) tests
//
// ------------------------------------

// ------------------------------------
//
//	partialRemoteUnderTest builds a remote handler scoped to the repository
//	root with its logging exposed for command-identity assertions
//
// ------------------------------------
func partialRemoteUnderTest(t *testing.T, root string) (*GitRemote, *logging.GittiLogging) {
	t.Helper()
	i18n.InitGittiLanguageMapping("en")
	gittiLogging := logging.InitGittiLogging(256, make(chan string, 256), 3)
	gr := InitGitRemote(make(chan string, 16), nil, executor.InitScopedCmdExecutor(root), gittiLogging)
	return gr, gittiLogging
}

// ------------------------------------
//
//	partialCommitUnderTest builds a commit (push) handler scoped to the
//	repository root with its logging exposed for refusal assertions
//
// ------------------------------------
func partialCommitUnderTest(t *testing.T, root string) (*GitCommit, *logging.GittiLogging) {
	t.Helper()
	i18n.InitGittiLanguageMapping("en")
	gittiLogging := logging.InitGittiLogging(256, make(chan string, 256), 3)
	gc := InitGitCommit(make(chan string, 16), InitGitProcessLock(gittiLogging), executor.InitScopedCmdExecutor(root), gittiLogging)
	return gc, gittiLogging
}

// ------------------------------------
//
//	trackedMasterFixture builds a real repository with a bare local origin
//	and a tracked master, the baseline every partial-config transition
//	starts from
//
// ------------------------------------
func trackedMasterFixture(t *testing.T) (string, func(gitArgs ...string)) {
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
//	assertUnpublishedSnapshot proves one published generation carries the
//	complete valid absent state: unpublished health, the observed branch,
//	and cleared upstream identity, icon (the no-upstream default), and
//	ahead/behind counts
//
// ------------------------------------
func assertUnpublishedSnapshot(t *testing.T, snapshot RemoteSyncUpstreamSnapshot, branch string) {
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
//	upstreamKeyWrapperScript answers `git config --get branch.<name>.remote`
//	and `.merge` from FAKE_REMOTE_MODE / FAKE_MERGE_MODE and defers every
//	other invocation to the real git. The "real" mode leaves the key to the
//	repository so the untouched key keeps its configured value.
//
// ------------------------------------
const upstreamKeyWrapperScript = `#!/bin/sh
want_config=0
for a in "$@"; do
  case "$a" in
    config) want_config=1 ;;
  esac
done
if [ "$want_config" = "1" ]; then
  want_get=0
  key=""
  for a in "$@"; do
    case "$a" in
      --get) want_get=1 ;;
      *.remote) key="remote" ;;
      *.merge) key="merge" ;;
    esac
  done
  if [ "$want_get" = "1" ] && [ -n "$key" ]; then
    if [ "$key" = "remote" ]; then
      mode="$FAKE_REMOTE_MODE"
      valid="origin"
    else
      mode="$FAKE_MERGE_MODE"
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
      *) printf 'unknown fake mode %%s\n' "$mode" >&2; exit 99 ;;
    esac
  fi
fi
exec %s "$@"
`

// ------------------------------------
//
//	upstreamCallLogWrapperScript records every git invocation's full command
//	line before deferring to the real git, so tests can prove which
//	upstream-domain commands ran
//
// ------------------------------------
const upstreamCallLogWrapperScript = `#!/bin/sh
echo "$*" >> "$UPSTREAM_CALL_LOG"
exec %s "$@"
`

// ------------------------------------
//
//	readUpstreamCallLog returns the recorded git command lines
//
// ------------------------------------
func readUpstreamCallLog(t *testing.T, logPath string) string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading the call log: %v", err)
	}
	return string(data)
}

// ------------------------------------
//
//	logNamesKey reports whether the logging recorded the failing upstream
//	config command for the given key after the given log length
//
// ------------------------------------
func logNamesKey(gittiLogging *logging.GittiLogging, before int, key string) bool {
	for _, entry := range gittiLogging.GetFullLogs()[before:] {
		if strings.Contains(entry.OpsCommand, "branch.master."+key) {
			return true
		}
	}
	return false
}

// ------------------------------------
//
//	TestPartialUpstreamConfigRefreshPublishesUnpublished proves a real
//	repository transition from tracked to each incomplete branch
//	configuration (remote-only, merge-only, neither key) refreshes as a
//	valid unpublished state: success with atomically cleared upstream
//	identity, no-upstream icon, and ahead/behind counts.
//
// ------------------------------------
func TestPartialUpstreamConfigRefreshPublishesUnpublished(t *testing.T) {
	root, run := trackedMasterFixture(t)
	gr, _ := partialRemoteUnderTest(t, root)

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
	assertUnpublishedSnapshot(t, gr.RemoteSyncStatusAndUpstream(), "master")

	// merge-only: the remote key alone is removed
	run("config", "--unset", "branch.master.remote")
	run("config", "branch.master.merge", "refs/heads/master")
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("merge-only refresh failed: %v, want unpublished success", err)
	}
	assertUnpublishedSnapshot(t, gr.RemoteSyncStatusAndUpstream(), "master")

	// neither key: no upstream configuration at all
	run("config", "--unset", "branch.master.merge")
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("neither-key refresh failed: %v, want unpublished success", err)
	}
	assertUnpublishedSnapshot(t, gr.RemoteSyncStatusAndUpstream(), "master")
}

// ------------------------------------
//
//	TestUpstreamConfigKeyValidationMatrix proves each upstream config key is
//	validated the same way: only the verified missing-key result (exit 1
//	with empty stdout and stderr) is absence, a trimmed single-line value
//	(including ".") is usable, and every other outcome (start failure
//	covered separately, exit 1 with stdout/stderr, other exits, zero-exit
//	stderr, whitespace-only/empty/multiline success) is a read error that
//	reports unavailable and keeps the last-good snapshot.
//
// ------------------------------------
func TestUpstreamConfigKeyValidationMatrix(t *testing.T) {
	for _, key := range []string{"remote", "merge"} {
		t.Run(key, func(t *testing.T) {
			root, _ := trackedMasterFixture(t)
			installGitWrapper(t, upstreamKeyWrapperScript, realGitPath(t))
			gr, gittiLogging := partialRemoteUnderTest(t, root)

			setModes := func(target, other string) {
				t.Helper()
				if key == "remote" {
					t.Setenv("FAKE_REMOTE_MODE", target)
					t.Setenv("FAKE_MERGE_MODE", other)
				} else {
					t.Setenv("FAKE_MERGE_MODE", target)
					t.Setenv("FAKE_REMOTE_MODE", other)
				}
			}

			// the untouched key stays real, so the baseline is the tracked
			// last-good snapshot every error case must retain
			setModes("value", "real")
			if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
				t.Fatalf("the baseline tracked-branch read failed: %v", err)
			}
			if got := gr.RemoteSyncStatusAndUpstream(); got.ObservationState != UpstreamStateTracked {
				t.Fatalf("baseline observation = %s, want tracked", got.ObservationState)
			}

			t.Run("usable single-line values", func(t *testing.T) {
				setModes("value", "real")
				if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
					t.Fatalf("%s value refresh failed: %v, want tracked success", key, err)
				}
				if got := gr.RemoteSyncStatusAndUpstream(); got.ObservationState != UpstreamStateTracked {
					t.Errorf("%s value observation = %s, want tracked", key, got.ObservationState)
				}
			})

			if key == "remote" {
				t.Run("dot remote is usable", func(t *testing.T) {
					setModes("dot", "real")
					if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
						t.Fatalf("dot remote refresh failed: %v, want tracked success", err)
					}
					got := gr.RemoteSyncStatusAndUpstream()
					if got.ObservationState != UpstreamStateTracked {
						t.Fatalf("dot remote observation = %s, want tracked", got.ObservationState)
					}
					if got.CurrentBranchUpStream != "master" {
						t.Errorf("dot remote upstream = %q, want the local master ref", got.CurrentBranchUpStream)
					}
				})
			}

			t.Run("verified absence is unpublished", func(t *testing.T) {
				setModes("absent", "real")
				if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
					t.Fatalf("%s absence refresh failed: %v, want unpublished success", key, err)
				}
				assertUnpublishedSnapshot(t, gr.RemoteSyncStatusAndUpstream(), "master")
			})

			for _, mode := range []string{"exit1-stdout", "exit1-stderr", "exit128", "exit2", "stderr-success", "whitespace", "empty", "multiline"} {
				t.Run(mode, func(t *testing.T) {
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
					if !logNamesKey(gittiLogging, before, key) {
						t.Errorf("%s mode %s did not identify the failing branch.master.%s command in the logs", key, mode, key)
					}
				})
			}
		})
	}
}

// ------------------------------------
//
//	TestUpstreamConfigStartFailureIsAnError proves a config read that cannot
//	start is a refresh error (unavailable), never a verified absence: with
//	no git on PATH the refresh fails instead of publishing unpublished.
//
// ------------------------------------
func TestUpstreamConfigStartFailureIsAnError(t *testing.T) {
	root, _ := trackedMasterFixture(t)
	gr, _ := partialRemoteUnderTest(t, root)

	t.Setenv("PATH", t.TempDir())
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
		t.Fatal("refresh with no git on PATH succeeded, want the start failure")
	}
	if got := gr.RemoteSyncStatusAndUpstream().ObservationState; got != UpstreamStateUnavailable {
		t.Errorf("start-failure observation = %s, want unavailable rather than a verified absence", got)
	}
}

// ------------------------------------
//
//	TestUpstreamConfigErrorWinsOverOtherKeyAbsence proves both keys are read
//	even when one is missing: a genuine read failure on either key reports
//	an error (identifying the failing command) even while the other key is
//	verifiably absent, and failures on both keys identify both commands.
//
// ------------------------------------
func TestUpstreamConfigErrorWinsOverOtherKeyAbsence(t *testing.T) {
	root, _ := trackedMasterFixture(t)
	installGitWrapper(t, upstreamKeyWrapperScript, realGitPath(t))
	gr, gittiLogging := partialRemoteUnderTest(t, root)

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
			t.Setenv("FAKE_REMOTE_MODE", tc.remoteMode)
			t.Setenv("FAKE_MERGE_MODE", tc.mergeMode)
			before := len(gittiLogging.GetFullLogs())
			if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
				t.Fatalf("%s refresh succeeded, want the config read error to win over the other key's absence", tc.name)
			}
			if got := gr.RemoteSyncStatusAndUpstream().ObservationState; got != UpstreamStateUnavailable {
				t.Errorf("%s observation = %s, want unavailable", tc.name, got)
			}
			for _, key := range tc.wantKeys {
				if !logNamesKey(gittiLogging, before, key) {
					t.Errorf("%s did not identify the failing branch.master.%s command in the logs", tc.name, key)
				}
			}
		})
	}
}

// ------------------------------------
//
//	TestCompleteConfigProbeFailuresKeepLastGood proves that with both keys
//	set, a failed upstream-ref probe and a failed count probe each report an
//	error, publish unavailable health, and retain the last-good snapshot
//	including the upstream icon and counts.
//
// ------------------------------------
func TestCompleteConfigProbeFailuresKeepLastGood(t *testing.T) {
	t.Run("failed upstream ref probe", func(t *testing.T) {
		root, run := trackedMasterFixture(t)
		gr, _ := partialRemoteUnderTest(t, root)

		if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
			t.Fatalf("the baseline tracked-branch read failed: %v", err)
		}
		iconBefore := gr.UpStreamRemoteIcon()

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
		root, run := trackedMasterFixture(t)
		run("remote", "set-url", "origin", "https://github.com/example/repo.git")
		gr, _ := partialRemoteUnderTest(t, root)

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

// ------------------------------------
//
//	TestPartialConfigSkipsUpstreamRefAndCountProbes proves neither the
//	upstream-ref resolution nor the ahead/behind counts run for valid
//	partial configuration: every partial variant reads both config keys and
//	issues no @{upstream} rev-parse and no rev-list.
//
// ------------------------------------
func TestPartialConfigSkipsUpstreamRefAndCountProbes(t *testing.T) {
	root, run := trackedMasterFixture(t)
	installGitWrapper(t, upstreamCallLogWrapperScript, realGitPath(t))
	callLog := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("UPSTREAM_CALL_LOG", callLog)
	gr, _ := partialRemoteUnderTest(t, root)

	// remote-only, then merge-only, then neither key
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
			assertUnpublishedSnapshot(t, gr.RemoteSyncStatusAndUpstream(), "master")
			logged := readUpstreamCallLog(t, callLog)
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

// ------------------------------------
//
//	TestPartialConfigUsesUnpublishedPushArguments proves remote-only,
//	merge-only, and neither-key branches keep the existing unpublished push
//	behavior without refusal: the publish intent builds the confirmed
//	publish argv and the push intent keeps the -u argv, through both the
//	background and the signing-required preparation.
//
// ------------------------------------
func TestPartialConfigUsesUnpublishedPushArguments(t *testing.T) {
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
			root, run := trackedMasterFixture(t)
			tc.setup(run)
			gc, _ := partialCommitUnderTest(t, root)

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

// ------------------------------------
//
//	TestRemoteOnlyPublishPushPublishesBranch is the end-to-end remote-only
//	publish: the confirmed route starts the existing unpublished push in
//	the generation's worktree and the bare remote receives the branch.
//
// ------------------------------------
func TestRemoteOnlyPublishPushPublishesBranch(t *testing.T) {
	root, run := trackedMasterFixture(t)
	run("checkout", "-q", "-b", "feature")
	run("commit", "--allow-empty", "-q", "-m", "feature work")
	run("config", "branch.feature.remote", "origin")

	gc, _ := partialCommitUnderTest(t, root)
	result := gc.GitPush(context.Background(), GitPushRoute{RemoteName: "origin", Branch: "feature", Intent: PushIntentPublish})

	if !result.Success() {
		t.Fatalf("the remote-only publish failed: exit %d, cancelled %v, err %v", result.ExitCode(), result.Cancelled(), result.Err())
	}
	wantTail := buildPublishGitArgs("origin")
	argv := result.Argv()
	if len(argv) < len(wantTail) || strings.Join(argv[len(argv)-len(wantTail):], " ") != strings.Join(wantTail, " ") {
		t.Fatalf("the publish argv tail = %v, want %v", argv, wantTail)
	}
	head := runGitRevParse(t, root, "HEAD")
	if got := runGitRevParse(t, root, "refs/remotes/origin/feature"); got != head {
		t.Fatalf("the remote-tracking ref = %s, want the published tip %s", got, head)
	}
}

// ------------------------------------
//
//	TestUpstreamConfigReadFailureRefusesPushBeforeStart proves a config-read
//	failure on either key refuses the push before any process starts: the
//	preparation errors and the background route returns an unstarted result
//	without launching a push process.
//
// ------------------------------------
func TestUpstreamConfigReadFailureRefusesPushBeforeStart(t *testing.T) {
	for _, tc := range []struct {
		name       string
		remoteMode string
		mergeMode  string
	}{
		{name: "remote key fails", remoteMode: "exit128", mergeMode: "real"},
		{name: "merge key fails", remoteMode: "real", mergeMode: "exit128"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := trackedMasterFixture(t)
			installGitWrapper(t, upstreamKeyWrapperScript, realGitPath(t))
			t.Setenv("FAKE_REMOTE_MODE", tc.remoteMode)
			t.Setenv("FAKE_MERGE_MODE", tc.mergeMode)
			gc, gittiLogging := partialCommitUnderTest(t, root)

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

// ------------------------------------
//
//	TestSuccessfulPushStaysDistinctFromRefreshFailure proves a successful
//	push and its later refresh failure stay distinct: the push records
//	Success while the complete-config probe failure refreshes as an
//	unavailable error that keeps the last-good snapshot.
//
// ------------------------------------
func TestSuccessfulPushStaysDistinctFromRefreshFailure(t *testing.T) {
	gitCommit, _, _, _, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"branch.master.remote"*) echo "origin"; exit 0 ;;
  *"branch.master.merge"*) echo "refs/heads/master"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse)
      if [ -n "$FAKE_UPSTREAM_RESOLVE_FAIL" ]; then
        echo "fatal: no upstream configured for branch 'master'" >&2
        exit 128
      fi
      echo "origin/master"
      exit 0
      ;;
    rev-list) echo "0 0"; exit 0 ;;
    push) echo "Everything up-to-date"; exit 0 ;;
  esac
done
exit 0
`)

	result := gitCommit.GitPush(context.Background(), pushRouteUnderTest("master"))
	if !result.Success() {
		t.Fatalf("the push did not succeed: exit %d, cancelled %v, err %v", result.ExitCode(), result.Cancelled(), result.Err())
	}

	gr := remoteSyncHandlerUnderTest(t)
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the baseline tracked-branch read failed: %v", err)
	}

	t.Setenv("FAKE_UPSTREAM_RESOLVE_FAIL", "1")
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

// ------------------------------------
//
//	TestTrackedDetachedUnbornPushBehaviorIntact pins the behavior phase 2
//	must not change: a tracked branch pushes with the tracked argv, and
//	detached and unborn heads refuse both intents without starting.
//
// ------------------------------------
func TestTrackedDetachedUnbornPushBehaviorIntact(t *testing.T) {
	root, run := trackedMasterFixture(t)
	gc, _ := partialCommitUnderTest(t, root)

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
