package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
)

var GITTICONFIGSETTINGS *GittiConfigSettings

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
	GITTICONFIGSETTINGS = &GittiDefaultConfigSettings

	cfgPath, err := getConfigPath()
	if err != nil {
		return
	}

	// If config doesn't exist, create a default one
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		writeDefaultConfig(cfgPath)
		return
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
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
//	Persist the given config settings to disk as JSON
//
// ------------------------------------
func saveConfig(cfgPath string, cfg GittiConfigSettings) {
	file, err := os.Create(cfgPath)
	if err != nil {
		return
	}
	defer file.Close()

	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	_ = enc.Encode(cfg)
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
