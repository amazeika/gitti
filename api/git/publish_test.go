package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gohyuhan/gitti/executor"
	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/logging"
)

// ------------------------------------
//
//	Helpers for the publish-route tests
//
// ------------------------------------

// ------------------------------------
//
//	realGitPath locates the real git binary so a wrapper script can defer to
//	it after recording or altering the invocation
//
// ------------------------------------
func realGitPath(t *testing.T) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("locating the real git: %v", err)
	}
	return realGit
}

// ------------------------------------
//
//	installGitWrapper puts a wrapper script named git at the front of PATH.
//	The body is Go source formatted with the real git path substituted, so
//	the wrapper can inspect or record an invocation before deferring to the
//	real binary.
//
// ------------------------------------
func installGitWrapper(t *testing.T, body string, args ...interface{}) {
	t.Helper()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(fmt.Sprintf(body, args...)), 0o755); err != nil {
		t.Fatalf("writing the git wrapper: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// ------------------------------------
//
//	publishFixture builds the publish fixture on top of repositoryUnderTest:
//	a bare local origin, a tracked master, and a local-only feature branch
//	checked out at the repository root
//
// ------------------------------------
func publishFixture(t *testing.T, run func(gitArgs ...string)) {
	t.Helper()
	run("init", "--bare", "-q", "origin.git")
	run("remote", "add", "origin", "origin.git")
	run("commit", "--allow-empty", "-q", "-m", "base")
	run("push", "-q", "-u", "origin", "master")
	run("checkout", "-q", "-b", "feature")
	run("commit", "--allow-empty", "-q", "-m", "feature work")
}

// ------------------------------------
//
//	publishCommitUnderTest builds a commit handler whose executor is scoped
//	to the repository root, the way a Git-operations generation binds its
//	worktree-bound routes
//
// ------------------------------------
func publishCommitUnderTest(t *testing.T, root string) *GitCommit {
	t.Helper()
	i18n.InitGittiLanguageMapping("en")
	gittiLogging := logging.InitGittiLogging(64, make(chan string, 256), 3)
	return InitGitCommit(make(chan string, 16), InitGitProcessLock(gittiLogging), executor.InitScopedCmdExecutor(root), gittiLogging)
}

// ------------------------------------
//
//	publishRemoteUnderTest builds a remote handler whose executor is scoped
//	to the repository root
//
// ------------------------------------
func publishRemoteUnderTest(t *testing.T, root string) *GitRemote {
	t.Helper()
	i18n.InitGittiLanguageMapping("en")
	gittiLogging := logging.InitGittiLogging(64, make(chan string, 256), 3)
	return InitGitRemote(make(chan string, 16), nil, executor.InitScopedCmdExecutor(root), gittiLogging)
}

// ------------------------------------
//
//	publishRoute builds a confirmed publish route for the given branch
//
// ------------------------------------
func publishRoute(branch string) GitPushRoute {
	return GitPushRoute{RemoteName: "origin", Branch: branch, Intent: PushIntentPublish}
}

// ------------------------------------
//
//	TestParseRemoteInventoryGroupsUrlsByRemoteAndRejectsMalformedReads
//	covers the immutable inventory parser.
//
//	Each configured remote name groups its complete fetch and push URL sets
//	in a deterministic order, and a fetch-only remote is not a publish
//	target.
//
//	Malformed output is a parse failure rather than a silently dropped
//	remote.
//
// ------------------------------------
func TestParseRemoteInventoryGroupsUrlsByRemoteAndRejectsMalformedReads(t *testing.T) {
	valid := []struct {
		name      string
		raw       string
		wantNames []string
		want      map[string]GitRemoteInventoryEntry
	}{
		{
			name:      "one fetch and one push url",
			raw:       "origin\thttps://example.test/repo.git (fetch)\norigin\thttps://example.test/repo.git (push)\n",
			wantNames: []string{"origin"},
			want: map[string]GitRemoteInventoryEntry{
				"origin": {Name: "origin", FetchURLs: []string{"https://example.test/repo.git"}, PushURLs: []string{"https://example.test/repo.git"}},
			},
		},
		{
			name:      "multiple push urls collapse into one destination",
			raw:       "origin\thttps://fetch.test/repo.git (fetch)\norigin\thttps://push-a.test/repo.git (push)\norigin\thttps://push-b.test/repo.git (push)\n",
			wantNames: []string{"origin"},
			want: map[string]GitRemoteInventoryEntry{
				"origin": {Name: "origin", FetchURLs: []string{"https://fetch.test/repo.git"}, PushURLs: []string{"https://push-a.test/repo.git", "https://push-b.test/repo.git"}},
			},
		},
		{
			name:      "several remotes are sorted deterministically",
			raw:       "zeta\thttps://z.test/repo.git (fetch)\nzeta\thttps://z.test/repo.git (push)\nalpha\thttps://a.test/repo.git (fetch)\nalpha\thttps://a.test/repo.git (push)\n",
			wantNames: []string{"alpha", "zeta"},
			want: map[string]GitRemoteInventoryEntry{
				"alpha": {Name: "alpha", FetchURLs: []string{"https://a.test/repo.git"}, PushURLs: []string{"https://a.test/repo.git"}},
				"zeta":  {Name: "zeta", FetchURLs: []string{"https://z.test/repo.git"}, PushURLs: []string{"https://z.test/repo.git"}},
			},
		},
		{
			name:      "fetch only remote is not push capable",
			raw:       "mirror\thttps://m.test/repo.git (fetch)\n",
			wantNames: []string{"mirror"},
			want: map[string]GitRemoteInventoryEntry{
				"mirror": {Name: "mirror", FetchURLs: []string{"https://m.test/repo.git"}},
			},
		},
		{
			name:      "url with a space is preserved verbatim",
			raw:       "origin\thttps://exa mple.test/repo.git (fetch)\norigin\thttps://exa mple.test/repo.git (push)\n",
			wantNames: []string{"origin"},
			want: map[string]GitRemoteInventoryEntry{
				"origin": {Name: "origin", FetchURLs: []string{"https://exa mple.test/repo.git"}, PushURLs: []string{"https://exa mple.test/repo.git"}},
			},
		},
		{
			name:      "empty output is the empty inventory",
			raw:       "   \n",
			wantNames: []string{},
			want:      map[string]GitRemoteInventoryEntry{},
		},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			inv, err := parseRemoteInventory([]byte(tc.raw))
			if err != nil {
				t.Fatalf("parseRemoteInventory = %v, want success", err)
			}
			gotNames := make([]string, 0, inv.Len())
			for _, entry := range inv.Entries() {
				gotNames = append(gotNames, entry.Name)
			}
			if !reflect.DeepEqual(gotNames, tc.wantNames) {
				t.Fatalf("entry names = %v, want %v", gotNames, tc.wantNames)
			}
			for name, want := range tc.want {
				var got GitRemoteInventoryEntry
				found := false
				for _, entry := range inv.Entries() {
					if entry.Name == name {
						got = entry
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("entry %q is missing from %+v", name, inv.Entries())
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("entry %q = %+v, want %+v", name, got, want)
				}
			}
		})
	}

	invalid := []struct {
		name string
		raw  string
	}{
		{name: "missing purpose", raw: "origin\thttps://example.test/repo.git\n"},
		{name: "unknown purpose", raw: "origin\thttps://example.test/repo.git (FETCH)\n"},
		{name: "empty url", raw: "origin\t(fetch)\n"},
		{name: "missing name", raw: "\thttps://example.test/repo.git (fetch)\n"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseRemoteInventory([]byte(tc.raw)); err == nil {
				t.Errorf("parseRemoteInventory(%q) succeeded, want the malformed line to be a parse failure", tc.raw)
			}
		})
	}
}

// ------------------------------------
//
//	TestPushCapableInventoryExposesOneDestinationPerPushCapableRemote
//	proves the inventory read against a real repository: a remote with
//	several push URLs is one chooser destination carrying its first push
//	URL, a plainly configured remote is its own destination, and the
//	inventory retains the full set of push URLs.
//
// ------------------------------------
func TestPushCapableInventoryExposesOneDestinationPerPushCapableRemote(t *testing.T) {
	root, run := repositoryUnderTest(t)
	run("init", "--bare", "-q", "origin.git")
	run("init", "--bare", "-q", "upstream.git")
	run("init", "--bare", "-q", "second.git")
	run("init", "--bare", "-q", "mirror.git")
	run("remote", "add", "origin", "origin.git")
	run("remote", "set-url", "--add", "--push", "origin", "upstream.git")
	run("remote", "set-url", "--add", "--push", "origin", "second.git")
	run("remote", "add", "mirror", "mirror.git")

	gr := publishRemoteUnderTest(t, root)
	if err := gr.CheckRemoteExist(true); err != nil {
		t.Fatalf("the inventory read failed: %v", err)
	}

	// a remote with several push URLs is one chooser destination carrying
	// its first push URL, and a plainly configured remote is its own
	// destination
	infos := gr.PushCapableRemoteInfos()
	if len(infos) != 2 {
		t.Fatalf("push-capable remotes = %+v, want origin and mirror", infos)
	}
	byName := map[string]string{}
	for _, info := range infos {
		byName[info.Name] = info.Url
	}
	if byName["origin"] != "upstream.git" {
		t.Errorf("the origin destination url = %q, want the first push URL upstream.git", byName["origin"])
	}
	if byName["mirror"] != "mirror.git" {
		t.Errorf("the mirror destination url = %q, want mirror.git", byName["mirror"])
	}

	// the inventory still carries the full set of push URLs for origin
	inv := gr.RemoteInventory()
	for _, entry := range inv.Entries() {
		if entry.Name == "origin" && !reflect.DeepEqual(entry.PushURLs, []string{"upstream.git", "second.git"}) {
			t.Errorf("the origin push URLs = %v, want [upstream.git second.git]", entry.PushURLs)
		}
	}
}

// ------------------------------------
//
//	TestCheckRemoteExistReadFailureKeepsTheLastGoodInventory proves the
//	inventory read is fallible: a failed command or a malformed read returns
//	an error, keeps the previous good generation published, and never turns
//	a failure into "no remotes"; the next successful read replaces it.
//
// ------------------------------------
func TestCheckRemoteExistReadFailureKeepsTheLastGoodInventory(t *testing.T) {
	root, run := repositoryUnderTest(t)
	failFile := filepath.Join(t.TempDir(), "fail")
	badFile := filepath.Join(t.TempDir(), "bad")
	installGitWrapper(t, `#!/bin/sh
sub=""
for a in "$@"; do
  case "$a" in
    remote) sub="$a";;
  esac
done
if [ "$sub" = "remote" ]; then
  if [ -f %q ]; then
    echo "fatal: unable to read remote configuration" >&2
    exit 128
  fi
  if [ -f %q ]; then
    printf 'origin\thttps://example.test/repo.git\n'
    exit 0
  fi
fi
exec %s "$@"
`, failFile, badFile, realGitPath(t))

	run("init", "--bare", "-q", "origin.git")
	run("remote", "add", "origin", "origin.git")

	gr := publishRemoteUnderTest(t, root)
	if err := gr.CheckRemoteExist(true); err != nil {
		t.Fatalf("the first inventory read failed: %v", err)
	}
	if got := gr.RemoteInventory().Len(); got != 1 {
		t.Fatalf("first inventory has %d entries, want 1", got)
	}

	// a failed command read: error, and the last good inventory survives
	if err := os.WriteFile(failFile, nil, 0o644); err != nil {
		t.Fatalf("arming the command failure: %v", err)
	}
	defer os.Remove(failFile)
	if err := gr.CheckRemoteExist(true); err == nil {
		t.Fatal("the failed command read returned nil, want the read error")
	}
	if got := gr.RemoteInventory().Len(); got != 1 {
		t.Fatalf("the failed read published an inventory of %d entries, want the last good 1", got)
	}

	// a malformed read: a parse failure, still not "no remotes"
	os.Remove(failFile)
	if err := os.WriteFile(badFile, nil, 0o644); err != nil {
		t.Fatalf("arming the parse failure: %v", err)
	}
	defer os.Remove(badFile)
	if err := gr.CheckRemoteExist(true); err == nil {
		t.Fatal("the malformed read returned nil, want the parse error")
	}
	if got := gr.RemoteInventory().Len(); got != 1 {
		t.Fatalf("the malformed read published an inventory of %d entries, want the last good 1", got)
	}

	// the next successful read restores a fresh generation
	os.Remove(badFile)
	if err := gr.CheckRemoteExist(true); err != nil {
		t.Fatalf("the recovery read failed: %v", err)
	}
	if got := gr.PushCapableRemoteInfos(); len(got) != 1 || got[0].Name != "origin" {
		t.Fatalf("recovered push-capable remotes = %+v, want origin", got)
	}
}

// ------------------------------------
//
//	TestConcurrentInventoryRefreshAndReadsStayWholeAndRaceFree hammers the
//	inventory with concurrent refreshes and UI reads (under -race) and
//	verifies every read observes a complete generation: unique names, and
//	for every name present the full fetch and push URL set, never a partial
//	one.
//
// ------------------------------------
func TestConcurrentInventoryRefreshAndReadsStayWholeAndRaceFree(t *testing.T) {
	root, run := repositoryUnderTest(t)
	// slow the inventory read so refreshes and reads interleave
	installGitWrapper(t, `#!/bin/sh
for a in "$@"; do
  case "$a" in
    remote)
      sleep 0.005
      break
      ;;
  esac
done
exec %s "$@"
`, realGitPath(t))

	run("init", "--bare", "-q", "origin.git")
	run("init", "--bare", "-q", "other.git")
	run("remote", "add", "origin", "origin.git")
	run("remote", "add", "other", "other.git")

	gr := publishRemoteUnderTest(t, root)
	if err := gr.CheckRemoteExist(true); err != nil {
		t.Fatalf("the baseline inventory read failed: %v", err)
	}

	done := make(chan struct{})
	var wg sync.WaitGroup

	validate := func(t *testing.T) func(GitRemoteInventory) {
		t.Helper()
		return func(inv GitRemoteInventory) {
			seen := map[string]bool{}
			if inv.Len() > 2 {
				t.Errorf("the read observed %d remotes, want at most 2", inv.Len())
			}
			for _, entry := range inv.Entries() {
				if seen[entry.Name] {
					t.Errorf("the read observed duplicate remote name %q", entry.Name)
				}
				seen[entry.Name] = true
				// every name present must carry its complete URL set: a
				// partial generation is a data race on the reader
				if len(entry.FetchURLs) != 1 || len(entry.PushURLs) != 1 {
					t.Errorf("the read observed a partial entry %+v, want one fetch and one push URL", entry)
				}
			}
		}
	}

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					_ = gr.CheckRemoteExist(true)
				}
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			check := validate(t)
			for {
				select {
				case <-done:
					return
				default:
					check(gr.RemoteInventory())
					if infos := gr.PushCapableRemoteInfos(); len(infos) != gr.RemoteInventory().Len() {
						t.Errorf("push-capable infos %d disagree with the inventory length %d", len(infos), gr.RemoteInventory().Len())
					}
					_ = gr.RemoteSyncStatusAndUpstream()
				}
			}
		}()
	}

	time.Sleep(400 * time.Millisecond)
	close(done)
	wg.Wait()
}

// ------------------------------------
//
//	TestBuildPublishGitArgsIsTheConfirmedNormalPush pins the publish argv:
//	a normal push with --progress and --set-upstream, the source pinned to
//	HEAD, and no force flag and no branch refspec.
//
// ------------------------------------
func TestBuildPublishGitArgsIsTheConfirmedNormalPush(t *testing.T) {
	want := []string{"push", "--progress", "--set-upstream", "origin", "HEAD"}
	if got := buildPublishGitArgs("origin"); !reflect.DeepEqual(got, want) {
		t.Fatalf("buildPublishGitArgs = %v, want %v", got, want)
	}
	for _, argument := range buildPublishGitArgs("origin") {
		if strings.HasPrefix(argument, "--force") {
			t.Errorf("the publish argv carries the force flag %q", argument)
		}
	}
}

// ------------------------------------
//
//	TestPublishRouteValidationRefusesUnsafeAndUnconfiguredRemotes proves the
//	confirmed route is re-validated before any process exists: an empty or
//	leading-hyphen remote name is an argument-injection shape, a name that
//	does not name a configured remote with a URL (never configured, or
//	removed since the confirmation) is refused, and an unknown intent is
//	rejected.
//
// ------------------------------------
func TestPublishRouteValidationRefusesUnsafeAndUnconfiguredRemotes(t *testing.T) {
	root, run := repositoryUnderTest(t)
	publishFixture(t, run)
	gc := publishCommitUnderTest(t, root)

	unsafe := []GitPushRoute{
		{RemoteName: "", Branch: "feature", Intent: PushIntentPublish},
		{RemoteName: "-f", Branch: "feature", Intent: PushIntentPublish},
		{RemoteName: "--force", Branch: "feature", Intent: PushIntentPublish},
		{RemoteName: "origin", Branch: "feature", Intent: GitPushIntent("weird")},
	}
	for _, route := range unsafe {
		if _, err := gc.prepareGitPush(route); err == nil {
			t.Errorf("prepareGitPush(%+v) succeeded, want the refusal", route)
		}
	}

	if _, err := gc.prepareGitPush(GitPushRoute{RemoteName: "nope", Branch: "feature", Intent: PushIntentPublish}); err == nil || !strings.Contains(err.Error(), "not a configured remote") {
		t.Fatalf("an unconfigured remote was not refused: %v", err)
	}

	// a remote removed after the confirmation is the same refusal
	if _, err := gc.prepareGitPush(publishRoute("feature")); err != nil {
		t.Fatalf("the baseline publish route failed before the remote was removed: %v", err)
	}
	run("remote", "remove", "origin")
	if _, err := gc.prepareGitPush(publishRoute("feature")); err == nil || !strings.Contains(err.Error(), "not a configured remote") {
		t.Fatalf("a removed remote was not refused: %v", err)
	}
}

// ------------------------------------
//
//	TestPublishRouteReResolvesTheBranchStateBeforeStart proves the publish
//	intent re-reads the branch state through the generation executor
//	immediately before the process starts.
//
//	An unpublished branch publishes with --set-upstream. A branch that
//	gained its upstream since the confirmation falls back to the normal
//	tracked push.
//
//	Branch drift, a detached HEAD, an unborn branch, and an unreadable
//	observation all refuse to start.
//
// ------------------------------------
func TestPublishRouteReResolvesTheBranchStateBeforeStart(t *testing.T) {
	t.Run("unpublished branch publishes", func(t *testing.T) {
		root, run := repositoryUnderTest(t)
		publishFixture(t, run)
		gc := publishCommitUnderTest(t, root)

		args, err := gc.prepareGitPush(publishRoute("feature"))
		if err != nil {
			t.Fatalf("the unpublished publish route failed: %v", err)
		}
		if want := buildPublishGitArgs("origin"); !reflect.DeepEqual(args, want) {
			t.Fatalf("publish argv = %v, want %v", args, want)
		}
	})

	t.Run("branch that gained an upstream uses the tracked push", func(t *testing.T) {
		root, run := repositoryUnderTest(t)
		publishFixture(t, run)
		run("push", "-q", "-u", "origin", "feature")
		gc := publishCommitUnderTest(t, root)

		args, err := gc.prepareGitPush(publishRoute("feature"))
		if err != nil {
			t.Fatalf("the tracked-fallback publish route failed: %v", err)
		}
		want := []string{"push", "--progress", "origin"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("tracked-fallback argv = %v, want %v (a normal push without --set-upstream)", args, want)
		}
	})

	t.Run("branch drift refuses", func(t *testing.T) {
		root, run := repositoryUnderTest(t)
		publishFixture(t, run)
		run("checkout", "-q", "master")
		gc := publishCommitUnderTest(t, root)

		_, err := gc.prepareGitPush(publishRoute("feature"))
		if err == nil || !strings.Contains(err.Error(), "changed from") {
			t.Fatalf("a drifted branch was not refused: %v", err)
		}
	})

	t.Run("detached head refuses", func(t *testing.T) {
		root, run := repositoryUnderTest(t)
		publishFixture(t, run)
		run("checkout", "-q", "--detach")
		gc := publishCommitUnderTest(t, root)

		_, err := gc.prepareGitPush(publishRoute(""))
		if err == nil || !strings.Contains(err.Error(), "no publishable branch") {
			t.Fatalf("a detached head was not refused: %v", err)
		}
	})

	t.Run("unborn branch refuses", func(t *testing.T) {
		root, run := repositoryUnderTest(t)
		publishFixture(t, run)
		run("checkout", "-q", "--orphan", "unborn")
		gc := publishCommitUnderTest(t, root)

		_, err := gc.prepareGitPush(publishRoute("unborn"))
		if err == nil || !strings.Contains(err.Error(), "no publishable branch") {
			t.Fatalf("an unborn branch was not refused: %v", err)
		}
	})

	t.Run("unreadable observation refuses", func(t *testing.T) {
		root, run := repositoryUnderTest(t)
		publishFixture(t, run)
		installGitWrapper(t, `#!/bin/sh
case "$*" in
  *"config --get branch.feature.remote"*)
    echo "fatal: unable to read branch configuration" >&2
    exit 128
    ;;
esac
exec %s "$@"
`, realGitPath(t))
		gc := publishCommitUnderTest(t, root)

		_, err := gc.prepareGitPush(publishRoute("feature"))
		if err == nil || !strings.Contains(err.Error(), "could not be re-read") {
			t.Fatalf("an unreadable observation was not refused: %v", err)
		}
	})
}

//	pushArgvCaptureWrapper records every push invocation's full argv, one
//	argument per line and a blank line between invocations, before deferring
//	to the real git
//
// ------------------------------------
const pushArgvCaptureWrapper = `#!/bin/sh
for a in "$@"; do
  case "$a" in
    push)
      {
        for b in "$@"; do printf '%%s\n' "$b"; done
        printf '\n'
      } >> "$PUSH_ARGV_LOG"
      break
      ;;
  esac
done
exec %s "$@"
`

// ------------------------------------
//
//	TestPublishPushPublishesTheUnpublishedBranch is the end-to-end publish:
//	the confirmed route starts exactly one normal push with --set-upstream
//	in the generation's worktree, the bare remote receives the branch, and
//	the upstream is configured for it.
//
// ------------------------------------
func TestPublishPushPublishesTheUnpublishedBranch(t *testing.T) {
	root, run := repositoryUnderTest(t)
	argvLog := filepath.Join(t.TempDir(), "push-argv.log")
	installGitWrapper(t, pushArgvCaptureWrapper, realGitPath(t))
	t.Setenv("PUSH_ARGV_LOG", argvLog)

	publishFixture(t, run)
	// drop the fixture's own push invocations from the log
	if err := os.WriteFile(argvLog, nil, 0o644); err != nil {
		t.Fatalf("clearing the argv log: %v", err)
	}

	gc := publishCommitUnderTest(t, root)
	result := gc.GitPush(context.Background(), publishRoute("feature"))

	if !result.Success() {
		t.Fatalf("the publish push failed: exit %d, err %v, stderr %s", result.ExitCode(), result.Err(), result.Stderr())
	}
	if got := result.WorkingDirectory(); filepath.Clean(got) != filepath.Clean(root) {
		t.Errorf("the push ran in %q, want the generation worktree %q", got, root)
	}
	argv := result.Argv()
	if len(argv) < 5 {
		t.Fatalf("the push argv is too short: %v", argv)
	}
	if want := []string{"push", "--progress", "--set-upstream", "origin", "HEAD"}; !reflect.DeepEqual(argv[len(argv)-5:], want) {
		t.Fatalf("the push argv tail = %v, want %v", argv[len(argv)-5:], want)
	}

	// the bare remote received the branch tip and the upstream is set
	head := runGitRevParse(t, root, "HEAD")
	if got := runGitRevParse(t, root, "refs/remotes/origin/feature"); got != head {
		t.Fatalf("the remote-tracking ref = %s, want the published tip %s", got, head)
	}
	if got := runGitRevParse(t, filepath.Join(root, "origin.git"), "refs/heads/feature"); got != head {
		t.Fatalf("the bare remote ref = %s, want the published tip %s", got, head)
	}
	if got := runGitOutput(t, root, "rev-parse", "--abbrev-ref", "feature@{upstream}"); got != "origin/feature" {
		t.Fatalf("the published upstream = %q, want origin/feature", got)
	}

	// exactly one push process ran, with the confirmed argv
	invocations := readArgvInvocations(t, argvLog)
	if len(invocations) != 1 {
		t.Fatalf("%d push invocations were recorded, want exactly 1", len(invocations))
	}
}

// ------------------------------------
//
//	TestPublishPushWorktreeGenerationDriftRefusesBeforeStart proves the
//	route's execution guard is checked after preparation and immediately
//	before Start: a stale generation gets an unstarted actionable result and
//	no push process is ever launched.
//
// ------------------------------------
func TestPublishPushWorktreeGenerationDriftRefusesBeforeStart(t *testing.T) {
	root, run := repositoryUnderTest(t)
	argvLog := filepath.Join(t.TempDir(), "push-argv.log")
	installGitWrapper(t, pushArgvCaptureWrapper, realGitPath(t))
	t.Setenv("PUSH_ARGV_LOG", argvLog)

	publishFixture(t, run)
	if err := os.WriteFile(argvLog, nil, 0o644); err != nil {
		t.Fatalf("clearing the argv log: %v", err)
	}

	gc := publishCommitUnderTest(t, root)
	stale := errors.New("stale worktree generation")
	route := publishRoute("feature")
	route.ActiveGuard = func() error { return stale }

	result := gc.GitPush(context.Background(), route)

	if result.Success() {
		t.Fatal("a stale generation published a successful push")
	}
	if result.Started() {
		t.Fatal("a stale generation started a push process")
	}
	if !errors.Is(result.Err(), stale) {
		t.Fatalf("the unstarted result error = %v, want the guard's error", result.Err())
	}
	if got := readArgvInvocations(t, argvLog); len(got) != 0 {
		t.Fatalf("a stale generation recorded %d push invocations, want none", len(got))
	}
}

// ------------------------------------
//
//	TestGitPushWithSigningSharesThePublishPreparation proves the
//	signing-required route builds its argv through exactly the same
//	preparation as the background route: the same confirmed argv on
//	success, and the same unstarted refusal on a removed remote or an
//	unsafe name.
//
// ------------------------------------
func TestGitPushWithSigningSharesThePublishPreparation(t *testing.T) {
	root, run := repositoryUnderTest(t)
	publishFixture(t, run)
	gc := publishCommitUnderTest(t, root)

	args, err := gc.GitPushWithSigning(publishRoute("feature"))
	if err != nil {
		t.Fatalf("the signing publish preparation failed: %v", err)
	}
	if want := buildPublishGitArgs("origin"); !reflect.DeepEqual(args, want) {
		t.Fatalf("the signing publish argv = %v, want %v", args, want)
	}

	// the same preparation refuses a removed remote
	run("remote", "remove", "origin")
	if args, err := gc.GitPushWithSigning(publishRoute("feature")); err == nil || !strings.Contains(err.Error(), "not a configured remote") {
		t.Fatalf("a removed remote was not refused for signing: args %v, err %v", args, err)
	}

	// ...and an unsafe remote name
	if args, err := gc.GitPushWithSigning(GitPushRoute{RemoteName: "-f", Branch: "feature", Intent: PushIntentPublish}); err == nil || !strings.Contains(err.Error(), "not safe") {
		t.Fatalf("an unsafe remote name was not refused for signing: args %v, err %v", args, err)
	}
}

// ------------------------------------
//
//	TestScopedCmdExecutorBindsTheGenerationWorktree proves a scoped executor
//	pins its git commands to the captured worktree without re-targeting the
//	shared global executor.
//
// ------------------------------------
func TestScopedCmdExecutorBindsTheGenerationWorktree(t *testing.T) {
	makeRepo := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		cmd := exec.Command("git", "init", "-q", "-b", "master", ".")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init in %s: %v\n%s", dir, err, out)
		}
		return dir
	}

	rootA := makeRepo(t)
	rootB := makeRepo(t)

	original := executor.GittiCmdExecutor
	t.Cleanup(func() { executor.GittiCmdExecutor = original })
	executor.InitCmdExecutor(rootA)

	scopedB := executor.InitScopedCmdExecutor(rootB)
	out, err := scopedB.RunGitCmd([]string{"rev-parse", "--show-toplevel"}, false).Output()
	if err != nil {
		t.Fatalf("the scoped executor read failed: %v", err)
	}
	if got, want := evalDir(t, strings.TrimSpace(string(out))), evalDir(t, rootB); got != want {
		t.Fatalf("the scoped executor ran in %q, want the captured worktree %q", got, want)
	}

	// the global executor is untouched by the scoped one
	if got := executor.GittiCmdExecutor.RepoPath(); filepath.Clean(got) != filepath.Clean(rootA) {
		t.Fatalf("the global executor path = %q, want the original %q", got, rootA)
	}
	out, err = executor.GittiCmdExecutor.RunGitCmd([]string{"rev-parse", "--show-toplevel"}, false).Output()
	if err != nil {
		t.Fatalf("the global executor read failed: %v", err)
	}
	if got, want := evalDir(t, strings.TrimSpace(string(out))), evalDir(t, rootA); got != want {
		t.Fatalf("the global executor ran in %q, want %q", got, want)
	}
}

// ------------------------------------
//
//	evalDir returns the canonical (symlinks resolved, cleaned) form of a
//	path so a rev-parse result and a t.TempDir path compare equal
//
// ------------------------------------
func evalDir(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolving %q: %v", dir, err)
	}
	return filepath.Clean(resolved)
}

// ------------------------------------
//
//	readArgvInvocations parses the argv log into one argument list per push
//	invocation
//
// ------------------------------------
func readArgvInvocations(t *testing.T, logPath string) [][]string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("reading the argv log: %v", err)
	}
	var invocations [][]string
	for _, block := range strings.Split(string(data), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		invocations = append(invocations, strings.Split(block, "\n"))
	}
	return invocations
}

// ------------------------------------
//
//	runGitRevParse and runGitOutput run real git reads for assertions
//
// ------------------------------------
func runGitOutput(t *testing.T, dir string, gitArgs ...string) string {
	t.Helper()
	cmd := exec.Command("git", gitArgs...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", gitArgs, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func runGitRevParse(t *testing.T, dir string, revision string) string {
	t.Helper()
	return runGitOutput(t, dir, "rev-parse", revision)
}
