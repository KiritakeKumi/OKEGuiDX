package platform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigDirPathIsUnderUserConfigDir(t *testing.T) {
	t.Parallel()
	base, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("os.UserConfigDir unavailable: %v", err)
	}
	got, err := ConfigDirPath()
	if err != nil {
		t.Fatalf("ConfigDirPath() error = %v", err)
	}
	want := filepath.Join(base, AppDirName)
	if got != want {
		t.Errorf("ConfigDirPath() = %q, want %q", got, want)
	}
	// The function must not touch the disk: the directory need not exist.
	if _, err := os.Stat(got); err == nil {
		t.Logf("note: %s already exists", got)
	}
}

func TestConfigFilePath(t *testing.T) {
	t.Parallel()
	path, err := ConfigFilePath()
	if err != nil {
		t.Fatalf("ConfigFilePath() error = %v", err)
	}
	if filepath.Base(path) != ConfigFileName {
		t.Errorf("ConfigFilePath() = %q, want base %q", path, ConfigFileName)
	}
	if filepath.Base(filepath.Dir(path)) != AppDirName {
		t.Errorf("ConfigFilePath() = %q, want parent %q", path, AppDirName)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)

	want := Config{
		VSPipePath:    `C:\tools\vapoursynth\vspipe.exe`,
		LogLevel:      "TRACE",
		SingleNUMA:    true,
		RPCheckerPath: `C:\tools\rpc\RPChecker.exe`,
		AVX512:        true,
		ReducePath:    false,
	}
	if err := saveConfigFile(path, want); err != nil {
		t.Fatalf("saveConfigFile() error = %v", err)
	}
	got, err := loadConfigFile(path)
	if err != nil {
		t.Fatalf("loadConfigFile() error = %v", err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestLoadConfigFileMissingUsesDefaults(t *testing.T) {
	t.Parallel()
	// Initializer.LoadConfig kept the built-in defaults when the file did not
	// exist; the defaults are the legacy field initialisers.
	got, err := loadConfigFile(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("loadConfigFile() error = %v, want nil for a missing file", err)
	}
	if got != DefaultConfig() {
		t.Errorf("defaults = %+v, want %+v", got, DefaultConfig())
	}
	if got.LogLevel != "DEBUG" || !got.ReducePath || got.SingleNUMA || got.AVX512 {
		t.Errorf("defaults do not match the legacy field initialisers: %+v", got)
	}
}

func TestLoadConfigFileCorruptReportsError(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), ConfigFileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfigFile(path); err == nil {
		t.Fatal("loadConfigFile() accepted a corrupt file")
	}
}

func TestLoadConfigFileIgnoresUnknownKeys(t *testing.T) {
	t.Parallel()
	// The legacy JSON had more fields than the current struct; unknown keys
	// must not make an old file unreadable.
	path := filepath.Join(t.TempDir(), ConfigFileName)
	raw := `{"logLevel":"INFO","memoryTotal":17179869184,"singleNuma":true}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadConfigFile(path)
	if err != nil {
		t.Fatalf("loadConfigFile() error = %v", err)
	}
	if got.LogLevel != "INFO" || !got.SingleNUMA {
		t.Errorf("decoded = %+v, want logLevel INFO and singleNuma true", got)
	}
}

func TestSaveConfigFileIsIndentedAndNewlineTerminated(t *testing.T) {
	t.Parallel()
	// WriteConfig used Formatting.Indented with a four-space indentation.
	path := filepath.Join(t.TempDir(), ConfigFileName)
	if err := saveConfigFile(path, Config{VSPipePath: "vspipe", LogLevel: "DEBUG", ReducePath: true}); err != nil {
		t.Fatalf("saveConfigFile() error = %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Error("file does not end with a newline")
	}
	if !strings.Contains(string(raw), "\n    \"logLevel\"") {
		t.Errorf("file is not indented with four spaces:\n%s", raw)
	}
	// The keys must be the legacy property names.
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"vspipePath", "logLevel", "singleNuma", "rpCheckerPath", "avx512", "reducePath"} {
		if _, ok := keys[key]; !ok {
			t.Errorf("JSON is missing the legacy key %q", key)
		}
	}
}

func TestEnsureConfigDirCreatesDirectory(t *testing.T) {
	t.Parallel()
	// The base is a temp dir, so this never touches the real user config.
	base := t.TempDir()
	got, err := ensureConfigDir(filepath.Join(base, AppDirName))
	if err != nil {
		t.Fatalf("ensureConfigDir() error = %v", err)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("directory was not created: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("%s is not a directory", got)
	}
	// Calling it again is not an error.
	if _, err := ensureConfigDir(got); err != nil {
		t.Fatalf("ensureConfigDir() on an existing directory error = %v", err)
	}
}
