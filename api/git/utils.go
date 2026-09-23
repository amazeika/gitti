package git

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gohyuhan/gitti/executor"
	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/logging"
)

// ------------------------------------
//
//	Parse raw git command output bytes into a clean string array, filtering out empty results
//
// ------------------------------------
func processGeneralGitOpsOutputIntoStringArray(dirtyGitOutput []byte) []string {
	var cleanedStringArray []string
	cleanedStringArray = strings.Split(strings.TrimSpace(string(dirtyGitOutput)), "\n")

	if len(cleanedStringArray) == 1 && cleanedStringArray[0] == "" {
		return []string{}
	}

	return cleanedStringArray
}

// ------------------------------------
//
//	Parse raw bytes into a clean string array, split on the given seperator, returning empty slice when there is no content
//
// ------------------------------------
func processOutputIntoStringArrayWithCustomSeperator(dirtyGitOutput []byte, seperator string) []string {
	var cleanedStringArray []string
	// Trim NUL alongside whitespace so NUL-delimited (-z) output does not leave a
	// trailing empty record/field from the final separator
	cleanedStringArray = strings.Split(strings.Trim(string(dirtyGitOutput), " \t\r\n\x00"), seperator)

	if len(cleanedStringArray) == 1 && cleanedStringArray[0] == "" {
		return []string{}
	}

	return cleanedStringArray
}

// ------------------------------------
//
//	Custom bufio split function that splits on both carriage return and newline,
//	preserving \r in the token while stripping \n for proper git progress stream handling
//
// ------------------------------------
func splitOnCarriageReturnOrNewline(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}

	// Find the first delimiter.
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		// Check which delimiter we found.
		if data[i] == '\r' {
			// It's a carriage return. Return the token *including* the \r.
			return i + 1, data[0 : i+1], nil
		}
		// It's a newline. Return the token *excluding* the \n.
		return i + 1, data[0:i], nil
	}

	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// ------------------------------------
//
//	Process streaming git output, handling CR-based line replacements for progress displays
//
// ------------------------------------
func handleProgressOutputStream(cursorIndex int, scanner *bufio.Scanner, outputArray []string) (int, []string) {
	// line counter was to determine when a line end with \r,
	// we should replace the latest line in the array or append because this is a new line
	line := scanner.Text()
	isReplaceLine := strings.HasSuffix(line, "\r")
	lineContent := strings.TrimRight(line, "\r")

	// if the cursor is larger or equal then the output array, append into the output outputArray
	// if not replace the latest itme in the array
	if cursorIndex >= len(outputArray) {
		outputArray = append(outputArray, lineContent)
	} else {
		// Otherwise, update the line the cursor is pointing to.
		outputArray[cursorIndex] = lineContent
	}

	// if it was not a replace string line, increment the cursorindex
	if !isReplaceLine {
		return cursorIndex + 1, outputArray
	}

	return cursorIndex, outputArray
}

// ------------------------------------
//
//	check if the format for git remote is correct and valid
//
// ------------------------------------
func isValidGitRemoteURL(remote string) bool {
	// Check HTTPS style
	if strings.HasPrefix(remote, "https://") || strings.HasPrefix(remote, "http://") {
		_, err := url.ParseRequestURI(remote)
		return err == nil
	}

	// Check SSH style (e.g. git@github.com:user/repo.git)
	sshPattern := `^[\w.-]+@[\w.-]+:[\w./-]+(\.git)?$`
	matched, _ := regexp.MatchString(sshPattern, remote)
	return matched
}

// ------------------------------------
//
//	Related to Git Init
//
// ------------------------------------
func GitInit(repoPath string, initBranchName string) {
	initGitArgs := []string{"init"}

	initCmd := executor.GittiCmdExecutor.RunGitCmd(initGitArgs, false)
	_, initErr := initCmd.Output()
	if initErr != nil {
		fmt.Printf("[GIT INIT ERROR]: %v", initErr)
		os.Exit(1)
	}

	// set the branch
	checkoutBranchGitArgs := []string{"checkout", "-b", initBranchName}

	checkoutBranchCmdExecutor := executor.GittiCmdExecutor.RunGitCmd(checkoutBranchGitArgs, false)
	_, checkoutBranchErr := checkoutBranchCmdExecutor.Output()
	if checkoutBranchErr != nil {
		fmt.Printf("[GIT INIT ERROR]: %v", checkoutBranchErr)
		os.Exit(1)
	}
}

// ------------------------------------
//
//	Report whether the commit before HEAD exists, which is what a reset to HEAD~1
//	needs. Git is asked rather than the panel. Once the commit log is widened the
//	panel holds rows HEAD cannot reach, so counting them would open a popup whose
//	reset then fails
//
// ------------------------------------
func HasCommitBeforeHead() bool {
	gitArgs := []string{"rev-parse", "--verify", "--quiet", "HEAD~1"}

	hasCommitBeforeHeadCmdExecutor := executor.GittiCmdExecutor.RunGitCmd(gitArgs, false)
	if err := hasCommitBeforeHeadCmdExecutor.Run(); err != nil {
		return false
	}

	return true
}

// ------------------------------------
//
//	resolveUpStream returns the upstream ref of the current head
//	(e.g. "origin/main"), or an error when the head has no resolvable
//	upstream.
//
// ------------------------------------
func resolveUpStream() (string, error) {
	gitArgs := []string{"rev-parse", "--abbrev-ref", "@{u}"}

	checkUpStreamCmdExecutor := executor.GittiCmdExecutor.RunGitCmd(gitArgs, false)
	checkUpStreamOutput, checkUpStreamErr := checkUpStreamCmdExecutor.Output()
	if checkUpStreamErr != nil {
		return "", checkUpStreamErr
	}

	return strings.TrimSpace(string(checkUpStreamOutput)), nil
}

func hasUpStream() (string, bool) {
	upStream, err := resolveUpStream()
	if err != nil {
		return "", false
	}

	return upStream, true
}

// ------------------------------------
//
//	UpstreamObservationState is the typed result of one upstream
//	observation pass, replacing the old empty-string inference:
//	pending before the first completed pass, tracked for an attached
//	committed branch whose upstream resolves with valid counts,
//	unpublished for an attached committed branch with no configured
//	upstream, not-applicable for a detached or unborn head, and
//	unavailable for an observation that could not be trusted.
//
// ------------------------------------
type UpstreamObservationState string

const (
	UpstreamStatePending       UpstreamObservationState = "pending"
	UpstreamStateTracked       UpstreamObservationState = "tracked"
	UpstreamStateUnpublished   UpstreamObservationState = "unpublished"
	UpstreamStateNotApplicable UpstreamObservationState = "not-applicable"
	UpstreamStateUnavailable   UpstreamObservationState = "unavailable"
)

// ------------------------------------
//
//	UpstreamObservation is the result of one upstream observation pass.
//	Branch is the observed head's symbolic branch name, UpStream the
//	resolved upstream ref, UpStreamIcon the remote-kind glyph, and
//	RemoteSync the validated ahead/behind counts. Only the tracked state
//	carries UpStream and RemoteSync.
//
// ------------------------------------
type UpstreamObservation struct {
	State        UpstreamObservationState
	Branch       string
	UpStream     string
	UpStreamIcon string
	RemoteSync   RemoteSyncStatus
}

// ------------------------------------
//
//	resolveUpstreamObservation classifies the current head as tracked,
//	unpublished, not-applicable, or unavailable. An attached committed
//	branch whose upstream resolves with valid counts is tracked. A missing
//	configured upstream is the only input that classifies as unpublished.
//	Every command, start, or parse failure classifies as unavailable, so a
//	caller never infers a first push from an inspection error. Each
//	unavailable classification is logged, and the error is returned so the
//	caller can report the failed read.
//
//	A named branch with no commit makes rev-parse fail to resolve HEAD.
//	The unresolvable head is re-checked with symbolic-ref. A resolvable
//	symbolic branch is the unborn-branch state (not-applicable), and a
//	branch that cannot even be named is an unavailable read.
//
//	Every read after the branch is captured runs against that branch's
//	refs, so a concurrent checkout cannot mix another branch's upstream or
//	counts into the published observation.
//
//	Every command runs through the given command executor so the whole pass
//	is pinned to one worktree generation's directory.
//
// ------------------------------------
func resolveUpstreamObservation(cmdExecutor *executor.CmdExecutor, gittiLogger *logging.GittiLogging) (UpstreamObservation, error) {
	checkoutBranchGitArgs := []string{"rev-parse", "--abbrev-ref", "HEAD"}
	checkoutBranchOutput, checkoutBranchErr := cmdExecutor.RunGitCmd(checkoutBranchGitArgs, false).Output()
	if checkoutBranchErr != nil {
		symbolicRefGitArgs := []string{"symbolic-ref", "--quiet", "--short", "HEAD"}
		symbolicRefOutput, symbolicRefErr := cmdExecutor.RunGitCmd(symbolicRefGitArgs, false).Output()
		if symbolicRefErr != nil {
			return unavailableUpstreamObservation(gittiLogger, checkoutBranchGitArgs, checkoutBranchErr)
		}
		branchName := strings.TrimSpace(string(symbolicRefOutput))
		if branchName == "" {
			return unavailableUpstreamObservation(gittiLogger, checkoutBranchGitArgs, checkoutBranchErr)
		}
		// Unborn branch: the symbolic branch exists but has no commit
		return UpstreamObservation{State: UpstreamStateNotApplicable, Branch: branchName}, nil
	}

	branchName := strings.TrimSpace(string(checkoutBranchOutput))
	if branchName == "HEAD" {
		// Detached head: there is no publishable symbolic branch
		return UpstreamObservation{State: UpstreamStateNotApplicable}, nil
	}
	if branchName == "" {
		return unavailableUpstreamObservation(gittiLogger, checkoutBranchGitArgs, fmt.Errorf("rev-parse --abbrev-ref HEAD returned no branch"))
	}

	configured, configuredErr := branchHasConfiguredUpstream(cmdExecutor, branchName)
	if configuredErr != nil {
		return unavailableUpstreamObservation(gittiLogger, []string{"config", "--get", fmt.Sprintf("branch.%s.remote", branchName)}, configuredErr)
	}
	if !configured {
		return UpstreamObservation{State: UpstreamStateUnpublished, Branch: branchName}, nil
	}

	upStream, upStreamErr := resolveUpStreamForBranch(cmdExecutor, branchName)
	if upStreamErr != nil {
		return unavailableUpstreamObservation(gittiLogger, []string{"rev-parse", "--abbrev-ref", branchName + "@{upstream}"}, upStreamErr)
	}

	local, remote, countsErr := remoteSyncCountsAgainstUpstream(cmdExecutor, branchName)
	if countsErr != nil {
		return unavailableUpstreamObservation(gittiLogger, []string{"rev-list", "--left-right", "--count", branchName + "..." + branchName + "@{upstream}"}, countsErr)
	}

	return UpstreamObservation{
		State:        UpstreamStateTracked,
		Branch:       branchName,
		UpStream:     upStream,
		UpStreamIcon: upstreamIconForUpStream(upStream),
		RemoteSync:   RemoteSyncStatus{Local: local, Remote: remote},
	}, nil
}

// ------------------------------------
//
//	resolveUpStreamForBranch returns the upstream ref of the named branch
//	(e.g. "origin/main"), or an error when the branch has no resolvable
//	upstream. Resolving against the captured branch name keeps an
//	observation pinned to it when HEAD moves during the read.
//
// ------------------------------------
func resolveUpStreamForBranch(cmdExecutor *executor.CmdExecutor, branchName string) (string, error) {
	gitArgs := []string{"rev-parse", "--abbrev-ref", branchName + "@{upstream}"}

	checkUpStreamCmdExecutor := cmdExecutor.RunGitCmd(gitArgs, false)
	checkUpStreamOutput, checkUpStreamErr := checkUpStreamCmdExecutor.Output()
	if checkUpStreamErr != nil {
		return "", checkUpStreamErr
	}

	return strings.TrimSpace(string(checkUpStreamOutput)), nil
}

// ------------------------------------
//
//	unavailableUpstreamObservation logs the failed inspection command and
//	builds the unavailable observation that introduces no new state.
//
// ------------------------------------
func unavailableUpstreamObservation(gittiLogger *logging.GittiLogging, gitArgs []string, cause error) (UpstreamObservation, error) {
	gittiLogger.RegisterNewLog(logging.CHECK_REMOTE_SYNC_STATUS_OPS, strings.Join(gitArgs, " "), logging.ERROR, fmt.Sprintf("[%s ERROR]: %s", logging.CHECK_REMOTE_SYNC_STATUS_OPS, cause.Error()), true)
	return UpstreamObservation{State: UpstreamStateUnavailable}, cause
}

// ------------------------------------
//
//	branchHasConfiguredUpstream reports whether branch.<name>.remote is
//	set. git exits 1 with no output and no stderr for an unset key, so
//	only that exact case counts as "no upstream configured"; every other
//	failure is a failed inspection and is returned as an error.
//
// ------------------------------------
func branchHasConfiguredUpstream(cmdExecutor *executor.CmdExecutor, branchName string) (bool, error) {
	gitArgs := []string{"config", "--get", fmt.Sprintf("branch.%s.remote", branchName)}
	cmd := cmdExecutor.RunGitCmd(gitArgs, false)
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && len(exitErr.Stderr) == 0 {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(string(output)) != "", nil
}

// ------------------------------------
//
//	remoteSyncCountsAgainstUpstream runs and validates the ahead/behind
//	rev-list of the named branch against its own upstream. Counting the
//	captured branch's refs rather than HEAD keeps the counts pinned to the
//	observed branch when HEAD moves during the read. Any start or parse
//	failure is an error rather than a cleared payload.
//
// ------------------------------------
func remoteSyncCountsAgainstUpstream(cmdExecutor *executor.CmdExecutor, branchName string) (string, string, error) {
	gitArgs := []string{"rev-list", "--left-right", "--count", branchName + "..." + branchName + "@{upstream}"}
	output, err := cmdExecutor.RunGitCmd(gitArgs, false).Output()
	if err != nil {
		return "", "", err
	}
	return parseRemoteSyncCounts(output)
}

// ------------------------------------
//
//	parseRemoteSyncCounts validates that rev-list left-right count output
//	is exactly two non-negative integers, so a malformed or empty result
//	is an error instead of a cleared remote state.
//
// ------------------------------------
func parseRemoteSyncCounts(raw []byte) (string, string, error) {
	parts := strings.Fields(strings.TrimSpace(string(raw)))
	if len(parts) != 2 {
		return "", "", fmt.Errorf("remote sync status: invalid output format")
	}
	for _, part := range parts {
		count, err := strconv.Atoi(part)
		if err != nil || count < 0 {
			return "", "", fmt.Errorf("remote sync status: invalid count %q", part)
		}
	}
	return parts[0], parts[1], nil
}

// ------------------------------------
//
//	DefaultUpStreamRemoteIcon is the generic remote glyph used when the
//	upstream identity cannot pick a remote-kind glyph.
//
// ------------------------------------
const DefaultUpStreamRemoteIcon = "\ue702"

// ------------------------------------
//
//	Related to return upstream with relevant icon
//
// ------------------------------------
func hasUpstreamWithIcon() (string, string, bool) {
	upStream, upStreamExist := hasUpStream()
	if !upStreamExist {
		return DefaultUpStreamRemoteIcon, upStream, upStreamExist
	}

	return upstreamIconForUpStream(upStream), upStream, upStreamExist
}

// ------------------------------------
//
//	upstreamIconForUpStream picks the remote-kind glyph for a resolved
//	upstream ref (e.g. "origin/main"); a missing remote url falls back to
//	the generic glyph.
//
// ------------------------------------
func upstreamIconForUpStream(upStream string) string {
	remoteIcon := DefaultUpStreamRemoteIcon

	upStreamRemoteName := strings.Split(upStream, "/")[0]
	gitArgs := []string{"remote", "get-url", upStreamRemoteName}
	getUpStreamUrlCmdExecutor := executor.GittiCmdExecutor.RunGitCmd(gitArgs, false)
	getUpStreamUrlOutput, getUpStreamUrlErr := getUpStreamUrlCmdExecutor.Output()
	if getUpStreamUrlErr != nil {
		return remoteIcon
	}

	parsedUpstreamUrl := strings.TrimSpace(string(getUpStreamUrlOutput))
	if strings.Contains(parsedUpstreamUrl, "github.com") {
		remoteIcon = "\uea84"
	} else if strings.Contains(parsedUpstreamUrl, "gitlab.com") {
		remoteIcon = "\ue7eb"
	} else if strings.Contains(parsedUpstreamUrl, "gitea.com") {
		remoteIcon = "\uf339"
	} else if strings.Contains(parsedUpstreamUrl, "bitbucket.org") {
		remoteIcon = "\ue703"
	} else if strings.Contains(parsedUpstreamUrl, "source.developers.google.com") {
		remoteIcon = "\ue7f0"
	} else if strings.Contains(parsedUpstreamUrl, "dev.azure.com") {
		remoteIcon = "\uebe8"
	}
	return remoteIcon
}

// ------------------------------------
//
//	check if a file is in a conflict state
//
// ------------------------------------
func isFilesInConflictState(indexState string, workTree string) bool {
	combinedState := indexState + workTree
	if combinedState == "UU" ||
		combinedState == "AA" ||
		combinedState == "DD" ||
		combinedState == "UD" ||
		combinedState == "DU" ||
		combinedState == "AU" ||
		combinedState == "UA" {
		return true
	}
	return false
}

// ------------------------------------
//
//	Related to Git Fetch
//
// ------------------------------------
func gitFetch(gittiLogger *logging.GittiLogging, userTriggered bool) {
	gitArgs := []string{"fetch", "--prune"}
	if userTriggered {
		gittiLogger.RegisterNewLog(logging.FETCH_OPS, strings.Join(gitArgs, ""), logging.INFO, "", true)
	}
	fetchCmdExecutor := executor.GittiCmdExecutor.RunGitCmd(gitArgs, false)
	err := fetchCmdExecutor.Run()

	if err != nil && userTriggered {
		gittiLogger.RegisterNewLog(logging.FETCH_OPS, "", logging.ERROR, fmt.Sprintf("[%s ERROR]: %s", logging.FETCH_OPS, err.Error()), true)
	}
}

// ------------------------------------
//
//	checkIfFileExistWithinDotGitFolder verifies if a specific file (e.g., MERGE_HEAD) exists in the .git directory.
//	This is useful for determining the current state of the repository (e.g., merging, rebasing).
//
// ------------------------------------
func checkIfFileExistWithinDotGitFolder(absolutePath string, fileName string) bool {
	// Join the provided absolute path with the path returned by git to check existence.
	filePath := filepath.Join(absolutePath, fileName)
	_, err := os.Stat(filePath)
	if err == nil {
		return true // Path exists
	}
	return false
}

// ------------------------------------
//
//	Time ago
//
// ------------------------------------
func timeAgo(unixTsString string) string {
	ts, err := strconv.ParseInt(unixTsString, 10, 64)
	if err != nil {
		return i18n.LANGUAGEMAPPING.TimeAgoParseError
	}

	duration := time.Since(time.Unix(ts, 0))
	if duration < 0 {
		return i18n.LANGUAGEMAPPING.TimeAgoJustNow
	}

	switch {
	case duration < time.Minute:
		return fmt.Sprintf(i18n.LANGUAGEMAPPING.TimeAgoSeconds, int(duration.Seconds()))
	case duration < time.Hour:
		return fmt.Sprintf(i18n.LANGUAGEMAPPING.TimeAgoMinutes, int(duration.Minutes()))
	case duration < 24*time.Hour:
		return fmt.Sprintf(i18n.LANGUAGEMAPPING.TimeAgoHours, int(duration.Hours()))
	case duration < 30*24*time.Hour:
		return fmt.Sprintf(i18n.LANGUAGEMAPPING.TimeAgoDays, int(duration.Hours()/24))
	case duration < 365*24*time.Hour:
		return fmt.Sprintf(i18n.LANGUAGEMAPPING.TimeAgoMonths, int(duration.Hours()/(24*30)))
	default:
		return fmt.Sprintf(i18n.LANGUAGEMAPPING.TimeAgoYears, int(duration.Hours()/(24*365)))
	}
}

// ------------------------------------
//
//	Read a submodule git dir's config file and return its `core.worktree` value,
//	the working tree path of the submodule relative to that git dir
//
// ------------------------------------
func getSubmoduleWorktreePath(configPath string) (string, error) {
	f, err := os.Open(configPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Scan for the `worktree = <path>` entry under the config's [core] section
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "worktree = ") {
			worktreePath := strings.TrimSpace(strings.TrimPrefix(line, "worktree = "))
			if worktreePath == "" {
				return "", fmt.Errorf("empty worktree path in config: %s", configPath)
			}
			return worktreePath, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("worktree path not found in config: %s", configPath)
}
