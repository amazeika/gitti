package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gohyuhan/gitti/constant"
)

// ------------------------------------
//
//	Decode a config file's raw bytes into both the typed config and the raw key
//	set, the way InitOrReadConfig derives them
//
// ------------------------------------
func decodeConfig(t *testing.T, raw []byte) (GittiConfigSettings, map[int]*bool) {
	t.Helper()

	var cfg GittiConfigSettings
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("decoding config: %v", err)
	}

	declaredBools, err := decodeDeclaredBools(raw)
	if err != nil {
		t.Fatalf("decoding declared bools: %v", err)
	}

	return cfg, declaredBools
}

// ------------------------------------
//
//	Marshal the defaults into a complete config file, then apply raw key edits
//
// ------------------------------------
func completeConfigBytes(t *testing.T, edits map[string]any) []byte {
	t.Helper()

	raw, err := json.Marshal(GittiDefaultConfigSettings)
	if err != nil {
		t.Fatalf("marshalling defaults: %v", err)
	}

	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decoding defaults: %v", err)
	}

	for key, value := range edits {
		if value == nil {
			delete(object, key)
			continue
		}
		object[key] = value
	}

	edited, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("marshalling edited config: %v", err)
	}
	return edited
}

func TestEnsureConfigIntegrityPreservesExplicitFalseBool(t *testing.T) {
	cfg, declaredBools := decodeConfig(t, completeConfigBytes(t, map[string]any{
		"auto_update": false,
	}))

	changed := ensureConfigIntegrity(&cfg, &GittiDefaultConfigSettings, declaredBools)

	if cfg.AutoUpdate {
		t.Error("auto_update set to false in the file was reset to the default")
	}
	if changed {
		t.Error("a config carrying every key was reported as changed")
	}
}

func TestEnsureConfigIntegrityDefaultsAbsentBool(t *testing.T) {
	cfg, declaredBools := decodeConfig(t, completeConfigBytes(t, map[string]any{
		"auto_update": nil,
	}))

	changed := ensureConfigIntegrity(&cfg, &GittiDefaultConfigSettings, declaredBools)

	if !cfg.AutoUpdate {
		t.Error("auto_update absent from the file did not take its default")
	}
	if !changed {
		t.Error("defaulting an absent key should mark the config changed so it is written back")
	}
}

func TestEnsureConfigIntegrityTreatsNullBoolAsAbsent(t *testing.T) {
	raw := completeConfigBytes(t, map[string]any{"auto_update": nil})

	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decoding config: %v", err)
	}
	object["auto_update"] = json.RawMessage("null")
	withNull, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("marshalling config: %v", err)
	}

	cfg, declaredBools := decodeConfig(t, withNull)

	changed := ensureConfigIntegrity(&cfg, &GittiDefaultConfigSettings, declaredBools)

	if !cfg.AutoUpdate {
		t.Error("auto_update present as null did not take its default")
	}
	if !changed {
		t.Error("a null bool should be treated as absent and mark the config changed")
	}
}

func TestEnsureConfigIntegrityDefaultsZeroInt(t *testing.T) {
	cfg, declaredBools := decodeConfig(t, completeConfigBytes(t, map[string]any{
		"max_commit_log_count": 0,
	}))

	changed := ensureConfigIntegrity(&cfg, &GittiDefaultConfigSettings, declaredBools)

	if cfg.MaxCommitLogCount != GittiDefaultConfigSettings.MaxCommitLogCount {
		t.Errorf("max_commit_log_count 0 should fall back to the default, got %d", cfg.MaxCommitLogCount)
	}
	if !changed {
		t.Error("replacing a zero int should mark the config changed")
	}
}

func TestEnsureConfigIntegrityDefaultsZeroTimestamp(t *testing.T) {
	cfg, declaredBools := decodeConfig(t, completeConfigBytes(t, map[string]any{
		"last_update_check_time": time.Time{},
	}))

	changed := ensureConfigIntegrity(&cfg, &GittiDefaultConfigSettings, declaredBools)

	if cfg.LastUpdateCheckTime.IsZero() {
		t.Error("a zero last_update_check_time should fall back to the default")
	}
	if !changed {
		t.Error("replacing a zero timestamp should mark the config changed")
	}
}

func TestEnsureConfigIntegrityLeavesCompleteConfigUnchanged(t *testing.T) {
	cfg, declaredBools := decodeConfig(t, completeConfigBytes(t, nil))

	changed := ensureConfigIntegrity(&cfg, &GittiDefaultConfigSettings, declaredBools)

	if changed {
		t.Error("a config carrying every key should not be rewritten on launch")
	}
}

func TestEnsureConfigIntegrityHonoursCaseVariantKeys(t *testing.T) {
	raw := completeConfigBytes(t, map[string]any{"auto_update": nil})

	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decoding config: %v", err)
	}
	// encoding/json accepts this spelling and decodes it into AutoUpdate, so the
	// presence check has to see it too or the value is silently reset.
	object["AUTO_UPDATE"] = json.RawMessage("false")
	variant, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("marshalling config: %v", err)
	}

	cfg, declaredBools := decodeConfig(t, variant)
	if cfg.AutoUpdate {
		t.Fatal("precondition: the decoder should have honoured the case-variant key")
	}

	ensureConfigIntegrity(&cfg, &GittiDefaultConfigSettings, declaredBools)

	if cfg.AutoUpdate {
		t.Error("a case-variant key the decoder honoured was treated as absent and reset")
	}
}

func TestEnsureConfigIntegrityHonoursDuplicateKeyResolution(t *testing.T) {
	// encoding/json processes members in order, so the last spelling of a key wins.
	// The bool decode must land on the same answer as the typed decode; if the two
	// disagree the file is rewritten with a value the user never wrote.
	variant := []byte(`{"auto_update":false,"AUTO_UPDATE":true}`)

	cfg, declaredBools := decodeConfig(t, variant)
	if !cfg.AutoUpdate {
		t.Fatal("precondition: the decoder should have taken the last spelling")
	}

	ensureConfigIntegrity(&cfg, &GittiDefaultConfigSettings, declaredBools)

	if !cfg.AutoUpdate {
		t.Error("bool presence disagreed with the typed decode on a duplicate key")
	}
}

func TestEnsureConfigIntegrityDefaultsBoolWhenLastSpellingIsNull(t *testing.T) {
	// The last spelling is null, so the decoder leaves the mirror nil and the field
	// takes its default. What matters is that cfg and the persisted file agree.
	variant := []byte(`{"auto_update":false,"auto_update":null}`)

	cfg, declaredBools := decodeConfig(t, variant)

	changed := ensureConfigIntegrity(&cfg, &GittiDefaultConfigSettings, declaredBools)

	if !cfg.AutoUpdate {
		t.Error("a trailing null should leave the field at its default")
	}
	if !changed {
		t.Error("defaulting a null bool should mark the config changed")
	}
}

// ------------------------------------
//
//	Point every platform's config-directory variable at one temporary directory.
//	os.UserConfigDir reads %AppData% on Windows and ignores HOME, so redirecting
//	only the Unix variables would let this test overwrite a real user's config.
//
// ------------------------------------
func isolateConfigDir(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AppData", home)
	t.Setenv("LocalAppData", home)

	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("resolving config dir: %v", err)
	}
	// Refuse to run rather than write outside the sandbox on a platform whose
	// config directory is resolved from somewhere this helper does not control.
	if !strings.HasPrefix(configDir, home) {
		t.Skipf("config dir %q is outside the test sandbox %q", configDir, home)
	}
	return configDir
}

func TestInitOrReadConfigKeepsDisabledBoolsAcrossRestarts(t *testing.T) {
	configDir := isolateConfigDir(t)

	original := GITTICONFIGSETTINGS
	t.Cleanup(func() { GITTICONFIGSETTINGS = original })

	appDir := filepath.Join(configDir, constant.APPNAME)
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	cfgPath := filepath.Join(appDir, "config.json")
	disabled := completeConfigBytes(t, map[string]any{
		"auto_update":              false,
		"allow_commit_graph_write": false,
	})
	if err := os.WriteFile(cfgPath, disabled, 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	InitOrReadConfig()

	if GITTICONFIGSETTINGS.AutoUpdate {
		t.Error("auto_update disabled in the file came back enabled after a restart")
	}
	if GITTICONFIGSETTINGS.AllowCommitGraphWrite {
		t.Error("allow_commit_graph_write disabled in the file came back enabled after a restart")
	}

	// The file itself must still hold the user's choice: rewriting it with the
	// defaults is how the setting used to be lost between launches.
	persisted, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("re-reading config: %v", err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(persisted, &onDisk); err != nil {
		t.Fatalf("decoding persisted config: %v", err)
	}
	if onDisk["auto_update"] != false {
		t.Errorf("auto_update on disk = %v, want false", onDisk["auto_update"])
	}
	if onDisk["allow_commit_graph_write"] != false {
		t.Errorf("allow_commit_graph_write on disk = %v, want false", onDisk["allow_commit_graph_write"])
	}
}

func TestMirrorFieldsSkipsWhatTheDecoderCannotAnswer(t *testing.T) {
	probe := reflect.TypeOf(struct {
		Persisted bool `json:"persisted"`
		Excluded  bool `json:"-"`
		Renamed   bool `json:"renamed,omitempty"`
		Plain     int  `json:"plain"`
	}{})

	mirrored, boolFields := mirrorFields(probe)

	if len(mirrored) != probe.NumField() {
		t.Fatalf("mirror carried %d fields, want %d", len(mirrored), probe.NumField())
	}

	declared := map[string]bool{}
	for mirrorIndex, cfgIndex := range boolFields {
		declared[probe.Field(cfgIndex).Name] = true
		if mirrored[mirrorIndex].Type.Kind() != reflect.Pointer {
			t.Errorf("%s was registered as a bool but not mirrored as a pointer", probe.Field(cfgIndex).Name)
		}
	}

	if !declared["Persisted"] || !declared["Renamed"] {
		t.Error("a persisted bool was not registered for presence detection")
	}
	// A json:"-" bool never reaches the file, so reporting it absent every launch
	// would mark the config changed and rewrite it forever.
	if declared["Excluded"] {
		t.Error(`a json:"-" bool was registered, so it would be reset on every launch`)
	}
	if declared["Plain"] {
		t.Error("a non-bool field was registered for bool presence detection")
	}
}

func TestCommitLogShowRefsDefaultsToOn(t *testing.T) {
	if !GittiDefaultConfigSettings.CommitLogShowRefs {
		t.Error("commit_log_show_refs defaults to off, so a new user never sees ref decorations")
	}
}

func TestInitOrReadConfigKeepsCommitLogShowRefsDisabled(t *testing.T) {
	configDir := isolateConfigDir(t)

	original := GITTICONFIGSETTINGS
	t.Cleanup(func() { GITTICONFIGSETTINGS = original })

	appDir := filepath.Join(configDir, constant.APPNAME)
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	cfgPath := filepath.Join(appDir, "config.json")
	if err := os.WriteFile(cfgPath, completeConfigBytes(t, map[string]any{"commit_log_show_refs": false}), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	InitOrReadConfig()

	if GITTICONFIGSETTINGS.CommitLogShowRefs {
		t.Error("commit_log_show_refs turned off came back on after a restart, so a default-true setting cannot be disabled")
	}
}

func TestUpdateCommitLogShowRefsReportsAFailedWrite(t *testing.T) {
	isolateConfigDir(t)

	original := GITTICONFIGSETTINGS
	t.Cleanup(func() { GITTICONFIGSETTINGS = original })
	cfg := GittiDefaultConfigSettings
	GITTICONFIGSETTINGS = &cfg

	// A directory where the config file belongs makes the write fail the way an
	// unwritable config directory would. Confirming success here would send the
	// user away with a setting the next launch discards.
	cfgPath, err := getConfigPath()
	if err != nil {
		t.Fatalf("resolving the config path: %v", err)
	}
	if err := os.RemoveAll(cfgPath); err != nil {
		t.Fatalf("clearing the config path: %v", err)
	}
	if err := os.MkdirAll(cfgPath, 0o755); err != nil {
		t.Fatalf("blocking the config path: %v", err)
	}

	if err := UpdateCommitLogShowRefs(false); err == nil {
		t.Error("a config that could not be written was reported as saved")
	}
}

func TestUpdateCommitLogShowRefsPersistsTheSetting(t *testing.T) {
	isolateConfigDir(t)

	original := GITTICONFIGSETTINGS
	t.Cleanup(func() { GITTICONFIGSETTINGS = original })
	cfg := GittiDefaultConfigSettings
	GITTICONFIGSETTINGS = &cfg

	if err := UpdateCommitLogShowRefs(false); err != nil {
		t.Fatalf("saving the setting: %v", err)
	}

	InitOrReadConfig()

	if GITTICONFIGSETTINGS.CommitLogShowRefs {
		t.Error("commit_log_show_refs came back enabled after being turned off and re-read")
	}
}

func TestCommitLogShowAllBranchesDefaultsToOff(t *testing.T) {
	if GittiDefaultConfigSettings.CommitLogShowAllBranches {
		t.Error("commit_log_show_all_branches defaults to on, which changes the panel for every existing user")
	}
}

func TestInitOrReadConfigRoundTripsCommitLogShowAllBranches(t *testing.T) {
	configDir := isolateConfigDir(t)

	original := GITTICONFIGSETTINGS
	t.Cleanup(func() { GITTICONFIGSETTINGS = original })

	appDir := filepath.Join(configDir, constant.APPNAME)
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	cfgPath := filepath.Join(appDir, "config.json")

	// Both directions matter: the setting defaults to false, so turning it on has
	// to survive, and turning it back off must not be mistaken for an absent key.
	for _, enabled := range []bool{true, false} {
		if err := os.WriteFile(cfgPath, completeConfigBytes(t, map[string]any{"commit_log_show_all_branches": enabled}), 0o644); err != nil {
			t.Fatalf("writing config: %v", err)
		}

		InitOrReadConfig()

		if GITTICONFIGSETTINGS.CommitLogShowAllBranches != enabled {
			t.Errorf("commit_log_show_all_branches = %v after a restart, want %v", GITTICONFIGSETTINGS.CommitLogShowAllBranches, enabled)
		}
	}
}

func TestWriteConfigReplacesTheConfigRatherThanTruncatingIt(t *testing.T) {
	isolateConfigDir(t)

	cfgPath, err := getConfigPath()
	if err != nil {
		t.Fatalf("resolving the config path: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"language_code":"JA"}`), 0o644); err != nil {
		t.Fatalf("writing the original config: %v", err)
	}
	before, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("inspecting the original config: %v", err)
	}

	if err := writeConfig(cfgPath, GittiDefaultConfigSettings); err != nil {
		t.Fatalf("writing the config: %v", err)
	}

	after, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("inspecting the written config: %v", err)
	}
	// The identity of the file is the whole point: a write that truncates in place
	// keeps the same file, and a failure part way through it destroys the user's
	// settings. A replacement cannot.
	if os.SameFile(before, after) {
		t.Error("the config was written in place, so an interrupted write would leave it truncated")
	}
}

func TestWriteConfigKeepsTheModeTheUserSet(t *testing.T) {
	requirePosixModes(t)
	isolateConfigDir(t)

	cfgPath, err := getConfigPath()
	if err != nil {
		t.Fatalf("resolving the config path: %v", err)
	}
	// Deliberately not 0600: that is also what CreateTemp produces, so a test
	// using it would pass even if the mode were never carried across.
	if err := os.WriteFile(cfgPath, []byte(`{}`), 0o640); err != nil {
		t.Fatalf("writing the original config: %v", err)
	}

	if err := writeConfig(cfgPath, GittiDefaultConfigSettings); err != nil {
		t.Fatalf("writing the config: %v", err)
	}

	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("inspecting the written config: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want the 0640 the user set: replacing the file must not change it", info.Mode().Perm())
	}
}

func TestWriteConfigCreatesANewConfigPrivate(t *testing.T) {
	requirePosixModes(t)
	isolateConfigDir(t)

	cfgPath, err := getConfigPath()
	if err != nil {
		t.Fatalf("resolving the config path: %v", err)
	}

	if err := writeConfig(cfgPath, GittiDefaultConfigSettings); err != nil {
		t.Fatalf("writing the config: %v", err)
	}

	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("inspecting the written config: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 for a config the user has not seen yet", info.Mode().Perm())
	}
}

// ------------------------------------
//
//	Skip a test whose assertion is a Unix permission bit. Go reports a writable
//	Windows file as 0666 and Chmod there only carries the owner-write bit, so an
//	exact mode is not a claim that can hold on that platform
//
// ------------------------------------
func requirePosixModes(t *testing.T) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("file modes are not represented as Unix permission bits on Windows")
	}
}

func TestWriteConfigWritesThroughASymlink(t *testing.T) {
	isolateConfigDir(t)

	cfgPath, err := getConfigPath()
	if err != nil {
		t.Fatalf("resolving the config path: %v", err)
	}
	if err := os.Remove(cfgPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("clearing the config path: %v", err)
	}

	// A config kept in a dotfiles repository and linked into place: replacing the
	// link would sever it and leave the repository copy stale.
	dotfiles := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(dotfiles, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("writing the dotfiles config: %v", err)
	}
	if err := os.Symlink(dotfiles, cfgPath); err != nil {
		t.Skipf("this filesystem does not support symlinks: %v", err)
	}

	if err := writeConfig(cfgPath, GittiDefaultConfigSettings); err != nil {
		t.Fatalf("writing the config: %v", err)
	}

	info, err := os.Lstat(cfgPath)
	if err != nil {
		t.Fatalf("inspecting the config path: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file")
	}

	written, err := os.ReadFile(dotfiles)
	if err != nil {
		t.Fatalf("reading the dotfiles config: %v", err)
	}
	if !strings.Contains(string(written), "commit_log_show_all_branches") {
		t.Error("the linked file did not receive the new settings")
	}
}

func TestWriteConfigLeavesNoTemporaryFileBehindWhenItFails(t *testing.T) {
	configDir := isolateConfigDir(t)

	cfgPath, err := getConfigPath()
	if err != nil {
		t.Fatalf("resolving the config path: %v", err)
	}
	if err := os.Remove(cfgPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("clearing the config path: %v", err)
	}
	// A directory cannot be replaced by a rename, so the write fails after its
	// temporary file has been created — the only path on which the deferred
	// cleanup is load-bearing.
	if err := os.MkdirAll(cfgPath, 0o755); err != nil {
		t.Fatalf("blocking the config path: %v", err)
	}

	if err := writeConfig(cfgPath, GittiDefaultConfigSettings); err == nil {
		t.Fatal("replacing a directory was reported as a successful write")
	}

	entries, err := os.ReadDir(filepath.Join(configDir, constant.APPNAME))
	if err != nil {
		t.Fatalf("reading the config dir: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != "config.json" {
			t.Errorf("%s was left behind by a failed write", entry.Name())
		}
	}
}

func TestWriteConfigWritesThroughADanglingSymlink(t *testing.T) {
	isolateConfigDir(t)

	cfgPath, err := getConfigPath()
	if err != nil {
		t.Fatalf("resolving the config path: %v", err)
	}
	if err := os.Remove(cfgPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("clearing the config path: %v", err)
	}

	// The link is set up before the file exists, which is what a fresh dotfiles
	// checkout looks like. Truncating in place used to create the target through
	// it; replacing the link instead would quietly undo the user's setup.
	dotfiles := filepath.Join(t.TempDir(), "config.json")
	if err := os.Symlink(dotfiles, cfgPath); err != nil {
		t.Skipf("this filesystem does not support symlinks: %v", err)
	}

	if err := writeConfig(cfgPath, GittiDefaultConfigSettings); err != nil {
		t.Fatalf("writing the config: %v", err)
	}

	info, err := os.Lstat(cfgPath)
	if err != nil {
		t.Fatalf("inspecting the config path: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("the dangling symlink was replaced by a regular file")
	}
	if _, err := os.Stat(dotfiles); err != nil {
		t.Errorf("the link target was not created: %v", err)
	}
}

func TestWriteConfigFollowsAChainWhoseLastHopIsMissing(t *testing.T) {
	isolateConfigDir(t)

	cfgPath, err := getConfigPath()
	if err != nil {
		t.Fatalf("resolving the config path: %v", err)
	}
	if err := os.Remove(cfgPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("clearing the config path: %v", err)
	}

	// config.json -> middle.json -> config.json in the dotfiles repository, where
	// only the last hop is missing. Stopping at the first hop would replace
	// middle.json and break the chain below it.
	dotfiles := t.TempDir()
	middle := filepath.Join(dotfiles, "middle.json")
	final := filepath.Join(dotfiles, "config.json")
	if err := os.Symlink(final, middle); err != nil {
		t.Skipf("this filesystem does not support symlinks: %v", err)
	}
	if err := os.Symlink(middle, cfgPath); err != nil {
		t.Fatalf("linking the config path: %v", err)
	}

	if err := writeConfig(cfgPath, GittiDefaultConfigSettings); err != nil {
		t.Fatalf("writing the config: %v", err)
	}

	middleInfo, err := os.Lstat(middle)
	if err != nil {
		t.Fatalf("inspecting the intermediate link: %v", err)
	}
	if middleInfo.Mode()&os.ModeSymlink == 0 {
		t.Error("the intermediate link was replaced by a regular file")
	}
	if _, err := os.Stat(final); err != nil {
		t.Errorf("the end of the chain was not written: %v", err)
	}
}

func TestWriteConfigRefusesASymlinkLoop(t *testing.T) {
	isolateConfigDir(t)

	cfgPath, err := getConfigPath()
	if err != nil {
		t.Fatalf("resolving the config path: %v", err)
	}
	if err := os.Remove(cfgPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("clearing the config path: %v", err)
	}

	// Two links pointing at each other. Following the chain has to stop rather
	// than spin, and writing to either would sever the other.
	loop := filepath.Join(t.TempDir(), "loop.json")
	if err := os.Symlink(loop, cfgPath); err != nil {
		t.Skipf("this filesystem does not support symlinks: %v", err)
	}
	if err := os.Symlink(cfgPath, loop); err != nil {
		t.Fatalf("closing the loop: %v", err)
	}

	if err := writeConfig(cfgPath, GittiDefaultConfigSettings); err == nil {
		t.Error("a symlink loop was reported as a successful write")
	}
}

func TestWriteConfigResolvesARelativeLinkOutOfASymlinkedDirectory(t *testing.T) {
	isolateConfigDir(t)

	cfgPath, err := getConfigPath()
	if err != nil {
		t.Fatalf("resolving the config path: %v", err)
	}

	// A folded dotfiles layout: the config's own directory is a link, and the
	// config inside it points out of that directory with "..". Joining the target
	// lexically would climb out of the link's path instead of the real one.
	root := t.TempDir()
	real := filepath.Join(root, "real")
	shared := filepath.Join(root, "shared")
	for _, directory := range []string{real, shared} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("creating %s: %v", directory, err)
		}
	}
	target := filepath.Join(shared, "config.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("writing the shared config: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "shared", "config.json"), filepath.Join(real, "config.json")); err != nil {
		t.Skipf("this filesystem does not support symlinks: %v", err)
	}

	// The link to that directory sits one level deeper than the directory itself,
	// so popping ".." off the link's path lands somewhere the real path does not.
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("creating %s: %v", nested, err)
	}
	linkedDirectory := filepath.Join(nested, "linked")
	if err := os.Symlink(real, linkedDirectory); err != nil {
		t.Fatalf("linking the directory: %v", err)
	}
	if err := os.Remove(cfgPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("clearing the config path: %v", err)
	}
	if err := os.Symlink(filepath.Join(linkedDirectory, "config.json"), cfgPath); err != nil {
		t.Fatalf("linking the config path: %v", err)
	}

	if err := writeConfig(cfgPath, GittiDefaultConfigSettings); err != nil {
		t.Fatalf("writing the config: %v", err)
	}

	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading the shared config: %v", err)
	}
	if !strings.Contains(string(written), "commit_log_show_all_branches") {
		t.Error("the settings were written somewhere other than the file the chain points at")
	}
}

func TestWriteConfigFollowsALongChainOfLinks(t *testing.T) {
	isolateConfigDir(t)

	cfgPath, err := getConfigPath()
	if err != nil {
		t.Fatalf("resolving the config path: %v", err)
	}
	if err := os.Remove(cfgPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("clearing the config path: %v", err)
	}

	// Well past any plausible dotfiles layout, and past the bound an earlier
	// version of the resolver used. Only the resolver's own walk is under test
	// here; the kernel counts the directory links in these paths as well.
	dotfiles := t.TempDir()
	target := filepath.Join(dotfiles, "config.json")
	previous := target
	for hop := range 20 {
		link := filepath.Join(dotfiles, fmt.Sprintf("hop-%d.json", hop))
		if err := os.Symlink(previous, link); err != nil {
			t.Skipf("this filesystem does not support symlinks: %v", err)
		}
		previous = link
	}
	if err := os.Symlink(previous, cfgPath); err != nil {
		t.Fatalf("linking the config path: %v", err)
	}

	if err := writeConfig(cfgPath, GittiDefaultConfigSettings); err != nil {
		t.Fatalf("writing the config: %v", err)
	}

	if _, err := os.Stat(target); err != nil {
		t.Errorf("the end of the chain was not written: %v", err)
	}
}

func TestResolveConfigTargetRefusesToGuessPastAnUnreadableDirectory(t *testing.T) {
	requirePosixModes(t)
	isolateConfigDir(t)

	cfgPath, err := getConfigPath()
	if err != nil {
		t.Fatalf("resolving the config path: %v", err)
	}
	if err := os.Remove(cfgPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("clearing the config path: %v", err)
	}

	// The link points into a directory that cannot be searched, so nothing is
	// known about what is at the other end. Treating that like an absent file
	// would write over whatever is really there.
	closed := filepath.Join(t.TempDir(), "closed")
	if err := os.MkdirAll(closed, 0o755); err != nil {
		t.Fatalf("creating the directory: %v", err)
	}
	target := filepath.Join(closed, "config.json")
	if err := os.Symlink(target, cfgPath); err != nil {
		t.Skipf("this filesystem does not support symlinks: %v", err)
	}
	if err := os.Chmod(closed, 0o000); err != nil {
		t.Fatalf("making the directory unsearchable: %v", err)
	}
	t.Cleanup(func() { os.Chmod(closed, 0o755) })

	// Running as root, or with CAP_DAC_OVERRIDE, a mode of 000 denies nothing.
	if _, err := os.Lstat(target); err == nil || os.IsNotExist(err) {
		t.Skip("this process is not denied access to an unreadable directory")
	}

	// Asserted against the resolver rather than through writeConfig: creating the
	// temporary file in that directory fails either way, so a write error alone
	// would not tell a guarded walk from a guessing one.
	if _, err := resolveConfigTarget(cfgPath); err == nil {
		t.Error("a link into an unreadable directory was resolved as a place to write")
	}
}

func TestWriteConfigResolvesAChainOfExactlyTheHopLimit(t *testing.T) {
	isolateConfigDir(t)

	cfgPath, err := getConfigPath()
	if err != nil {
		t.Fatalf("resolving the config path: %v", err)
	}
	if err := os.Remove(cfgPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("clearing the config path: %v", err)
	}

	// The boundary itself: the resolver must accept a chain of exactly its own
	// limit and refuse a longer one. This is about the resolver's count, not about
	// what the kernel will read back - it counts directory links too, so this
	// layout is deeper than it looks to os.ReadFile.
	dotfiles := t.TempDir()
	target := filepath.Join(dotfiles, "config.json")
	previous := target
	for hop := range MAXCONFIGSYMLINKHOPS - 1 {
		link := filepath.Join(dotfiles, fmt.Sprintf("hop-%d.json", hop))
		if err := os.Symlink(previous, link); err != nil {
			t.Skipf("this filesystem does not support symlinks: %v", err)
		}
		previous = link
	}
	if err := os.Symlink(previous, cfgPath); err != nil {
		t.Fatalf("linking the config path: %v", err)
	}

	if err := writeConfig(cfgPath, GittiDefaultConfigSettings); err != nil {
		t.Fatalf("a chain of exactly %d links was refused: %v", MAXCONFIGSYMLINKHOPS, err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("the end of the chain was not written: %v", err)
	}
}

func TestInitOrReadConfigLeavesAnUnreadableConfigAlone(t *testing.T) {
	requirePosixModes(t)
	isolateConfigDir(t)

	original := GITTICONFIGSETTINGS
	t.Cleanup(func() { GITTICONFIGSETTINGS = original })
	t.Cleanup(func() { configUnreadable = false })

	cfgPath, err := getConfigPath()
	if err != nil {
		t.Fatalf("resolving the config path: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"language_code":"JA"}`), 0o644); err != nil {
		t.Fatalf("writing the config: %v", err)
	}
	if err := os.Chmod(cfgPath, 0o000); err != nil {
		t.Fatalf("making the config unreadable: %v", err)
	}
	t.Cleanup(func() { os.Chmod(cfgPath, 0o644) })

	// Running as root a mode of 000 denies nothing, and the case cannot be posed.
	if _, err := os.ReadFile(cfgPath); err == nil {
		t.Skip("this process is not denied access to an unreadable file")
	}

	InitOrReadConfig()

	if err := os.Chmod(cfgPath, 0o644); err != nil {
		t.Fatalf("reopening the config: %v", err)
	}
	surviving, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("re-reading the config: %v", err)
	}
	// A config that cannot be read says nothing about whether it is valid, and a
	// symlink chain the kernel refuses reaches this path too. Replacing it would
	// destroy settings that are almost certainly intact.
	if !strings.Contains(string(surviving), `"language_code":"JA"`) {
		t.Errorf("an unreadable config was replaced by the defaults: %s", surviving)
	}
}

func TestUpdatingASettingDoesNotOverwriteAnUnreadableConfig(t *testing.T) {
	requirePosixModes(t)
	isolateConfigDir(t)

	original := GITTICONFIGSETTINGS
	t.Cleanup(func() { GITTICONFIGSETTINGS = original })
	t.Cleanup(func() { configUnreadable = false })

	cfgPath, err := getConfigPath()
	if err != nil {
		t.Fatalf("resolving the config path: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"language_code":"JA"}`), 0o644); err != nil {
		t.Fatalf("writing the config: %v", err)
	}
	if err := os.Chmod(cfgPath, 0o000); err != nil {
		t.Fatalf("making the config unreadable: %v", err)
	}
	t.Cleanup(func() { os.Chmod(cfgPath, 0o644) })

	if _, err := os.ReadFile(cfgPath); err == nil {
		t.Skip("this process is not denied access to an unreadable file")
	}

	InitOrReadConfig()

	// Leaving the file alone at startup is not enough on its own: a replacement by
	// rename needs only the directory, so the first setting anyone changes would
	// otherwise put the defaults over a config that is merely unreadable. Any
	// setter reaches the same write path; this is the one that runs unprompted.
	UpdateLastFetchTime()

	if err := os.Chmod(cfgPath, 0o644); err != nil {
		t.Fatalf("reopening the config: %v", err)
	}
	surviving, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("re-reading the config: %v", err)
	}
	if !strings.Contains(string(surviving), `"language_code":"JA"`) {
		t.Errorf("a later save replaced an unreadable config with the defaults: %s", surviving)
	}
}

func TestInitOrReadConfigDoesNotAliasTheDefaults(t *testing.T) {
	isolateConfigDir(t)

	original := GITTICONFIGSETTINGS
	t.Cleanup(func() { GITTICONFIGSETTINGS = original })

	before := GittiDefaultConfigSettings.LastUpdateCheckTime
	// Restored explicitly: when this test fails it is because the write reached
	// the package defaults, and leaving them shifted would follow every later test.
	t.Cleanup(func() { GittiDefaultConfigSettings.LastUpdateCheckTime = before })

	// No config file, so the pointer is left on the defaults for the session. A
	// setter must not write through it into the struct ensureConfigIntegrity
	// compares every later config against.
	InitOrReadConfig()
	GITTICONFIGSETTINGS.LastUpdateCheckTime = before.Add(time.Hour)

	if !GittiDefaultConfigSettings.LastUpdateCheckTime.Equal(before) {
		t.Error("changing a setting reached through into the package defaults")
	}
}

func TestAnUnlocatableConfigAlsoBarsWriting(t *testing.T) {
	original := GITTICONFIGSETTINGS
	t.Cleanup(func() { GITTICONFIGSETTINGS = original })
	t.Cleanup(func() { configUnreadable = false })

	// A home that is a file, not a directory, so the config directory cannot be
	// created and the path never resolves.
	home := filepath.Join(t.TempDir(), "home")
	if err := os.WriteFile(home, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("writing the blocking file: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AppData", home)
	t.Setenv("LocalAppData", home)

	if _, err := getConfigPath(); err == nil {
		t.Skip("this platform resolved a config path despite an unusable home")
	}

	InitOrReadConfig()

	// The session never saw a config, so it must not write one later either: by
	// then the path may resolve again, and the defaults would land on a file this
	// process has never read.
	if err := writeConfig(filepath.Join(t.TempDir(), "config.json"), GittiDefaultConfigSettings); err == nil {
		t.Error("a session that could not locate the config was still allowed to write one")
	}
}
