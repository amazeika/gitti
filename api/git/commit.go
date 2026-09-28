package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gohyuhan/gitti/executor"
	"github.com/gohyuhan/gitti/logging"
)

const (
	gitPushStdoutStream = "stdout"
	gitPushStderrStream = "stderr"

	// pushQueuedDrainGracePeriod bounds the final drain after context
	// cancellation. It retains only bytes already read before the timed
	// read-end closure. Closing the owned read ends immediately would race
	// with concurrent readers that have not yet consumed pipe-buffered
	// output. The grace window gives those readers a bounded opportunity
	// to retain pre-close bytes. Its expiry closes the read ends to unblock
	// a drain held open by an inherited writer. Bytes still unread when a
	// reader stays blocked past the cutoff may be lost: only bytes copied
	// before closure survive.
	pushQueuedDrainGracePeriod = 500 * time.Millisecond

	// pushCancelEscalationPeriod bounds the wait for the direct child to
	// terminate after the distinguishable cancellation signal before the
	// handshake escalates to SIGKILL. Escalation only keeps the handshake
	// bounded for a signal-resistant child; its SIGKILL status is never
	// cancellation evidence, so an escalated kill stays an observed
	// process outcome.
	pushCancelEscalationPeriod = 500 * time.Millisecond
)

type GitCommit struct {
	gitCommitOutput           []string
	gitCommitOutputMu         sync.RWMutex
	gitRemotePushStdoutLines  []string
	gitRemotePushStdoutCursor int
	gitRemotePushStderrLines  []string
	gitRemotePushStderrCursor int
	gitRemotePushOutputMu     sync.RWMutex
	gitPushResult             GitPushResult
	gitPushResultMu           sync.RWMutex
	updateChannel             chan string
	gitProcessLock            *GitProcessLock
	cmdExecutor               *executor.CmdExecutor // generation executor bound to the captured worktree
	logging                   *logging.GittiLogging
	// gitPushDrainPreRead, when set, is invoked by each push output stream
	// reader before its first read; tests use it to hold the readers until
	// after the child exits
	gitPushDrainPreRead func()
	// p1oWaitGateAck and p1oWaitGateRelease form the optional phase-1
	// ordered-fixture gate between direct Wait return and the buffered
	// completion-notification send. Both nil in normal use: the reaper
	// behaves exactly as without the gate and never blocks.
	p1oWaitGateMu      sync.Mutex
	p1oWaitGateAck     chan<- struct{}
	p1oWaitGateRelease <-chan struct{}
}

type LatestCommitMsgAndDesc struct {
	Message     string
	Description string
}

// ------------------------------------
//
//	Initialize the git commit handler with shared dependencies. The command
//	executor is the generation's scoped executor so the push routes run in
//	the captured worktree.
//
// ------------------------------------
func InitGitCommit(updateChannel chan string, gitProcessLock *GitProcessLock, cmdExecutor *executor.CmdExecutor, logging *logging.GittiLogging) *GitCommit {
	gitCommit := GitCommit{
		gitCommitOutput:          []string{},
		gitRemotePushStdoutLines: []string{},
		gitRemotePushStderrLines: []string{},
		updateChannel:            updateChannel,
		gitProcessLock:           gitProcessLock,
		cmdExecutor:              cmdExecutor,
		logging:                  logging,
	}

	return &gitCommit
}

// ------------------------------------
//
//	Return git commit output
//
// ------------------------------------
func (gc *GitCommit) GitCommitOutput() []string {
	gc.gitCommitOutputMu.RLock()
	defer gc.gitCommitOutputMu.RUnlock()

	copied := make([]string, len(gc.gitCommitOutput))
	copy(copied, gc.gitCommitOutput)
	return copied
}

// ------------------------------------
//
//	Return copies of the separately retained push progress lines for stdout and
//	stderr
//
// ------------------------------------
func (gc *GitCommit) GitRemotePushOutput() (stdoutLines, stderrLines []string) {
	gc.gitRemotePushOutputMu.RLock()
	defer gc.gitRemotePushOutputMu.RUnlock()

	stdoutLines = make([]string, len(gc.gitRemotePushStdoutLines))
	copy(stdoutLines, gc.gitRemotePushStdoutLines)
	stderrLines = make([]string, len(gc.gitRemotePushStderrLines))
	copy(stderrLines, gc.gitRemotePushStderrLines)
	return stdoutLines, stderrLines
}

// ------------------------------------
//
//	GitPushResult is the immutable, definitive outcome of one background git
//	push. It records the exact process argv and working directory and whether
//	the process started. Its exit code is the exact process status when one
//	exists, -1 otherwise. It also records cancellation and error state and
//	the separately retained stdout and stderr byte streams. Accessors return
//	copies so the API goroutine and the TUI never share output storage.
//
// ------------------------------------
type GitPushResult struct {
	argv             []string
	workingDirectory string
	started          bool
	exitCode         int
	cancelled        bool
	err              error
	stdout           []byte
	stderr           []byte
}

// ------------------------------------
//
//	Return a copy of the exact process argv the push executed with
//
// ------------------------------------
func (r GitPushResult) Argv() []string {
	argv := make([]string, len(r.argv))
	copy(argv, r.argv)
	return argv
}

// ------------------------------------
//
//	Return the working directory the push process was started in
//
// ------------------------------------
func (r GitPushResult) WorkingDirectory() string {
	return r.workingDirectory
}

// ------------------------------------
//
//	Report whether the push process was started
//
// ------------------------------------
func (r GitPushResult) Started() bool {
	return r.started
}

// ------------------------------------
//
//	Return the process exit code, -1 when no process status exists
//
// ------------------------------------
func (r GitPushResult) ExitCode() int {
	return r.exitCode
}

// ------------------------------------
//
//	Report whether the push was cancelled before completion
//
// ------------------------------------
func (r GitPushResult) Cancelled() bool {
	return r.cancelled
}

// ------------------------------------
//
//	Return the recorded setup, wait, or read error, if any
//
// ------------------------------------
func (r GitPushResult) Err() error {
	return r.err
}

// ------------------------------------
//
//	Return a copy of the retained stdout bytes
//
// ------------------------------------
func (r GitPushResult) Stdout() []byte {
	stdout := make([]byte, len(r.stdout))
	copy(stdout, r.stdout)
	return stdout
}

// ------------------------------------
//
//	Return a copy of the retained stderr bytes
//
// ------------------------------------
func (r GitPushResult) Stderr() []byte {
	stderr := make([]byte, len(r.stderr))
	copy(stderr, r.stderr)
	return stderr
}

// ------------------------------------
//
//	Report whether the push process completed with a zero exit status without
//	being cancelled
//
// ------------------------------------
func (r GitPushResult) Success() bool {
	return r.started && !r.cancelled && r.exitCode == 0
}

// ------------------------------------
//
//	Related to Git Commit
//
// ------------------------------------
func (gc *GitCommit) GitCommit(ctx context.Context, message, description string, isAmendCommit bool) int {
	if !gc.gitProcessLock.CanProceedWithGitOps() {
		return -1
	}

	defer func() {
		gc.gitProcessLock.ReleaseGitOpsLock()
	}()

	gc.ClearGitCommitOutput()
	gitArgs := []string{"commit", "-m", message}
	if isAmendCommit {
		gitArgs = []string{"commit", "--amend", "-m", message}
	}
	if utf8.RuneCountInString(description) > 0 {
		gitArgs = append(gitArgs, "-m", description)
	}

	commitCmd := executor.GittiCmdExecutor.RunGitCmdWithContext(ctx, gitArgs, true)

	// Combine stderr into stdout
	stdout, err := commitCmd.StdoutPipe()
	if err != nil {
		gc.logging.RegisterNewLog(logging.COMMIT_OPS, "", logging.ERROR, fmt.Sprintf("[PIPE ERROR]: %s", err.Error()), false)
		return -1
	}
	commitCmd.Stderr = commitCmd.Stdout

	// Start the process
	if err := commitCmd.Start(); err != nil {
		gc.logging.RegisterNewLog(logging.COMMIT_OPS, "", logging.ERROR, fmt.Sprintf("[START ERROR]: %s", err.Error()), false)
		return -1
	} else {
		gc.logging.RegisterNewLog(logging.COMMIT_OPS, strings.Join(gitArgs, " "), logging.INFO, "", true)
	}

	// Stream combined output
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		scanner.Split(splitOnCarriageReturnOrNewline)
		cursorIndex := 0
		lastSent := time.Time{}
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return // Stop immediately on cancel
			default:
				gc.gitCommitOutputMu.Lock()
				updatedCursorIndex, updatedGitCommitOutput := handleProgressOutputStream(cursorIndex, scanner, gc.gitCommitOutput)
				gc.gitCommitOutput = updatedGitCommitOutput
				cursorIndex = updatedCursorIndex
				gc.gitCommitOutputMu.Unlock()
				if time.Since(lastSent) >= STREAMUPDATETHROTTLEMS*time.Millisecond {
					if isAmendCommit {
						select {
						case gc.updateChannel <- GIT_AMEND_COMMIT_OUTPUT_UPDATE:
							lastSent = time.Now()
						default:
						}
					} else {
						select {
						case gc.updateChannel <- GIT_COMMIT_OUTPUT_UPDATE:
							lastSent = time.Now()
						default:
						}
					}
				}
			}
		}
		// trigger an update once it ends
		if isAmendCommit {
			gc.updateChannel <- GIT_AMEND_COMMIT_OUTPUT_UPDATE
		} else {
			gc.updateChannel <- GIT_COMMIT_OUTPUT_UPDATE
		}
	}()

	waitErr := commitCmd.Wait()
	wg.Wait()

	if ctx.Err() != nil {
		gc.logging.RegisterNewLog(logging.COMMIT_OPS, strings.Join(gitArgs, " "), logging.WARN, fmt.Sprintf("[%s CANCELLED]", logging.COMMIT_OPS), true)
		return -1
	}

	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			status := exitErr.ExitCode()
			gc.logging.RegisterNewLog(logging.COMMIT_OPS, strings.Join(gitArgs, " "), logging.ERROR, fmt.Sprintf("[%s ERROR]: %s", logging.COMMIT_OPS, waitErr.Error()), true)
			return status
		}
		gc.logging.RegisterNewLog(logging.COMMIT_OPS, "", logging.ERROR, fmt.Sprintf("[UNEXPECTED ERROR]: %s", waitErr.Error()), false)
		return -1
	}
	return 0
}

// ------------------------------------
//
//	Clear the stored commit output buffer
//
// ------------------------------------
func (gc *GitCommit) ClearGitCommitOutput() {
	gc.gitCommitOutputMu.Lock()
	defer gc.gitCommitOutputMu.Unlock()
	gc.gitCommitOutput = []string{}
}

// ------------------------------------
//
//	GitCommitWithSigning constructs a git commit command for terminal execution when signing is required.
//	When commit signing is enabled, gitti UI is suspended and the commit is executed directly in the terminal,
//	allowing the user to interact with the signing prompt (e.g., GPG passphrase).
//
// ------------------------------------
func (gc *GitCommit) GitCommitWithSigning(message, description string, isAmendCommit bool) []string {
	gitArgs := []string{"commit", "-m", message}
	if isAmendCommit {
		gitArgs = []string{"commit", "--amend", "-m", message}
	}
	if utf8.RuneCountInString(description) > 0 {
		gitArgs = append(gitArgs, "-m", description)
	}

	return gitArgs
}

// ------------------------------------
//
//	Retrieve the subject and body of the latest commit (HEAD) for pre-filling the amend commit input
//
// ------------------------------------
func (gc *GitCommit) GetLatestCommitMsgAndDesc() LatestCommitMsgAndDesc {
	gitArgs := []string{"log", "-1", "--pretty=format:%s%n%b", "HEAD"}
	latestCommitCmd := executor.GittiCmdExecutor.RunGitCmd(gitArgs, false)
	commitMsgAndDesc, cmdErr := latestCommitCmd.Output()
	gc.logging.RegisterNewLog(logging.GET_LATEST_COMMIT_MSG_AND_DESC_OPS, strings.Join(gitArgs, " "), logging.INFO, "", true)
	if cmdErr != nil {
		gc.logging.RegisterNewLog(logging.GET_LATEST_COMMIT_MSG_AND_DESC_OPS, strings.Join(gitArgs, " "), logging.ERROR, fmt.Sprintf("[%s ERROR]: %s", logging.GET_LATEST_COMMIT_MSG_AND_DESC_OPS, cmdErr.Error()), true)
		return LatestCommitMsgAndDesc{}
	}

	parsed := strings.SplitN(string(commitMsgAndDesc), "\n", 2)
	title := parsed[0]
	description := ""
	if len(parsed) > 1 {
		description = parsed[1]
	}

	return LatestCommitMsgAndDesc{
		Message:     title,
		Description: description,
	}
}

// ------------------------------------
//
//	Build the git push operation arguments for the selected push mode and
//	upstream state. Tracked branches push to the remote alone; untracked
//	branches add -u and the current branch so Git learns the upstream.
//
// ------------------------------------
func buildPushGitArgs(originName string, pushType string, currentCheckOutBranch string, hasUpstream bool) []string {
	gitArgs := []string{"push"}
	if !hasUpstream {
		gitArgs = []string{"push", "-u"}
	}
	switch pushType {
	case FORCEPUSHSAFE:
		gitArgs = append(gitArgs, []string{"--progress", "--force-with-lease", originName}...)
	case FORCEPUSHDANGEROUS:
		gitArgs = append(gitArgs, []string{"--progress", "--force", originName}...)
	default:
		gitArgs = append(gitArgs, []string{"--progress", originName}...)
	}

	// include the current checkout branch name at the end if there was no upstream so that git know which branch to push
	if !hasUpstream {
		gitArgs = append(gitArgs, currentCheckOutBranch)
	}

	return gitArgs
}

// ------------------------------------
//
//	buildPublishGitArgs builds the argv for the first push of an
//	unpublished branch. The branch is still unpublished, so --set-upstream
//	persists the branch-to-remote association; the source is pinned to HEAD
//	so the push publishes exactly the confirmed branch's tip. A publish
//	never carries a force flag.
//
// ------------------------------------
func buildPublishGitArgs(remoteName string) []string {
	return []string{"push", "--progress", "--set-upstream", remoteName, "HEAD"}
}

// ------------------------------------
//
//	confirmPushRemoteIsConfigured verifies through the generation executor
//	that the confirmed remote name still names a configured remote with a
//	URL, so a remote removed between the confirmation and the launch is
//	refused before any process starts instead of failing as a started push
//
// ------------------------------------
func (gc *GitCommit) confirmPushRemoteIsConfigured(remoteName string) error {
	cmd := gc.cmdExecutor.RunGitCmd([]string{"remote", "get-url", remoteName}, false)
	if _, err := cmd.Output(); err != nil {
		return fmt.Errorf("the push remote %q is not a configured remote with a URL", remoteName)
	}
	return nil
}

// ------------------------------------
//
//	prepareGitPush validates the confirmed route and builds its argv, shared
//	by the background route and the signing-required route.
//
//	The remote name is validated against argument-injection shapes before
//	any git state is read.
//
//	For a publish intent, the validation runs in order. The configured
//	remote check confirms the name still names a remote with a URL. The
//	upstream observation is re-resolved through the generation executor and
//	must still name the route's captured branch.
//
//	A still-unpublished branch publishes with --set-upstream. A branch that
//	gained its upstream since the confirmation proceeds as a normal push
//	without --set-upstream. Any other unsettled or unreadable state refuses
//	to start.
//
//	For a push intent, the same re-observation applies. A tracked branch
//	pushes without the -u flag, and an unpublished branch keeps the existing
//	semantics with the -u flag. Any other unsettled or unreadable state
//	refuses to start.
//
//	A nil error means the process may start with the returned argv. Any
//	error is an unstarted, actionable refusal.
//
// ------------------------------------
func (gc *GitCommit) prepareGitPush(route GitPushRoute) ([]string, error) {
	if gc.cmdExecutor == nil {
		return nil, errors.New("the push route has no worktree-bound command executor")
	}
	if route.RemoteName == "" || strings.HasPrefix(route.RemoteName, "-") {
		return nil, fmt.Errorf("the push remote name %q is not safe to use as a push argument", route.RemoteName)
	}

	switch route.Intent {
	case PushIntentPublish:
		if err := gc.confirmPushRemoteIsConfigured(route.RemoteName); err != nil {
			return nil, err
		}
		observation, observationErr := resolveUpstreamObservation(gc.cmdExecutor, gc.logging)
		if observationErr != nil {
			return nil, fmt.Errorf("the branch state could not be re-read before the publish: %w", observationErr)
		}
		if observation.Branch != route.Branch {
			return nil, fmt.Errorf("the checked-out branch changed from %q to %q before the publish", route.Branch, observation.Branch)
		}
		switch observation.State {
		case UpstreamStateUnpublished:
			return buildPublishGitArgs(route.RemoteName), nil
		case UpstreamStateTracked:
			// the branch gained its upstream since the confirmation: a
			// normal push without --set-upstream
			return buildPushGitArgs(route.RemoteName, route.PushType, route.Branch, true), nil
		case UpstreamStateNotApplicable:
			return nil, errors.New("there is no publishable branch: the head is detached or has no commit")
		default:
			// pending or unavailable: the state has not settled, and a
			// first push must never start from an unsettled read
			return nil, errors.New("the branch state has not settled, so the publish was not started")
		}
	case PushIntentPush:
		// the tracked push re-reads the branch state through the generation
		// executor for the same reasons the publish does: a branch that lost
		// its upstream since the confirmation still pushes without -u, a
		// switched head is refused instead of pushing the wrong ref, and an
		// unreadable observation is an unstarted refusal
		observation, observationErr := resolveUpstreamObservation(gc.cmdExecutor, gc.logging)
		if observationErr != nil {
			return nil, fmt.Errorf("the branch state could not be re-read before the push: %w", observationErr)
		}
		if observation.Branch != route.Branch {
			return nil, fmt.Errorf("the checked-out branch changed from %q to %q before the push", route.Branch, observation.Branch)
		}
		switch observation.State {
		case UpstreamStateTracked:
			return buildPushGitArgs(route.RemoteName, route.PushType, route.Branch, true), nil
		case UpstreamStateUnpublished:
			return buildPushGitArgs(route.RemoteName, route.PushType, route.Branch, false), nil
		case UpstreamStateNotApplicable:
			return nil, errors.New("there is no pushable branch: the head is detached or has no commit")
		default:
			// pending or unavailable: the state has not settled, and a push
			// must never start from an unsettled read
			return nil, errors.New("the branch state has not settled, so the push was not started")
		}
	default:
		return nil, fmt.Errorf("unknown push intent %q", route.Intent)
	}
}

// ------------------------------------
//
//	UnstartedGitPushResult builds the definitive result for a push refused
//	before any process existed, so a signing-required push whose preparation
//	fails can display the same "not started" diagnostics as the background
//	route.
//
// ------------------------------------
func UnstartedGitPushResult(workingDirectory string, cause error) GitPushResult {
	result := newGitPushResult()
	result.workingDirectory = workingDirectory
	result.err = cause
	return result
}

// ------------------------------------
//
//	Drain one git push output pipe to EOF, retaining the raw bytes in
//	rawBuffer and mirroring the lines into the matching live progress buffer.
//	After the context is cancelled the pipe keeps draining so the child can
//	never block on a full pipe; the scanner error, when the stream could not
//	be read to completion, is returned for the caller to record and log.
//
// ------------------------------------
func (gc *GitCommit) drainGitPushStream(ctx context.Context, stream string, pipe io.Reader, rawBuffer *bytes.Buffer) error {
	if gc.gitPushDrainPreRead != nil {
		gc.gitPushDrainPreRead()
	}
	scanner := bufio.NewScanner(io.TeeReader(pipe, rawBuffer))
	scanner.Split(splitOnCarriageReturnOrNewline)
	lastSent := time.Time{}
	for scanner.Scan() {
		gc.gitRemotePushOutputMu.Lock()
		if stream == gitPushStdoutStream {
			updatedCursor, updatedLines := handleProgressOutputStream(gc.gitRemotePushStdoutCursor, scanner, gc.gitRemotePushStdoutLines)
			gc.gitRemotePushStdoutCursor = updatedCursor
			gc.gitRemotePushStdoutLines = updatedLines
		} else {
			updatedCursor, updatedLines := handleProgressOutputStream(gc.gitRemotePushStderrCursor, scanner, gc.gitRemotePushStderrLines)
			gc.gitRemotePushStderrCursor = updatedCursor
			gc.gitRemotePushStderrLines = updatedLines
		}
		gc.gitRemotePushOutputMu.Unlock()

		select {
		case <-ctx.Done():
			// stop notifying the live viewport but keep draining the pipe
		default:
			if time.Since(lastSent) >= STREAMUPDATETHROTTLEMS*time.Millisecond {
				select {
				case gc.updateChannel <- GIT_REMOTE_PUSH_OUTPUT_UPDATE:
					lastSent = time.Now()
				default:
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		// The scanner stopped before EOF; drain the rest raw so the child
		// cannot block on a full pipe and the retained bytes stay complete.
		if _, copyErr := io.Copy(rawBuffer, pipe); copyErr != nil {
			return errors.Join(fmt.Errorf("%s stream: %w", stream, err), fmt.Errorf("%s stream drain: %w", stream, copyErr))
		}
		return fmt.Errorf("%s stream: %w", stream, err)
	}

	return nil
}

// Direct-process liveness, the distinguishable cancellation signal and the
// SIGTERM reaped-status filter live in the build-tagged push_liveness_*
// files so non-Unix targets keep compiling. The older SIGKILL status helper
// is retained for its frozen unit contract; the handshake below no longer
// uses a SIGKILL claim.

// ------------------------------------
//
//	Build a push result with no process status recorded yet; the -1 exit code
//	kills the ambiguity of a zero value that would otherwise read as a clean
//	exit
//
// ------------------------------------
func newGitPushResult() GitPushResult {
	return GitPushResult{exitCode: -1}
}

// ------------------------------------
//
//	Clear the stored push result so a new attempt cannot display the
//	previous one's command, status, or output
//
// ------------------------------------
func (gc *GitCommit) clearGitPushResult() {
	gc.gitPushResultMu.Lock()
	defer gc.gitPushResultMu.Unlock()
	gc.gitPushResult = newGitPushResult()
}

// ------------------------------------
//
//	Return a push result with no process status recorded, suitable for
//	clearing a stored attempt before a new one starts
//
// ------------------------------------
func EmptyGitPushResult() GitPushResult {
	return newGitPushResult()
}

// ------------------------------------
//
//	Publish an immutable push result and return it
//
// ------------------------------------
func (gc *GitCommit) publishGitPushResult(result GitPushResult) GitPushResult {
	gc.gitPushResultMu.Lock()
	gc.gitPushResult = result
	gc.gitPushResultMu.Unlock()

	return result
}

// ------------------------------------
//
//	Return the last published push result. The copy keeps the API and the TUI
//	from sharing storage for the retained output.
//
// ------------------------------------
func (gc *GitCommit) GitPushResult() GitPushResult {
	gc.gitPushResultMu.RLock()
	defer gc.gitPushResultMu.RUnlock()

	return gc.gitPushResult
}

// ------------------------------------
//
//	Install the phase-1 ordered-fixture wait gate. The reaper closes ack
//	after direct Wait returns and blocks until release closes before the
//	completion notification can be received. Unset by default: ordinary
//	pushes never block.
//
// ------------------------------------
func (gc *GitCommit) SetP1oWaitGate(ack chan<- struct{}, release <-chan struct{}) {
	gc.p1oWaitGateMu.Lock()
	defer gc.p1oWaitGateMu.Unlock()
	gc.p1oWaitGateAck = ack
	gc.p1oWaitGateRelease = release
}

// ------------------------------------
//
//	GitPush executes the confirmed push or publish route on the generation's
//	command executor. The route's intent, remote, push type, and branch are
//	re-validated by prepareGitPush immediately before the process is
//	started, and the route's execution guard is checked just before Start so
//	a worktree switch between confirmation and launch refuses the attempt
//	instead of pushing from the wrong generation.
//
// ------------------------------------
func (gc *GitCommit) GitPush(ctx context.Context, route GitPushRoute) GitPushResult {
	if !gc.gitProcessLock.CanProceedWithGitOps() {
		return gc.publishGitPushResult(GitPushResult{
			exitCode:         -1,
			workingDirectory: gc.pushWorkingDirectory(),
			err:              fmt.Errorf("%s", gc.gitProcessLock.OtherProcessRunningWarning()),
		})
	}
	defer func() {
		gc.gitProcessLock.ReleaseGitOpsLock()
	}()

	gc.ClearGitRemotePushOutput()
	gc.clearGitPushResult()

	pushGitArgs, prepareErr := gc.prepareGitPush(route)
	if prepareErr != nil {
		gc.logging.RegisterNewLog(logging.GIT_PUSH_OPS, "", logging.WARN, fmt.Sprintf("[%s NOT STARTED]: %s", logging.GIT_PUSH_OPS, prepareErr.Error()), true)
		result := newGitPushResult()
		result.workingDirectory = gc.pushWorkingDirectory()
		result.err = prepareErr
		return gc.publishGitPushResult(result)
	}

	cmd := gc.cmdExecutor.RunGitCmd(pushGitArgs, true)

	// The result owns defensive copies of the process identity before anything
	// can mutate the command.
	result := newGitPushResult()
	result.argv = append([]string(nil), cmd.Args...)
	result.workingDirectory = cmd.Dir

	// A cancellation observed before launch never starts a process: it is
	// reported as cancelled with no exit status rather than a setup failure.
	if ctx.Err() != nil {
		result.cancelled = true
		result.err = ctx.Err()
		gc.logging.RegisterNewLog(logging.GIT_PUSH_OPS, strings.Join(pushGitArgs, " "), logging.WARN, fmt.Sprintf("[%s CANCELLED]", logging.GIT_PUSH_OPS), true)
		return gc.publishGitPushResult(result)
	}

	// The push owns both pipe ends explicitly instead of sharing StdoutPipe
	// readers with Wait.
	// The parent closes its write ends after Start.
	// The single reaper below then observes the direct child independently of
	// descendants that inherit the write ends.
	stdoutR, stdoutW, pipeErr := os.Pipe()
	if pipeErr != nil {
		gc.logging.RegisterNewLog(logging.GIT_PUSH_OPS, "", logging.ERROR, fmt.Sprintf("[PIPE ERROR]: %s", pipeErr.Error()), false)
		result.err = pipeErr
		return gc.publishGitPushResult(result)
	}
	stderrR, stderrW, pipeErr := os.Pipe()
	if pipeErr != nil {
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		gc.logging.RegisterNewLog(logging.GIT_PUSH_OPS, "", logging.ERROR, fmt.Sprintf("[PIPE ERROR]: %s", pipeErr.Error()), false)
		result.err = pipeErr
		return gc.publishGitPushResult(result)
	}
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	// The route's execution guard rejects the attempt when the Git-
	// operations generation changed after the route was confirmed and
	// before the process started. No process is launched from a stale
	// generation.
	// The result keeps the not-started shape of a prepare refusal.
	if route.ActiveGuard != nil {
		if guardErr := route.ActiveGuard(); guardErr != nil {
			_ = stdoutR.Close()
			_ = stdoutW.Close()
			_ = stderrR.Close()
			_ = stderrW.Close()
			result.argv = nil
			result.workingDirectory = gc.pushWorkingDirectory()
			if ctx.Err() != nil {
				// The context was cancelled while the guard held the
				// pre-Start gate: the attempt stays unstarted and is
				// reported as cancelled with no exit status rather
				// than a setup failure.
				result.cancelled = true
				result.err = errors.Join(guardErr, ctx.Err())
				gc.logging.RegisterNewLog(logging.GIT_PUSH_OPS, strings.Join(pushGitArgs, " "), logging.WARN, fmt.Sprintf("[%s CANCELLED]", logging.GIT_PUSH_OPS), true)
			} else {
				result.err = guardErr
				gc.logging.RegisterNewLog(logging.GIT_PUSH_OPS, "", logging.WARN, fmt.Sprintf("[%s NOT STARTED]: %s", logging.GIT_PUSH_OPS, guardErr.Error()), true)
			}
			return gc.publishGitPushResult(result)
		}
	}

	// A context cancelled during the guard or setup (after the pre-launch
	// check above) still starts nothing: all setup pipe ends close and the
	// attempt is reported as an unstarted cancellation with no exit status.
	if ctx.Err() != nil {
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
		result.cancelled = true
		result.err = ctx.Err()
		gc.logging.RegisterNewLog(logging.GIT_PUSH_OPS, strings.Join(pushGitArgs, " "), logging.WARN, fmt.Sprintf("[%s CANCELLED]", logging.GIT_PUSH_OPS), true)
		return gc.publishGitPushResult(result)
	}

	// Start the process
	if err := cmd.Start(); err != nil {
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
		if ctx.Err() != nil {
			// Cancellation before Start is a cancellation, not a setup
			// failure: no process exists to carry an exit status.
			result.cancelled = true
			result.err = ctx.Err()
			gc.logging.RegisterNewLog(logging.GIT_PUSH_OPS, strings.Join(pushGitArgs, " "), logging.WARN, fmt.Sprintf("[%s CANCELLED]", logging.GIT_PUSH_OPS), true)
			return gc.publishGitPushResult(result)
		}
		gc.logging.RegisterNewLog(logging.GIT_PUSH_OPS, "", logging.ERROR, fmt.Sprintf("[START ERROR]: %s", err.Error()), false)
		result.err = err
		return gc.publishGitPushResult(result)
	}
	// The child (and any descendants) hold their own duplicates of the write
	// ends; the parent closes its copies so EOF tracks the last live writer.
	_ = stdoutW.Close()
	_ = stderrW.Close()
	result.started = true
	gc.logging.RegisterNewLog(logging.GIT_PUSH_OPS, strings.Join(pushGitArgs, " "), logging.INFO, "", true)

	// Drain both streams concurrently while retaining their bytes.
	var stdoutBuffer bytes.Buffer
	var stderrBuffer bytes.Buffer
	var stdoutReadErr, stderrReadErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		stdoutReadErr = gc.drainGitPushStream(ctx, gitPushStdoutStream, stdoutR, &stdoutBuffer)
	}()
	go func() {
		defer wg.Done()
		stderrReadErr = gc.drainGitPushStream(ctx, gitPushStderrStream, stderrR, &stderrBuffer)
	}()

	// Exactly one reaper observes the direct child independently of output
	// EOF. The buffered channel records completion synchronously at Wait
	// return.
	// A later cancellation check then sees a completed process even when the
	// completion notification has not been consumed yet.
	// Snapshot the optional phase-1 wait gate once at push start: when set,
	// the reaper acknowledges direct Wait return before the completion
	// notification can be received and holds that notification until the
	// test releases it. Unset leaves Wait delivery exactly as before.
	gc.p1oWaitGateMu.Lock()
	p1oGateAck := gc.p1oWaitGateAck
	p1oGateRelease := gc.p1oWaitGateRelease
	gc.p1oWaitGateMu.Unlock()
	waitCh := make(chan error, 1)
	go func() {
		reapedErr := cmd.Wait()
		if p1oGateAck != nil {
			close(p1oGateAck)
			if p1oGateRelease != nil {
				<-p1oGateRelease
			}
		}
		waitCh <- reapedErr
	}()

	// Ordinary completion retains every byte by draining both readers to
	// EOF; with owned pipes Wait no longer closes the read ends, so an
	// uninterrupted drain loses nothing already available. Cancellation
	// instead applies the timed read-end closure below: only bytes copied
	// before that closure survive, and bytes still unread when a reader
	// stays blocked past the cutoff may be lost.
	drained := make(chan struct{})
	go func() {
		wg.Wait()
		close(drained)
	}()

	// Coordinate direct-process completion, context cancellation and the
	// kill in one handshake. Cancellation is claimed only when the direct
	// process had not already completed.
	// A completed Wait observed in the buffered channel wins over a later cancellation.
	// When Wait completion and the context notification are simultaneously ready, the handshake observes the process outcome without claiming cancellation.
	// That simultaneous rule does not cover the same-signal overlap below: an independent SIGTERM racing our own SIGTERM stays indistinguishable and keeps the cancellation claim.
	// A Kill call alone is never treated as proof of causation.
	var waitErr error
	waitSel := waitCh
	drainSel := drained
	ctxSel := ctx.Done()
	waitDone := false
	drainDone := false
	cancelSignalled := false
	for waitSel != nil || drainSel != nil {
		select {
		case err := <-waitSel:
			waitErr = err
			waitDone = true
			waitSel = nil
		case <-drainSel:
			drainDone = true
			drainSel = nil
		case <-ctxSel:
			select {
			case err := <-waitSel:
				waitErr = err
				waitDone = true
				waitSel = nil
			default:
				if !waitDone && cmd.Process != nil {
					// Only a successfully signalled, positively live target
					// keeps a cancellation claim, and only when the reaped
					// status below agrees with our distinguishable signal.
					// A failed signal never establishes that cancellation
					// terminated the direct process.
					// A successful signal against an already dead, unreaped
					// zombie proves nothing by itself (signals succeed on
					// zombies).
					// The pre-signal probes supply the first liveness
					// evidence. The reaped status check below must still
					// agree with our SIGTERM.
					// An independent SIGKILL (or any numeric/other-signal exit)
					// that lands in the final observation-to-signal window reaps
					// a status our signal could not have produced, so it stays
					// an observed process failure even though cancellation was requested.
					//
					// The ps fallback inside that probe can itself span an
					// independent death. Its window is milliseconds wide.
					// A SIGKILL delivered inside it leaves a zombie that is
					// still materializing when a single immediate recheck runs.
					// Settle-poll the fast kernel-state checks before
					// signalling. Such a death then becomes visible instead
					// of racing the cancellation signal.
					// A still-live child exhausts the bounded poll and is
					// still signalled, so genuine cancellation is never
					// suppressed by undeterminable state.
					//
					// Residual ambiguity, stated explicitly: an independent
					// SIGTERM racing our own SIGTERM reaps the same status
					// our signal produces, so that one overlap is
					// observationally indistinguishable and keeps the
					// cancellation claim. The amended phase-1.3 policy accepts
					// this overlap as a cancellation outcome without asserting
					// which signal caused death: matching status is not causal proof.
					// No independent SIGKILL, numeric exit or other signal
					// can be relabelled that way.
					if !pushDirectProcessAlreadyExited(cmd.Process.Pid) && !pushDirectProcessSettledDead(cmd.Process.Pid) {
						if pushCancelSignalDirectProcess(cmd.Process) {
							cancelSignalled = true
							// Bounded escalation for a child resisting the
							// first signal: SIGTERM normally reaps promptly,
							// so a short wait still reports genuine live
							// kills without delay. When the child resists,
							// SIGKILL keeps the handshake bounded instead of
							// hanging, but that escalation SIGKILL is never
							// attribution evidence: its status disagrees with
							// our signal, so the outcome below stays observed.
							select {
							case err := <-waitSel:
								waitErr = err
								waitDone = true
								waitSel = nil
							case <-time.After(pushCancelEscalationPeriod):
								_ = cmd.Process.Kill()
							}
						}
					}
				}
			}
			// After direct exit, inherited descriptors may remain open.
			// Bound that drain while retaining only bytes already read before
			// the timed closure. Closing the read ends immediately would
			// race with readers that have not yet consumed pipe-buffered
			// output. Give those readers a bounded window to retain pre-close
			// bytes. The timer still closes to unblock a drain held open
			// by an inherited writer. Bytes still unread when a reader
			// stays blocked past that cutoff may be lost. A context that
			// never cancels imposes no post-exit timeout.
			go func() {
				select {
				case <-drained:
				case <-time.After(pushQueuedDrainGracePeriod):
				}
				_ = stdoutR.Close()
				_ = stderrR.Close()
			}()
			ctxSel = nil
		}
	}
	// Owned read ends close on every return: the cancellation branch above
	// bounds the inherited-descriptor drain with a timed close, and closing
	// them again after an ordinary EOF drain is harmless. Both drains
	// already finished (the readers returned before `drained` closed), so no
	// byte already copied before closure is lost.
	_ = stdoutR.Close()
	_ = stderrR.Close()
	_ = drainDone
	// A zero-exit Wait can only arrive from an independently completed
	// child; it is never retroactively attributed to a concurrent
	// cancellation. A signal success alone is not proof of causation either.
	// Only a SIGTERM reaped status agrees with our distinguishable
	// cancellation signal. E.g. independent SIGKILL, any other signal or
	// any numeric exit wins conservatively as the observed process outcome.
	// An escalated SIGKILL against a signal-resistant child disagrees the
	// same way and likewise stays observed.
	if waitErr == nil {
		cancelSignalled = false
	} else if cancelSignalled && !pushProcessKilledBySigTerm(cmd.ProcessState) {
		cancelSignalled = false
	}

	result.stdout = stdoutBuffer.Bytes()
	result.stderr = stderrBuffer.Bytes()

	// Record the exact exit code from the reaped process status, even when
	// the wait operation carries a wrapping error.
	if cmd.ProcessState != nil {
		result.exitCode = cmd.ProcessState.ExitCode()
	} else if waitErr == nil {
		result.exitCode = 0
	} else {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			result.exitCode = exitErr.ExitCode()
		}
	}

	// Each terminal outcome is distinguishable.
	// No path returns an unexplained sentinel.
	// A requested cancellation with delivered SIGTERM, no known earlier
	// completion, and matching reaped SIGTERM status is reported as cancelled.
	// That matching-SIGTERM overlap is observationally indistinguishable
	// from an independent same-SIGTERM race; matching status claims no signal provenance.
	// A late context error never relabels a known completed process.
	// A nonmatching status stays the observed process outcome.
	streamErr := errors.Join(stdoutReadErr, stderrReadErr)
	switch {
	case cancelSignalled:
		// Genuine cancellation keeps the reaped process status/error and
		// joins the context cause so errors.Is(Err(), context.Canceled)
		// holds; the cancelled popup guard keeps a late result away from
		// a popup the user already closed.
		result.cancelled = true
		// Join both the standard cancellation sentinel and any custom
		// cancel cause: WithCancelCause supplies a non-sentinel cause
		// while ctx.Err() stays context.Canceled, so joining the cause
		// alone would drop the standard identity. When the cause already
		// carries the sentinel, joining it alone avoids duplication.
		cancelErr := context.Cause(ctx)
		if ctxErr := ctx.Err(); ctxErr != nil && (cancelErr == nil || !errors.Is(cancelErr, ctxErr)) {
			cancelErr = errors.Join(ctxErr, cancelErr)
		}
		result.err = errors.Join(waitErr, cancelErr, streamErr)
		gc.logging.RegisterNewLog(logging.GIT_PUSH_OPS, strings.Join(pushGitArgs, " "), logging.WARN, fmt.Sprintf("[%s CANCELLED]", logging.GIT_PUSH_OPS), true)
	case waitErr != nil:
		// A process failure and a stream capture failure can happen at the
		// same time, so the result keeps both instead of discarding one.
		result.err = errors.Join(waitErr, streamErr)
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			gc.logging.RegisterNewLog(logging.GIT_PUSH_OPS, strings.Join(pushGitArgs, " "), logging.ERROR, fmt.Sprintf("[%s ERROR]: %s", logging.GIT_PUSH_OPS, waitErr.Error()), true)
		} else {
			gc.logging.RegisterNewLog(logging.GIT_PUSH_OPS, "", logging.ERROR, fmt.Sprintf("[UNEXPECTED ERROR]: %s", waitErr.Error()), false)
		}
	default:
		result.err = streamErr
	}

	// Stream read failures are logged independently of the process outcome
	// so a nonzero exit does not hide a broken stream. On genuine
	// cancellation the read errors are the expected consequence of the pipe
	// close that unblocked the readers.
	if !cancelSignalled {
		if stdoutReadErr != nil {
			gc.logging.RegisterNewLog(logging.GIT_PUSH_OPS, strings.Join(pushGitArgs, " "), logging.ERROR, fmt.Sprintf("[READ ERROR]: %s", stdoutReadErr), true)
		}
		if stderrReadErr != nil {
			gc.logging.RegisterNewLog(logging.GIT_PUSH_OPS, strings.Join(pushGitArgs, " "), logging.ERROR, fmt.Sprintf("[READ ERROR]: %s", stderrReadErr), true)
		}
	}

	return gc.publishGitPushResult(result)
}

// ------------------------------------
//
//	pushWorkingDirectory returns the directory the push routes run in: the
//	generation executor's captured worktree.
//
// ------------------------------------
func (gc *GitCommit) pushWorkingDirectory() string {
	if gc.cmdExecutor != nil {
		return gc.cmdExecutor.RepoPath()
	}
	return executor.GittiCmdExecutor.RepoPath()
}

// ------------------------------------
//
//	GitPushWithSigning constructs the git push argv for terminal execution
//	when signing is required. The route is validated with exactly the same
//	preparation as the background route; a non-nil error is an unstarted,
//	actionable refusal the caller must surface without suspending the UI.
//
// ------------------------------------
func (gc *GitCommit) GitPushWithSigning(route GitPushRoute) ([]string, error) {
	return gc.prepareGitPush(route)
}

// ------------------------------------
//
//	Clear the stored remote push output buffers
//
// ------------------------------------
func (gc *GitCommit) ClearGitRemotePushOutput() {
	gc.gitRemotePushOutputMu.Lock()
	defer gc.gitRemotePushOutputMu.Unlock()
	gc.gitRemotePushStdoutLines = []string{}
	gc.gitRemotePushStdoutCursor = 0
	gc.gitRemotePushStderrLines = []string{}
	gc.gitRemotePushStderrCursor = 0
}

// ------------------------------------
//
//	Related to Git Commit RESET (apply to the latest commit only)
//
// ------------------------------------
func (gc *GitCommit) GitResetLatestCommit(resetType string) {
	if !gc.gitProcessLock.CanProceedWithGitOps() {
		return
	}
	defer func() {
		gc.gitProcessLock.ReleaseGitOpsLock()
	}()

	var gitArgs []string

	switch resetType {
	case RESETSOFT:
		gitArgs = []string{"reset", "--soft", "HEAD~1"}
	case RESETHARD:
		gitArgs = []string{"reset", "--hard", "HEAD~1"}
	case RESETMIXED:
		gitArgs = []string{"reset", "--mixed", "HEAD~1"}
	default:
		// we default to reset mixed as default option for reset
		gitArgs = []string{"reset", "--mixed", "HEAD~1"}
	}

	commitLatestResetCmdExecutor := executor.GittiCmdExecutor.RunGitCmd(gitArgs, false)
	err := commitLatestResetCmdExecutor.Run()
	gc.logging.RegisterNewLog(logging.GIT_RESET_LATEST_COMMIT_OPS, strings.Join(gitArgs, " "), logging.INFO, "", true)
	if err != nil {
		gc.logging.RegisterNewLog(logging.GIT_RESET_LATEST_COMMIT_OPS, strings.Join(gitArgs, " "), logging.ERROR, fmt.Sprintf("[%s ERROR]: %s", logging.GIT_RESET_LATEST_COMMIT_OPS, err.Error()), true)
		return
	}
}

// ------------------------------------
//
//	Related to Git Commit RESET (apply to selected commit [using commit hash])
//
// ------------------------------------
func (gc *GitCommit) GitResetToSelectedCommit(resetType string, commitHash string) {
	if !gc.gitProcessLock.CanProceedWithGitOps() {
		return
	}
	defer func() {
		gc.gitProcessLock.ReleaseGitOpsLock()
	}()

	var gitArgs []string

	switch resetType {
	case RESETSOFT:
		gitArgs = []string{"reset", "--soft", commitHash}
	case RESETHARD:
		gitArgs = []string{"reset", "--hard", commitHash}
	case RESETMIXED:
		gitArgs = []string{"reset", "--mixed", commitHash}
	default:
		// we default to reset mixed as default option for reset
		gitArgs = []string{"reset", "--mixed", commitHash}
	}

	resetToSelectedCommitCmdExecutor := executor.GittiCmdExecutor.RunGitCmd(gitArgs, false)
	err := resetToSelectedCommitCmdExecutor.Run()
	gc.logging.RegisterNewLog(logging.GIT_RESET_TO_SELECTED_COMMIT_OPS, strings.Join(gitArgs, " "), logging.INFO, "", true)
	if err != nil {
		gc.logging.RegisterNewLog(logging.GIT_RESET_TO_SELECTED_COMMIT_OPS, strings.Join(gitArgs, " "), logging.ERROR, fmt.Sprintf("[%s ERROR]: %s", logging.GIT_RESET_TO_SELECTED_COMMIT_OPS, err.Error()), true)
		return
	}
}
