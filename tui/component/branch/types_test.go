package branch

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	"github.com/gohyuhan/gitti/api"
	gitapi "github.com/gohyuhan/gitti/api/git"
	"github.com/gohyuhan/gitti/executor"
	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/logging"
	"github.com/gohyuhan/gitti/tui/constant"
	"github.com/gohyuhan/gitti/tui/types"
)

func TestGitBranchItemRendersStatusWithoutChangingFilterIdentity(t *testing.T) {
	delegate := GitBranchItemDelegate{}
	model := list.New([]list.Item{}, delegate, 80, 2)

	for _, test := range []struct {
		name string
		item GitBranchItem
		want string
	}{
		{name: "current", item: GitBranchItem{BranchName: "main", IsCheckedOut: true}, want: "* main"},
		{name: "linked", item: GitBranchItem{BranchName: "+skill", IsCheckedOutInLinkedWorktree: true}, want: "+ +skill"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var rendered bytes.Buffer
			delegate.Render(&rendered, model, 0, test.item)
			if !strings.Contains(rendered.String(), test.want) {
				t.Errorf("rendered row = %q, want %q", rendered.String(), test.want)
			}
			if got := test.item.FilterValue(); got != test.item.BranchName {
				t.Errorf("FilterValue = %q, want canonical %q", got, test.item.BranchName)
			}
		})
	}
}

func TestInitBranchListOmitsDetachedBlankCurrentRow(t *testing.T) {
	i18n.InitGittiLanguageMapping("en")
	gitBranch := gitapi.InitGitBranch(nil, false, nil)
	model := branchListModel(gitBranch, list.New([]list.Item{}, GitBranchItemDelegate{}, 80, 10))

	InitBranchList(model)
	if model.CheckOutBranch != "" {
		t.Errorf("CheckOutBranch = %q, want empty detached state", model.CheckOutBranch)
	}
	if len(model.CurrentRepoBranchesInfoList.Items()) != 0 {
		t.Errorf("detached list has %d rows, want no blank current row", len(model.CurrentRepoBranchesInfoList.Items()))
	}
}

func TestInitBranchListUsesOneSnapshotAndConditionalCurrentOffset(t *testing.T) {
	i18n.InitGittiLanguageMapping("en")
	root := t.TempDir()
	runGit := func(gitArgs ...string) {
		t.Helper()
		cmd := exec.Command("git", gitArgs...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(gitArgs, " "), err, output)
		}
	}
	runGit("init", "-q", "-b", "master")
	runGit("config", "user.name", "Ada")
	runGit("config", "user.email", "ada@example.com")
	runGit("commit", "-q", "--allow-empty", "-m", "first")
	runGit("branch", "feature")

	originalExecutor := executor.GittiCmdExecutor
	t.Cleanup(func() { executor.GittiCmdExecutor = originalExecutor })
	executor.InitCmdExecutor(root)
	gitBranch := gitapi.InitGitBranch(nil, false, logging.InitGittiLogging(8, make(chan string, 16), 3))
	gitBranch.GetLatestBranchesInfo(nil)
	previousList := list.New([]list.Item{GitBranchItem{BranchName: "feature"}}, GitBranchItemDelegate{}, 80, 10)
	model := branchListModel(gitBranch, previousList)

	InitBranchList(model)
	if got := branchListNames(model.CurrentRepoBranchesInfoList.Items()); strings.Join(got, ",") != "master,feature" {
		t.Errorf("attached list = %v, want current followed by other refs", got)
	}
	if model.CurrentRepoBranchesInfoList.Index() != 1 {
		t.Errorf("attached selected index = %d, want +1 offset for feature", model.CurrentRepoBranchesInfoList.Index())
	}

	runGit("switch", "-q", "--detach", "HEAD")
	gitBranch.GetLatestBranchesInfo(nil)
	InitBranchList(model)
	if got := branchListNames(model.CurrentRepoBranchesInfoList.Items()); strings.Join(got, ",") != "feature,master" {
		t.Errorf("detached list = %v, want all refs without a blank current row", got)
	}
	if model.CurrentRepoBranchesInfoList.Index() != 0 {
		t.Errorf("detached selected index = %d, want no current-row offset", model.CurrentRepoBranchesInfoList.Index())
	}
}

// ------------------------------------
//
//	Build the minimal model required to exercise local-branch list projection.
//
// ------------------------------------
func branchListModel(gitBranch *gitapi.GitBranch, currentList list.Model) *types.GittiModel {
	return &types.GittiModel{
		WindowLeftPanelWidth:              80,
		LocalBranchesComponentPanelHeight: 10,
		CurrentRepoBranchesInfoList:       currentList,
		GitOperations:                     &api.GitOperations{GitBranch: gitBranch},
		PanelFilterQuery:                  map[string]string{constant.SHOW_LOCAL_BRANCH: ""},
	}
}

// ------------------------------------
//
//	Project branch-list items to their canonical names for assertions.
//
// ------------------------------------
func branchListNames(items []list.Item) []string {
	branchNames := make([]string, 0, len(items))
	for _, item := range items {
		branchNames = append(branchNames, item.(GitBranchItem).BranchName)
	}
	return branchNames
}
