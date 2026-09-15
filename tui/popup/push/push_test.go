package push

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gohyuhan/gitti/api"
	gitapi "github.com/gohyuhan/gitti/api/git"
	"github.com/gohyuhan/gitti/executor"
	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/logging"
	"github.com/gohyuhan/gitti/tui/types"
)

// ------------------------------------
//
//	Shadow PATH with a fake git so the popup tests exercise real push results
//	without a remote
//
// ------------------------------------
func fakeGitOnPath(t *testing.T, script string) {
	t.Helper()

	binDir := t.TempDir()
	gitPath := filepath.Join(binDir, "git")
	if err := os.WriteFile(gitPath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the fake git: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// ------------------------------------
//
//	Build a model whose push popup is ready for a scripted push, returning the
//	model, the push handler, its process lock, and its logging
//
// ------------------------------------
func pushPopupModelUnderTest(t *testing.T, script string) (*types.GittiModel, *gitapi.GitCommit, *gitapi.GitProcessLock, *logging.GittiLogging, string) {
	t.Helper()

	i18n.InitGittiLanguageMapping("en")
	fakeGitOnPath(t, script)

	root := t.TempDir()
	original := executor.GittiCmdExecutor
	t.Cleanup(func() { executor.GittiCmdExecutor = original })
	executor.InitCmdExecutor(root)

	gittiLogging := logging.InitGittiLogging(64, make(chan string, 256), 3)
	processLock := gitapi.InitGitProcessLock(gittiLogging)
	gitCommit := gitapi.InitGitCommit(make(chan string, 16), processLock, gittiLogging)

	model := &types.GittiModel{
		Width:         100,
		GitOperations: &api.GitOperations{GitCommit: gitCommit},
	}
	InitGitRemotePushPopUpModel(model)

	// Widen the viewport so the assertions read the full diagnostics without
	// wrapping or scrolling.
	popUp := model.PopUpModel.(*GitRemotePushPopUpModel)
	popUp.GitRemotePushOutputViewport.SetWidth(200)
	popUp.GitRemotePushOutputViewport.SetHeight(40)

	return model, gitCommit, processLock, gittiLogging, root
}

// ------------------------------------
//
//	Publish a push result to the popup the same way the TUI event dispatch does
//
// ------------------------------------
func publishPushResult(t *testing.T, model *types.GittiModel, result gitapi.GitPushResult) *GitRemotePushPopUpModel {
	t.Helper()

	popUp := model.PopUpModel.(*GitRemotePushPopUpModel)
	UpdateGitPushResultEvent(model, types.GitPushResultEventDataStructure{
		Success: result.Success(),
		Result:  result,
		Attempt: popUp.ActivePushAttemptID.Load(),
	})
	// The live-update pass resets the viewport width; re-widen before the
	// assertions so no diagnostic line wraps.
	popUp.GitRemotePushOutputViewport.SetWidth(200)
	popUp.GitRemotePushOutputViewport.SetHeight(40)
	return popUp
}

// ------------------------------------
//
//	Strip the viewport's space padding so content assertions are independent of
//	the viewport size
//
// ------------------------------------
func unpadView(view string) string {
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}

func TestCompletedPopUpRendersCwdQuotedArgvStatusAndSeparateStreams(t *testing.T) {
	model, gitCommit, _, _, root := pushPopupModelUnderTest(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    push)
      echo "hello-stdout"
      echo "hello-stderr" >&2
      ;;
  esac
done
exit 0
`)

	result := gitCommit.GitPush(context.Background(), "origin", gitapi.PUSH, "master")
	if !result.Success() {
		t.Fatalf("the scripted push did not succeed: exit %d, err %v", result.ExitCode(), result.Err())
	}

	// The live viewport receives the progress while the attempt is still
	// processing, tagged by source because both streams carry output.
	popUp := model.PopUpModel.(*GitRemotePushPopUpModel)
	popUp.IsProcessing.Store(true)
	UpdatePopUpGitRemotePushOutputViewport(model)
	live := unpadView(popUp.GitRemotePushOutputViewport.View())
	for _, want := range []string{"stdout:", "hello-stdout", "stderr:", "hello-stderr"} {
		if !strings.Contains(live, want) {
			t.Errorf("live viewport = %q, want it to carry %q", live, want)
		}
	}

	publishPushResult(t, model, result)

	if popUp.IsProcessing.Load() {
		t.Error("IsProcessing is still true after the result event")
	}
	if !popUp.ProcessSuccess.Load() || popUp.HasError.Load() {
		t.Errorf("flags = success %v, error %v, want a successful push", popUp.ProcessSuccess.Load(), popUp.HasError.Load())
	}

	view := unpadView(popUp.GitRemotePushOutputViewport.View())
	for _, want := range []string{
		"Working directory: " + root,
		`argv[0]: "git"`,
		`argv[1]: "-c"`,
		`argv[10]: "push"`,
		`argv[12]: "origin"`,
		"Exit status: 0",
		"stdout:\nhello-stdout",
		"stderr:\nhello-stderr",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("completed popup =\n%s\nmissing %q", view, want)
		}
	}
}

func TestCompletedPopUpShowsActionableTextForANonZeroExit(t *testing.T) {
	model, gitCommit, _, _, _ := pushPopupModelUnderTest(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    push) echo "error: failed to push some refs" >&2; exit 1 ;;
  esac
done
exit 0
`)

	result := gitCommit.GitPush(context.Background(), "origin", gitapi.PUSH, "master")
	popUp := publishPushResult(t, model, result)

	if !popUp.HasError.Load() || popUp.ProcessSuccess.Load() {
		t.Errorf("flags = success %v, error %v, want a failed push", popUp.ProcessSuccess.Load(), popUp.HasError.Load())
	}

	view := unpadView(popUp.GitRemotePushOutputViewport.View())
	for _, want := range []string{
		"Exit status: 1",
		"Push failed with exit status 1",
		"error: failed to push some refs",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("completed popup =\n%s\nmissing the actionable %q", view, want)
		}
	}
}

func TestCompletedPopUpShowsActionableTextForANotStartedPush(t *testing.T) {
	model, gitCommit, processLock, _, _ := pushPopupModelUnderTest(t, `#!/bin/sh
exit 0
`)

	// Another git operation already holds the universal lock, so the push is
	// refused before any process exists.
	if !processLock.CanProceedWithGitOps() {
		t.Fatal("the test could not hold the git operations lock")
	}
	defer processLock.ReleaseGitOpsLock()

	result := gitCommit.GitPush(context.Background(), "origin", gitapi.PUSH, "master")
	popUp := publishPushResult(t, model, result)

	if !popUp.HasError.Load() || popUp.ProcessSuccess.Load() {
		t.Errorf("flags = success %v, error %v, want a failed push", popUp.ProcessSuccess.Load(), popUp.HasError.Load())
	}

	view := unpadView(popUp.GitRemotePushOutputViewport.View())
	for _, want := range []string{
		"Exit status: not started",
		"Push could not be started:",
		i18n.LANGUAGEMAPPING.OtherGitOpsIsRunningWarning,
	} {
		if !strings.Contains(view, want) {
			t.Errorf("completed popup =\n%s\nmissing the actionable %q", view, want)
		}
	}
}

func TestCompletedPopUpShowsExplicitMarkersForEmptyStreams(t *testing.T) {
	model, gitCommit, _, _, _ := pushPopupModelUnderTest(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    push) exit 0 ;;
  esac
done
exit 0
`)

	result := gitCommit.GitPush(context.Background(), "origin", gitapi.PUSH, "master")
	popUp := publishPushResult(t, model, result)

	view := unpadView(popUp.GitRemotePushOutputViewport.View())
	for _, want := range []string{"stdout:\n(no output)", "stderr:\n(no output)"} {
		if !strings.Contains(view, want) {
			t.Errorf("completed popup =\n%s\nmissing the explicit empty marker %q", view, want)
		}
	}
}

func TestCancelledResultNeverMutatesAClosedPopUp(t *testing.T) {
	model, gitCommit, _, _, _ := pushPopupModelUnderTest(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    push) echo "error: failed" >&2; exit 1 ;;
  esac
done
exit 0
`)

	result := gitCommit.GitPush(context.Background(), "origin", gitapi.PUSH, "master")

	// The user closed the popup; the cancel service cleared it and flagged it.
	popUp := model.PopUpModel.(*GitRemotePushPopUpModel)
	popUp.IsCancelled.Store(true)
	ResetGitRemotePushPopUpDiagnostics(popUp)

	UpdateGitPushResultEvent(model, types.GitPushResultEventDataStructure{
		Success: result.Success(),
		Result:  result,
	})

	if popUp.HasError.Load() || popUp.ProcessSuccess.Load() || popUp.IsProcessing.Load() {
		t.Errorf("late result recoloured the closed popup: success %v, error %v, processing %v",
			popUp.ProcessSuccess.Load(), popUp.HasError.Load(), popUp.IsProcessing.Load())
	}
	if got := len(popUp.LastPushResult.Argv()); got != 0 {
		t.Errorf("late result installed %d argv elements into the closed popup", got)
	}
	if got := strings.TrimSpace(unpadView(popUp.GitRemotePushOutputViewport.View())); got != "" {
		t.Errorf("late result re-populated the closed popup viewport: %q", got)
	}
}

func TestSecondPushDoesNotDisplayTheFirstAttempt(t *testing.T) {
	model, gitCommit, _, _, _ := pushPopupModelUnderTest(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    push)
      if [ -n "$FAKE_GIT_TAG" ]; then echo "$FAKE_GIT_TAG" >&2; fi
      exit "${FAKE_GIT_EXIT:-0}"
      ;;
  esac
done
exit 0
`)

	t.Setenv("FAKE_GIT_TAG", "FIRST-ATTEMPT")
	t.Setenv("FAKE_GIT_EXIT", "1")
	first := gitCommit.GitPush(context.Background(), "origin", gitapi.PUSH, "master")
	publishPushResult(t, model, first)
	if view := model.PopUpModel.(*GitRemotePushPopUpModel).GitRemotePushOutputViewport.View(); !strings.Contains(view, "FIRST-ATTEMPT") {
		t.Fatalf("first attempt diagnostics missing from the popup: %q", view)
	}

	// Starting the second push clears every field from the first attempt.
	popUp := model.PopUpModel.(*GitRemotePushPopUpModel)
	popUp.HasError.Store(false)
	popUp.ProcessSuccess.Store(false)
	popUp.IsProcessing.Store(true)
	ResetGitRemotePushPopUpDiagnostics(popUp)
	if got := len(popUp.LastPushResult.Argv()); got != 0 {
		t.Fatalf("the second start retained %d argv elements from the first attempt", got)
	}
	if got := strings.TrimSpace(unpadView(popUp.GitRemotePushOutputViewport.View())); got != "" {
		t.Fatalf("the second start retained the first attempt's viewport: %q", got)
	}

	t.Setenv("FAKE_GIT_TAG", "SECOND-ATTEMPT")
	t.Setenv("FAKE_GIT_EXIT", "0")
	second := gitCommit.GitPush(context.Background(), "origin", gitapi.PUSH, "master")
	publishPushResult(t, model, second)

	view := unpadView(model.PopUpModel.(*GitRemotePushPopUpModel).GitRemotePushOutputViewport.View())
	if !strings.Contains(view, "SECOND-ATTEMPT") {
		t.Errorf("second attempt diagnostics missing from the popup: %q", view)
	}
	if strings.Contains(view, "FIRST-ATTEMPT") {
		t.Errorf("the second push displayed output retained from the first: %q", view)
	}
}

func TestBuildGitRemotePushLiveOutputTagsStreamsOnlyWhenBothAreUsed(t *testing.T) {
	if got := buildGitRemotePushLiveOutput([]string{"out-a", "out-b"}, []string{"err-a"}); got != "stdout:\nout-a\nout-b\nstderr:\nerr-a" {
		t.Errorf("both-streams live output = %q, want labelled sections in order", got)
	}
	if got := buildGitRemotePushLiveOutput(nil, []string{"err-a"}); got != "err-a" {
		t.Errorf("stderr-only live output = %q, want the plain line without a label", got)
	}
	if got := buildGitRemotePushLiveOutput([]string{"out-a"}, nil); got != "out-a" {
		t.Errorf("stdout-only live output = %q, want the plain line without a label", got)
	}
}

func TestQueuedProgressAfterCompletionDoesNotEraseDiagnostics(t *testing.T) {
	model, gitCommit, _, _, _ := pushPopupModelUnderTest(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    push)
      echo "hello-stdout"
      echo "hello-stderr" >&2
      ;;
  esac
done
exit 0
`)

	result := gitCommit.GitPush(context.Background(), "origin", gitapi.PUSH, "master")
	if !result.Success() {
		t.Fatalf("the scripted push did not succeed: exit %d, err %v", result.ExitCode(), result.Err())
	}
	popUp := publishPushResult(t, model, result)
	if view := unpadView(popUp.GitRemotePushOutputViewport.View()); !strings.Contains(view, "Exit status: 0") {
		t.Fatalf("completed popup missing the exit status: %q", view)
	}

	// A queued progress event arrives after the attempt finished; the live
	// update must not replace the completed diagnostics with stale output.
	UpdatePopUpGitRemotePushOutputViewport(model)
	popUp.GitRemotePushOutputViewport.SetWidth(200)
	popUp.GitRemotePushOutputViewport.SetHeight(40)
	after := unpadView(popUp.GitRemotePushOutputViewport.View())
	if !strings.Contains(after, "Exit status: 0") {
		t.Errorf("queued progress erased the completed diagnostics: %q", after)
	}
	if !strings.Contains(after, "Working directory:") {
		t.Errorf("queued progress replaced the diagnostics with raw live output: %q", after)
	}
}

func TestQueuedProgressAfterCancellationDoesNotMutateAClosedPopUp(t *testing.T) {
	model, gitCommit, _, _, _ := pushPopupModelUnderTest(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    push) echo "queued-progress-line" ;;
  esac
done
exit 0
`)

	// A push runs, leaving lines in the live progress buffer.
	result := gitCommit.GitPush(context.Background(), "origin", gitapi.PUSH, "master")
	if !result.Success() {
		t.Fatalf("the scripted push did not succeed: exit %d, err %v", result.ExitCode(), result.Err())
	}
	popUp := model.PopUpModel.(*GitRemotePushPopUpModel)

	// The user closed the popup: flagged cancelled and cleared. IsProcessing
	// may still read true for a progress event queued just before the
	// cancel, so the cancelled guard is what protects here.
	popUp.IsProcessing.Store(true)
	popUp.IsCancelled.Store(true)
	ResetGitRemotePushPopUpDiagnostics(popUp)

	// The queued progress event is now dispatched; it must not re-populate
	// the closed popup with the live buffer's retained lines.
	UpdatePopUpGitRemotePushOutputViewport(model)

	if got := strings.TrimSpace(unpadView(popUp.GitRemotePushOutputViewport.View())); got != "" {
		t.Errorf("queued progress re-populated the closed popup viewport: %q", got)
	}
	if popUp.HasError.Load() || popUp.ProcessSuccess.Load() {
		t.Errorf("queued progress recoloured the closed popup: success %v, error %v", popUp.ProcessSuccess.Load(), popUp.HasError.Load())
	}
}

func TestLateResultFromACancelledAttemptCannotOverwriteANewPush(t *testing.T) {
	model, gitCommit, _, _, _ := pushPopupModelUnderTest(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    push)
      if [ -n "$FAKE_GIT_TAG" ]; then echo "$FAKE_GIT_TAG" >&2; fi
      exit "${FAKE_GIT_EXIT:-0}"
      ;;
  esac
done
exit 0
`)
	popUp := model.PopUpModel.(*GitRemotePushPopUpModel)

	// Attempt A is running, then the user cancels it.
	attemptA := BeginGitRemotePushAttempt(popUp)
	popUp.IsProcessing.Store(true)
	t.Setenv("FAKE_GIT_TAG", "ATTEMPT-A")
	t.Setenv("FAKE_GIT_EXIT", "1")
	resultA := gitCommit.GitPush(context.Background(), "origin", gitapi.PUSH, "master")
	popUp.IsCancelled.Store(true)
	ResetGitRemotePushPopUpDiagnostics(popUp)
	popUp.IsProcessing.Store(false)

	// The user starts attempt B while A's result is still in flight.
	attemptB := BeginGitRemotePushAttempt(popUp)
	popUp.IsCancelled.Store(false)
	popUp.IsProcessing.Store(true)
	ResetGitRemotePushPopUpDiagnostics(popUp)
	t.Setenv("FAKE_GIT_TAG", "ATTEMPT-B")
	t.Setenv("FAKE_GIT_EXIT", "0")
	resultB := gitCommit.GitPush(context.Background(), "origin", gitapi.PUSH, "master")

	// B's result is delivered and accepted.
	UpdateGitPushResultEvent(model, types.GitPushResultEventDataStructure{Success: resultB.Success(), Result: resultB, Attempt: attemptB})
	if view := unpadView(popUp.GitRemotePushOutputViewport.View()); !strings.Contains(view, "ATTEMPT-B") {
		t.Fatalf("attempt B's result was not displayed: %q", view)
	}

	// A's late result is now delivered; it belongs to a different attempt
	// and must be rejected without touching B's popup.
	UpdateGitPushResultEvent(model, types.GitPushResultEventDataStructure{Success: resultA.Success(), Result: resultA, Attempt: attemptA})
	after := unpadView(popUp.GitRemotePushOutputViewport.View())
	if !strings.Contains(after, "ATTEMPT-B") {
		t.Errorf("the late result overwrote attempt B's diagnostics: %q", after)
	}
	if strings.Contains(after, "ATTEMPT-A") {
		t.Errorf("the late result displayed attempt A's output in the active popup: %q", after)
	}
	if !popUp.ProcessSuccess.Load() {
		t.Error("the late result reset attempt B's success flag")
	}
	if popUp.HasError.Load() {
		t.Error("the late result recoloured attempt B's popup as failed")
	}
}

// ------------------------------------
//
//	Reconstructing the push popup must not recycle an attempt id: a late
//	result from a cancelled attempt on the old instance cannot pass the new
//	popup's identity guard and overwrite its push.
//
// ------------------------------------
func TestLateResultCannotOverwriteAReconstructedPopUpsAttempt(t *testing.T) {
	model, gitCommit, _, _, _ := pushPopupModelUnderTest(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    push)
      if [ -n "$FAKE_GIT_TAG" ]; then echo "$FAKE_GIT_TAG" >&2; fi
      exit "${FAKE_GIT_EXIT:-0}"
      ;;
  esac
done
exit 0
`)
	firstPopUp := model.PopUpModel.(*GitRemotePushPopUpModel)

	// Attempt A starts on the first popup instance, then the user cancels
	// it before its result is delivered.
	attemptA := BeginGitRemotePushAttempt(firstPopUp)
	firstPopUp.IsProcessing.Store(true)
	t.Setenv("FAKE_GIT_TAG", "ATTEMPT-A")
	t.Setenv("FAKE_GIT_EXIT", "1")
	resultA := gitCommit.GitPush(context.Background(), "origin", gitapi.PUSH, "master")
	firstPopUp.IsCancelled.Store(true)
	ResetGitRemotePushPopUpDiagnostics(firstPopUp)
	firstPopUp.IsProcessing.Store(false)

	// The user reopens the push popup: a fresh popup instance replaces the
	// closed one, as InitGitRemotePushPopUpModelAndStartGitRemotePushService
	// does when the active popup is not already a push popup.
	InitGitRemotePushPopUpModel(model)
	secondPopUp := model.PopUpModel.(*GitRemotePushPopUpModel)
	if secondPopUp == firstPopUp {
		t.Fatal("the reconstructed popup reused the old instance")
	}

	// Attempt B starts on the reconstructed popup and its result is
	// delivered and accepted.
	attemptB := BeginGitRemotePushAttempt(secondPopUp)
	if attemptB == attemptA {
		t.Fatalf("the reconstructed popup recycled attempt id %d from the first popup", attemptB)
	}
	secondPopUp.IsProcessing.Store(true)
	t.Setenv("FAKE_GIT_TAG", "ATTEMPT-B")
	t.Setenv("FAKE_GIT_EXIT", "0")
	resultB := gitCommit.GitPush(context.Background(), "origin", gitapi.PUSH, "master")
	publishPushResult(t, model, resultB)
	if view := unpadView(secondPopUp.GitRemotePushOutputViewport.View()); !strings.Contains(view, "ATTEMPT-B") {
		t.Fatalf("attempt B's result was not displayed: %q", view)
	}

	// A's late result now arrives; it belongs to a different attempt on a
	// different popup instance, so it must be rejected wholesale.
	UpdateGitPushResultEvent(model, types.GitPushResultEventDataStructure{Success: resultA.Success(), Result: resultA, Attempt: attemptA})
	after := unpadView(secondPopUp.GitRemotePushOutputViewport.View())
	if !strings.Contains(after, "ATTEMPT-B") {
		t.Errorf("the late result overwrote the reconstructed popup's diagnostics: %q", after)
	}
	if strings.Contains(after, "ATTEMPT-A") {
		t.Errorf("the late result displayed the cancelled attempt's output: %q", after)
	}
	if !secondPopUp.ProcessSuccess.Load() {
		t.Error("the late result reset the reconstructed popup's success flag")
	}
	if secondPopUp.HasError.Load() {
		t.Error("the late result recoloured the reconstructed popup as failed")
	}
	if secondPopUp.IsProcessing.Load() {
		t.Error("the late result left the reconstructed popup processing")
	}
}

// ------------------------------------
//
//	While a zero-status push waits for its post-push refresh ticket the popup
//	remains open in the visible reconciliation stage instead of the plain
//	processing text.
//
// ------------------------------------
func TestReconcilingStageShowsTheRefreshStageText(t *testing.T) {
	model, _, _, _, _ := pushPopupModelUnderTest(t, `#!/bin/sh
exit 0
`)

	popUp := model.PopUpModel.(*GitRemotePushPopUpModel)
	popUp.IsProcessing.Store(true)
	MarkGitRemotePushPopUpReconciling(popUp)

	view := RenderGitRemotePushPopUp(model)
	if !strings.Contains(view, i18n.LANGUAGEMAPPING.GitPushPopUpReconciling) {
		t.Errorf("reconciling popup =\n%s\nmissing the reconciliation stage text", view)
	}
	if strings.Contains(view, i18n.LANGUAGEMAPPING.GitRemotePushPopUpProcessing) {
		t.Errorf("reconciling popup still shows the plain processing text:\n%s", view)
	}

	// the final result ends the reconciliation stage
	UpdateGitPushResultEvent(model, types.GitPushResultEventDataStructure{
		Success: true,
		Result:  gitapi.EmptyGitPushResult(),
		Attempt: popUp.ActivePushAttemptID.Load(),
		Refresh: &api.PostPushRefreshResult{Refreshed: true},
	})
	if popUp.IsReconciling.Load() {
		t.Error("the final result did not end the reconciliation stage")
	}
}

// ------------------------------------
//
//	A successful push plus a successful reconciliation renders the refresh
//	success statement in the completed diagnostics.
//
// ------------------------------------
func TestCompletedPopUpShowsReconciledRefreshOutcome(t *testing.T) {
	model, gitCommit, _, _, _ := pushPopupModelUnderTest(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    push) exit 0 ;;
  esac
done
exit 0
`)

	result := gitCommit.GitPush(context.Background(), "origin", gitapi.PUSH, "master")
	if !result.Success() {
		t.Fatalf("the scripted push did not succeed: exit %d, err %v", result.ExitCode(), result.Err())
	}

	popUp := model.PopUpModel.(*GitRemotePushPopUpModel)
	UpdateGitPushResultEvent(model, types.GitPushResultEventDataStructure{
		Success: true,
		Result:  result,
		Attempt: popUp.ActivePushAttemptID.Load(),
		Refresh: &api.PostPushRefreshResult{Refreshed: true},
	})

	if !popUp.ProcessSuccess.Load() || popUp.HasError.Load() {
		t.Errorf("flags = success %v, error %v, want a successful push", popUp.ProcessSuccess.Load(), popUp.HasError.Load())
	}
	view := unpadView(popUp.GitRemotePushOutputViewport.View())
	if !strings.Contains(view, i18n.LANGUAGEMAPPING.GitPushPopUpRefreshSucceeded) {
		t.Errorf("completed popup =\n%s\nmissing the reconciled refresh statement", view)
	}
	if strings.Contains(view, i18n.LANGUAGEMAPPING.GitPushPopUpRefreshFailed[:10]) {
		t.Errorf("completed popup shows a refresh warning for a successful reconciliation:\n%s", view)
	}
}

// ------------------------------------
//
//	Git succeeded but the reconciliation failed: the popup keeps the success
//	statement, adds a visible refresh warning naming the failed domains, and
//	never relabels the successful push as failed.
//
// ------------------------------------
func TestCompletedPopUpShowsRefreshWarningWithoutFailingThePush(t *testing.T) {
	model, gitCommit, _, _, _ := pushPopupModelUnderTest(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    push) exit 0 ;;
  esac
done
exit 0
`)

	result := gitCommit.GitPush(context.Background(), "origin", gitapi.PUSH, "master")
	if !result.Success() {
		t.Fatalf("the scripted push did not succeed: exit %d, err %v", result.ExitCode(), result.Err())
	}

	popUp := model.PopUpModel.(*GitRemotePushPopUpModel)
	UpdateGitPushResultEvent(model, types.GitPushResultEventDataStructure{
		Success: true,
		Result:  result,
		Attempt: popUp.ActivePushAttemptID.Load(),
		Refresh: &api.PostPushRefreshResult{
			Refreshed:     false,
			FailedDomains: []string{"branch", "commit log"},
			Errors:        []error{errors.New("branch read failed"), errors.New("commit log read failed")},
		},
	})

	// the push is still a success: green state, no error flag
	if !popUp.ProcessSuccess.Load() || popUp.HasError.Load() {
		t.Errorf("the refresh failure relabelled the successful push: success %v, error %v",
			popUp.ProcessSuccess.Load(), popUp.HasError.Load())
	}

	view := unpadView(popUp.GitRemotePushOutputViewport.View())
	if !strings.Contains(view, i18n.LANGUAGEMAPPING.GitPushPopUpPushSucceeded) {
		t.Errorf("completed popup lost the push success statement:\n%s", view)
	}
	if strings.Contains(view, i18n.LANGUAGEMAPPING.GitPushPopUpRefreshSucceeded) {
		t.Errorf("completed popup claims the state was reconciled:\n%s", view)
	}
	if !strings.Contains(view, i18n.LANGUAGEMAPPING.GitPushPopUpRefreshFailed[:10]) {
		t.Errorf("completed popup missing the refresh warning:\n%s", view)
	}
	for _, domain := range []string{"branch", "commit log"} {
		if !strings.Contains(view, domain) {
			t.Errorf("refresh warning does not name the failed domain %q:\n%s", domain, view)
		}
	}
}
