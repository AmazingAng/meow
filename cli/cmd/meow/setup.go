package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"meow.blue/meow/cli/internal/config"
	"meow.blue/meow/cli/internal/setup"
	"meow.blue/meow/client"
)

const setupUsage = `meow setup — connect a coding agent to Meow

Usage:
  meow setup claude [flags]    Claude Code: Stop and Notification hooks, plus the meow skill
  meow setup codex [flags]     Codex: notify command, plus the meow skill

You get a notification when the agent finishes a task or needs you, and the
skill lets the agent ask you with "meow ask". meow setup shows the changes
and asks before writing; each changed file is backed up first.

Flags:
  --uninstall    remove what meow setup added
  --no-summary   leave the agent's last message out of notifications
  --no-skill     don't install the meow skill
  --no-test      don't send a test notification
  --force        (codex) replace an existing notify command
  --yes          apply without asking
`

type setupOptions struct {
	install, summary, skill, force bool
	exe                            string // absolute path of this meow binary
}

// fileChange is one file meow setup writes; nil data removes the file.
type fileChange struct {
	path   string
	data   []byte
	exists bool
	lines  []string // what changes, for the preview
}

func cmdSetup(args []string) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprint(os.Stderr, setupUsage)
		return exitUsage
	}
	agent := args[0]
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	uninstall := fs.Bool("uninstall", false, "")
	noSummary := fs.Bool("no-summary", false, "")
	noSkill := fs.Bool("no-skill", false, "")
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
	o := setupOptions{install: !*uninstall, summary: !*noSummary, skill: !*noSkill, force: *force}

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
	}

	changes, notes, err := plan(o)
	if err != nil {
		return fail(err)
	}
	if len(changes) == 0 {
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
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, n)
	}
	if o.install && !*noTest {
		sendSetupTest(cfg, name)
	}
	return exitOK
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
	data, err := setup.ClaudeSettings(old, stopCmd, notifyCmd, o.install)
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
	if c, ok, err := skillChange(filepath.Join(dir, "skills", "meow", "SKILL.md"), "claude", o); err != nil {
		return nil, nil, err
	} else if ok {
		changes = append(changes, c)
	}

	var notes []string
	if o.install {
		notes = append(notes, "Claude Code picks up the hooks automatically, including sessions that are already running.")
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
	existing, ours := setup.CodexNotify(old)
	data, err := setup.CodexConfig(old, argv, o.install, o.force)
	var conflict *setup.NotifyConflictError
	if errors.As(err, &conflict) {
		return nil, nil, fmt.Errorf("%s already has\n    %s\nCodex runs only one notify command. Re-run with --force to replace it (the file is backed up first)",
			tilde(path), conflict.Existing)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", tilde(path), err)
	}

	var changes []fileChange
	if !bytes.Equal(old, data) {
		c := fileChange{path: path, data: data, exists: exists, lines: []string{"remove the meow notify command"}}
		if o.install {
			c.lines = []string{"notify = " + setup.TOMLArray(argv)}
			if existing != "" && !ours {
				c.lines = append(c.lines, "replaces: "+existing)
			}
		}
		changes = append(changes, c)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, err
	}
	if c, ok, err := skillChange(filepath.Join(home, ".agents", "skills", "meow", "SKILL.md"), "codex", o); err != nil {
		return nil, nil, err
	} else if ok {
		changes = append(changes, c)
	}

	var notes []string
	if o.install {
		notes = append(notes,
			"Start a new Codex session to pick up the change.",
			"If Codex's own terminal notifications are on, you may get both; set notifications = false under [tui] to keep only Meow.")
	}
	return changes, notes, nil
}

func skillChange(path, agent string, o setupOptions) (fileChange, bool, error) {
	old, exists, err := setup.ReadOptional(path)
	if err != nil {
		return fileChange{}, false, err
	}
	if !o.install {
		if exists && setup.IsMeowSkill(old) {
			return fileChange{path: path, exists: true, lines: []string{"remove the meow skill"}}, true, nil
		}
		return fileChange{}, false, nil
	}
	if !o.skill {
		return fileChange{}, false, nil
	}
	data := setup.Skill(agent)
	if bytes.Equal(old, data) {
		return fileChange{}, false, nil
	}
	c := fileChange{path: path, data: data, exists: exists, lines: []string{`skill that lets the agent ask you with "meow ask"`}}
	if exists && !setup.IsMeowSkill(old) {
		c.lines = append(c.lines, "replaces an existing skill named meow (it is backed up first)")
	}
	return c, true, nil
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
	if strings.Contains(exe, "go-build") {
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
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
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
	fmt.Fprintln(os.Stderr, "Sent a test notification to your iPhone.")
}

func tilde(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" && strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}
