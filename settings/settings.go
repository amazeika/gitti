package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/gohyuhan/gitti/api/git"
	"github.com/gohyuhan/gitti/constant"
)

const (
	MAXFILEWATCHERDEBOUNCEMS           = 1000
	MAXGITFILESACTIVEREFRESHDURATIONMS = 5000

	MAXLEFTPANELWIDTHRATIO = 0.65
	MINLEFTPANELWIDTHRATIO = 0.3

	// How far a chain of symlinks to the config file is followed before it is
	// treated as a loop. Chosen as the smallest limit any supported kernel applies
	// to a chain of its own (macOS 32, Linux 40, Windows 63), but it is a sanity
	// bound rather than a guarantee: the kernel counts every symlink it traverses,
	// including directory links in the path, so no per-hop count here can promise
	// that what this resolver writes is what os.ReadFile can read back. What makes
	// the mismatch harmless is InitOrReadConfig, which leaves an unreadable config
	// alone instead of replacing it.
	MAXCONFIGSYMLINKHOPS = 32
)

var GITTICONFIGSETTINGS *GittiConfigSettings

// Set when the config could not be located or could not be read, which bars every
// write for the rest of the session. Truncating in place used to make this impossible
// by accident: a file the process cannot open cannot be opened for writing
// either. Replacing the file by a rename needs only the directory, so without
// this the first setting anyone changes would put the defaults over a config
// whose contents are intact and merely unavailable.
var configUnreadable bool

type GittiConfigSettings struct {
	FileWatcherDebounceMS           int       `json:"file_watcher_debounce_milli_second"`
	GitFilesActiveRefreshDurationMS int       `json:"git_files_active_refresh_duration_milli_second"`
	GitRemoteSyncStatusDurationMS   int       `json:"git_fetch_duration_milli_second"`
	GitInitDefaultBranch            string    `json:"git_init_default_branch"`
	LeftPanelWidthRatio             float64   `json:"left_panel_width_ratio"`
	RightPanelWidthRatio            float64   `json:"right_panel_width_ratio"`
	LanguageCode                    string    `json:"language_code"`
	LastUpdateCheckTime             time.Time `json:"last_update_check_time"`
	AutoUpdate                      bool      `json:"auto_update"`
	Editor                          string    `json:"editor"`
	MaxCommitLogCount               int       `json:"max_commit_log_count"`
	MaxRefLogCount                  int       `json:"max_reflog_count"`
	AllowCommitGraphWrite           bool      `json:"allow_commit_graph_write"`
	MaxLogCount                     int       `json:"max_log_count"`
	ShowXLog                        int       `json:"show_x_log"`
	OverrideSigningUISuspend        bool      `json:"override_signing_ui_suspend"`
	FfMerge                         bool      `json:"ff_merge"`
	CommitLogShowRefs               bool      `json:"commit_log_show_refs"`
	CommitLogShowAllBranches        bool      `json:"commit_log_show_all_branches"`
}

var GittiDefaultConfigSettings = GittiConfigSettings{
	FileWatcherDebounceMS:           200,
	GitFilesActiveRefreshDurationMS: 2500,
	GitRemoteSyncStatusDurationMS:   60000,
	GitInitDefaultBranch:            "master",
	LeftPanelWidthRatio:             0.3,
	RightPanelWidthRatio:            0.7,
	LanguageCode:                    "EN",
	LastUpdateCheckTime:             time.Now().UTC(),
	AutoUpdate:                      true,
	Editor:                          "vim",
	MaxCommitLogCount:               2500,
	MaxRefLogCount:                  2500,
	AllowCommitGraphWrite:           true,
	MaxLogCount:                     300,
	ShowXLog:                        3,
	OverrideSigningUISuspend:        false,
	FfMerge:                         false,
	CommitLogShowRefs:               true,
	CommitLogShowAllBranches:        false,
}

// ------------------------------------
//
//	Get the config path (creates directories if needed)
//
// ------------------------------------
func getConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	appDir := filepath.Join(dir, constant.APPNAME)
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(appDir, "config.json"), nil
}

// ------------------------------------
//
//	Initialize or read the configuration from file, with schema validation
//
// ------------------------------------
func InitOrReadConfig() {
	// A copy, not the package defaults themselves: every path that leaves this
	// pointer in place carries on into Update* setters, and those would otherwise
	// write through it into the very struct ensureConfigIntegrity compares against.
	defaults := GittiDefaultConfigSettings
	GITTICONFIGSETTINGS = &defaults
	configUnreadable = false

	cfgPath, err := getConfigPath()
	if err != nil {
		// The config could not even be located, so whether one exists is unknown.
		// That is the same bar as a config that could not be read: writing later,
		// once the path resolves again, would put the defaults over a file this
		// session never saw.
		configUnreadable = true
		return
	}

	// If config doesn't exist, create a default one
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		writeDefaultConfig(cfgPath)
		return
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if !os.IsNotExist(err) {
			// The file is there but unreadable - a permission problem, a symlink
			// chain the kernel refuses, a failing disk. A config that cannot be read
			// is not a config that is wrong, and rewriting it with the defaults
			// would destroy settings that are almost certainly fine. Run on the
			// defaults for this session and write nothing.
			configUnreadable = true
			return
		}
		writeDefaultConfig(cfgPath)
		return
	}

	var cfg GittiConfigSettings
	if err := json.Unmarshal(data, &cfg); err != nil {
		// Bad JSON → reset
		writeDefaultConfig(cfgPath)
		return
	}

	// A bool's zero value is a legitimate setting, so absence has to be decoded
	// rather than inferred. Let encoding/json answer it: the same bytes go into a
	// mirror of the struct whose bools are pointers, which leaves a nil for a key
	// that is absent or null. Asking the decoder means duplicate keys, case-variant
	// keys and null all resolve exactly as they did for the typed decode above.
	declaredBools, err := decodeDeclaredBools(data)
	if err != nil {
		writeDefaultConfig(cfgPath)
		return
	}

	// Validate and fix missing or invalid fields
	changed := ensureConfigIntegrity(&cfg, &GittiDefaultConfigSettings, declaredBools)
	if changed {
		saveConfig(cfgPath, cfg)
	}

	// limit to be wihtin the defined maximum
	// this is to ensure that the left panel ratio is within the set area
	if cfg.LeftPanelWidthRatio > MAXLEFTPANELWIDTHRATIO || cfg.LeftPanelWidthRatio < MINLEFTPANELWIDTHRATIO {
		cfg.LeftPanelWidthRatio = 0.3
		cfg.RightPanelWidthRatio = 0.7
		saveConfig(cfgPath, cfg)
	} else {
		// this is to ensure that the set width ratio of both left and right add up to 1.0
		if 1-cfg.LeftPanelWidthRatio != cfg.RightPanelWidthRatio {
			cfg.RightPanelWidthRatio = 1 - cfg.LeftPanelWidthRatio
			saveConfig(cfgPath, cfg)
		}
	}
	cfg.FileWatcherDebounceMS = min(cfg.FileWatcherDebounceMS, MAXFILEWATCHERDEBOUNCEMS)
	cfg.GitFilesActiveRefreshDurationMS = min(cfg.GitFilesActiveRefreshDurationMS, MAXGITFILESACTIVEREFRESHDURATIONMS)

	// max log count should always be equal or larger than show x log
	if cfg.MaxLogCount < cfg.ShowXLog {
		cfg.MaxLogCount = cfg.ShowXLog
		saveConfig(cfgPath, cfg)
	}

	GITTICONFIGSETTINGS = &cfg
}

// ------------------------------------
//
//	Check every field against the default and assign default values if zero
//
// ------------------------------------
func ensureConfigIntegrity(cfg *GittiConfigSettings, def *GittiConfigSettings, declaredBools map[int]*bool) bool {
	cfgVal := reflect.ValueOf(cfg).Elem()
	defVal := reflect.ValueOf(def).Elem()
	changed := false

	for i := 0; i < cfgVal.NumField(); i++ {
		field := cfgVal.Field(i)
		defaultField := defVal.Field(i)

		switch field.Kind() {
		case reflect.Bool:
			// Take the decoder's answer rather than testing for the zero value:
			// resetting a zero-valued bool would rewrite every explicit false back
			// to its default and make a default-true setting impossible to turn off.
			declared, ok := declaredBools[i]
			if !ok {
				continue
			}
			if declared == nil {
				field.Set(defaultField)
				changed = true
				continue
			}
			field.SetBool(*declared)
		case reflect.String:
			if field.String() == "" {
				field.SetString(defaultField.String())
				changed = true
			}
		case reflect.Int, reflect.Int64:
			if field.Int() == 0 {
				field.SetInt(defaultField.Int())
				changed = true
			}
		case reflect.Float64:
			if field.Float() == 0 {
				field.SetFloat(defaultField.Float())
				changed = true
			}
		default:
			// for unsupported types, just reset if zero
			if reflect.DeepEqual(field.Interface(), reflect.Zero(field.Type()).Interface()) {
				field.Set(defaultField)
				changed = true
			}
		}
	}
	return changed
}

// ------------------------------------
//
//	Build the mirror's field list, reporting which mirror fields carry a bool the
//	decoder can actually answer for. A field the encoder never writes cannot come
//	back from the decoder, so mirroring it as a pointer would report it absent on
//	every launch and rewrite the file forever.
//
// ------------------------------------
func mirrorFields(cfgType reflect.Type) ([]reflect.StructField, map[int]int) {
	mirrored := make([]reflect.StructField, 0, cfgType.NumField())
	boolFields := make(map[int]int, cfgType.NumField())

	for i := 0; i < cfgType.NumField(); i++ {
		field := cfgType.Field(i)
		if field.PkgPath != "" {
			// Unexported: encoding/json ignores it and reflect.StructOf refuses it.
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if field.Type.Kind() == reflect.Bool && name != "-" {
			boolFields[len(mirrored)] = i
			field.Type = reflect.PointerTo(field.Type)
		}
		mirrored = append(mirrored, field)
	}
	return mirrored, boolFields
}

// ------------------------------------
//
//	Decode the config bytes into a mirror of GittiConfigSettings whose bool fields
//	are pointers, so encoding/json itself reports which bools the file declares.
//	A nil means the key was absent or null; anything else is the decoder's own
//	resolution of duplicate and case-variant spellings.
//
// ------------------------------------
func decodeDeclaredBools(data []byte) (map[int]*bool, error) {
	mirrored, boolFields := mirrorFields(reflect.TypeOf(GittiConfigSettings{}))
	mirror := reflect.New(reflect.StructOf(mirrored))
	if err := json.Unmarshal(data, mirror.Interface()); err != nil {
		return nil, err
	}

	declared := make(map[int]*bool, len(boolFields))
	for mirrorIndex, cfgIndex := range boolFields {
		value := mirror.Elem().Field(mirrorIndex)
		if value.IsNil() {
			declared[cfgIndex] = nil
			continue
		}
		declared[cfgIndex] = value.Interface().(*bool)
	}
	return declared, nil
}

// ------------------------------------
//
//	Write the default configuration to file
//
// ------------------------------------
func writeDefaultConfig(cfgPath string) {
	saveConfig(cfgPath, GittiDefaultConfigSettings)
}

// ------------------------------------
//
//	Persist the given config settings to disk as JSON, reporting why the write
//	failed so a caller that can tell the user does not have to guess. The file is
//	replaced by a rename rather than written in place: adding a setting makes
//	every older config missing a key, so the first launch after an upgrade
//	rewrites it for everyone, and a write interrupted halfway through leaves JSON
//	that the next launch discards for the defaults
//
// ------------------------------------
func writeConfig(cfgPath string, cfg GittiConfigSettings) error {
	if configUnreadable {
		return fmt.Errorf("refusing to replace %s: it could not be located or read when gitti started", cfgPath)
	}

	target, err := resolveConfigTarget(cfgPath)
	if err != nil {
		return err
	}

	// A brand-new config is created private; an existing one keeps whatever mode
	// the user gave it, which truncating in place preserved for free.
	permission := os.FileMode(0o600)
	if info, err := os.Stat(target); err == nil {
		permission = info.Mode().Perm()
	}

	directory := filepath.Dir(target)
	file, err := os.CreateTemp(directory, ".config-*.json")
	if err != nil {
		return fmt.Errorf("creating a temporary file beside %s: %w", target, err)
	}
	// Harmless once the rename has succeeded, and the reason a failed write
	// leaves nothing behind.
	defer os.Remove(file.Name())

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		file.Close()
		return fmt.Errorf("writing %s: %w", file.Name(), err)
	}

	// The rename only publishes a complete file if the bytes are on disk first.
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("flushing %s: %w", file.Name(), err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", file.Name(), err)
	}

	if err := os.Chmod(file.Name(), permission); err != nil {
		return fmt.Errorf("setting permissions on %s: %w", file.Name(), err)
	}

	if err := os.Rename(file.Name(), target); err != nil {
		return fmt.Errorf("replacing %s: %w", target, err)
	}

	syncDirectory(directory)

	return nil
}

// ------------------------------------
//
//	Resolve where the config file actually lives. Truncating in place used to
//	follow a symlink, so a config linked out of a dotfiles repository has to keep
//	working; renaming onto the link would sever it and leave the linked copy
//	stale. The chain is walked a hop at a time rather than through EvalSymlinks,
//	which gives up on a link whose target does not exist yet - a dotfiles checkout
//	before the first launch - and would leave an intermediate link to be replaced
//
// ------------------------------------
func resolveConfigTarget(cfgPath string) (string, error) {
	target := cfgPath
	for hops := 0; ; hops++ {
		info, err := os.Lstat(target)
		if err != nil {
			if os.IsNotExist(err) {
				// The end of a chain whose last link points at a file that does not
				// exist yet: this is where the config belongs.
				return target, nil
			}
			// Anything else - a permission or I/O failure part way along - says
			// nothing about where the config lives, and writing here would replace
			// whatever is actually at this path.
			return "", fmt.Errorf("inspecting %s: %w", target, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return target, nil
		}

		// Counted per hop rather than per check, so a chain of exactly this length
		// still resolves and only a longer one is refused.
		if hops == MAXCONFIGSYMLINKHOPS {
			// A loop, or a chain long enough to be one. Following it further would
			// spin, and writing to any link along it would sever the rest.
			return "", fmt.Errorf("resolving %s: more than %d symlinks deep", cfgPath, MAXCONFIGSYMLINKHOPS)
		}

		link, err := os.Readlink(target)
		if err != nil {
			return "", fmt.Errorf("resolving the symlink at %s: %w", target, err)
		}
		if !filepath.IsAbs(link) {
			// The link's own directory may be a symlink too, and a relative target
			// with a ".." has to climb out of the real one. Join alone would pop the
			// link component instead. The directory exists whenever the link does,
			// so resolving it cannot fail the way the target itself can.
			directory := filepath.Dir(target)
			if resolved, err := filepath.EvalSymlinks(directory); err == nil {
				directory = resolved
			}
			link = filepath.Join(directory, link)
		}
		target = link
	}
}

// ------------------------------------
//
//	Flush the directory entry the rename created, so a crash straight after a
//	save cannot bring back the previous file. The save itself has already
//	succeeded by this point, so a failure here weakens durability rather than
//	losing the setting, and is deliberately not reported as a failed save
//
// ------------------------------------
func syncDirectory(directory string) {
	// Windows has no directory handle to flush: opening one for sync fails, and
	// the file's own bytes are already durable.
	if runtime.GOOS == "windows" {
		return
	}

	handle, err := os.Open(directory)
	if err != nil {
		return
	}
	defer handle.Close()

	_ = handle.Sync()
}

// ------------------------------------
//
//	Persist the given config settings without reporting a failure, for the call
//	sites that have no error path of their own. Several are CLI setters that
//	confirm success regardless; closing that gap across all of them is a separate
//	change
//
// ------------------------------------
func saveConfig(cfgPath string, cfg GittiConfigSettings) {
	_ = writeConfig(cfgPath, cfg)
}

// ------------------------------------
//
//	Update and persist the language code setting
//
// ------------------------------------
func UpdateLanguageCode(languageCode string) {
	GITTICONFIGSETTINGS.LanguageCode = strings.ToUpper(languageCode)
	cfgPath, err := getConfigPath()
	if err == nil {
		saveConfig(cfgPath, *GITTICONFIGSETTINGS)
	}
}

// ------------------------------------
//
//	Update and persist the default branch name, optionally applying it to git global config
//
// ------------------------------------
func UpdateDefaultBranch(branchName string, applyToGit bool, cwd string) {
	GITTICONFIGSETTINGS.GitInitDefaultBranch = branchName
	cfgPath, err := getConfigPath()
	if err == nil {
		saveConfig(cfgPath, *GITTICONFIGSETTINGS)
		if applyToGit {
			git.SetGitInitDefaultBranch(branchName, cwd)
		}
	}
}

// ------------------------------------
//
//	Update and persist the last update check time to current UTC time
//
// ------------------------------------
func UpdateLastFetchTime() {
	GITTICONFIGSETTINGS.LastUpdateCheckTime = time.Now().UTC()
	cfgPath, err := getConfigPath()
	if err == nil {
		saveConfig(cfgPath, *GITTICONFIGSETTINGS)
	}
}

// ------------------------------------
//
//	Update and persist the auto update setting
//
// ------------------------------------
func UpdateAutoUpdate(autoUpdate bool) {
	GITTICONFIGSETTINGS.AutoUpdate = autoUpdate
	cfgPath, err := getConfigPath()
	if err == nil {
		saveConfig(cfgPath, *GITTICONFIGSETTINGS)
	}
}

// ------------------------------------
//
//	Update and persist the editor preference
//
// ------------------------------------
func UpdateEditor(editor string) {
	GITTICONFIGSETTINGS.Editor = editor
	cfgPath, err := getConfigPath()
	if err == nil {
		saveConfig(cfgPath, *GITTICONFIGSETTINGS)
	}
}

// ------------------------------------
//
//	Update and persist the maximum commit log count
//
// ------------------------------------
func UpdateMaxCommitLogCount(maxCount int) {
	GITTICONFIGSETTINGS.MaxCommitLogCount = maxCount
	cfgPath, err := getConfigPath()
	if err == nil {
		saveConfig(cfgPath, *GITTICONFIGSETTINGS)
	}
}

// ------------------------------------
//
//	Update and persist the maximum reflog count
//
// ------------------------------------
func UpdateMaxRefLogCount(maxCount int) {
	GITTICONFIGSETTINGS.MaxRefLogCount = maxCount
	cfgPath, err := getConfigPath()
	if err == nil {
		saveConfig(cfgPath, *GITTICONFIGSETTINGS)
	}
}

// ------------------------------------
//
//	Update and persist the commit graph write permission setting
//
// ------------------------------------
func UpdateAllowCommitGraphWrite(allow bool) {
	GITTICONFIGSETTINGS.AllowCommitGraphWrite = allow
	cfgPath, err := getConfigPath()
	if err == nil {
		saveConfig(cfgPath, *GITTICONFIGSETTINGS)
	}
}

// ------------------------------------
//
//	Update and persist the maximum log count
//
// ------------------------------------
func UpdateMaxLogCount(maxLog int) {
	GITTICONFIGSETTINGS.MaxLogCount = maxLog
	cfgPath, err := getConfigPath()
	if err == nil {
		saveConfig(cfgPath, *GITTICONFIGSETTINGS)
	}
}

// ------------------------------------
//
//	Update and persist the number of latest logs to display
//
// ------------------------------------
func UpdateShowXLog(x int) {
	GITTICONFIGSETTINGS.ShowXLog = x
	cfgPath, err := getConfigPath()
	if err == nil {
		saveConfig(cfgPath, *GITTICONFIGSETTINGS)
	}
}

// ------------------------------------
//
//	Update and persist the signing UI suspend override setting
//
// ------------------------------------
func UpdateOverrideSigningUISuspend(override bool) {
	GITTICONFIGSETTINGS.OverrideSigningUISuspend = override
	cfgPath, err := getConfigPath()
	if err == nil {
		saveConfig(cfgPath, *GITTICONFIGSETTINGS)
	}
}

// ------------------------------------
//
//	Update and persist the merge setting (fast forward or non fast forward)
//
// ------------------------------------
func UpdateFfMerge(ffMerge bool) {
	GITTICONFIGSETTINGS.FfMerge = ffMerge
	cfgPath, err := getConfigPath()
	if err == nil {
		saveConfig(cfgPath, *GITTICONFIGSETTINGS)
	}
}

// ------------------------------------
//
//	Update and persist whether the commit log shows ref decorations, reporting a
//	failure rather than leaving the flag to confirm a setting that never reached
//	disk
//
// ------------------------------------
func UpdateCommitLogShowRefs(showRefs bool) error {
	GITTICONFIGSETTINGS.CommitLogShowRefs = showRefs
	cfgPath, err := getConfigPath()
	if err != nil {
		return fmt.Errorf("resolving the config path: %w", err)
	}

	return writeConfig(cfgPath, *GITTICONFIGSETTINGS)
}

// ------------------------------------
//
//	Update and persist whether the commit log walks every branch, reporting a
//	failure rather than leaving the flag to confirm a setting that never reached
//	disk
//
// ------------------------------------
func UpdateCommitLogShowAllBranches(showAllBranches bool) error {
	GITTICONFIGSETTINGS.CommitLogShowAllBranches = showAllBranches
	cfgPath, err := getConfigPath()
	if err != nil {
		return fmt.Errorf("resolving the config path: %w", err)
	}

	return writeConfig(cfgPath, *GITTICONFIGSETTINGS)
}
