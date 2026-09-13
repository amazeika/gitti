package git

import (
	"fmt"
	"strings"

	"github.com/gohyuhan/gitti/executor"
)

type localRefInfo struct {
	name     string
	current  bool
	occupied bool
}

const localRefFormat = "%(refname:lstrip=2)%00%(if)%(HEAD)%(then)1%(else)0%(end)%00%(if)%(worktreepath)%(then)1%(else)0%(end)"

// ------------------------------------
//
//	Discover every local ref and its current-worktree and occupancy state without
//	using presentation-oriented git branch output.
//
// ------------------------------------
func discoverLocalRefs() ([]localRefInfo, error) {
	gitArgs := []string{"for-each-ref", "--format=" + localRefFormat, "refs/heads/"}
	output, err := executor.GittiCmdExecutor.RunGitCmd(gitArgs, false).Output()
	if err != nil {
		return nil, err
	}
	return parseLocalRefs(output)
}

func parseLocalRefs(output []byte) ([]localRefInfo, error) {
	if len(output) == 0 {
		return []localRefInfo{}, nil
	}
	if output[len(output)-1] != '\n' {
		return nil, fmt.Errorf("local-ref snapshot has a partial record")
	}

	records := strings.Split(string(output[:len(output)-1]), "\n")
	parsed := make([]localRefInfo, 0, len(records))
	currentCount := 0
	for _, record := range records {
		fields := strings.Split(record, "\x00")
		if len(fields) != 3 {
			return nil, fmt.Errorf("local-ref record has %d fields, want 3", len(fields))
		}
		if fields[0] == "" {
			return nil, fmt.Errorf("local-ref record has an empty name")
		}
		if (fields[1] != "0" && fields[1] != "1") || (fields[2] != "0" && fields[2] != "1") {
			return nil, fmt.Errorf("local-ref record has an unknown boolean")
		}

		current := fields[1] == "1"
		if current {
			currentCount++
			if currentCount > 1 {
				return nil, fmt.Errorf("local-ref snapshot has multiple current refs")
			}
		}
		parsed = append(parsed, localRefInfo{name: fields[0], current: current, occupied: fields[2] == "1"})
	}
	return parsed, nil
}
