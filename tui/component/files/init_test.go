package files

import (
	"testing"

	"charm.land/bubbles/v2/list"
	"github.com/gohyuhan/gitti/api"
	"github.com/gohyuhan/gitti/api/git"
	"github.com/gohyuhan/gitti/i18n"
	"github.com/gohyuhan/gitti/tui/constant"
	"github.com/gohyuhan/gitti/tui/types"
)

func TestInitModifiedFilesListPreservesLayoutGeometry(t *testing.T) {
	originalLanguageMapping := i18n.LANGUAGEMAPPING
	i18n.InitGittiLanguageMapping("EN")
	t.Cleanup(func() {
		i18n.LANGUAGEMAPPING = originalLanguageMapping
	})

	tests := []struct {
		name                  string
		listWidth             int
		listHeight            int
		stackPanelHeight      int
		wantWidth, wantHeight int
	}{
		{
			name:             "two-column inner width",
			listWidth:        34,
			listHeight:       14,
			stackPanelHeight: 14,
			wantWidth:        34,
			wantHeight:       14,
		},
		{
			name:             "focused full height",
			listWidth:        118,
			listHeight:       37,
			stackPanelHeight: 14,
			wantWidth:        118,
			wantHeight:       37,
		},
		{
			name:             "uninitialized geometry fallback",
			stackPanelHeight: 14,
			wantWidth:        118,
			wantHeight:       14,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modifiedFilesList := list.New(nil, GitModifiedFilesItemDelegate{}, tt.listWidth, tt.listHeight)
			model := &types.GittiModel{
				GitOperations: &api.GitOperations{
					GitFiles: git.InitGitFile(nil, nil, nil),
				},
				WindowLeftPanelWidth:              120,
				ModifiedFilesComponentPanelHeight: tt.stackPanelHeight,
				CurrentRepoModifiedFilesInfoList:  modifiedFilesList,
				PanelFilterQuery:                  map[string]string{constant.ModifiedFilesComponentPanel: ""},
			}

			InitModifiedFilesList(model)

			if got := model.CurrentRepoModifiedFilesInfoList.Width(); got != tt.wantWidth {
				t.Errorf("modified-files list width = %d, want %d", got, tt.wantWidth)
			}
			if got := model.CurrentRepoModifiedFilesInfoList.Height(); got != tt.wantHeight {
				t.Errorf("modified-files list height = %d, want %d", got, tt.wantHeight)
			}
		})
	}
}
