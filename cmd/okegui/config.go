package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
	"github.com/KiritakeKumi/OKEGuiDX/internal/toolchain"
)

// queueFileName is the persisted task queue inside the configuration
// directory. The engine reads it back on start, which is what gives the
// standalone build crash recovery (CLUSTER.md §2, reservation 3).
const queueFileName = "queue.json"

// logDirName is the log directory below the configuration directory.
const logDirName = "log"

// configLoader resolves the application settings.
//
// platform.LoadConfig only reads the per-user location, while this command
// needs an explicit --config path and tests need a temporary directory, so the
// file is read here and decoded into platform.Config. The schema and the
// defaults still come from platform, which owns them.
type configLoader struct {
	// dir overrides the configuration directory. Tests set it; the real
	// program leaves it empty and uses the per-user directory.
	dir string

	cfg    platform.Config
	loaded bool
}

// load reads the settings file once. A damaged file is reported through the
// log and the defaults are used, mirroring the legacy prompt that offered to
// reset the settings (Initializer.LoadConfig).
func (c *configLoader) load(explicitPath string) {
	if c.loaded {
		return
	}
	c.loaded = true
	c.cfg = platform.DefaultConfig()

	path := explicitPath
	if path == "" {
		var err error
		path, err = c.configFilePath()
		if err != nil {
			// Without a configuration directory there is nothing to load; the
			// defaults are still usable.
			return
		}
	}
	cfg, err := loadConfigFile(path)
	if err != nil {
		log.Warn("无法读取配置文件，使用默认设置", "path", path, "err", err)
		return
	}
	c.cfg = cfg
}

// loadConfigFile decodes an OKEGuiConfig.json from an explicit path. A missing
// file is not an error: the defaults apply.
func loadConfigFile(path string) (platform.Config, error) {
	cfg := platform.DefaultConfig()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, okerr.Wrap(err, okerr.KindIO, "无法读取配置文件", "%s: %v", path, err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, okerr.Wrap(err, okerr.KindConfig, "配置文件已损坏", "%s 不是合法的 JSON: %v", path, err)
	}
	return cfg, nil
}

// LogLevel returns the configured log level.
func (c *configLoader) LogLevel() string { return c.cfg.LogLevel }

// SingleNUMA reports whether per-socket pinning is disabled.
func (c *configLoader) SingleNUMA() bool { return c.cfg.SingleNUMA }

// Explicit maps the tool paths named in the settings file to tool names, so an
// explicitly configured vspipe wins over discovery, as it did in the legacy
// code.
func (c *configLoader) Explicit() map[string]string {
	out := map[string]string{}
	if c.cfg.VSPipePath != "" {
		out[toolchain.ToolVSPipe] = c.cfg.VSPipePath
	}
	if c.cfg.RPCheckerPath != "" {
		out[toolchain.ToolRPCChecker] = c.cfg.RPCheckerPath
	}
	return out
}

// Dir returns the configuration directory, creating it when needed.
func (c *configLoader) Dir() (string, error) {
	if c.dir != "" {
		if err := os.MkdirAll(c.dir, 0o700); err != nil {
			return "", okerr.Wrap(err, okerr.KindIO, "无法创建配置目录", "%s: %v", c.dir, err)
		}
		return c.dir, nil
	}
	return platform.ConfigDir()
}

// DirPath returns the configuration directory without creating it. A command
// that never needs to write there must not leave an empty directory behind.
func (c *configLoader) DirPath() (string, error) {
	if c.dir != "" {
		return c.dir, nil
	}
	return platform.ConfigDirPath()
}

// configFilePath returns the settings file location without creating anything.
func (c *configLoader) configFilePath() (string, error) {
	if c.dir != "" {
		return filepath.Join(c.dir, platform.ConfigFileName), nil
	}
	return platform.ConfigFilePath()
}
