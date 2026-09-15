package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
	logging                   *logging.GittiLogging
	// gitPushDrainPreRead, when set, is invoked by each push output stream
	// reader before its first read; tests use it to hold the readers until
	// after the child exits
	gitPushDrainPreRead func()
}

type LatestCommitMsgAndDesc struct {
	Message     string
	Description string
}

// ------------------------------------
//
//	Initialize the git commit handler with shared dependencies
//
// ------------------------------------
func InitGitCommit(updateChannel chan string, gitProcessLock *GitProcessLock, logging *logging.GittiLogging) *GitCommit {
	gitCommit := GitCommit{
		gitCommitOutput:          []string{},
		gitRemotePushStdoutLines: []string{},
		gitRemotePushStderrLines: []string{},
		updateChannel:            updateChannel,
		gitProcessLock:           gitProcessLock,
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
//	Related to Git Push
//
// ------------------------------------
func (gc *GitCommit) GitPush(ctx context.Context, originName string, pushType string, currentCheckOutBranch string) GitPushResult {
	if !gc.gitProcessLock.CanProceedWithGitOps() {
		return gc.publishGitPushResult(GitPushResult{
			exitCode: -1,
			err:      fmt.Errorf("%s", gc.gitProcessLock.OtherProcessRunningWarning()),
		})
	}
	defer func() {
		gc.gitProcessLock.ReleaseGitOpsLock()
	}()

	gc.ClearGitRemotePushOutput()
	gc.clearGitPushResult()

	// check if the checkoutbranch has upstream if not include "-u" flag
	_, hasUpstream := hasUpStream()
	pushGitArgs := buildPushGitArgs(originName, pushType, currentCheckOutBranch, hasUpstream)

	cmd := executor.GittiCmdExecutor.RunGitCmdWithContext(ctx, pushGitArgs, true)

	// The result owns defensive copies of the process identity before anything
	// can mutate the command.
	result := newGitPushResult()
	result.argv = append([]string(nil), cmd.Args...)
	result.workingDirectory = cmd.Dir

	// Separate stdout and stderr so the two streams are retained and drained
	// concurrently without either pipe filling and blocking the child.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		gc.logging.RegisterNewLog(logging.GIT_PUSH_OPS, "", logging.ERROR, fmt.Sprintf("[PIPE ERROR]: %s", err.Error()), false)
		result.err = err
		return gc.publishGitPushResult(result)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		gc.logging.RegisterNewLog(logging.GIT_PUSH_OPS, "", logging.ERROR, fmt.Sprintf("[PIPE ERROR]: %s", err.Error()), false)
		result.err = err
		return gc.publishGitPushResult(result)
	}

	// Start the process
	if err := cmd.Start(); err != nil {
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
		stdoutReadErr = gc.drainGitPushStream(ctx, gitPushStdoutStream, stdout, &stdoutBuffer)
	}()
	go func() {
		defer wg.Done()
		stderrReadErr = gc.drainGitPushStream(ctx, gitPushStderrStream, stderr, &stderrBuffer)
	}()

	// The readers must reach EOF before Wait: Wait closes the pipe read
	// ends when it returns, so bytes a reader had not yet consumed would be
	// lost from the retained output.
	drained := make(chan struct{})
	go func() {
		wg.Wait()
		close(drained)
	}()

	select {
	case <-drained:
		// Both streams drained to EOF; Wait now only reaps the process.
	case <-ctx.Done():
		// A killed child's descendants can retain the pipe write ends, so
		// EOF may never arrive on its own; close the read ends to unblock
		// the readers deterministically.
		_ = stdout.Close()
		_ = stderr.Close()
		<-drained
	}

	waitErr := cmd.Wait()

	result.stdout = stdoutBuffer.Bytes()
	result.stderr = stderrBuffer.Bytes()

	// Record the exact exit code whenever a process status exists.
	if waitErr == nil {
		result.exitCode = 0
	} else {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			result.exitCode = exitErr.ExitCode()
		}
	}

	// Each terminal outcome is distinguishable and logged in the existing
	// style; no path returns an unexplained sentinel.
	streamErr := errors.Join(stdoutReadErr, stderrReadErr)
	switch {
	case ctx.Err() != nil:
		// Cancellation remains its own outcome; the cancelled popup guard
		// keeps a late result away from a popup the user already closed.
		result.cancelled = true
		result.err = ctx.Err()
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
	// so a nonzero exit does not hide a broken stream. On cancellation the
	// read errors are the expected consequence of the pipe close that
	// unblocked the readers.
	if ctx.Err() == nil {
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
//	GitPushWithSigning constructs a git push command for terminal execution when signing is required.
//	When push signing is enabled, gitti UI is suspended and the push is executed directly in the terminal,
//	allowing the user to interact with the signing prompt (e.g., GPG passphrase).
//
// ------------------------------------
func (gc *GitCommit) GitPushWithSigning(originName string, pushType string, currentCheckOutBranch string) []string {
	// check if the checkoutbranch has upstream if not include "-u" flag
	_, hasUpstream := hasUpStream()

	return buildPushGitArgs(originName, pushType, currentCheckOutBranch, hasUpstream)
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
