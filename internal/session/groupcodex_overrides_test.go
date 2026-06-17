package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for the [groups.X.codex] / [conductors.X.codex] key surface added for
// codex conductor/worker config parity with claude: command, model, inline env
// map, config_dir (CODEX_HOME), env_file, and the yolo_mode autonomy override.
// Codex twin of groupclaude_overrides_test.go. Reuses withIsolatedHomeAndConfig
// from pergroupconfig_nested_test.go.

func TestGroupConductorCodex_CommandResolution(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[codex]
command = "codex-global"

[groups."work".codex]
command = "codex-work"

[conductors.lilu.codex]
command = "codex-lilu"
`)

	cases := []struct {
		name  string
		title string
		group string
		want  string
	}{
		{"group exact", "s1", "work", "codex-work"},
		{"group ancestor walk", "s2", "work/sub/deep", "codex-work"},
		{"conductor beats group", "conductor-lilu", "work", "codex-lilu"},
		{"global fallback", "s3", "other", "codex-global"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst := NewInstanceWithGroupAndTool(tc.title, "/tmp/p", tc.group, "codex")
			if got := GetCodexCommandForInstance(inst); got != tc.want {
				t.Errorf("GetCodexCommandForInstance=%q want %q", got, tc.want)
			}
			// The resolved command is what buildCodexCommand execs (baseCommand
			// "codex" is the default, so the per-group/conductor override applies).
			cmd := inst.buildCodexCommand("codex")
			if !strings.Contains(cmd, tc.want) {
				t.Errorf("spawn command does not exec %q:\n%s", tc.want, cmd)
			}
		})
	}
}

func TestGroupConductorCodex_CommandDefaultsToCodexWithoutConfig(t *testing.T) {
	withIsolatedHomeAndConfig(t, ``)
	inst := NewInstanceWithGroupAndTool("s1", "/tmp/p", "work", "codex")
	if got := GetCodexCommandForInstance(inst); got != "codex" {
		t.Errorf("GetCodexCommandForInstance=%q want codex", got)
	}
}

func TestGroupConductorCodex_ModelResolution(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[groups."work".codex]
model = "gpt-5"

[conductors.lilu.codex]
model = "o3"
`)

	t.Run("group model applies when session has none", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("s1", "/tmp/p", "work/sub", "codex")
		cmd := inst.buildCodexCommand("codex")
		if !strings.Contains(cmd, "--model gpt-5") {
			t.Errorf("expected group model flag in command:\n%s", cmd)
		}
	})

	t.Run("conductor model beats group model", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("conductor-lilu", "/tmp/p", "work", "codex")
		cmd := inst.buildCodexCommand("codex")
		if !strings.Contains(cmd, "--model o3") {
			t.Errorf("expected conductor model flag in command:\n%s", cmd)
		}
	})

	t.Run("explicit per-session model wins", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("s2", "/tmp/p", "work", "codex")
		if err := inst.SetCodexOptions(&CodexOptions{Model: "gpt-4o"}); err != nil {
			t.Fatalf("SetCodexOptions: %v", err)
		}
		cmd := inst.buildCodexCommand("codex")
		if !strings.Contains(cmd, "--model gpt-4o") {
			t.Errorf("expected per-session model flag in command:\n%s", cmd)
		}
		if strings.Contains(cmd, "gpt-5") {
			t.Errorf("group model must not override per-session model:\n%s", cmd)
		}
	})

	t.Run("no model levels set emits no flag", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("s3", "/tmp/p", "other", "codex")
		cmd := inst.buildCodexCommand("codex")
		if strings.Contains(cmd, "--model") {
			t.Errorf("expected no --model flag (empty falls through, #1172 semantics):\n%s", cmd)
		}
	})
}

func TestGroupConductorCodex_InlineEnvLayering(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[groups."personal".codex]
env = { AGENT_ROLE = "parent", SHARED = "from-parent" }

[groups."personal/sub".codex]
env = { AGENT_ROLE = "child" }

[conductors.lilu.codex]
env = { AGENT_ROLE = "lilu", LILU_ONLY = "1" }
`)

	t.Run("child group key wins, parent-only key persists", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("s1", "/tmp/p", "personal/sub", "codex")
		cmd := inst.buildEnvSourceCommand()
		if !strings.Contains(cmd, "export AGENT_ROLE='child'") {
			t.Errorf("nearest group must win per key:\n%s", cmd)
		}
		if !strings.Contains(cmd, "export SHARED='from-parent'") {
			t.Errorf("parent-only key must persist through the merge:\n%s", cmd)
		}
	})

	t.Run("conductor env wins over group env per key", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("conductor-lilu", "/tmp/p", "personal/sub", "codex")
		cmd := inst.buildEnvSourceCommand()
		for _, want := range []string{"export AGENT_ROLE='lilu'", "export LILU_ONLY='1'", "export SHARED='from-parent'"} {
			if !strings.Contains(cmd, want) {
				t.Errorf("missing %q in:\n%s", want, cmd)
			}
		}
	})

	t.Run("claude sessions get no codex inline env", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("s2", "/tmp/p", "personal/sub", "claude")
		cmd := inst.buildEnvSourceCommand()
		if strings.Contains(cmd, "AGENT_ROLE") {
			t.Errorf("[groups.X.codex].env must not leak into claude spawns:\n%s", cmd)
		}
	})
}

// TestGroupCodexClaudeEnvIsolation locks that the two per-tool env tables do
// not bleed into each other: a codex session sees only [*.codex].env, a claude
// session only [*.claude].env.
func TestGroupCodexClaudeEnvIsolation(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[groups."work".claude]
env = { ONLY_CLAUDE = "c" }

[groups."work".codex]
env = { ONLY_CODEX = "x" }
`)

	codexCmd := NewInstanceWithGroupAndTool("s1", "/tmp/p", "work", "codex").buildEnvSourceCommand()
	if !strings.Contains(codexCmd, "export ONLY_CODEX='x'") {
		t.Errorf("codex session must export codex inline env:\n%s", codexCmd)
	}
	if strings.Contains(codexCmd, "ONLY_CLAUDE") {
		t.Errorf("codex session must not export claude inline env:\n%s", codexCmd)
	}

	claudeCmd := NewInstanceWithGroupAndTool("s2", "/tmp/p", "work", "claude").buildEnvSourceCommand()
	if !strings.Contains(claudeCmd, "export ONLY_CLAUDE='c'") {
		t.Errorf("claude session must export claude inline env:\n%s", claudeCmd)
	}
	if strings.Contains(claudeCmd, "ONLY_CODEX") {
		t.Errorf("claude session must not export codex inline env:\n%s", claudeCmd)
	}
}

func TestGroupCodex_InlineEnvExportedAfterEnvFile(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[groups."personal".codex]
env_file = "~/.agent-deck/personal-codex.env"
env = { AGENT_ROLE = "inline-wins" }
`)
	envPath := filepath.Join(tmpHome, ".agent-deck", "personal-codex.env")
	if err := os.WriteFile(envPath, []byte("export AGENT_ROLE=from-file\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	inst := NewInstanceWithGroupAndTool("s1", "/tmp/p", "personal", "codex")
	cmd := inst.buildEnvSourceCommand()

	sourceIdx := strings.Index(cmd, `source "`+envPath+`"`)
	exportIdx := strings.Index(cmd, "export AGENT_ROLE='inline-wins'")
	if sourceIdx == -1 || exportIdx == -1 {
		t.Fatalf("missing env_file source (%d) or inline export (%d) in:\n%s", sourceIdx, exportIdx, cmd)
	}
	if exportIdx < sourceIdx {
		t.Errorf("inline env must be exported AFTER the env_file source so it wins on conflict:\n%s", cmd)
	}
}

func TestGroupConductorCodex_EnvFileChain(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[codex]
env_file = "/tmp/global-codex.env"

[groups."work".codex]
env_file = "/tmp/group-codex.env"

[conductors.lilu.codex]
env_file = "/tmp/conductor-codex.env"
`)
	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}

	if got := cfg.GetGroupCodexEnvFile("work/sub"); got != "/tmp/group-codex.env" {
		t.Errorf("group env_file ancestor walk=%q want /tmp/group-codex.env", got)
	}
	if got := cfg.GetConductorCodexEnvFile("lilu"); got != "/tmp/conductor-codex.env" {
		t.Errorf("conductor env_file=%q want /tmp/conductor-codex.env", got)
	}

	// getToolEnvFile must prefer conductor over group over global.
	conductor := NewInstanceWithGroupAndTool("conductor-lilu", "/tmp/p", "work", "codex")
	if got := conductor.getToolEnvFile(); got != "/tmp/conductor-codex.env" {
		t.Errorf("conductor session env_file=%q want conductor value", got)
	}
	plain := NewInstanceWithGroupAndTool("s1", "/tmp/p", "work", "codex")
	if got := plain.getToolEnvFile(); got != "/tmp/group-codex.env" {
		t.Errorf("group session env_file=%q want group value", got)
	}
	ungrouped := NewInstanceWithGroupAndTool("s2", "/tmp/p", "other", "codex")
	if got := ungrouped.getToolEnvFile(); got != "/tmp/global-codex.env" {
		t.Errorf("ungrouped session env_file=%q want global value", got)
	}
}

func TestGroupConductorCodex_ConfigDirResolution(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[groups."work".codex]
config_dir = "~/.codex-work"

[conductors.lilu.codex]
config_dir = "~/.codex-lilu"
`)
	t.Setenv("CODEX_HOME", "") // ensure no ambient env override

	workDir := filepath.Join(tmpHome, ".codex-work")
	liluDir := filepath.Join(tmpHome, ".codex-lilu")

	// getCodexHomeDir (instance method) precedence: conductor > group.
	groupInst := NewInstanceWithGroupAndTool("s1", "/tmp/p", "work/sub", "codex")
	if got := groupInst.getCodexHomeDir(); got != workDir {
		t.Errorf("group config_dir resolution=%q want %q", got, workDir)
	}
	if !groupInst.isCodexHomeExplicit() {
		t.Error("group config_dir must make CODEX_HOME explicit")
	}
	conductorInst := NewInstanceWithGroupAndTool("conductor-lilu", "/tmp/p", "work", "codex")
	if got := conductorInst.getCodexHomeDir(); got != liluDir {
		t.Errorf("conductor config_dir must beat group: got %q want %q", got, liluDir)
	}

	// The CODEX_HOME= prefix is injected into the spawn command.
	cmd := groupInst.buildCodexCommand("codex")
	if !strings.Contains(cmd, "CODEX_HOME="+workDir+" ") {
		t.Errorf("spawn command must inject the resolved CODEX_HOME:\n%s", cmd)
	}
}

func TestGroupConductorCodex_YoloAutonomy(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[codex]
yolo_mode = false

[groups."fleet".codex]
yolo_mode = true

[groups."fleet/safe".codex]
yolo_mode = false

[conductors.runner.codex]
yolo_mode = true
`)

	t.Run("group yolo_mode adds --yolo", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("s1", "/tmp/p", "fleet", "codex")
		if !strings.Contains(inst.buildCodexCommand("codex"), " --yolo") {
			t.Error("group yolo_mode=true must add --yolo")
		}
	})

	t.Run("explicit false at child group pins autonomy off", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("s2", "/tmp/p", "fleet/safe", "codex")
		if strings.Contains(inst.buildCodexCommand("codex"), "--yolo") {
			t.Error("child group yolo_mode=false must override the parent's true")
		}
	})

	t.Run("conductor yolo_mode marks the conductor autonomous", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("conductor-runner", "/tmp/p", "other", "codex")
		if !strings.Contains(inst.buildCodexCommand("codex"), " --yolo") {
			t.Error("conductor yolo_mode=true must add --yolo (the on-request approval blocker)")
		}
	})

	t.Run("global false default emits no --yolo", func(t *testing.T) {
		inst := NewInstanceWithGroupAndTool("s3", "/tmp/p", "other", "codex")
		if strings.Contains(inst.buildCodexCommand("codex"), "--yolo") {
			t.Error("ungrouped session must fall through to global yolo_mode=false")
		}
	})
}

func TestResolveGroupCodex_SourcesAndEnvFileExistence(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[codex]
command = "codex-global"

[groups."work".codex]
env_file = "~/.agent-deck/groups/work-codex.env"
model = "gpt-5"
config_dir = "~/.codex-work"
env = { AGENT_ROLE = "work" }
yolo_mode = true
`)
	t.Setenv("CODEX_HOME", "")

	res := ResolveGroupCodex("work/sub")
	if res.ConfigError != "" {
		t.Fatalf("unexpected config error: %s", res.ConfigError)
	}
	if res.ConfigDir != filepath.Join(tmpHome, ".codex-work") || res.ConfigDirSource != "group:work" {
		t.Errorf("config_dir=%q [%s] want ~/.codex-work [group:work]", res.ConfigDir, res.ConfigDirSource)
	}
	if res.EnvFileSource != "group:work" {
		t.Errorf("env_file source=%q want group:work", res.EnvFileSource)
	}
	if res.EnvFileExists {
		t.Error("env_file must report missing before the file is created")
	}
	if res.Command != "codex-global" || res.CommandSource != "global" {
		t.Errorf("command=%q [%s] want codex-global [global]", res.Command, res.CommandSource)
	}
	if res.Model != "gpt-5" || res.ModelSource != "group:work" {
		t.Errorf("model=%q [%s] want gpt-5 [group:work]", res.Model, res.ModelSource)
	}
	if !res.Yolo || res.YoloSource != "group" {
		t.Errorf("yolo=%v [%s] want true [group]", res.Yolo, res.YoloSource)
	}
	if res.Env["AGENT_ROLE"] != "work" {
		t.Errorf("env=%v want AGENT_ROLE=work", res.Env)
	}

	envPath := filepath.Join(tmpHome, ".agent-deck", "groups", "work-codex.env")
	if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(envPath, []byte("export A=1\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	res = ResolveGroupCodex("work")
	if !res.EnvFileExists {
		t.Error("env_file must report exists after creation")
	}
}

func TestResolveGroupCodex_SurfacesConfigError(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[groups."work".codex
broken =
`)
	res := ResolveGroupCodex("work")
	if res.ConfigError == "" {
		t.Fatal("a broken config.toml must surface in ConfigError — the zero-diagnostics failure mode group show --resolved exists to catch")
	}
	if res.Command != "codex" || res.CommandSource != "default" {
		t.Errorf("broken config must resolve to defaults, got command=%q [%s]", res.Command, res.CommandSource)
	}
}
