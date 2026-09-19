package platform

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// AppDirName is the per-user directory that holds OKEGuiDX state. It replaces
// the HKCU\Software\OKEGui registry key that Utils/RegistryStorage.cs used.
const AppDirName = "OKEGuiDX"

// ConfigFileName is the settings file inside AppDirName. The name is kept from
// the legacy layout so an existing OKEGuiConfig.json can be moved rather than
// rewritten (PLAN.md §6); only its location changes, from beside the
// executable to the per-user configuration directory.
const ConfigFileName = "OKEGuiConfig.json"

// Config is the application settings schema, the Go equivalent of the legacy
// OKEGuiConfig class. JSON keys are the legacy property names, so a file
// written by the .NET program parses unchanged.
//
// RegistryStorage's Load/Save helpers have no counterpart here: their only
// caller was the updater's LastCheck timestamp, and the updater was removed in
// commit 0ca3dba. RegistryAddCount is discussed in the hand-off notes.
type Config struct {
	// VSPipePath is the VapourSynth vspipe executable.
	VSPipePath string `json:"vspipePath"`
	// LogLevel is TRACE, DEBUG, INFO, WARN, ERROR or FATAL.
	LogLevel string `json:"logLevel"`
	// SingleNUMA forces a single NUMA node, disabling per-socket pinning.
	SingleNUMA bool `json:"singleNuma"`
	// RPCheckerPath is the RPChecker executable.
	RPCheckerPath string `json:"rpCheckerPath"`
	// AVX512 enables the AVX-512 assembly path in x265.
	AVX512 bool `json:"avx512"`
	// ReducePath shortens long input paths when building output names.
	ReducePath bool `json:"reducePath"`
}

// DefaultConfig returns the settings used when no file exists, matching the
// field initialisers of the legacy OKEGuiConfig class.
func DefaultConfig() Config {
	return Config{LogLevel: "DEBUG", ReducePath: true}
}

// ConfigDirPath returns the per-user configuration directory without touching
// the disk: os.UserConfigDir()/OKEGuiDX. On Windows that is
// %AppData%\OKEGuiDX, on Linux $XDG_CONFIG_HOME/OKEGuiDX or ~/.config/OKEGuiDX.
func ConfigDirPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", okerr.Wrap(err, okerr.KindIO, "无法确定配置目录",
			"无法确定当前用户的配置目录: %v", err)
	}
	return filepath.Join(base, AppDirName), nil
}

// ConfigDir returns the per-user configuration directory, creating it if
// needed.
func ConfigDir() (string, error) {
	dir, err := ConfigDirPath()
	if err != nil {
		return "", err
	}
	return ensureConfigDir(dir)
}

func ensureConfigDir(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", okerr.Wrap(err, okerr.KindIO, "无法创建配置目录",
			"无法创建配置目录 %s: %v", dir, err)
	}
	return dir, nil
}

// ConfigFilePath returns the settings file path without touching the disk.
func ConfigFilePath() (string, error) {
	dir, err := ConfigDirPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ConfigFileName), nil
}

// LoadConfig reads the settings file, falling back to DefaultConfig when the
// file does not exist, exactly as Initializer.LoadConfig did. A file that
// exists but cannot be read or parsed is reported: the legacy code showed a
// MessageBox and offered to reset or quit, and the caller decides which of
// those to do here.
func LoadConfig() (Config, error) {
	path, err := ConfigFilePath()
	if err != nil {
		return DefaultConfig(), err
	}
	return loadConfigFile(path)
}

func loadConfigFile(path string) (Config, error) {
	cfg := DefaultConfig()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, okerr.Wrap(err, okerr.KindIO, "无法读取配置文件",
			"无法读取配置文件 %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, okerr.Wrap(err, okerr.KindConfig, "配置文件已损坏",
			"配置文件 %s 不是合法的 JSON: %v", path, err)
	}
	return cfg, nil
}

// SaveConfig writes the settings as four-space indented JSON, mirroring
// Initializer.WriteConfig. The directory is created first.
func SaveConfig(cfg Config) error {
	path, err := ConfigFilePath()
	if err != nil {
		return err
	}
	if _, err := ensureConfigDir(filepath.Dir(path)); err != nil {
		return err
	}
	return saveConfigFile(path, cfg)
}

func saveConfigFile(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "    ")
	if err != nil {
		return okerr.Wrap(err, okerr.KindConfig, "无法序列化配置文件",
			"无法序列化配置: %v", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return okerr.Wrap(err, okerr.KindIO, "无法写入配置文件",
			"无法写入配置文件 %s: %v", path, err)
	}
	return nil
}
