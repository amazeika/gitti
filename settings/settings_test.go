package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
