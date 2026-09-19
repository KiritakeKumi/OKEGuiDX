package api

import (
	"net/http"
	"strings"

	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
)

// ConfigStore reads and writes the application settings. It exists so the
// package can be tested without touching the per-user configuration directory
// and so a future coordinator can keep its settings somewhere else.
type ConfigStore interface {
	// Load returns the stored settings.
	Load() (platform.Config, error)
	// Save writes the settings.
	Save(platform.Config) error
}

// platformConfigStore is the production store: the OKEGuiConfig.json that
// internal/platform owns. It deliberately has no state, so the daemon's
// --config override stays in cmd/okegui, which is where the path is decided.
type platformConfigStore struct{}

// Load implements ConfigStore.
func (platformConfigStore) Load() (platform.Config, error) { return platform.LoadConfig() }

// Save implements ConfigStore.
func (platformConfigStore) Save(cfg platform.Config) error { return platform.SaveConfig(cfg) }

// configResponse is the payload of the config endpoints.
type configResponse struct {
	Config platform.Config `json:"config"`
}

// configRequest is the body of PUT /api/v1/config. Config is a pointer so that
// an absent or null field is rejected instead of being decoded into a
// zero-valued settings object, which would silently wipe every setting.
type configRequest struct {
	Config *platform.Config `json:"config"`
}

// handleConfigGet implements GET /api/v1/config.
func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.config.Load()
	if err != nil {
		// Reading the settings is a server-side problem whatever the error
		// kind: the client asked for a resource it cannot influence.
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, configResponse{Config: cfg})
}

// handleConfigPut implements PUT /api/v1/config.
//
// The body is the whole settings object, not a patch: that is what the settings
// panel edits (it works on a copy of the configuration and writes it back),
// and a full replace cannot half-apply a set of related changes.
//
// The settings are validated before they are written. LogLevel is the one
// field with a constrained set of values; internal/log silently falls back to
// DEBUG on an unknown level, and a settings endpoint that accepts a typo and
// then behaves differently than requested is worse than one that refuses.
func (s *Server) handleConfigPut(w http.ResponseWriter, r *http.Request) {
	var req configRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Config == nil {
		writeError(w, http.StatusBadRequest, okerr.New(okerr.KindConfig,
			"请求内容不合法", "必须提供 config 对象。"))
		return
	}
	if err := validateConfig(*req.Config); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if err := s.config.Save(*req.Config); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if s.onConfigChange != nil {
		// The callback applies what cannot be applied by writing the file:
		// the log level and the tool paths. It runs after the write so a
		// failing callback cannot lose the operator's change.
		s.onConfigChange(*req.Config)
	}
	writeJSON(w, http.StatusOK, configResponse{Config: *req.Config})
}

// validateConfig rejects settings the program cannot honour.
func validateConfig(cfg platform.Config) error {
	switch strings.ToUpper(strings.TrimSpace(cfg.LogLevel)) {
	case "", "TRACE", "DEBUG", "INFO", "WARN", "WARNING", "ERROR", "FATAL":
		return nil
	default:
		return okerr.New(okerr.KindConfig, "日志级别不合法",
			"logLevel 只能是 TRACE / DEBUG / INFO / WARN / ERROR / FATAL（当前 %q）。", cfg.LogLevel)
	}
}
