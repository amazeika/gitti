package git

// Phase 2 repair regressions: F09 retains the latest published payload on
// failed reads, F19 resolves local-dot tracking through Git's first merge
// ref, and F10 keys per-key config start failures. The must-remain-green
// behaviors stay pinned by phase2_absent_upstream_test.go,
// upstream_partial_config_test.go, and api/daemon_partial_upstream_test.go.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gohyuhan/gitti/executor"
	"github.com/gohyuhan/gitti/logging"
)

// ------------------------------------
//
//	p2rGitOutput runs one real git read in dir with the developer's own git
//	config isolated out, returning trimmed stdout so tests can oracle
//	published identity and counts against Git itself rather than the code
//
// ------------------------------------
func p2rGitOutput(t *testing.T, dir string, gitArgs ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", gitArgs...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// ------------------------------------
//
//	p2rAssertSnapshotEquals proves one published generation carries exactly
//	the retained payload: health, observed branch, upstream identity, icon,
//	and ahead/behind counts
//
// ------------------------------------
func p2rAssertSnapshotEquals(t *testing.T, what string, got, want RemoteSyncUpstreamSnapshot) {
	t.Helper()
	if got != want {
		t.Errorf("%s snapshot = %+v, want %+v", what, got, want)
	}
}

// ------------------------------------
//
//	p2rConfigFailWrapperScript breaks one upstream config key on demand
//	with an exit-code failure while every other invocation defers to the
//	real git. P2R_FAIL_REMOTE and P2R_FAIL_MERGE select the failing key;
//	the other key keeps its repository value so absence-versus-error
//	precedence is observable. Genuine command-start failures use the
//	upstreamConfigCmd seam instead: a shell exit status is an
//	exec.ExitError, never a start error.
//
// ------------------------------------
const p2rConfigFailWrapperScript = `#!/bin/sh
case "$*" in
  *branch.master.remote*)
    case "$P2R_FAIL_REMOTE" in
      1)
        echo "fatal: unable to read config" >&2
        exit 128
        ;;
    esac
    ;;
esac
case "$*" in
  *branch.master.merge*)
    case "$P2R_FAIL_MERGE" in
      1)
        echo "fatal: unable to read config" >&2
        exit 128
        ;;
    esac
    ;;
esac
exec %s "$@"
`

// ------------------------------------
//
//	p2rRevListFailWrapperScript breaks the ahead/behind count probe while
//	every other invocation defers to the real git, so a failure after a
//	valid local-dot publication exercises retention without touching config
//
// ------------------------------------
const p2rRevListFailWrapperScript = `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-list)
      echo "fatal: ambiguous argument" >&2
      exit 128
      ;;
  esac
done
exec %s "$@"
`

func TestPhase2RepairFailureRetainsUnpublishedSnapshot(t *testing.T) {
	root, run := trackedMasterFixture(t)
	run("remote", "set-url", "origin", "https://github.com/example/repo.git")
	gr, _ := partialRemoteUnderTest(t, root)

	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the baseline tracked-branch read failed: %v", err)
	}
	if got := gr.RemoteSyncStatusAndUpstream(); got.ObservationState != UpstreamStateTracked {
		t.Fatalf("baseline observation = %s, want tracked", got.ObservationState)
	}

	// publish the valid absence: no keys, cleared payload
	run("branch", "--unset-upstream")
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the unpublished refresh failed: %v, want unpublished success", err)
	}
	unpublished := RemoteSyncUpstreamSnapshot{
		ObservationState:   UpstreamStateUnpublished,
		ObservedBranch:     "master",
		UpStreamRemoteIcon: DefaultUpStreamRemoteIcon,
	}
	p2rAssertSnapshotEquals(t, "unpublished", gr.RemoteSyncStatusAndUpstream(), unpublished)

	// break only the merge key: the error must win over the remote key's
	// genuine absence and retain the unpublished payload, not the older
	// tracked remote snapshot
	installGitWrapper(t, p2rConfigFailWrapperScript, realGitPath(t))
	t.Setenv("P2R_FAIL_MERGE", "1")
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
		t.Fatal("the failed config refresh returned nil, want the read error")
	}
	got := gr.RemoteSyncStatusAndUpstream()
	if got.ObservationState != UpstreamStateUnavailable {
		t.Errorf("failed-read observation = %s, want unavailable", got.ObservationState)
	}
	if got.CurrentBranchUpStream != "" {
		t.Errorf("the failed read resurrected the older remote upstream %q, want the retained unpublished absence", got.CurrentBranchUpStream)
	}
	p2rAssertSnapshotEquals(t, "failed-read retained", RemoteSyncUpstreamSnapshot{
		ObservationState:      got.ObservationState,
		ObservedBranch:        got.ObservedBranch,
		CurrentBranchUpStream: got.CurrentBranchUpStream,
		UpStreamRemoteIcon:    got.UpStreamRemoteIcon,
		RemoteSyncStatus:      got.RemoteSyncStatus,
	}, RemoteSyncUpstreamSnapshot{
		ObservationState:   UpstreamStateUnavailable,
		ObservedBranch:     "master",
		UpStreamRemoteIcon: DefaultUpStreamRemoteIcon,
	})

	// a repeated failure keeps that same retained payload
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
		t.Fatal("the repeated failed refresh returned nil, want the read error")
	}
	repeated := gr.RemoteSyncStatusAndUpstream()
	if repeated.CurrentBranchUpStream != "" {
		t.Errorf("the repeated failure resurrected the older remote upstream %q, want the retained unpublished absence", repeated.CurrentBranchUpStream)
	}
	if repeated != got {
		t.Errorf("the repeated failure moved the snapshot from %+v to %+v, want it retained", got, repeated)
	}
}

func TestPhase2RepairFailureRetainsLocalDotSnapshot(t *testing.T) {
	root, run := trackedMasterFixture(t)
	run("remote", "set-url", "origin", "https://github.com/example/repo.git")
	run("branch", "target")
	run("config", "branch.master.remote", ".")
	run("config", "branch.master.merge", "refs/heads/target")
	gr, _ := partialRemoteUnderTest(t, root)

	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the baseline local-dot read failed: %v", err)
	}
	localDot := gr.RemoteSyncStatusAndUpstream()
	if localDot.ObservationState != UpstreamStateTracked {
		t.Fatalf("local-dot observation = %s, want tracked", localDot.ObservationState)
	}
	if localDot.CurrentBranchUpStream != "target" {
		t.Fatalf("local-dot upstream = %q, want the local target ref", localDot.CurrentBranchUpStream)
	}

	// break only the count probe: the read must report unavailable and
	// retain the local-dot payload rather than the older remote snapshot
	installGitWrapper(t, p2rRevListFailWrapperScript, realGitPath(t))
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
		t.Fatal("the failed count refresh returned nil, want the rev-list error")
	}
	got := gr.RemoteSyncStatusAndUpstream()
	if got.ObservationState != UpstreamStateUnavailable {
		t.Errorf("failed-read observation = %s, want unavailable", got.ObservationState)
	}
	if got.CurrentBranchUpStream != "target" {
		t.Errorf("the failed read resurrected the older remote upstream %q, want the retained local-dot upstream %q", got.CurrentBranchUpStream, "target")
	}
	p2rAssertSnapshotEquals(t, "failed-read retained", RemoteSyncUpstreamSnapshot{
		ObservationState:      got.ObservationState,
		ObservedBranch:        got.ObservedBranch,
		CurrentBranchUpStream: got.CurrentBranchUpStream,
		UpStreamRemoteIcon:    got.UpStreamRemoteIcon,
		RemoteSyncStatus:      got.RemoteSyncStatus,
	}, RemoteSyncUpstreamSnapshot{
		ObservationState:      UpstreamStateUnavailable,
		ObservedBranch:        "master",
		CurrentBranchUpStream: "target",
		UpStreamRemoteIcon:    localDot.UpStreamRemoteIcon,
		RemoteSyncStatus:      localDot.RemoteSyncStatus,
	})

	// a repeated failure keeps that same retained payload
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
		t.Fatal("the repeated failed refresh returned nil, want the rev-list error")
	}
	if repeated := gr.RemoteSyncStatusAndUpstream(); repeated != got {
		t.Errorf("the repeated failure moved the snapshot from %+v to %+v, want it retained", got, repeated)
	}
}

func TestPhase2RepairStaleGuardLeavesSuccessfulSnapshotUnchanged(t *testing.T) {
	root, run := trackedMasterFixture(t)
	gr, _ := partialRemoteUnderTest(t, root)

	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the baseline tracked-branch read failed: %v", err)
	}
	baseline := gr.RemoteSyncStatusAndUpstream()

	staleErr := errors.New("stale worktree generation")
	guard := func() error { return staleErr }

	// a read that would fail still publishes nothing under a stale guard
	run("config", "branch.master.merge", "refs/heads/does-not-exist")
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(guard); err != staleErr {
		t.Fatalf("guarded failing read error = %v, want the guard's error", err)
	}
	p2rAssertSnapshotEquals(t, "stale failing read", gr.RemoteSyncStatusAndUpstream(), baseline)

	// a read that would succeed publishes nothing either
	run("branch", "--unset-upstream")
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(guard); err != staleErr {
		t.Fatalf("guarded succeeding read error = %v, want the guard's error", err)
	}
	p2rAssertSnapshotEquals(t, "stale succeeding read", gr.RemoteSyncStatusAndUpstream(), baseline)
}

func TestPhase2RepairSingleMergeLocalDotMatchesGitUpstream(t *testing.T) {
	root, run := trackedMasterFixture(t)
	run("branch", "target")
	run("config", "branch.master.remote", ".")
	run("config", "branch.master.merge", "refs/heads/target")
	gr, _ := partialRemoteUnderTest(t, root)

	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the local-dot read failed: %v", err)
	}
	got := gr.RemoteSyncStatusAndUpstream()

	oracleUpstream, err := p2rGitOutput(t, root, "rev-parse", "--abbrev-ref", "master@{upstream}")
	if err != nil {
		t.Fatalf("the Git upstream oracle failed: %v", err)
	}
	if got.CurrentBranchUpStream != oracleUpstream {
		t.Errorf("local-dot upstream = %q, want Git's captured-branch upstream %q", got.CurrentBranchUpStream, oracleUpstream)
	}
	oracleCounts, err := p2rGitOutput(t, root, "rev-list", "--left-right", "--count", "master...master@{upstream}")
	if err != nil {
		t.Fatalf("the Git counts oracle failed: %v", err)
	}
	parts := strings.Fields(oracleCounts)
	if len(parts) != 2 || got.RemoteSyncStatus != (RemoteSyncStatus{Local: parts[0], Remote: parts[1]}) {
		t.Errorf("local-dot counts = %v, want Git's captured-branch counts %q", got.RemoteSyncStatus, oracleCounts)
	}
	if got.ObservationState != UpstreamStateTracked || got.ObservedBranch != "master" {
		t.Errorf("local-dot observation = %s/%s, want tracked/master", got.ObservationState, got.ObservedBranch)
	}
}

func TestPhase2RepairFirstMergeValueWinsOverLastConfigValue(t *testing.T) {
	root, run := trackedMasterFixture(t)
	run("branch", "targetA")
	run("branch", "targetB")
	run("checkout", "-q", "targetA")
	run("commit", "--allow-empty", "-q", "-m", "A1")
	run("checkout", "-q", "targetB")
	run("commit", "--allow-empty", "-q", "-m", "B1")
	run("commit", "--allow-empty", "-q", "-m", "B2")
	run("checkout", "-q", "master")
	run("config", "branch.master.remote", ".")
	run("config", "--unset-all", "branch.master.merge")
	run("config", "--add", "branch.master.merge", "refs/heads/targetA")
	run("config", "--add", "branch.master.merge", "refs/heads/targetB")

	// the trap input is armed: config --get reports the last value while
	// Git's captured-branch upstream must resolve the first
	lastMerge, err := p2rGitOutput(t, root, "config", "--get", "branch.master.merge")
	if err != nil {
		t.Fatalf("the config trap oracle failed: %v", err)
	}
	if lastMerge != "refs/heads/targetB" {
		t.Fatalf("test setup: config --get merge = %q, want the last value refs/heads/targetB", lastMerge)
	}
	oracleUpstream, err := p2rGitOutput(t, root, "rev-parse", "--abbrev-ref", "master@{upstream}")
	if err != nil {
		t.Fatalf("the Git upstream oracle failed: %v", err)
	}
	if oracleUpstream != "targetA" {
		t.Fatalf("test setup: Git resolved master@{upstream} to %q, want the first merge value targetA", oracleUpstream)
	}

	gr, _ := partialRemoteUnderTest(t, root)
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the multi-merge local-dot read failed: %v", err)
	}
	got := gr.RemoteSyncStatusAndUpstream()
	if got.ObservationState != UpstreamStateTracked {
		t.Fatalf("multi-merge observation = %s, want tracked", got.ObservationState)
	}
	if got.CurrentBranchUpStream != oracleUpstream {
		t.Errorf("multi-merge upstream = %q, want Git's first-ref upstream %q rather than the last config value", got.CurrentBranchUpStream, oracleUpstream)
	}
	oracleCounts, err := p2rGitOutput(t, root, "rev-list", "--left-right", "--count", "master...master@{upstream}")
	if err != nil {
		t.Fatalf("the Git counts oracle failed: %v", err)
	}
	parts := strings.Fields(oracleCounts)
	if len(parts) != 2 || got.RemoteSyncStatus != (RemoteSyncStatus{Local: parts[0], Remote: parts[1]}) {
		t.Errorf("multi-merge counts = %v, want Git's first-ref counts %q", got.RemoteSyncStatus, oracleCounts)
	}
}

func TestPhase2RepairUnresolvableFirstMergeRefusesPushBeforeStart(t *testing.T) {
	root, run := trackedMasterFixture(t)
	run("branch", "targetB")
	run("commit", "--allow-empty", "-q", "-m", "B1")
	gr, _ := partialRemoteUnderTest(t, root)

	// the latest successful payload the failure must retain
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the baseline tracked-branch read failed: %v", err)
	}
	retained := gr.RemoteSyncStatusAndUpstream()
	if retained.ObservationState != UpstreamStateTracked {
		t.Fatalf("baseline observation = %s, want tracked", retained.ObservationState)
	}

	// first merge ref is missing while the later one resolves: Git itself
	// cannot resolve the captured-branch upstream
	run("config", "branch.master.remote", ".")
	run("config", "--unset-all", "branch.master.merge")
	run("config", "--add", "branch.master.merge", "refs/heads/does-not-exist")
	run("config", "--add", "branch.master.merge", "refs/heads/targetB")
	if _, err := p2rGitOutput(t, root, "rev-parse", "--abbrev-ref", "master@{upstream}"); err == nil {
		t.Fatal("test setup: Git resolved master@{upstream} with a missing first merge ref, want the resolution failure")
	}

	argvLog := filepath.Join(t.TempDir(), "push-argv.log")
	installGitWrapper(t, pushArgvCaptureWrapper, realGitPath(t))
	t.Setenv("PUSH_ARGV_LOG", argvLog)

	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
		t.Fatal("the unresolvable-first read returned nil, want the resolution error")
	}
	got := gr.RemoteSyncStatusAndUpstream()
	if got.ObservationState != UpstreamStateUnavailable {
		t.Errorf("unresolvable-first observation = %s, want unavailable rather than an invented tracked ref", got.ObservationState)
	}
	if got.CurrentBranchUpStream == "targetB" {
		t.Errorf("the read published the later merge value %q as the tracked upstream, want no invented tracked ref", got.CurrentBranchUpStream)
	}
	p2rAssertSnapshotEquals(t, "unresolvable-first retained", RemoteSyncUpstreamSnapshot{
		ObservationState:      got.ObservationState,
		ObservedBranch:        got.ObservedBranch,
		CurrentBranchUpStream: got.CurrentBranchUpStream,
		UpStreamRemoteIcon:    got.UpStreamRemoteIcon,
		RemoteSyncStatus:      got.RemoteSyncStatus,
	}, RemoteSyncUpstreamSnapshot{
		ObservationState:      UpstreamStateUnavailable,
		ObservedBranch:        retained.ObservedBranch,
		CurrentBranchUpStream: retained.CurrentBranchUpStream,
		UpStreamRemoteIcon:    retained.UpStreamRemoteIcon,
		RemoteSyncStatus:      retained.RemoteSyncStatus,
	})

	// push preparation refuses before starting any push process
	gc, gittiLogging := partialCommitUnderTest(t, root)
	route := GitPushRoute{RemoteName: "origin", Branch: "master", Intent: PushIntentPublish}
	if _, err := gc.prepareGitPush(route); err == nil {
		t.Fatal("unresolvable-first push preparation succeeded, want the re-read refusal")
	}
	result := gc.GitPush(context.Background(), route)
	if result.Started() {
		t.Error("the unresolvable-first push started a process, want refusal before process start")
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
	if invocations := readArgvInvocations(t, argvLog); len(invocations) != 0 {
		t.Errorf("the refused push recorded %d push invocations, want none before start", len(invocations))
	}
}

func TestPhase2RepairPerKeyConfigStartFailureIsKeyedError(t *testing.T) {
	root, _ := trackedMasterFixture(t)
	t.Setenv("PATH", t.TempDir())
	cmdExecutor := executor.InitScopedCmdExecutor(root)

	// each key's config command is unstartable on its own: the read is a
	// keyed error, never a verified absence
	for _, key := range []string{"remote", "merge"} {
		value, absent, err := readUpstreamConfigKey(cmdExecutor, "master", key)
		if err == nil {
			t.Fatalf("%s config start succeeded without git on PATH, want the start failure", key)
		}
		if absent {
			t.Errorf("%s config start failure reported absence, want the read error", key)
		}
		if value != "" {
			t.Errorf("%s config start failure returned value %q, want empty", key, value)
		}
		if !strings.Contains(err.Error(), "branch.master."+key) {
			t.Errorf("%s config start error = %v, want it to identify the failing branch.master.%s command", key, err, key)
		}
	}
}

func TestPhase2RepairStartFailureBeatsPartialAbsence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		setup      func(run func(gitArgs ...string))
		missingKey string
	}{
		{name: "remote key missing", setup: func(run func(gitArgs ...string)) {
			run("branch", "--unset-upstream")
			run("config", "branch.master.merge", "refs/heads/master")
		}, missingKey: "remote"},
		{name: "merge key missing", setup: func(run func(gitArgs ...string)) {
			run("branch", "--unset-upstream")
			run("config", "branch.master.remote", "origin")
		}, missingKey: "merge"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, run := trackedMasterFixture(t)
			gr, _ := partialRemoteUnderTest(t, root)

			if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
				t.Fatalf("the baseline tracked-branch read failed: %v", err)
			}
			retained := gr.RemoteSyncStatusAndUpstream()

			// the other key is genuinely absent, yet the unstartable git
			// binary is a read error rather than an absence: with no git
			// on PATH the refresh fails before config selection, so key
			// identity is proven at the readUpstreamConfigKey layer above
			// while this refresh proves error-beats-absence with retention
			tc.setup(run)
			t.Setenv("PATH", t.TempDir())
			if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
				t.Fatalf("refresh with no git on PATH and the %s key %s succeeded, want the start failure", tc.missingKey, "missing")
			}
			got := gr.RemoteSyncStatusAndUpstream()
			if got.ObservationState != UpstreamStateUnavailable {
				t.Errorf("start-failure observation = %s, want unavailable rather than a verified absence", got.ObservationState)
			}
			p2rAssertSnapshotEquals(t, "start-failure retained", RemoteSyncUpstreamSnapshot{
				ObservationState:      got.ObservationState,
				ObservedBranch:        got.ObservedBranch,
				CurrentBranchUpStream: got.CurrentBranchUpStream,
				UpStreamRemoteIcon:    got.UpStreamRemoteIcon,
				RemoteSyncStatus:      got.RemoteSyncStatus,
			}, RemoteSyncUpstreamSnapshot{
				ObservationState:      UpstreamStateUnavailable,
				ObservedBranch:        retained.ObservedBranch,
				CurrentBranchUpStream: retained.CurrentBranchUpStream,
				UpStreamRemoteIcon:    retained.UpStreamRemoteIcon,
				RemoteSyncStatus:      retained.RemoteSyncStatus,
			})
		})
	}
}

func TestPhase2RepairPerKeyReadErrorBeatsAbsenceAndRetainsSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failEnv   string
		absentKey string
		setup     func(run func(gitArgs ...string))
	}{
		{name: "remote read fails with merge absent", failEnv: "P2R_FAIL_REMOTE", absentKey: "merge", setup: func(run func(gitArgs ...string)) {
			run("branch", "--unset-upstream")
		}},
		{name: "merge read fails with remote absent", failEnv: "P2R_FAIL_MERGE", absentKey: "remote", setup: func(run func(gitArgs ...string)) {
			run("branch", "--unset-upstream")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, run := trackedMasterFixture(t)
			installGitWrapper(t, p2rConfigFailWrapperScript, realGitPath(t))
			gr, gittiLogging := partialRemoteUnderTest(t, root)

			if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
				t.Fatalf("the baseline tracked-branch read failed: %v", err)
			}
			retained := gr.RemoteSyncStatusAndUpstream()
			if retained.ObservationState != UpstreamStateTracked {
				t.Fatalf("baseline observation = %s, want tracked", retained.ObservationState)
			}

			// the other key is genuinely absent while the targeted key
			// fails: the error wins over the absence
			tc.setup(run)
			t.Setenv(tc.failEnv, "1")
			failingKey := strings.TrimPrefix(tc.failEnv, "P2R_FAIL_")
			failingKey = strings.ToLower(failingKey)
			before := len(gittiLogging.GetFullLogs())
			refreshErr := gr.GetLatestRemoteSyncStatusAndUpstream(nil)
			if refreshErr == nil {
				t.Fatalf("%s refresh succeeded, want the config read error to win over the absence", tc.name)
			}
			if !strings.Contains(refreshErr.Error(), "branch.master."+failingKey) {
				t.Errorf("%s error = %v, want it to identify the failing branch.master.%s command", tc.name, refreshErr, failingKey)
			}
			got := gr.RemoteSyncStatusAndUpstream()
			if got.ObservationState != UpstreamStateUnavailable {
				t.Errorf("%s observation = %s, want unavailable", tc.name, got.ObservationState)
			}
			p2rAssertSnapshotEquals(t, "error-beats-absence retained", RemoteSyncUpstreamSnapshot{
				ObservationState:      got.ObservationState,
				ObservedBranch:        got.ObservedBranch,
				CurrentBranchUpStream: got.CurrentBranchUpStream,
				UpStreamRemoteIcon:    got.UpStreamRemoteIcon,
				RemoteSyncStatus:      got.RemoteSyncStatus,
			}, RemoteSyncUpstreamSnapshot{
				ObservationState:      UpstreamStateUnavailable,
				ObservedBranch:        retained.ObservedBranch,
				CurrentBranchUpStream: retained.CurrentBranchUpStream,
				UpStreamRemoteIcon:    retained.UpStreamRemoteIcon,
				RemoteSyncStatus:      retained.RemoteSyncStatus,
			})
			namesFailingKey := false
			for _, entry := range gittiLogging.GetFullLogs()[before:] {
				if strings.Contains(entry.OpsCommand, "branch.master."+failingKey) {
					namesFailingKey = true
				}
			}
			if !namesFailingKey {
				t.Errorf("%s did not identify the failing branch.master.%s command in the logs", tc.name, failingKey)
			}
			// a repeated failure keeps that same retained tracked payload
			if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
				t.Fatalf("%s repeated refresh succeeded, want the config read error again", tc.name)
			}
			if repeated := gr.RemoteSyncStatusAndUpstream(); repeated != got {
				t.Errorf("%s repeated failure moved the snapshot from %+v to %+v, want it retained", tc.name, got, repeated)
			}
		})
	}
}

func TestPhase2RepairPerKeyUnstartableBeatsAbsenceAndRetainsSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failingKey string
		otherKey   string
	}{
		{name: "remote command unstartable with merge absent", failingKey: "remote", otherKey: "merge"},
		{name: "merge command unstartable with remote absent", failingKey: "merge", otherKey: "remote"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, run := trackedMasterFixture(t)
			gr, gittiLogging := partialRemoteUnderTest(t, root)

			if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
				t.Fatalf("the baseline tracked-branch read failed: %v", err)
			}
			retained := gr.RemoteSyncStatusAndUpstream()
			if retained.ObservationState != UpstreamStateTracked {
				t.Fatalf("baseline observation = %s, want tracked", retained.ObservationState)
			}

			// both keys are genuinely absent from here on; the real git
			// proves each absence while the seam below makes only the
			// targeted key's command fail to start
			run("branch", "--unset-upstream")
			if _, err := p2rGitOutput(t, root, "config", "--get", "branch.master."+tc.otherKey); err == nil {
				t.Fatalf("test setup: branch.master.%s resolves, want the verified absence", tc.otherKey)
			}
			if _, err := p2rGitOutput(t, root, "config", "--get", "branch.master."+tc.failingKey); err == nil {
				t.Fatalf("test setup: branch.master.%s resolves, want the verified absence", tc.failingKey)
			}

			// the seam fails only the targeted key with a genuine
			// process-start error; a nil return keeps the other key on
			// the generation-bound executor so its absence is read for
			// real and the HEAD probe stays untouched
			missingExe := filepath.Join(t.TempDir(), "unstartable-git")
			var sawFailing bool
			var calls int
			gr.upstreamConfigCmd = func(gitArgs []string) *exec.Cmd {
				calls++
				if strings.Contains(strings.Join(gitArgs, " "), "branch.master."+tc.failingKey) {
					sawFailing = true
					return exec.Command(missingExe)
				}
				return nil
			}
			before := len(gittiLogging.GetFullLogs())
			refreshErr := gr.GetLatestRemoteSyncStatusAndUpstream(nil)
			if refreshErr == nil {
				t.Fatalf("%s refresh succeeded, want the unstartable-command error to win over the absence", tc.name)
			}
			if calls != 2 {
				t.Errorf("%s read %d config commands, want both keys read so the error wins over the absence", tc.name, calls)
			}
			if !sawFailing {
				t.Errorf("%s seam never selected the failing branch.master.%s command", tc.name, tc.failingKey)
			}
			if !strings.Contains(refreshErr.Error(), "branch.master."+tc.failingKey) {
				t.Errorf("%s error = %v, want it to identify the failing branch.master.%s command", tc.name, refreshErr, tc.failingKey)
			}
			if strings.Contains(refreshErr.Error(), "branch.master."+tc.otherKey) {
				t.Errorf("%s error = %v, want no failure identity for the absent branch.master.%s key", tc.name, refreshErr, tc.otherKey)
			}
			var exitErr *exec.ExitError
			if errors.As(refreshErr, &exitErr) {
				t.Errorf("%s error is %v, want a command-start error rather than a shell exit status", tc.name, refreshErr)
			}
			var pathErr *os.PathError
			var execErr *exec.Error
			if !errors.As(refreshErr, &pathErr) && !errors.As(refreshErr, &execErr) {
				t.Errorf("%s error = %v, want the wrapped process-start failure", tc.name, refreshErr)
			}
			// the counterpart is a verified absence through the same
			// validation path, not a second read error
			otherValue, otherAbsent, otherErr := readUpstreamConfigKey(executor.InitScopedCmdExecutor(root), "master", tc.otherKey)
			if otherErr != nil || !otherAbsent || otherValue != "" {
				t.Errorf("%s counterpart %s = (%q, %v, %v), want the verified absence", tc.name, tc.otherKey, otherValue, otherAbsent, otherErr)
			}
			got := gr.RemoteSyncStatusAndUpstream()
			if got.ObservationState != UpstreamStateUnavailable {
				t.Errorf("%s observation = %s, want unavailable", tc.name, got.ObservationState)
			}
			if got.ObservedBranch != "master" {
				t.Errorf("%s observed branch = %q, want the HEAD probe's master", tc.name, got.ObservedBranch)
			}
			p2rAssertSnapshotEquals(t, "unstartable-beats-absence retained", RemoteSyncUpstreamSnapshot{
				ObservationState:      got.ObservationState,
				ObservedBranch:        got.ObservedBranch,
				CurrentBranchUpStream: got.CurrentBranchUpStream,
				UpStreamRemoteIcon:    got.UpStreamRemoteIcon,
				RemoteSyncStatus:      got.RemoteSyncStatus,
			}, RemoteSyncUpstreamSnapshot{
				ObservationState:      UpstreamStateUnavailable,
				ObservedBranch:        retained.ObservedBranch,
				CurrentBranchUpStream: retained.CurrentBranchUpStream,
				UpStreamRemoteIcon:    retained.UpStreamRemoteIcon,
				RemoteSyncStatus:      retained.RemoteSyncStatus,
			})
			namesFailingKey := false
			for _, entry := range gittiLogging.GetFullLogs()[before:] {
				if strings.Contains(entry.OpsCommand, "branch.master."+tc.failingKey) {
					namesFailingKey = true
				}
			}
			if !namesFailingKey {
				t.Errorf("%s did not identify the failing branch.master.%s command in the logs", tc.name, tc.failingKey)
			}
			// a repeated failure keeps that same retained tracked payload
			if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
				t.Fatalf("%s repeated refresh succeeded, want the unstartable-command error again", tc.name)
			}
			if repeated := gr.RemoteSyncStatusAndUpstream(); repeated != got {
				t.Errorf("%s repeated failure moved the snapshot from %+v to %+v, want it retained", tc.name, got, repeated)
			}
		})
	}
}

func TestPhase2RepairFailureRetainsLocalDotAfterTrackedSnapshot(t *testing.T) {
	root, run := trackedMasterFixture(t)
	run("remote", "set-url", "origin", "https://github.com/example/repo.git")
	gr, _ := partialRemoteUnderTest(t, root)

	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the baseline tracked-branch read failed: %v", err)
	}
	tracked := gr.RemoteSyncStatusAndUpstream()
	if tracked.ObservationState != UpstreamStateTracked || tracked.CurrentBranchUpStream != "origin/master" {
		t.Fatalf("baseline observation = %+v, want tracked origin/master", tracked)
	}
	if tracked.UpStreamRemoteIcon == DefaultUpStreamRemoteIcon {
		t.Fatal("the baseline icon is the generic glyph, want the github remote-kind glyph so the later local-dot payload differs")
	}

	// publish the local-dot generation with a different identity, icon,
	// and counts than the tracked baseline
	run("branch", "target")
	run("config", "branch.master.remote", ".")
	run("config", "branch.master.merge", "refs/heads/target")
	run("commit", "--allow-empty", "-q", "-m", "ahead of target")
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the local-dot read failed: %v", err)
	}
	localDot := gr.RemoteSyncStatusAndUpstream()
	if localDot.ObservationState != UpstreamStateTracked || localDot.CurrentBranchUpStream != "target" {
		t.Fatalf("local-dot observation = %+v, want tracked target", localDot)
	}
	if localDot.CurrentBranchUpStream == tracked.CurrentBranchUpStream ||
		localDot.UpStreamRemoteIcon == tracked.UpStreamRemoteIcon ||
		localDot.RemoteSyncStatus == tracked.RemoteSyncStatus {
		t.Fatalf("test setup: local-dot payload %+v must differ from the tracked payload %+v in identity, icon, and counts", localDot, tracked)
	}

	// break only the count probe: the read must report unavailable and
	// retain the local-dot payload rather than the older remote snapshot
	installGitWrapper(t, p2rRevListFailWrapperScript, realGitPath(t))
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
		t.Fatal("the failed count refresh returned nil, want the rev-list error")
	}
	got := gr.RemoteSyncStatusAndUpstream()
	if got.ObservationState != UpstreamStateUnavailable {
		t.Errorf("failed-read observation = %s, want unavailable", got.ObservationState)
	}
	if got.CurrentBranchUpStream != "target" {
		t.Errorf("the failed read resurrected the older remote upstream %q, want the retained local-dot upstream %q", got.CurrentBranchUpStream, "target")
	}
	p2rAssertSnapshotEquals(t, "failed-read retained", got, RemoteSyncUpstreamSnapshot{
		ObservationState:      UpstreamStateUnavailable,
		ObservedBranch:        localDot.ObservedBranch,
		CurrentBranchUpStream: localDot.CurrentBranchUpStream,
		UpStreamRemoteIcon:    localDot.UpStreamRemoteIcon,
		RemoteSyncStatus:      localDot.RemoteSyncStatus,
	})

	// a repeated failure keeps that same retained payload
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
		t.Fatal("the repeated failed refresh returned nil, want the rev-list error")
	}
	if repeated := gr.RemoteSyncStatusAndUpstream(); repeated != got {
		t.Errorf("the repeated failure moved the snapshot from %+v to %+v, want it retained", got, repeated)
	}
}

func TestPhase2RepairEmbeddedNewlineFirstMergeRefusesPushBeforeStart(t *testing.T) {
	root, run := trackedMasterFixture(t)
	run("branch", "targetB")
	gr, _ := partialRemoteUnderTest(t, root)

	// the latest successful payload the failure must retain
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the baseline tracked-branch read failed: %v", err)
	}
	retained := gr.RemoteSyncStatusAndUpstream()
	if retained.ObservationState != UpstreamStateTracked {
		t.Fatalf("baseline observation = %s, want tracked", retained.ObservationState)
	}

	// first merge value carries a literal embedded newline after an
	// otherwise valid ref prefix, followed by a valid later merge:
	// config --get reports the last value while Git cannot resolve the
	// captured-branch upstream at all, so first-line parsing alone must
	// not publish a tracked ref
	run("config", "branch.master.remote", ".")
	run("config", "--unset-all", "branch.master.merge")
	run("config", "--add", "branch.master.merge", "refs/heads/targetB\nbogus-line")
	run("config", "--add", "branch.master.merge", "refs/heads/targetB")
	lastMerge, err := p2rGitOutput(t, root, "config", "--get", "branch.master.merge")
	if err != nil {
		t.Fatalf("the config trap oracle failed: %v", err)
	}
	if lastMerge != "refs/heads/targetB" {
		t.Fatalf("test setup: config --get merge = %q, want the last value refs/heads/targetB", lastMerge)
	}
	if _, err := p2rGitOutput(t, root, "rev-parse", "--abbrev-ref", "master@{upstream}"); err == nil {
		t.Fatal("test setup: Git resolved master@{upstream} with an embedded-newline first merge value, want the resolution failure")
	}

	argvLog := filepath.Join(t.TempDir(), "push-argv.log")
	installGitWrapper(t, pushArgvCaptureWrapper, realGitPath(t))
	t.Setenv("PUSH_ARGV_LOG", argvLog)

	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
		t.Fatal("the embedded-newline-first read returned nil, want the resolution error")
	}
	got := gr.RemoteSyncStatusAndUpstream()
	if got.ObservationState != UpstreamStateUnavailable {
		t.Errorf("embedded-newline-first observation = %s, want unavailable rather than the first-line tracked ref", got.ObservationState)
	}
	if got.CurrentBranchUpStream == "targetB" {
		t.Errorf("the read published the first-line prefix %q as the tracked upstream, want no invented tracked ref", got.CurrentBranchUpStream)
	}
	p2rAssertSnapshotEquals(t, "embedded-newline-first retained", RemoteSyncUpstreamSnapshot{
		ObservationState:      got.ObservationState,
		ObservedBranch:        got.ObservedBranch,
		CurrentBranchUpStream: got.CurrentBranchUpStream,
		UpStreamRemoteIcon:    got.UpStreamRemoteIcon,
		RemoteSyncStatus:      got.RemoteSyncStatus,
	}, RemoteSyncUpstreamSnapshot{
		ObservationState:      UpstreamStateUnavailable,
		ObservedBranch:        retained.ObservedBranch,
		CurrentBranchUpStream: retained.CurrentBranchUpStream,
		UpStreamRemoteIcon:    retained.UpStreamRemoteIcon,
		RemoteSyncStatus:      retained.RemoteSyncStatus,
	})

	// push preparation refuses before starting any push process
	gc, gittiLogging := partialCommitUnderTest(t, root)
	route := GitPushRoute{RemoteName: "origin", Branch: "master", Intent: PushIntentPublish}
	if _, err := gc.prepareGitPush(route); err == nil {
		t.Fatal("embedded-newline-first push preparation succeeded, want the re-read refusal")
	}
	result := gc.GitPush(context.Background(), route)
	if result.Started() {
		t.Error("the embedded-newline-first push started a process, want refusal before process start")
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
	if invocations := readArgvInvocations(t, argvLog); len(invocations) != 0 {
		t.Errorf("the refused push recorded %d push invocations, want none before start", len(invocations))
	}
}

func TestPhase2RepairLocalDotStaysPinnedToCapturedBranch(t *testing.T) {
	root, run := trackedMasterFixture(t)
	run("branch", "target")
	run("branch", "side")
	run("push", "-q", "-u", "origin", "side")
	run("commit", "--allow-empty", "-q", "-m", "ahead of target")
	run("config", "branch.master.remote", ".")
	run("config", "branch.master.merge", "refs/heads/target")

	oracleUpstream, err := p2rGitOutput(t, root, "rev-parse", "--abbrev-ref", "master@{upstream}")
	if err != nil {
		t.Fatalf("the Git upstream oracle failed: %v", err)
	}
	if oracleUpstream != "target" {
		t.Fatalf("test setup: Git resolved master@{upstream} to %q, want the local target ref", oracleUpstream)
	}
	oracleCounts, err := p2rGitOutput(t, root, "rev-list", "--left-right", "--count", "master...master@{upstream}")
	if err != nil {
		t.Fatalf("the Git counts oracle failed: %v", err)
	}
	parts := strings.Fields(oracleCounts)
	if len(parts) != 2 || parts[0] != "1" || parts[1] != "0" {
		t.Fatalf("test setup: Git counts for master against target = %q, want 1 0 so a mixed payload is visible", oracleCounts)
	}
	sideUpstream, err := p2rGitOutput(t, root, "rev-parse", "--abbrev-ref", "side@{upstream}")
	if err != nil {
		t.Fatalf("the side-branch oracle failed: %v", err)
	}
	if sideUpstream == oracleUpstream {
		t.Fatalf("test setup: side upstream %q must differ from the captured master upstream %q", sideUpstream, oracleUpstream)
	}

	// switch to the other branch at the first local-dot probe: every read
	// after the branch capture must stay pinned to master rather than
	// mixing side's upstream or counts into the publication
	installGitWrapper(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    --get-all)
      %s checkout -q side 2>/dev/null || exit 1
      break
      ;;
  esac
done
exec %s "$@"
`, realGitPath(t), realGitPath(t))
	gr, _ := partialRemoteUnderTest(t, root)
	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the pinned local-dot read failed: %v", err)
	}
	got := gr.RemoteSyncStatusAndUpstream()
	if got.ObservationState != UpstreamStateTracked {
		t.Fatalf("pinned local-dot observation = %s, want tracked", got.ObservationState)
	}
	if got.ObservedBranch != "master" {
		t.Errorf("observed branch = %q, want the captured master branch", got.ObservedBranch)
	}
	if got.CurrentBranchUpStream != oracleUpstream {
		t.Errorf("pinned local-dot upstream = %q, want Git's captured-branch upstream %q rather than the switched branch %q", got.CurrentBranchUpStream, oracleUpstream, sideUpstream)
	}
	if got.RemoteSyncStatus != (RemoteSyncStatus{Local: parts[0], Remote: parts[1]}) {
		t.Errorf("pinned local-dot counts = %v, want Git's captured-branch counts %q", got.RemoteSyncStatus, oracleCounts)
	}
}
