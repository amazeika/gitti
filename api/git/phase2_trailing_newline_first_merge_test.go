package git

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gohyuhan/gitti/logging"
)

func TestTrailingNewlineFirstMergeRefusesPushBeforeStart(t *testing.T) {
	root, run := trackedMasterFixture(t)
	run("branch", "targetB")
	run("commit", "--allow-empty", "-q", "-m", "B1")
	gr, _ := partialRemoteUnderTest(t, root)

	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the baseline tracked-branch read failed: %v", err)
	}
	retained := gr.RemoteSyncStatusAndUpstream()
	if retained.ObservationState != UpstreamStateTracked {
		t.Fatalf("baseline observation = %s, want tracked", retained.ObservationState)
	}

	// a first merge value with a literal trailing newline followed by a
	// resolvable later ref under a local-dot remote: config --get reports
	// the later value while Git must resolve the first
	run("config", "branch.master.remote", ".")
	run("config", "--unset-all", "branch.master.merge")
	run("config", "--add", "branch.master.merge", "refs/heads/targetB\n")
	run("config", "--add", "branch.master.merge", "refs/heads/targetB")

	lastMerge, err := p2rGitOutput(t, root, "config", "--get", "branch.master.merge")
	if err != nil {
		t.Fatalf("the config trap oracle failed: %v", err)
	}
	if lastMerge != "refs/heads/targetB" {
		t.Fatalf("test setup: config --get merge = %q, want the later value refs/heads/targetB", lastMerge)
	}
	if _, err := p2rGitOutput(t, root, "rev-parse", "--abbrev-ref", "master@{upstream}"); err == nil {
		t.Fatal("test setup: Git resolved master@{upstream} with a trailing-newline first merge value, want the resolution failure")
	}

	argvLog := filepath.Join(t.TempDir(), "push-argv.log")
	installGitWrapper(t, pushArgvCaptureWrapper, realGitPath(t))
	t.Setenv("PUSH_ARGV_LOG", argvLog)

	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
		t.Fatal("the trailing-newline-first read returned nil, want the resolution error")
	}
	got := gr.RemoteSyncStatusAndUpstream()
	if got.ObservationState != UpstreamStateUnavailable {
		t.Errorf("trailing-newline-first observation = %s, want unavailable rather than the trimmed later ref", got.ObservationState)
	}
	if got.CurrentBranchUpStream == "targetB" {
		t.Errorf("the read normalized the trailing-newline first value %q to the tracked upstream, want no invented tracked ref", got.CurrentBranchUpStream)
	}
	if got.ObservedBranch != retained.ObservedBranch ||
		got.CurrentBranchUpStream != retained.CurrentBranchUpStream ||
		got.UpStreamRemoteIcon != retained.UpStreamRemoteIcon ||
		got.RemoteSyncStatus != retained.RemoteSyncStatus {
		t.Errorf("the trailing-newline-first failure moved the payload from %+v to %+v, want the retained latest payload with unavailable health", retained, got)
	}

	gc, gittiLogging := partialCommitUnderTest(t, root)
	route := GitPushRoute{RemoteName: "origin", Branch: "master", Intent: PushIntentPublish}
	if _, err := gc.prepareGitPush(route); err == nil {
		t.Fatal("trailing-newline-first push preparation succeeded, want the re-read refusal")
	}
	result := gc.GitPush(context.Background(), route)
	if result.Started() {
		t.Error("the trailing-newline-first push started a process, want refusal before process start")
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

func TestSurroundingWhitespaceFirstMergeRefusesPushBeforeStart(t *testing.T) {
	root, run := trackedMasterFixture(t)
	run("branch", "targetB")
	run("commit", "--allow-empty", "-q", "-m", "B1")
	gr, _ := partialRemoteUnderTest(t, root)

	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err != nil {
		t.Fatalf("the baseline tracked-branch read failed: %v", err)
	}
	retained := gr.RemoteSyncStatusAndUpstream()
	if retained.ObservationState != UpstreamStateTracked {
		t.Fatalf("baseline observation = %s, want tracked", retained.ObservationState)
	}

	// a first merge value with surrounding whitespace followed by a
	// resolvable later ref under a local-dot remote: config --get reports
	// the later value while Git must resolve the first
	run("config", "branch.master.remote", ".")
	run("config", "--unset-all", "branch.master.merge")
	run("config", "--add", "branch.master.merge", "  refs/heads/targetB  ")
	run("config", "--add", "branch.master.merge", "refs/heads/targetB")

	lastMerge, err := p2rGitOutput(t, root, "config", "--get", "branch.master.merge")
	if err != nil {
		t.Fatalf("the config trap oracle failed: %v", err)
	}
	if lastMerge != "refs/heads/targetB" {
		t.Fatalf("test setup: config --get merge = %q, want the later value refs/heads/targetB", lastMerge)
	}
	if _, err := p2rGitOutput(t, root, "rev-parse", "--abbrev-ref", "master@{upstream}"); err == nil {
		t.Fatal("test setup: Git resolved master@{upstream} with a whitespace-padded first merge value, want the resolution failure")
	}

	argvLog := filepath.Join(t.TempDir(), "push-argv.log")
	installGitWrapper(t, pushArgvCaptureWrapper, realGitPath(t))
	t.Setenv("PUSH_ARGV_LOG", argvLog)

	if err := gr.GetLatestRemoteSyncStatusAndUpstream(nil); err == nil {
		t.Fatal("the whitespace-padded-first read returned nil, want the resolution error")
	}
	got := gr.RemoteSyncStatusAndUpstream()
	if got.ObservationState != UpstreamStateUnavailable {
		t.Errorf("whitespace-padded-first observation = %s, want unavailable rather than the trimmed later ref", got.ObservationState)
	}
	if got.CurrentBranchUpStream == "targetB" {
		t.Errorf("the read normalized the whitespace-padded first value %q to the tracked upstream, want no invented tracked ref", got.CurrentBranchUpStream)
	}
	if got.ObservedBranch != retained.ObservedBranch ||
		got.CurrentBranchUpStream != retained.CurrentBranchUpStream ||
		got.UpStreamRemoteIcon != retained.UpStreamRemoteIcon ||
		got.RemoteSyncStatus != retained.RemoteSyncStatus {
		t.Errorf("the whitespace-padded-first failure moved the payload from %+v to %+v, want the retained latest payload with unavailable health", retained, got)
	}

	gc, gittiLogging := partialCommitUnderTest(t, root)
	route := GitPushRoute{RemoteName: "origin", Branch: "master", Intent: PushIntentPublish}
	if _, err := gc.prepareGitPush(route); err == nil {
		t.Fatal("whitespace-padded-first push preparation succeeded, want the re-read refusal")
	}
	result := gc.GitPush(context.Background(), route)
	if result.Started() {
		t.Error("the whitespace-padded-first push started a process, want refusal before process start")
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
