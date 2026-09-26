package git

// Independently authored additive regression coverage for phase 1.1.1 of
// docs/devel/13-harden-push-cancellation-and-partial-upstream-state.md:
//
//	Preserve causal direct-push outcomes and owned-pipe cleanup.
//
// These tests are written from the specification's acceptance criteria
// without reference to the implementation's internal helpers. Every test
// exercises only the observable GitPush contract (Started, Cancelled,
// ExitCode, Success, Err, Stdout, Stderr, Argv, WorkingDirectory) plus
// directly observable descriptor accounting. Ordering uses explicit file
// and progress signals; no test depends on wall-clock sleeps for
// correctness, and every spawned descendant is released and reaped by
// teardown. Same-package fixtures from the retained api/git/commit_test.go
// (gitCommitUnderTestWithFakeGit, pushRouteUnderTest,
// argvCarriesOperationArgs, fakeGitOnPath) are reused; all other helpers
// below carry a phase111 prefix so this file is self-contained.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// phase111ProbeHeader answers the generation-executor upstream probe and
// rev/count reads so the push route reaches process start without a
// remote.
const phase111ProbeHeader = `case "$*" in
  *"--abbrev-ref HEAD"*) echo "master"; exit 0 ;;
  *"config --get"*) echo "origin"; exit 0 ;;
esac
for a in "$@"; do
  case "$a" in
    rev-parse) echo "origin/master"; exit 0 ;;
    rev-list) echo "0 0"; exit 0 ;;
`

func phase111WaitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatalf("timed out waiting for synchronization file %q", path)
		}
	}
}

func phase111WaitForPushLines(t *testing.T, gc *GitCommit, wantStdout, wantStderr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		stdoutLines, stderrLines := gc.GitRemotePushOutput()
		haveStdout := wantStdout == ""
		haveStderr := wantStderr == ""
		for _, line := range stdoutLines {
			if wantStdout != "" && strings.Contains(line, wantStdout) {
				haveStdout = true
				break
			}
		}
		for _, line := range stderrLines {
			if wantStderr != "" && strings.Contains(line, wantStderr) {
				haveStderr = true
				break
			}
		}
		if haveStdout && haveStderr {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for push progress lines stdout %q stderr %q", wantStdout, wantStderr)
}

func phase111RunBounded(t *testing.T, gc *GitCommit, ctx context.Context, route GitPushRoute, what string) GitPushResult {
	t.Helper()
	done := make(chan GitPushResult, 1)
	go func() { done <- gc.GitPush(ctx, route) }()
	select {
	case result := <-done:
		return result
	case <-time.After(15 * time.Second):
		t.Fatalf("GitPush did not return %s", what)
		return GitPushResult{}
	}
}

// phase111ReleaseDescendant best-effort releases a holder descendant; it
// never fails the test so deferred teardown cannot mask the verdict.
func phase111ReleaseDescendant(release, done string) {
	_ = os.WriteFile(release, []byte{}, 0o600)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(done); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// phase111RequireDescendantExit proves no descendant leak for a holder
// that was proven to exist.
func phase111RequireDescendantExit(t *testing.T, release, done string) {
	t.Helper()
	if err := os.WriteFile(release, []byte{}, 0o600); err != nil {
		t.Fatalf("releasing the descendant holder: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(done); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("the descendant did not exit after its release, suggesting a leaked process still holding push descriptors (missing %q)", done)
}

func phase111CountFDs(t *testing.T) int {
	t.Helper()
	for _, dir := range []string{"/proc/self/fd", "/dev/fd"} {
		if entries, err := os.ReadDir(dir); err == nil {
			return len(entries)
		}
	}
	t.Skip("no file-descriptor enumeration available on this platform")
	return 0
}

// TestPhase111IndependentSigTermSurvivesLateCancel pins criterion 1 with
// a SIGTERM (rather than SIGKILL) independent death: the direct push
// terminates itself by signal while a descendant holds the inherited
// descriptors open, the single reaper observes the death (reaped marker)
// while the completion notification still cannot be consumed (drain held
// open), and only then does the context cancel. The outcome must stay a
// non-cancelled process failure with the real Wait error/status,
// retained streams and no context-cancellation contamination.
func TestPhase111IndependentSigTermSurvivesLateCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the signal-specific push fixture requires POSIX shell signals")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+phase111ProbeHeader+`    push)
      echo "sigterm-stdout-marker"
      echo "sigterm-stderr-marker" >&2
      parent=$$
      ( while kill -0 "$parent" 2>/dev/null; do
          state=$(ps -o stat= -p "$parent" 2>/dev/null)
          case "$state" in Z*) break ;; esac
        done
        touch "$FAKE_GIT_PARENT_DEAD"
        while kill -0 "$parent" 2>/dev/null; do :; done
        touch "$FAKE_GIT_PARENT_REAPED"
        while [ ! -e "$FAKE_GIT_RELEASE_DESCENDANT" ]; do :; done
        touch "$FAKE_GIT_DESCENDANT_DONE"
      ) &
      kill -TERM "$parent"
      ;;
  esac
done
exit 0
`)
	parentDead := filepath.Join(root, "phase111-sigterm-parent-dead")
	parentReaped := filepath.Join(root, "phase111-sigterm-parent-reaped")
	releaseDescendant := filepath.Join(root, "phase111-sigterm-release")
	descendantDone := filepath.Join(root, "phase111-sigterm-done")
	t.Setenv("FAKE_GIT_PARENT_DEAD", parentDead)
	t.Setenv("FAKE_GIT_PARENT_REAPED", parentReaped)
	t.Setenv("FAKE_GIT_RELEASE_DESCENDANT", releaseDescendant)
	t.Setenv("FAKE_GIT_DESCENDANT_DONE", descendantDone)
	defer phase111ReleaseDescendant(releaseDescendant, descendantDone)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	// The direct child died by its own signal before any cancellation:
	// zombie observed, then PID disappearance proves the reaper already
	// returned while the held-open drain still blocks EOF. Readers
	// consumed both pre-exit bytes before the late cancel arrives.
	phase111WaitForFile(t, parentDead)
	phase111WaitForFile(t, parentReaped)
	phase111WaitForPushLines(t, gitCommit, "sigterm-stdout-marker", "sigterm-stderr-marker")
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the signal-terminated process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("an independently signal-terminated push must not be reported as cancelled after a later context cancellation")
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the real direct-process *exec.ExitError", result.Err())
		} else {
			if result.ExitCode() != exitErr.ExitCode() {
				t.Errorf("ExitCode() = %d, want the reaped signal status %d", result.ExitCode(), exitErr.ExitCode())
			}
			if result.ExitCode() != -1 {
				t.Errorf("ExitCode() = %d, want -1 for the signal-terminated direct process", result.ExitCode())
			}
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not join the late context cancellation for an independent failure", result.Err())
		}
		if result.Success() {
			t.Error("a signal-terminated push must not be reported as a success")
		}
		if !strings.Contains(string(result.Stdout()), "sigterm-stdout-marker") {
			t.Errorf("independent failure lost retained stdout: %q", result.Stdout())
		}
		if !strings.Contains(string(result.Stderr()), "sigterm-stderr-marker") {
			t.Errorf("independent failure lost retained stderr: %q", result.Stderr())
		}
		if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
			t.Errorf("argv = %v, want the tracked-branch operation arguments", result.Argv())
		}
		phase111RequireDescendantExit(t, releaseDescendant, descendantDone)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the independently signal-terminated drain")
	}
}

// TestPhase111LiveCancelJoinsCauseKeepsStatusAndOutput pins criterion 2:
// cancellation of a provably still-running direct push reports cancelled
// while retaining the reaped process status/error, the context cause and
// the output captured before termination.
func TestPhase111LiveCancelJoinsCauseKeepsStatusAndOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+phase111ProbeHeader+`    push)
      echo "livecancel-stdout-marker"
      echo "livecancel-stderr-marker" >&2
      touch "$FAKE_GIT_READY"
      ( while [ ! -e "$FAKE_GIT_RELEASE_DESCENDANT" ]; do :; done
        touch "$FAKE_GIT_DESCENDANT_DONE"
      ) &
      exec sleep 30
      ;;
  esac
done
exit 0
`)
	ready := filepath.Join(root, "phase111-livecancel-ready")
	releaseDescendant := filepath.Join(root, "phase111-livecancel-release")
	descendantDone := filepath.Join(root, "phase111-livecancel-done")
	t.Setenv("FAKE_GIT_READY", ready)
	t.Setenv("FAKE_GIT_RELEASE_DESCENDANT", releaseDescendant)
	t.Setenv("FAKE_GIT_DESCENDANT_DONE", descendantDone)
	defer phase111ReleaseDescendant(releaseDescendant, descendantDone)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	// The direct child is provably still running: it touched readiness
	// and both readers consumed its pre-cancel bytes.
	phase111WaitForFile(t, ready)
	phase111WaitForPushLines(t, gitCommit, "livecancel-stdout-marker", "livecancel-stderr-marker")
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the live process had started before the cancellation")
		}
		if !result.Cancelled() {
			t.Error("cancellation that terminates a still-running direct push must report cancelled")
		}
		if result.Success() {
			t.Error("a cancelled push must not be reported as a success")
		}
		if !errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, want the joined context-cancellation cause", result.Err())
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the reaped process *exec.ExitError alongside the context cause", result.Err())
		} else if result.ExitCode() != exitErr.ExitCode() {
			t.Errorf("ExitCode() = %d, want the reaped process status %d", result.ExitCode(), exitErr.ExitCode())
		}
		if !strings.Contains(string(result.Stdout()), "livecancel-stdout-marker") {
			t.Errorf("cancelled push lost captured stdout: %q", result.Stdout())
		}
		if !strings.Contains(string(result.Stderr()), "livecancel-stderr-marker") {
			t.Errorf("cancelled push lost captured stderr: %q", result.Stderr())
		}
		phase111RequireDescendantExit(t, releaseDescendant, descendantDone)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after cancellation of the still-running direct push")
	}
}

// TestPhase111ZeroExitLateDrainKeepsSuccess pins the first half of
// criterion 3: a direct process that completed successfully before a
// late drain cancellation is not falsely marked killed. Success stays
// true with exit 0 even if Err carries a stream-capture failure from the
// cancellation-time pipe close; only success-only reconciliation may
// follow.
func TestPhase111ZeroExitLateDrainKeepsSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+phase111ProbeHeader+`    push)
      echo "zerosuccess-stdout-marker"
      echo "zerosuccess-stderr-marker" >&2
      ( touch "$FAKE_GIT_DESCENDANT_STARTED"
        while [ ! -e "$FAKE_GIT_RELEASE_DESCENDANT" ]; do :; done
        touch "$FAKE_GIT_DESCENDANT_DONE"
      ) &
      touch "$FAKE_GIT_CHILD_DONE"
      exit 0
      ;;
  esac
done
exit 0
`)
	childDone := filepath.Join(root, "phase111-zerosuccess-child-done")
	descendantStarted := filepath.Join(root, "phase111-zerosuccess-started")
	releaseDescendant := filepath.Join(root, "phase111-zerosuccess-release")
	descendantDone := filepath.Join(root, "phase111-zerosuccess-done")
	t.Setenv("FAKE_GIT_CHILD_DONE", childDone)
	t.Setenv("FAKE_GIT_DESCENDANT_STARTED", descendantStarted)
	t.Setenv("FAKE_GIT_RELEASE_DESCENDANT", releaseDescendant)
	t.Setenv("FAKE_GIT_DESCENDANT_DONE", descendantDone)
	defer phase111ReleaseDescendant(releaseDescendant, descendantDone)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	// The zero-exit child already finished and its holder exists, so the
	// drain is held open by the descendant alone; readers consumed the
	// success bytes before the late cancellation arrives.
	phase111WaitForFile(t, childDone)
	phase111WaitForFile(t, descendantStarted)
	phase111WaitForPushLines(t, gitCommit, "zerosuccess-stdout-marker", "zerosuccess-stderr-marker")
	cancel()

	select {
	case result := <-done:
		if !result.Started() {
			t.Error("the successful process started, so Started() must be true")
		}
		if result.Cancelled() {
			t.Error("a zero-exit process completed before the late drain cancellation must not be marked killed")
		}
		if result.ExitCode() != 0 {
			t.Errorf("ExitCode() = %d, want 0 for the independently successful direct process", result.ExitCode())
		}
		if !result.Success() {
			t.Errorf("Success() = false, want true even though Err() = %v", result.Err())
		}
		if errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, must not join the late context cancellation for a completed success", result.Err())
		}
		if !strings.Contains(string(result.Stdout()), "zerosuccess-stdout-marker") {
			t.Errorf("completed success lost retained stdout: %q", result.Stdout())
		}
		if !strings.Contains(string(result.Stderr()), "zerosuccess-stderr-marker") {
			t.Errorf("completed success lost retained stderr: %q", result.Stderr())
		}
		phase111RequireDescendantExit(t, releaseDescendant, descendantDone)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late drain cancellation of the completed success")
	}
}

// TestPhase111NonzeroExitNeverRelabelledAndPreStartUnstarted pins the
// rest of criterion 3: an independently failed exit keeps its status
// across a late cancellation, and a pre-start cancellation stays
// unstarted and cancelled with exit -1.
func TestPhase111NonzeroExitNeverRelabelledAndPreStartUnstarted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the POSIX shell push fixture is not available on Windows")
	}
	t.Run("nonzero exit survives late cancellation", func(t *testing.T) {
		gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+phase111ProbeHeader+`    push)
      echo "nonzero42-stdout-marker"
      echo "nonzero42-stderr-marker" >&2
      ( while [ ! -e "$FAKE_GIT_RELEASE_DESCENDANT" ]; do :; done
        touch "$FAKE_GIT_DESCENDANT_DONE"
      ) &
      touch "$FAKE_GIT_CHILD_DONE"
      exit 42
      ;;
  esac
done
exit 0
`)
		childDone := filepath.Join(root, "phase111-nonzero42-done")
		releaseDescendant := filepath.Join(root, "phase111-nonzero42-release")
		descendantDone := filepath.Join(root, "phase111-nonzero42-descendant-done")
		t.Setenv("FAKE_GIT_CHILD_DONE", childDone)
		t.Setenv("FAKE_GIT_RELEASE_DESCENDANT", releaseDescendant)
		t.Setenv("FAKE_GIT_DESCENDANT_DONE", descendantDone)
		defer phase111ReleaseDescendant(releaseDescendant, descendantDone)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan GitPushResult, 1)
		go func() { done <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

		phase111WaitForFile(t, childDone)
		phase111WaitForPushLines(t, gitCommit, "nonzero42-stdout-marker", "nonzero42-stderr-marker")
		cancel()

		select {
		case result := <-done:
			var exitErr *exec.ExitError
			if result.Cancelled() {
				t.Error("an independently failed exit must never be relabelled cancelled by a late cancellation")
			}
			if result.ExitCode() != 42 {
				t.Errorf("ExitCode() = %d, want the independent 42 status", result.ExitCode())
			}
			if !errors.As(result.Err(), &exitErr) {
				t.Errorf("Err() = %v, want the direct-process *exec.ExitError", result.Err())
			}
			if result.Success() {
				t.Error("a nonzero exit must not be reported as a success")
			}
			phase111RequireDescendantExit(t, releaseDescendant, descendantDone)
		case <-time.After(15 * time.Second):
			t.Fatal("GitPush did not return after late cancellation of the independently failed drain")
		}
	})

	t.Run("pre-start cancellation is unstarted and cancelled", func(t *testing.T) {
		gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+phase111ProbeHeader+`    push)
      touch "$FAKE_GIT_PUSH_RAN"
      exit 0
      ;;
  esac
done
exit 0
`)
		pushRan := filepath.Join(root, "phase111-prestart-push-ran")
		t.Setenv("FAKE_GIT_PUSH_RAN", pushRan)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		route := pushRouteUnderTest("master")
		route.ActiveGuard = func() error { return nil }
		result := phase111RunBounded(t, gitCommit, ctx, route, "after the pre-start cancellation")
		if result.Started() {
			t.Error("a pre-start cancellation must not start the prepared process")
		}
		if !result.Cancelled() {
			t.Error("a pre-start cancellation must report cancelled")
		}
		if result.ExitCode() != -1 {
			t.Errorf("ExitCode() = %d, want -1 for the absent process status", result.ExitCode())
		}
		if !errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, want the pre-start context cancellation", result.Err())
		}
		if result.Success() {
			t.Error("a pre-start cancellation must not be reported as a success")
		}
		if _, err := os.Stat(pushRan); err == nil {
			t.Error("the push process launched although the context was already cancelled before Start")
		} else if !os.IsNotExist(err) {
			t.Errorf("checking the push launch marker: %v", err)
		}
	})
}

// TestPhase111ConcurrentDrainArgvAndImmutableAccessors pins criterion 4:
// both streams drain concurrently to completion, argv keeps the existing
// tracked-branch behavior, and result accessors return defensive copies.
func TestPhase111ConcurrentDrainArgvAndImmutableAccessors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the POSIX shell push fixture is not available on Windows")
	}
	gitCommit, _, _, _, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+phase111ProbeHeader+`    push)
      i=1
      while [ "$i" -le 50 ]; do
        echo "phase111-interleaved-stdout-$i"
        echo "phase111-interleaved-stderr-$i" >&2
        i=$((i + 1))
      done
      exit 0
      ;;
  esac
done
exit 0
`)
	result := phase111RunBounded(t, gitCommit, context.Background(), pushRouteUnderTest("master"), "after the interleaved ordinary push")
	if !result.Started() || result.Cancelled() || result.ExitCode() != 0 || !result.Success() {
		t.Fatalf("ordinary push result = started=%v cancelled=%v exit=%d success=%v err=%v, want a clean success",
			result.Started(), result.Cancelled(), result.ExitCode(), result.Success(), result.Err())
	}
	if result.Err() != nil {
		t.Errorf("ordinary push Err() = %v, want nil when both streams drain cleanly", result.Err())
	}
	for _, want := range []string{"phase111-interleaved-stdout-1", "phase111-interleaved-stdout-50"} {
		if !strings.Contains(string(result.Stdout()), want) {
			t.Errorf("concurrent drain lost stdout volume around %q (%d bytes retained)", want, len(result.Stdout()))
		}
	}
	for _, want := range []string{"phase111-interleaved-stderr-1", "phase111-interleaved-stderr-50"} {
		if !strings.Contains(string(result.Stderr()), want) {
			t.Errorf("concurrent drain lost stderr volume around %q (%d bytes retained)", want, len(result.Stderr()))
		}
	}
	if !argvCarriesOperationArgs(result.Argv(), []string{"push", "--progress", "origin"}) {
		t.Errorf("argv = %v, want the unchanged tracked-branch operation arguments", result.Argv())
	}
	// Immutable accessors: mutating a returned copy must not affect the
	// stored result.
	argv := result.Argv()
	if len(argv) == 0 {
		t.Fatal("argv must not be empty for a started push")
	}
	argv[0] = "mutated"
	if result.Argv()[0] == "mutated" {
		t.Error("Argv() shares storage with the stored result instead of returning a copy")
	}
	stdout := result.Stdout()
	if len(stdout) == 0 {
		t.Fatal("retained stdout must not be empty for the interleaved push")
	}
	origStdout0 := result.Stdout()[0]
	stdout[0] ^= 0xff
	if result.Stdout()[0] != origStdout0 {
		t.Error("Stdout() shares storage with the stored result instead of returning a copy")
	}
	stderr := result.Stderr()
	if len(stderr) == 0 {
		t.Fatal("retained stderr must not be empty for the interleaved push")
	}
	origStderr0 := result.Stderr()[0]
	stderr[0] ^= 0xff
	if result.Stderr()[0] != origStderr0 {
		t.Error("Stderr() shares storage with the stored result instead of returning a copy")
	}
	// Re-read both streams and confirm the stored bytes still carry the
	// original markers.
	if !strings.Contains(string(result.Stdout()), "phase111-interleaved-stdout-1") {
		t.Error("Stdout() shares storage with the stored result instead of returning a copy")
	}
	if !strings.Contains(string(result.Stderr()), "phase111-interleaved-stderr-1") {
		t.Error("Stderr() shares storage with the stored result instead of returning a copy")
	}
}

// TestPhase111FailingPsDiagnosticKeepsLiveKill pins the first half of R3:
// a ps diagnostic that completes with a nonzero exit and stderr while
// the direct child is alive is unknown liveness, not positive child
// death, so cancellation of the still-live child must still terminate it
// and report cancelled rather than hang or skip termination.
func TestPhase111FailingPsDiagnosticKeepsLiveKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the ps-diagnostic fallback path is POSIX-specific")
	}
	gitCommit, _, _, root, gitPath := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+phase111ProbeHeader+`    push)
      echo "r3failps-stdout-marker"
      echo "r3failps-stderr-marker" >&2
      touch "$FAKE_GIT_READY"
      ( while [ ! -e "$FAKE_GIT_RELEASE_DESCENDANT" ]; do :; done
        touch "$FAKE_GIT_DESCENDANT_DONE"
      ) &
      exec sleep 30
      ;;
  esac
done
exit 0
`)
	if err := os.WriteFile(filepath.Join(filepath.Dir(gitPath), "ps"),
		[]byte("#!/bin/sh\necho \"ps: cannot examine process\" >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("shadowing the ps diagnostic: %v", err)
	}
	ready := filepath.Join(root, "phase111-failps-ready")
	releaseDescendant := filepath.Join(root, "phase111-failps-release")
	descendantDone := filepath.Join(root, "phase111-failps-done")
	t.Setenv("FAKE_GIT_READY", ready)
	t.Setenv("FAKE_GIT_RELEASE_DESCENDANT", releaseDescendant)
	t.Setenv("FAKE_GIT_DESCENDANT_DONE", descendantDone)
	defer phase111ReleaseDescendant(releaseDescendant, descendantDone)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	phase111WaitForFile(t, ready)
	phase111WaitForPushLines(t, gitCommit, "r3failps-stdout-marker", "r3failps-stderr-marker")
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if !result.Started() {
			t.Error("the live process had started before the cancellation")
		}
		if !result.Cancelled() {
			t.Error("a failing ps diagnostic must be unknown liveness: the still-live child must still be terminated and reported cancelled")
		}
		if !errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, want the context-cancellation cause for the live kill", result.Err())
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the reaped process *exec.ExitError", result.Err())
		}
		phase111RequireDescendantExit(t, releaseDescendant, descendantDone)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush hung after cancellation with a failing ps diagnostic: the live kill must not be skipped")
	}
}

// TestPhase111SignalledPsDiagnosticKeepsLiveKill pins the second half of
// R3: a ps diagnostic terminated by a signal proves nothing about the
// target, so the still-live direct child must still be terminated.
func TestPhase111SignalledPsDiagnosticKeepsLiveKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the ps-diagnostic fallback path is POSIX-specific")
	}
	gitCommit, _, _, root, gitPath := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+phase111ProbeHeader+`    push)
      echo "r3sigps-stdout-marker"
      echo "r3sigps-stderr-marker" >&2
      touch "$FAKE_GIT_READY"
      ( while [ ! -e "$FAKE_GIT_RELEASE_DESCENDANT" ]; do :; done
        touch "$FAKE_GIT_DESCENDANT_DONE"
      ) &
      exec sleep 30
      ;;
  esac
done
exit 0
`)
	if err := os.WriteFile(filepath.Join(filepath.Dir(gitPath), "ps"),
		[]byte("#!/bin/sh\nkill -TERM $$\n"), 0o755); err != nil {
		t.Fatalf("shadowing the ps diagnostic: %v", err)
	}
	ready := filepath.Join(root, "phase111-sigps-ready")
	releaseDescendant := filepath.Join(root, "phase111-sigps-release")
	descendantDone := filepath.Join(root, "phase111-sigps-done")
	t.Setenv("FAKE_GIT_READY", ready)
	t.Setenv("FAKE_GIT_RELEASE_DESCENDANT", releaseDescendant)
	t.Setenv("FAKE_GIT_DESCENDANT_DONE", descendantDone)
	defer phase111ReleaseDescendant(releaseDescendant, descendantDone)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	phase111WaitForFile(t, ready)
	phase111WaitForPushLines(t, gitCommit, "r3sigps-stdout-marker", "r3sigps-stderr-marker")
	cancel()

	select {
	case result := <-done:
		if !result.Cancelled() {
			t.Error("a signal-terminated ps diagnostic must be unknown liveness: the still-live child must still be terminated and reported cancelled")
		}
		if !errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, want the context-cancellation cause for the live kill", result.Err())
		}
		phase111RequireDescendantExit(t, releaseDescendant, descendantDone)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush hung after cancellation with a signal-terminated ps diagnostic: the live kill must not be skipped")
	}
}

// TestPhase111AmbiguousSelfSigKillStaysObserved pins the R3 remainder:
// an ambiguous independent SIGKILL (self-inflicted, racing the
// probe-to-Kill window) remains a non-cancelled observed outcome with
// its reaped status, even when cancellation arrives before the drain
// can reach EOF.
func TestPhase111AmbiguousSelfSigKillStaysObserved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the signal-specific push fixture requires POSIX shell signals")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+phase111ProbeHeader+`    push)
      echo "r3ambig-stdout-marker"
      echo "r3ambig-stderr-marker" >&2
      parent=$$
      ( while kill -0 "$parent" 2>/dev/null; do :; done
        touch "$FAKE_GIT_PARENT_GONE"
        while [ ! -e "$FAKE_GIT_RELEASE_DESCENDANT" ]; do :; done
        touch "$FAKE_GIT_DESCENDANT_DONE"
      ) &
      kill -KILL "$parent"
      ;;
  esac
done
exit 0
`)
	parentGone := filepath.Join(root, "phase111-ambig-parent-gone")
	releaseDescendant := filepath.Join(root, "phase111-ambig-release")
	descendantDone := filepath.Join(root, "phase111-ambig-done")
	t.Setenv("FAKE_GIT_PARENT_GONE", parentGone)
	t.Setenv("FAKE_GIT_RELEASE_DESCENDANT", releaseDescendant)
	t.Setenv("FAKE_GIT_DESCENDANT_DONE", descendantDone)
	defer phase111ReleaseDescendant(releaseDescendant, descendantDone)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	// The direct child is independently dead (PID gone) while its
	// descendant still holds the drain open; readers consumed the
	// pre-death bytes before the late cancellation arrives.
	phase111WaitForFile(t, parentGone)
	phase111WaitForPushLines(t, gitCommit, "r3ambig-stdout-marker", "r3ambig-stderr-marker")
	cancel()

	select {
	case result := <-done:
		var exitErr *exec.ExitError
		if result.Cancelled() {
			t.Error("an ambiguous independent SIGKILL must remain a non-cancelled observed outcome, not a cancellation claim")
		}
		if !errors.As(result.Err(), &exitErr) {
			t.Errorf("Err() = %v, want the reaped direct-process *exec.ExitError", result.Err())
		} else if result.ExitCode() != exitErr.ExitCode() {
			t.Errorf("ExitCode() = %d, want the reaped status %d", result.ExitCode(), exitErr.ExitCode())
		}
		if result.Success() {
			t.Error("an independently killed push must not be reported as a success")
		}
		if !strings.Contains(string(result.Stdout()), "r3ambig-stdout-marker") {
			t.Errorf("observed outcome lost retained stdout: %q", result.Stdout())
		}
		phase111RequireDescendantExit(t, releaseDescendant, descendantDone)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after late cancellation of the independently SIGKILLed drain")
	}
}

// TestPhase111OrdinaryCompletionClosesOwnedDescriptors pins the first
// half of R4: ordinary successful and failed push completion closes both
// owned pipe read ends without relying on garbage collection, observed
// directly through descriptor accounting across repeated normal returns.
func TestPhase111OrdinaryCompletionClosesOwnedDescriptors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file-descriptor accounting requires POSIX /dev/fd or /proc/self/fd")
	}
	newCommitter := func(t *testing.T, exitStmt string) *GitCommit {
		t.Helper()
		gc, _, _, _, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+phase111ProbeHeader+`    push)
      echo "r4fd-stdout-marker"
      echo "r4fd-stderr-marker" >&2
      `+exitStmt+`
      ;;
  esac
done
exit 0
`)
		return gc
	}
	for _, exitStmt := range []string{"exit 0", "exit 3"} {
		gc := newCommitter(t, exitStmt)
		before := phase111CountFDs(t)
		for i := 0; i < 3; i++ {
			result := phase111RunBounded(t, gc, context.Background(), pushRouteUnderTest("master"), "after the ordinary push")
			if !result.Started() {
				t.Fatal("the ordinary push must start its process")
			}
			if result.Cancelled() {
				t.Fatalf("ordinary push (exit %q) must not report cancelled", exitStmt)
			}
			if !strings.Contains(string(result.Stdout()), "r4fd-stdout-marker") {
				t.Fatalf("ordinary push lost retained stdout: %q", result.Stdout())
			}
			if !strings.Contains(string(result.Stderr()), "r4fd-stderr-marker") {
				t.Fatalf("ordinary push lost retained stderr: %q", result.Stderr())
			}
		}
		if delta := phase111CountFDs(t) - before; delta > 2 {
			t.Errorf("exit %q: open descriptors grew by %d across three ordinary pushes, want both owned read ends closed on every normal return", exitStmt, delta)
		}
	}
}

// TestPhase111CancelBoundsDrainRetainsPreCloseBytes pins the second half
// of R4: cancellation bounds the inherited-descriptor drain (the call
// returns promptly) and retains the bytes read before closure.
func TestPhase111CancelBoundsDrainRetainsPreCloseBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the inherited-descriptor push fixture requires POSIX shell control")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+phase111ProbeHeader+`    push)
      echo "r4preclose-stdout-marker"
      echo "r4preclose-stderr-marker" >&2
      touch "$FAKE_GIT_READY"
      ( while [ ! -e "$FAKE_GIT_RELEASE_DESCENDANT" ]; do :; done
        touch "$FAKE_GIT_DESCENDANT_DONE"
      ) &
      exec sleep 30
      ;;
  esac
done
exit 0
`)
	ready := filepath.Join(root, "phase111-preclose-ready")
	releaseDescendant := filepath.Join(root, "phase111-preclose-release")
	descendantDone := filepath.Join(root, "phase111-preclose-done")
	t.Setenv("FAKE_GIT_READY", ready)
	t.Setenv("FAKE_GIT_RELEASE_DESCENDANT", releaseDescendant)
	t.Setenv("FAKE_GIT_DESCENDANT_DONE", descendantDone)
	defer phase111ReleaseDescendant(releaseDescendant, descendantDone)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, pushRouteUnderTest("master")) }()

	// Both pre-close bytes are provably in the readers before the
	// cancellation bounds the still-open drain.
	phase111WaitForFile(t, ready)
	phase111WaitForPushLines(t, gitCommit, "r4preclose-stdout-marker", "r4preclose-stderr-marker")
	cancel()

	select {
	case result := <-done:
		if !result.Cancelled() {
			t.Error("cancelling the still-running push must report cancelled")
		}
		if !strings.Contains(string(result.Stdout()), "r4preclose-stdout-marker") {
			t.Errorf("bounded drain lost pre-close stdout bytes: %q", result.Stdout())
		}
		if !strings.Contains(string(result.Stderr()), "r4preclose-stderr-marker") {
			t.Errorf("bounded drain lost pre-close stderr bytes: %q", result.Stderr())
		}
		phase111RequireDescendantExit(t, releaseDescendant, descendantDone)
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after cancellation: the inherited-descriptor drain was not bounded")
	}
}

// TestPhase111GuardTimeCancelIsUnstartedCancelled pins R5: a context
// cancelled while the ActiveGuard holds the pre-Start gate produces an
// unstarted cancelled result with exit -1, closes all setup pipe ends
// and launches no push.
func TestPhase111GuardTimeCancelIsUnstartedCancelled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file-descriptor accounting requires POSIX /dev/fd or /proc/self/fd")
	}
	gitCommit, _, _, root, _ := gitCommitUnderTestWithFakeGit(t, `#!/bin/sh
`+phase111ProbeHeader+`    push)
      touch "$FAKE_GIT_PUSH_RAN"
      exit 0
      ;;
  esac
done
exit 0
`)
	pushRan := filepath.Join(root, "phase111-guardtime-push-ran")
	guardEntered := filepath.Join(root, "phase111-guardtime-entered")
	t.Setenv("FAKE_GIT_PUSH_RAN", pushRan)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	route := pushRouteUnderTest("master")
	route.ActiveGuard = func() error {
		_ = os.WriteFile(guardEntered, []byte{}, 0o600)
		<-ctx.Done()
		return ctx.Err()
	}
	before := phase111CountFDs(t)
	done := make(chan GitPushResult, 1)
	go func() { done <- gitCommit.GitPush(ctx, route) }()

	// The guard provably holds the pre-Start gate before the context is
	// cancelled during it.
	phase111WaitForFile(t, guardEntered)
	cancel()

	select {
	case result := <-done:
		if result.Started() {
			t.Error("a guard-time cancellation must not start the prepared process")
		}
		if !result.Cancelled() {
			t.Error("a context cancelled during the guard must report cancelled, not a setup refusal")
		}
		if result.ExitCode() != -1 {
			t.Errorf("ExitCode() = %d, want -1 for the absent process status", result.ExitCode())
		}
		if !errors.Is(result.Err(), context.Canceled) {
			t.Errorf("Err() = %v, want the guard-time context cancellation", result.Err())
		}
		if result.Success() {
			t.Error("a guard-time cancellation must not be reported as a success")
		}
		if _, err := os.Stat(pushRan); err == nil {
			t.Error("the push process launched although the guard-time cancellation preceded Start")
		} else if !os.IsNotExist(err) {
			t.Errorf("checking the push launch marker: %v", err)
		}
		if delta := phase111CountFDs(t) - before; delta > 2 {
			t.Errorf("open descriptors grew by %d across the guard-time refusal, want all setup pipe ends closed", delta)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("GitPush did not return after the guard-time cancellation")
	}
}
