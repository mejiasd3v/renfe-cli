package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/mejiasd3v/renfe-cli/skills"
)

// agentRoot is where one agent loads user-level skills from.
type agentRoot struct {
	agent     string
	installed string // folder whose presence means the agent is installed
	skills    string // skills folder the install writes to
	// sharesAgentsSkills reports whether the agent also loads ~/.agents/skills, where
	// the codex install goes; a second copy would be redundant (OpenClaw) or make the
	// skill name ambiguous (Hermes).
	sharesAgentsSkills bool
}

// agentRoots follows each agent's own folder rules and environment overrides.
func agentRoots(home string) []agentRoot {
	claude := filepath.Join(home, ".claude")
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		claude = dir
	}
	codex := filepath.Join(home, ".codex")
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		codex = dir
	}
	// OpenClaw: OPENCLAW_STATE_DIR, else OPENCLAW_CONFIG_PATH's folder, else ~/.openclaw.
	// It reads ~/.agents/skills only with the default state folder.
	openclaw, openclawDefault := filepath.Join(home, ".openclaw"), true
	if dir := os.Getenv("OPENCLAW_STATE_DIR"); dir != "" {
		openclaw, openclawDefault = dir, false
	} else if path := os.Getenv("OPENCLAW_CONFIG_PATH"); path != "" {
		openclaw, openclawDefault = filepath.Dir(path), false
	}
	// Hermes reads ~/.agents/skills only when it is listed in skills.external_dirs.
	hermes := filepath.Join(home, ".hermes")
	if dir := os.Getenv("HERMES_HOME"); dir != "" {
		hermes = dir
	}
	hermesConfig, _ := os.ReadFile(filepath.Join(hermes, "config.yaml"))
	return []agentRoot{
		{"claude", claude, filepath.Join(claude, "skills"), false},
		{"codex", codex, filepath.Join(home, ".agents", "skills"), false},
		{"openclaw", openclaw, filepath.Join(openclaw, "skills"), openclawDefault},
		{"hermes", hermes, filepath.Join(hermes, "skills"), strings.Contains(string(hermesConfig), ".agents/skills")},
	}
}

type skillInstall struct {
	Agent   string `json:"agent"`
	Path    string `json:"path"`
	Binary  bool   `json:"binary"`
	Skipped string `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
}

func runSkill(args []string, out, stderr io.Writer) error {
	const help = "usage: renfe skill install [--agent claude,codex,openclaw,hermes|all] [--dir SKILLS_DIR] [--no-binary] [--force]"
	if len(args) == 0 || args[0] != "install" {
		return errors.New(help)
	}
	fs := flag.NewFlagSet("skill install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	agents := fs.String("agent", "", "comma-separated agents, or all (default: the agents found in your home folder)")
	dir := fs.String("dir", "", "install into this skills folder instead of an agent's")
	noBinary := fs.Bool("no-binary", false, "do not copy this renfe binary into the skill's bin/ folder")
	force := fs.Bool("force", false, "replace an existing symlink at the skill's path")
	format := fs.String("format", "json", "json or table")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || !validFormat(*format) || (*dir != "" && *agents != "") {
		return errors.New(help)
	}
	targets, err := skillTargets(*agents, *dir)
	if err != nil {
		return err
	}
	exe := ""
	if !*noBinary {
		if exe, err = os.Executable(); err == nil {
			exe, err = filepath.EvalSymlinks(exe)
		}
		if err != nil {
			return fmt.Errorf("cannot locate the renfe binary to bundle: %w", err)
		}
	}
	failed := 0
	for i := range targets {
		if targets[i].Skipped != "" {
			continue
		}
		if err := installSkill(targets[i].Path, exe, *force); err != nil {
			targets[i].Error = err.Error()
			failed++
		} else {
			targets[i].Binary = exe != ""
		}
	}
	if *format == "table" {
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "AGENT\tSKILL\tRESULT")
		for _, t := range targets {
			result := "installed"
			if t.Binary {
				result += " with binary"
			}
			switch {
			case t.Skipped != "":
				result = "skipped: " + t.Skipped
			case t.Error != "":
				result = "failed: " + t.Error
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", t.Agent, t.Path, result)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	} else if err := writeJSON(out, targets); err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d skill installs failed", failed, len(targets))
	}
	return nil
}

// skillTargets resolves --agent/--dir into skill folders (".../renfe"). Without
// --agent it picks the agents installed in the home folder.
func skillTargets(agents, dir string) ([]skillInstall, error) {
	if dir != "" {
		return []skillInstall{{Agent: "custom", Path: filepath.Join(dir, "renfe")}}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, a := range strings.Split(agents, ",") {
		if a = strings.TrimSpace(strings.ToLower(a)); a != "" {
			wanted[a] = true
		}
	}
	roots := agentRoots(home)
	known := map[string]bool{"all": true}
	for _, r := range roots {
		known[r.agent] = true
	}
	for a := range wanted {
		if !known[a] {
			return nil, fmt.Errorf("unknown agent %q (supported: claude, codex, openclaw, hermes, all)", a)
		}
	}
	var targets []skillInstall
	codexCopy := false
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "renfe")); err == nil {
		codexCopy = true
	}
	for _, r := range roots {
		if len(wanted) == 0 {
			if _, err := os.Stat(r.installed); err != nil {
				continue // agent not installed
			}
		} else if !wanted["all"] && !wanted[r.agent] {
			continue
		}
		t := skillInstall{Agent: r.agent, Path: filepath.Join(r.skills, "renfe")}
		if r.agent == "codex" {
			codexCopy = true
		}
		if r.sharesAgentsSkills && codexCopy {
			t.Skipped = r.agent + " already loads ~/.agents/skills/renfe; a second copy would duplicate it"
		}
		targets = append(targets, t)
	}
	if len(targets) == 0 {
		return nil, errors.New("no supported agent found in your home folder; pass --agent or --dir")
	}
	return targets, nil
}

// installSkill writes the embedded skill to dest, with exe copied to bin/renfe when set.
// It replaces only an earlier renfe skill, never another folder, and never writes
// through a symlink: with force, the link itself is replaced.
func installSkill(dest, exe string, force bool) error {
	if info, err := os.Lstat(dest); err == nil {
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			if !force {
				target, _ := os.Readlink(dest)
				return fmt.Errorf("%s is a symlink to %s; left unchanged (use --force to replace the link)", dest, target)
			}
		case !isRenfeSkill(dest):
			return fmt.Errorf("%s exists and is not the renfe skill; left unchanged", dest)
		}
	}
	root := filepath.Dir(dest)
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(root, ".renfe-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := os.Chmod(tmp, 0755); err != nil {
		return err
	}
	err = fs.WalkDir(skills.FS, "renfe", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(tmp, filepath.FromSlash(strings.TrimPrefix(name, "renfe")))
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := skills.FS.ReadFile(name)
		if err != nil {
			return err
		}
		mode := os.FileMode(0644)
		if path.Base(path.Dir(name)) == "scripts" {
			mode = 0755
		}
		return os.WriteFile(target, data, mode)
	})
	if err != nil {
		return err
	}
	if exe != "" {
		if err := copyFile(exe, filepath.Join(tmp, "bin", "renfe"), 0755); err != nil {
			return fmt.Errorf("cannot bundle the binary: %w", err)
		}
	}
	old := ""
	if _, err := os.Lstat(dest); err == nil {
		old = tmp + ".old"
		if err := os.Rename(dest, old); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		if old != "" {
			os.Rename(old, dest)
		}
		return err
	}
	if old != "" {
		return os.RemoveAll(old)
	}
	return nil
}

// isRenfeSkill reports whether dir holds a SKILL.md for this skill.
func isRenfeSkill(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	return err == nil && strings.Contains(string(data), "\nname: renfe\n")
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
