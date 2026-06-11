package session

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// ApplyConfiguredLoadout materializes the declarative per-group /
// per-conductor skill and MCP loadout ([groups.X.claude].skills/.mcps,
// [conductors.X.claude].skills/.mcps) for a claude-compatible session, by
// driving the existing project-skills attach machinery and local .mcp.json
// writer — exactly as if the user had run `skill attach` / `mcp attach` by
// hand. Called at session create (add/launch) and re-asserted before every
// Start/StartWithMessage/Restart spawn, so a config edit takes effect on
// the next start and a healthy state is a cheap no-op.
//
// Semantics (the loadout is an attach-only FLOOR):
//
//   - already attached (manifest entry + healthy target) → silent no-op
//   - manifest entry whose target went missing → re-materialized (heal)
//   - target exists as a real dir or foreign symlink (not manifest-managed)
//     → skip + warning, never clobber; a human-placed dir beats config
//   - entry not resolvable in the skill-source registry → skip + warning
//   - removing an entry from the config list does NOT detach — subtraction
//     is a deliberate `skill detach` (a config typo must not strip a live
//     session's skills)
//
// MCP entries are [mcps.X] catalog names appended to the session's local
// .mcp.json (never removed); unknown names skip + warn.
//
// The effective lists are the union of the group ancestor chain and, for
// conductor sessions, the conductor block (group floor + conductor extras).
//
// Returns the warnings (also slog-warned) so CLI call sites can print them;
// a nil return means nothing to do or everything healthy. Failures never
// block the spawn — the loadout is provisioning, not a launch gate.
func ApplyConfiguredLoadout(inst *Instance) []string {
	if inst == nil || !IsClaudeCompatible(inst.Tool) {
		return nil
	}
	if inst.ProjectPath == "" || inst.SSHHost != "" {
		// No local project path to materialize into (SSH sessions run on a
		// remote working dir agent-deck cannot symlink into).
		return nil
	}

	config, cfgErr := LoadUserConfig()
	if cfgErr != nil {
		w := fmt.Sprintf("config.toml error — declarative skill/mcp loadout inactive: %v", cfgErr)
		sessionLog.Warn("loadout_config_unreadable",
			slog.String("session", inst.Title),
			slog.String("error", cfgErr.Error()))
		return []string{w}
	}
	if config == nil {
		return nil
	}

	skills := unionLoadoutEntries(
		config.GetGroupClaudeSkills(inst.GroupPath),
		config.GetConductorClaudeSkills(conductorNameFromInstance(inst)),
	)
	mcps := unionLoadoutEntries(
		config.GetGroupClaudeMCPs(inst.GroupPath),
		config.GetConductorClaudeMCPs(conductorNameFromInstance(inst)),
	)
	if len(skills) == 0 && len(mcps) == 0 {
		return nil
	}

	var warnings []string
	warn := func(format string, args ...interface{}) {
		w := fmt.Sprintf(format, args...)
		warnings = append(warnings, w)
		sessionLog.Warn("loadout_entry_skipped",
			slog.String("session", inst.Title),
			slog.String("group", inst.GroupPath),
			slog.String("detail", w))
	}

	for _, entry := range skills {
		attachment, err := AttachSkillToProject(inst.ProjectPath, inst.Tool, entry, "")
		switch {
		case err == nil:
			sessionLog.Info("loadout_skill_attached",
				slog.String("session", inst.Title),
				slog.String("skill", entry),
				slog.String("target", attachment.TargetPath))
		case errors.Is(err, ErrSkillAlreadyAttached):
			// Healthy floor — nothing to do.
		case errors.Is(err, ErrSkillNotFound) || errors.Is(err, ErrSkillSourceNotFound):
			warn("skill %q: not found in the skill-source registry (register the store with `agent-deck skill source add`)", entry)
		case errors.Is(err, ErrSkillAmbiguous):
			warn("skill %q: ambiguous — qualify as <source>/<name>: %v", entry, err)
		case errors.Is(err, ErrSkillUnsupportedKind):
			warn("skill %q: not an attachable directory skill: %v", entry, err)
		default:
			// Covers the never-clobber conflicts ("target already exists and
			// is not managed", "target already managed by …") and IO errors.
			warn("skill %q: %v", entry, err)
		}
	}

	if len(mcps) > 0 {
		available := GetAvailableMCPs()
		info := inst.MCPInfoForLocalAttach()
		if info == nil {
			info = &MCPInfo{}
		}
		current := info.Local()
		attached := make(map[string]bool, len(current))
		for _, name := range current {
			attached[name] = true
		}
		newLocal := append([]string{}, current...)
		added := false
		for _, name := range mcps {
			if attached[name] {
				continue
			}
			if _, ok := available[name]; !ok {
				warn("mcp %q: not defined in config.toml [mcps.%s]", name, name)
				continue
			}
			newLocal = append(newLocal, name)
			attached[name] = true
			added = true
			sessionLog.Info("loadout_mcp_attached",
				slog.String("session", inst.Title),
				slog.String("mcp", name))
		}
		if added {
			if err := inst.WriteLocalMCPConfig(newLocal); err != nil {
				warn("mcp loadout: failed to write local .mcp.json: %v", err)
			} else {
				inst.InvalidateProjectMCPIntegrationsCache()
			}
		}
	}

	return warnings
}

// unionLoadoutEntries merges loadout lists preserving order (group floor
// first, conductor extras after), deduplicated, blanks dropped.
func unionLoadoutEntries(lists ...[]string) []string {
	seen := make(map[string]bool)
	var union []string
	for _, list := range lists {
		for _, entry := range list {
			entry = strings.TrimSpace(entry)
			if entry == "" || seen[entry] {
				continue
			}
			seen[entry] = true
			union = append(union, entry)
		}
	}
	return union
}
