package session

import (
	"os"
	"path/filepath"
	"strings"
)

// GroupCodexResolution is the resolved view of the effective Codex
// configuration for a group path — the codex twin of GroupClaudeResolution,
// built for `agent-deck group show --resolved --tool codex`. A misconfigured
// [groups.X.codex] stanza (key typo, TOML parse error, missing env_file) is
// otherwise indistinguishable at launch; this surfaces it.
//
// Source labels: "group:<path>" (the ancestor that matched), "global", "env",
// "profile", "default", or "" when unset.
type GroupCodexResolution struct {
	ConfigDir       string `json:"config_dir,omitempty"`
	ConfigDirSource string `json:"config_dir_source"`

	EnvFile         string `json:"env_file,omitempty"`
	EnvFileSource   string `json:"env_file_source,omitempty"`
	EnvFileResolved string `json:"env_file_resolved,omitempty"`
	// EnvFileExists is meaningful only when EnvFileResolved is absolute;
	// a relative env_file resolves against each session's working dir.
	EnvFileExists bool `json:"env_file_exists"`

	Command       string `json:"command"`
	CommandSource string `json:"command_source"`

	Model       string `json:"model,omitempty"`
	ModelSource string `json:"model_source,omitempty"`

	Env map[string]string `json:"env,omitempty"`

	// Yolo reports the resolved autonomous (--yolo) state for a session in
	// this group: conductor overrides are per-session and not part of a group
	// view, so this is the group-chain → global [codex].yolo_mode result.
	Yolo       bool   `json:"yolo"`
	YoloSource string `json:"yolo_source,omitempty"`

	// ConfigError carries the config.toml load error verbatim when the file
	// failed to parse — in that state every value above is a default.
	ConfigError string `json:"config_error,omitempty"`
}

// ResolveGroupCodex resolves the effective Codex settings for a group path
// using the same chains the codex spawn builders use (group ancestor-walk →
// global → default; $CODEX_HOME beats group for config_dir on the no-instance
// view). Conductor-level overrides are per-session and therefore not part of a
// group view — same scoping rule as ResolveGroupClaude.
func ResolveGroupCodex(groupPath string) GroupCodexResolution {
	res := GroupCodexResolution{}

	config, cfgErr := LoadUserConfig()
	if cfgErr != nil {
		res.ConfigError = cfgErr.Error()
	}

	// config_dir (CODEX_HOME). Group-view precedence mirrors
	// ResolveGroupClaude: $CODEX_HOME env wins, then group ancestor-walk,
	// then profile, then global [codex].config_dir, then ~/.codex default.
	if envHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); envHome != "" {
		res.ConfigDir = ExpandPath(envHome)
		res.ConfigDirSource = "env"
	} else if config != nil {
		if dir, matched := config.findGroupCodexSetting(groupPath, func(s GroupCodexSettings) string { return s.ConfigDir }); dir != "" {
			res.ConfigDir = ExpandPath(dir)
			res.ConfigDirSource = "group:" + matched
		} else if profileDir := config.GetProfileCodexConfigDir(GetEffectiveProfile("")); profileDir != "" {
			res.ConfigDir = profileDir
			res.ConfigDirSource = "profile"
		} else if config.Codex.ConfigDir != "" {
			res.ConfigDir = ExpandPath(config.Codex.ConfigDir)
			res.ConfigDirSource = "global"
		}
	}
	if res.ConfigDir == "" {
		home, _ := os.UserHomeDir()
		res.ConfigDir = filepath.Join(home, ".codex")
		res.ConfigDirSource = "default"
	}

	if config == nil {
		res.Command = "codex"
		res.CommandSource = "default"
		return res
	}

	// env_file — group chain then global, matching getToolEnvFile's codex branch.
	if envFile, matched := config.findGroupCodexSetting(groupPath, func(s GroupCodexSettings) string { return s.EnvFile }); envFile != "" {
		res.EnvFile = envFile
		res.EnvFileSource = "group:" + matched
	} else if config.Codex.EnvFile != "" {
		res.EnvFile = config.Codex.EnvFile
		res.EnvFileSource = "global"
	}
	if res.EnvFile != "" {
		res.EnvFileResolved = ExpandPath(res.EnvFile)
		if filepath.IsAbs(res.EnvFileResolved) {
			_, statErr := os.Stat(res.EnvFileResolved)
			res.EnvFileExists = statErr == nil
		}
	}

	// command — group chain → global [codex].command → "codex".
	if cmd, matched := config.findGroupCodexSetting(groupPath, func(s GroupCodexSettings) string { return s.Command }); cmd != "" {
		res.Command = cmd
		res.CommandSource = "group:" + matched
	} else if config.Codex.Command != "" {
		res.Command = config.Codex.Command
		res.CommandSource = "global"
	} else {
		res.Command = "codex"
		res.CommandSource = "default"
	}

	// model — group chain only (the global [codex] block has no model key).
	if model, matched := config.findGroupCodexSetting(groupPath, func(s GroupCodexSettings) string { return s.Model }); model != "" {
		res.Model = model
		res.ModelSource = "group:" + matched
	}

	// yolo — group chain (tri-state) → global [codex].yolo_mode.
	if y := config.GetGroupCodexYolo(groupPath); y != nil {
		res.Yolo = *y
		res.YoloSource = "group"
	} else {
		res.Yolo = config.Codex.YoloMode
		res.YoloSource = "global"
	}

	res.Env = config.GetGroupCodexEnv(groupPath)

	return res
}
