package git

// Additive R5 coverage for phase 1.1.1 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md.
//
// The frozen api/git/commit_test.go pins the pre-start cancellation
// outcome and the guard refusal outcome, and the step-1 additive file
// pins guard-time cancellation with descriptor accounting. The two tests
// below pin the remaining R5 cleanup evidence: a pre-Start cancellation
// (with a passing guard) launches no push and leaves no setup descriptor
// behind, and a stale-generation guard refusal without any context
// closes the already-created setup pipes, stays uncancelled, and
// launches no push. Both are fully deterministic: neither uses sleeps,
// descendants, or timing windows. Same-package helpers
// (gitCommitUnderTestWithFakeGit, pushRouteUnderTest,
// countOpenFDsForCausality, gitPushWithCausalityTimeout) are reused from
// the retained files and are not redefined here.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGitPushPreStartCancellationClosesSetupPipesAndStartsNothing(t *testing.T) {
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
    push)
      touch "$FAKE_GIT_PUSH_RAN"
      echo "must-not-run"
      exit 0
      ;;
  esac
done
exit 0
`)
	pushRan := filepath.Join(root, "setup-push-ran")
	t.Setenv("FAKE_GIT_PUSH_RAN", pushRan)

	// A context already cancelled before Start never creates a process:
	// the attempt stays unstarted and cancelled with exit -1, launches
	// no push, and leaves no setup descriptor behind. No timing is
	// involved: the cancellation precedes the call.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	route := pushRouteUnderTest("master")
	route.ActiveGuard = func() error { return nil }
	before := countOpenFDsForCausality(t)
	result := gitPushWithCausalityTimeout(t, gitCommit, ctx, route, 10*time.Second, "after the pre-Start cancellation")

	if result.Started() {
		t.Error("a pre-Start cancellation must not start the prepared process")
	}
	if !result.Cancelled() {
		t.Error("a pre-Start cancellation must report cancelled with no exit status rather than a setup outcome")
	}
	if result.ExitCode() != -1 {
		t.Errorf("ExitCode() = %d, want -1 for the absent process status", result.ExitCode())
	}
	if !errors.Is(result.Err(), context.Canceled) {
		t.Errorf("Err() = %v, want the pre-Start context cancellation", result.Err())
	}
	if result.Success() {
		t.Error("a pre-Start cancellation must not be reported as a success")
	}
	if _, err := os.Stat(pushRan); err == nil {
		t.Error("the push process launched although the context was already cancelled before Start")
	} else if !os.IsNotExist(err) {
		t.Errorf("checking the push launch marker: %v", err)
	}
	if delta := countOpenFDsForCausality(t) - before; delta > 2 {
		t.Errorf("open file descriptors grew by %d across the pre-Start refusal, want all setup pipe ends closed", delta)
	}
}

func TestGitPushGuardRefusalClosesSetupPipesAndStartsNothing(t *testing.T) {
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
    push)
      touch "$FAKE_GIT_PUSH_RAN"
      echo "must-not-run"
      exit 0
      ;;
  esac
done
exit 0
`)
	pushRan := filepath.Join(root, "guard-refusal-push-ran")
	t.Setenv("FAKE_GIT_PUSH_RAN", pushRan)

	// A stale-generation guard refusal happens after the setup pipes
	// exist, so all four ends must close: the attempt stays unstarted
	// and uncancelled with exit -1 and launches no push. The refusal
	// carries no context, so it must not read as a cancellation either.
	guardErr := errors.New("the git operations generation changed after the push was confirmed")
	route := pushRouteUnderTest("master")
	route.ActiveGuard = func() error { return guardErr }
	before := countOpenFDsForCausality(t)
	result := gitPushWithCausalityTimeout(t, gitCommit, context.Background(), route, 10*time.Second, "after the guard refusal")

	if result.Started() {
		t.Error("a guard refusal must not start the prepared process")
	}
	if result.Cancelled() {
		t.Error("a guard refusal without cancellation must not report cancelled")
	}
	if result.ExitCode() != -1 {
		t.Errorf("ExitCode() = %d, want -1 for the absent process status", result.ExitCode())
	}
	if !errors.Is(result.Err(), guardErr) {
		t.Errorf("Err() = %v, want the guard refusal error", result.Err())
	}
	if result.Success() {
		t.Error("a guard refusal must not be reported as a success")
	}
	if _, err := os.Stat(pushRan); err == nil {
		t.Error("the push process launched although the guard refused the attempt before Start")
	} else if !os.IsNotExist(err) {
		t.Errorf("checking the push launch marker: %v", err)
	}
	if delta := countOpenFDsForCausality(t) - before; delta > 2 {
		t.Errorf("open file descriptors grew by %d across the guard refusal, want all setup pipe ends closed", delta)
	}
}
