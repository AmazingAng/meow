package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"meow.blue/meow/cli/internal/config"
	"meow.blue/meow/cli/internal/setup"
	"meow.blue/meow/client"
)

const setupUsage = `meow setup — connect a coding agent to Meow

Usage:
  meow setup [--server URL]    guided: check the server, log in, connect every agent found
  meow setup claude [flags]    Claude Code: Stop and Notification hooks
  meow setup codex [flags]     Codex: notify command

You get a notification when the agent finishes a task or needs you. Reply in
the agent on your computer. meow setup shows the changes and asks before
writing; each changed file is backed up first.

The meow skill, which teaches agents to set up and use Meow, is installed
separately: npx skills add AmazingAng/meow -g

Flags:
  --uninstall    remove what meow setup added
  --no-summary   leave the agent's last message out of notifications
  --no-test      don't send a test notification
  --force        (codex) replace an existing notify command
  --yes          apply without asking
`

type setupOptions struct {
	install, summary, force bool
	exe                     string   // absolute path of this meow binary (install only)
	known                   []string // meow binaries setup may have installed before
}

// executables lists every meow binary whose hooks belong to meow: this one
// and any recorded by an earlier install.
func (o setupOptions) executables() []string {
	return append([]string{o.exe}, o.known...)
}

// fileChange is one file meow setup writes; nil data removes the file.
type fileChange struct {
	path   string
	data   []byte
	exists bool
	lines  []string // what changes, for the preview
}

func cmdSetup(args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprint(os.Stderr, setupUsage)
		return exitUsage
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return cmdSetupWizard(args)
	}
	agent := args[0]
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	uninstall := fs.Bool("uninstall", false, "")
	noSummary := fs.Bool("no-summary", false, "")
	fs.Bool("no-skill", false, "") // accepted for compatibility; setup no longer installs the skill
	noTest := fs.Bool("no-test", false, "")
	force := fs.Bool("force", false, "")
	yes := fs.Bool("yes", false, "")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return usageErr(err.Error())
	}
	if len(pos) > 0 {
		return usageErr("unexpected argument " + pos[0])
	}

	var name string
	var plan func(setupOptions) ([]fileChange, []string, error)
	switch agent {
	case "claude":
		name, plan = "Claude Code", planClaude
	case "codex":
		name, plan = "Codex", planCodex
	default:
		return usageErr(`meow setup supports "claude" and "codex"`)
	}
	o := setupOptions{install: !*uninstall, summary: !*noSummary, force: *force}

	cfg, err := config.Load()
	if err != nil {
		return fail(err)
	}
	if o.install {
		if cfg.Key == "" {
			return fail(errors.New(`no Device Key configured; run "meow login" first`))
		}
		if o.exe, err = meowExecutable(); err != nil {
			return fail(err)
		}
	} else {
		// Also recognize this binary if it was installed under a custom name.
		o.exe, _ = meowExecutable()
	}

	// Recognise hooks written by an earlier install, wherever that binary was.
	o.known = []string{cfg.Installed[agent]}
	if !o.install {
		if exe, err := meowExecutable(); err == nil {
			o.known = append(o.known, exe)
		}
	}

	changes, notes, err := plan(o)
	if err != nil {
		return fail(err)
	}
	if len(changes) == 0 {
		recordInstall(agent, o) // covers installs made before setup recorded them
		if o.install {
			fmt.Fprintf(os.Stderr, "%s is already connected to Meow.\n", name)
		} else {
			fmt.Fprintf(os.Stderr, "Nothing to remove for %s.\n", name)
		}
		return exitOK
	}

	verb := "connect " + name + " to Meow"
	if !o.install {
		verb = "disconnect " + name + " from Meow"
	}
	fmt.Fprintf(os.Stderr, "meow setup will %s:\n", verb)
	for _, c := range changes {
		action := "update"
		switch {
		case c.data == nil:
			action = "remove"
		case !c.exists:
			action = "create"
		}
		fmt.Fprintf(os.Stderr, "\n  %s %s\n", action, tilde(c.path))
		for _, l := range c.lines {
			fmt.Fprintf(os.Stderr, "      %s\n", l)
		}
	}
	fmt.Fprintln(os.Stderr)
	if !*yes {
		ok, err := confirm("Apply these changes? [y/N] ")
		if err != nil {
			return fail(err)
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "Cancelled; nothing was changed.")
			return exitOK
		}
	}

	now := time.Now()
	for _, c := range changes {
		if c.data == nil {
			if err := setup.RemoveFile(c.path); err != nil {
				return fail(fmt.Errorf("remove %s: %w", tilde(c.path), err))
			}
			fmt.Fprintf(os.Stderr, "Removed %s\n", tilde(c.path))
			continue
		}
		backup, err := setup.WriteFile(c.path, c.data, now)
		if err != nil {
			return fail(fmt.Errorf("write %s: %w", tilde(c.path), err))
		}
		if backup != "" {
			fmt.Fprintf(os.Stderr, "Updated %s (backup: %s)\n", tilde(c.path), filepath.Base(backup))
		} else {
			fmt.Fprintf(os.Stderr, "Created %s\n", tilde(c.path))
		}
	}
	recordInstall(agent, o)
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, n)
	}
	if o.install && !*noTest {
		sendSetupTest(cfg, name)
	}
	return exitOK
}

// recordInstall remembers which executable the agent's config runs, or
// forgets it after an uninstall. Only the config file is updated, so MEOW_KEY
// and MEOW_SERVER overrides are never saved. Failure only affects a later
// uninstall of a binary not named meow, so it is a warning.
func recordInstall(agent string, o setupOptions) {
	cfg, err := config.LoadFile()
	if err == nil {
		if o.install {
			if cfg.Installed == nil {
				cfg.Installed = map[string]string{}
			}
			if cfg.Installed[agent] == o.exe {
				return
			}
			cfg.Installed[agent] = o.exe
		} else {
			if _, ok := cfg.Installed[agent]; !ok {
				return
			}
			delete(cfg.Installed, agent)
		}
		err = config.Save(cfg)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "meow: couldn't record the installed executable: %v\n", err)
	}
}

func claudeDir() (string, error) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".claude"), err
}

func codexDir() (string, error) {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".codex"), err
}

func planClaude(o setupOptions) ([]fileChange, []string, error) {
	dir, err := claudeDir()
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, "settings.json")
	old, exists, err := setup.ReadOptional(path)
	if err != nil {
		return nil, nil, err
	}
	var stopCmd, notifyCmd string
	if o.install {
		base := setup.ShellQuote(o.exe) + " hook "
		stopCmd, notifyCmd = base+"claude-stop", base+"claude-notify"
		if !o.summary {
			stopCmd += " --no-summary"
			notifyCmd += " --no-summary"
		}
	}
	data, err := setup.ClaudeSettings(old, stopCmd, notifyCmd, o.install, o.executables()...)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", tilde(path), err)
	}

	var changes []fileChange
	if !setup.EquivalentJSON(old, data) {
		c := fileChange{path: path, data: data, exists: exists, lines: []string{"remove the meow hooks"}}
		if o.install {
			c.lines = []string{
				"Stop hook (task complete): " + stopCmd,
				"Notification hook (" + setup.ClaudeNotifyMatcher + "): " + notifyCmd,
			}
		}
		changes = append(changes, c)
	}
	skill := filepath.Join(dir, "skills", "meow", "SKILL.md")
	if c, ok, err := legacySkillChange(skill, o); err != nil {
		return nil, nil, err
	} else if ok {
		changes = append(changes, c)
	}

	var notes []string
	if o.install {
		notes = append(notes, "Claude Code picks up the hooks automatically, including sessions that are already running.")
		notes = append(notes, skillHint(skill, "claude-code")...)
	}
	return changes, notes, nil
}

func planCodex(o setupOptions) ([]fileChange, []string, error) {
	dir, err := codexDir()
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, "config.toml")
	old, exists, err := setup.ReadOptional(path)
	if err != nil {
		return nil, nil, err
	}
	var argv []string
	if o.install {
		argv = []string{o.exe, "hook", "codex"}
		if !o.summary {
			argv = append(argv, "--no-summary")
		}
	}
	existing, ours := setup.CodexNotify(old, o.executables()...)
	original, err := setup.CodexOriginalNotify(old, o.executables()...)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", tilde(path), err)
	}
	if o.install && !o.force && len(original) > 0 {
		encoded, _ := json.Marshal(original)
		argv = append(argv, setup.ForwardNotifyPrefix+string(encoded))
	}
	data, err := setup.CodexConfig(old, argv, o.install, o.force || (o.install && existing != ""), o.executables()...)
	if !o.install && ours && len(original) > 0 {
		data, err = setup.CodexConfig(old, original, true, true, o.executables()...)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", tilde(path), err)
	}

	var changes []fileChange
	if !bytes.Equal(old, data) {
		c := fileChange{path: path, data: data, exists: exists, lines: []string{"remove the meow notify command"}}
		if !o.install && ours && len(original) > 0 {
			c.lines = []string{"restore original notify = " + setup.TOMLArray(original)}
		}
		if o.install {
			c.lines = []string{"notify = " + setup.TOMLArray(argv)}
			if existing != "" && !ours && o.force {
				c.lines = append(c.lines, "replaces: "+existing)
			}
			if len(original) > 0 && !o.force {
				c.lines = append(c.lines, "keeps the existing notification command running alongside Meow")
			}
		}
		changes = append(changes, c)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, err
	}
	skill := filepath.Join(home, ".agents", "skills", "meow", "SKILL.md")
	if c, ok, err := legacySkillChange(skill, o); err != nil {
		return nil, nil, err
	} else if ok {
		changes = append(changes, c)
	}

	var notes []string
	if o.install {
		notes = append(notes,
			"Start a new Codex session to pick up the change.",
			"If Codex's own terminal notifications are on, you may get both; set notifications = false under [tui] to keep only Meow.")
		notes = append(notes, skillHint(skill, "codex")...)
	}
	return changes, notes, nil
}

// legacySkillChange removes a SKILL.md written by an earlier meow setup. Its
// commands are outdated, and the skill is now installed with skills.sh, so
// setup never writes a skill and leaves every other SKILL.md alone, including
// the one skills.sh installs at the same path.
func legacySkillChange(path string, o setupOptions) (fileChange, bool, error) {
	old, exists, err := setup.ReadOptional(path)
	if err != nil || !exists || !setup.IsMeowSkill(old) {
		return fileChange{}, false, err
	}
	line := "remove the meow skill"
	if o.install {
		line = "remove the outdated meow skill written by an earlier meow setup"
	}
	return fileChange{path: path, exists: true, lines: []string{line}}, true, nil
}

// skillHint suggests installing the skill when the agent doesn't have it.
// A legacy copy counts as missing because this install removes it.
func skillHint(path, agent string) []string {
	if old, exists, err := setup.ReadOptional(path); err == nil && exists && !setup.IsMeowSkill(old) {
		return nil
	}
	return []string{"Optional: to teach the agent to set up and use Meow, run: npx skills add AmazingAng/meow -g -a " + agent}
}

// meowExecutable returns the path the agents should run. A PATH entry such
// as a Homebrew symlink is preferred over the resolved file so upgrades keep
// working. MEOW_EXECUTABLE overrides it.
func meowExecutable() (string, error) {
	if p := os.Getenv("MEOW_EXECUTABLE"); p != "" {
		return filepath.Abs(p)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if isGoRunExecutable(exe) {
		return "", errors.New(`meow is running from a temporary "go run" build; install it (go install ./cli/cmd/meow) and run "meow setup" from the installed binary`)
	}
	if p, err := exec.LookPath("meow"); err == nil {
		if abs, err := filepath.Abs(p); err == nil && sameFile(abs, exe) {
			return abs, nil
		}
	}
	return exe, nil
}

func sameFile(a, b string) bool {
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(fa, fb)
}

func confirm(prompt string) (bool, error) {
	if !stdinIsTerminal() {
		return false, errors.New("not running in a terminal; re-run with --yes to apply these changes")
	}
	fmt.Fprint(os.Stderr, prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes", nil
}

func sendSetupTest(cfg config.Config, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := client.New(cfg.Server, cfg.Key).Notify(ctx, client.NotifyRequest{
		Title:  "Meow is connected to " + name,
		Body:   "You'll get a notification when a task finishes or needs you.",
		Source: name,
	}, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "meow: test notification failed: %v (setup itself is done)\n", err)
		return
	}
	// The server accepting it doesn't prove the phone showed it, and a Device
	// Key can't read delivery status, so ask the user to look.
	fmt.Fprintln(os.Stderr, `Meow accepted a test notification. Check that it appears on your iPhone; if it doesn't, run "meow doctor".`)
}

func tilde(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" && strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}

var goRunExecutable = regexp.MustCompile(`(?:^|/)go-build[0-9]+/b[0-9]+/exe/[^/]+$`)

func isGoRunExecutable(path string) bool { return goRunExecutable.MatchString(filepath.ToSlash(path)) }
