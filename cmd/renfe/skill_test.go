package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installSkill must never write through a symlink (users link skill folders to their
// checkouts) or replace a folder that is not this skill.
func TestInstallSkillProtectsExistingFolders(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "fake-renfe")
	if err := os.WriteFile(exe, []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}

	fresh := filepath.Join(root, "agent", "renfe")
	if err := installSkill(fresh, exe, false); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"SKILL.md", "agents/openai.yaml", "scripts/renfe", "bin/renfe"} {
		if _, err := os.Stat(filepath.Join(fresh, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	if info, _ := os.Stat(filepath.Join(fresh, "scripts", "renfe")); info.Mode().Perm()&0100 == 0 {
		t.Error("launcher is not executable")
	}
	if err := installSkill(fresh, "", false); err != nil { // replacing an earlier renfe skill is fine
		t.Fatalf("reinstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fresh, "bin", "renfe")); !os.IsNotExist(err) {
		t.Error("reinstall without a binary kept the old bin/renfe")
	}

	checkout := filepath.Join(root, "checkout")
	os.MkdirAll(checkout, 0755)
	os.WriteFile(filepath.Join(checkout, "SKILL.md"), []byte("---\nname: renfe\n---\nlocal edits\n"), 0644)
	linked := filepath.Join(root, "linked", "renfe")
	os.MkdirAll(filepath.Dir(linked), 0755)
	os.Symlink(checkout, linked)
	if err := installSkill(linked, exe, false); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("symlink: got %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(checkout, "SKILL.md")); !strings.Contains(string(data), "local edits") {
		t.Error("wrote through the symlink into the checkout")
	}

	other := filepath.Join(root, "other", "renfe")
	os.MkdirAll(other, 0755)
	os.WriteFile(filepath.Join(other, "SKILL.md"), []byte("---\nname: something-else\n---\n"), 0644)
	if err := installSkill(other, exe, false); err == nil || !strings.Contains(err.Error(), "not the renfe skill") {
		t.Errorf("foreign folder: got %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(fresh)); len(entries) != 1 {
		t.Errorf("leftover temporary folders: %v", entries)
	}
}

// OpenClaw (default state folder) and Hermes (when configured) also load ~/.agents/skills,
// so the codex copy serves them; a second copy would duplicate the skill, which Hermes
// rejects as an ambiguous name.
func TestSkillTargetsAvoidDuplicateCopies(t *testing.T) {
	for _, tc := range []struct {
		name          string
		hermesConfig  string
		openclawState bool
		want          map[string]bool // agent -> installed (false: skipped)
	}{
		{"shared folders", "skills:\n  external_dirs:\n    - ~/.agents/skills\n", false, map[string]bool{"claude": true, "codex": true, "openclaw": false, "hermes": false}},
		{"separate folders", "model: x\n", true, map[string]bool{"claude": true, "codex": true, "openclaw": true, "hermes": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			for _, v := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "OPENCLAW_STATE_DIR", "OPENCLAW_CONFIG_PATH", "HERMES_HOME"} {
				t.Setenv(v, "")
			}
			for _, d := range []string{".claude", ".codex", ".openclaw", ".hermes"} {
				os.MkdirAll(filepath.Join(home, d), 0755)
			}
			os.WriteFile(filepath.Join(home, ".hermes", "config.yaml"), []byte(tc.hermesConfig), 0644)
			if tc.openclawState {
				state := filepath.Join(home, "oc-state")
				os.MkdirAll(state, 0755)
				t.Setenv("OPENCLAW_STATE_DIR", state)
			}
			targets, err := skillTargets("", "")
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]bool{}
			for _, target := range targets {
				got[target.Agent] = target.Skipped == ""
			}
			if len(got) != len(tc.want) {
				t.Fatalf("targets %+v", targets)
			}
			for agent, installed := range tc.want {
				if got[agent] != installed {
					t.Errorf("%s: installed=%v, want %v (%+v)", agent, got[agent], installed, targets)
				}
			}
		})
	}
}
