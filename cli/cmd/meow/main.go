// Command meow sends agent notifications to your iPhone.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"meow.blue/meow/cli/internal/config"
	"meow.blue/meow/client"
)

var version = "dev"

// Exit codes for "meow ask", so scripts can branch without parsing output.
const (
	exitOK        = 0
	exitError     = 1
	exitUsage     = 2
	exitRejected  = 3
	exitExpired   = 4
	exitCancelled = 5
	exitInterrupt = 130
)

const usage = `meow — push notifications for agents

Usage:
  meow <message>                       send an info notification
  meow send <message> [flags]          send a notification (reads stdin if no message)
  meow login [--key KEY] [--server URL]  save your Device Key
  meow whoami                          show the configured key and device
  meow setup                           guided setup: server, Device Key, agents, test
  meow setup claude|codex              notify you when an agent finishes or needs you
  meow doctor                          check the server, Device Key, iPhone and agents
  meow version

Send flags:
  --title T        title (the message becomes the body)
  --priority P     low | normal | high (default normal)
  --alert          mark as an alert instead of info
  --source S       sender label, e.g. "Claude Code" (default $MEOW_SOURCE)

Meow V1 is notification-only. Respond to questions in the agent on your computer.

Configuration: MEOW_KEY and MEOW_SERVER override ~/.config/meow/config.json.
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "send":
		return cmdSend(rest)
	case "ask", "wait":
		return usageErr("Meow V1 is notification-only. Use meow send --alert to notify; respond in the agent on your computer.")
	case "login":
		return cmdLogin(rest)
	case "whoami":
		return cmdWhoAmI(rest)
	case "setup":
		return cmdSetup(rest)
	case "doctor":
		return cmdDoctor(rest)
	case "hook":
		return cmdHook(rest)
	case "dev":
		return usageErr("the phone emulator commands were removed; use the Meow iOS app")
	case "version", "--version":
		fmt.Println("meow", version)
		return exitOK
	case "help", "-h", "--help":
		fmt.Print(usage)
		return exitOK
	}
	return cmdSend(args) // meow "Build completed", meow --priority high "text"
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "meow:", err)
	return exitError
}

func usageErr(msg string) int {
	fmt.Fprintln(os.Stderr, "meow:", msg)
	fmt.Fprintln(os.Stderr, `run "meow help" for usage`)
	return exitUsage
}

// parse parses flags that may appear before or after positional arguments,
// so that `meow ask "Deploy?" --choices approve,reject` works.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			return nil, fmt.Errorf("unknown flag %s", a)
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 >= len(args) {
			return nil, fmt.Errorf("flag %s needs a value", a)
		}
		i++
		flags = append(flags, args[i])
	}
	if err := fs.Parse(flags); err != nil {
		return nil, err
	}
	return pos, nil
}

func newClient() (*client.Client, error) {
	c, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if c.Key == "" {
		return nil, errors.New(`no Device Key configured; copy it from the Meow app and run "meow login"`)
	}
	if strings.TrimSpace(c.Server) == "" {
		return nil, client.ErrNoServer
	}
	return client.New(c.Server, c.Key), nil
}

func newIdemKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "cli-" + hex.EncodeToString(b)
}

// retry runs fn up to three times on transient errors. fn must be idempotent
// (the caller passes the same Idempotency-Key each time).
func retry[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var v T
	var err error
	for attempt := range 3 {
		if v, err = fn(); err == nil {
			return v, nil
		}
		var ae *client.Error
		if errors.As(err, &ae) && !ae.Temporary() {
			return v, err
		}
		if ctx.Err() != nil {
			return v, ctx.Err()
		}
		select {
		case <-ctx.Done():
			return v, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
	return v, err
}

func stdinText() (string, error) {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice != 0 {
		return "", nil // interactive terminal: don't block waiting for input
	}
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 16<<10))
	return strings.TrimSpace(string(b)), err
}

func cmdSend(args []string) int {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	title := fs.String("title", "", "")
	priority := fs.String("priority", "normal", "")
	alert := fs.Bool("alert", false, "")
	source := fs.String("source", os.Getenv("MEOW_SOURCE"), "")
	quiet := fs.Bool("quiet", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return usageErr(err.Error())
	}
	msg := strings.Join(pos, " ")
	if msg == "" {
		if msg, err = stdinText(); err != nil {
			return fail(err)
		}
	}
	req := client.NotifyRequest{Title: *title, Body: msg, Source: *source, Priority: *priority}
	if req.Title == "" {
		req.Title, req.Body, _ = strings.Cut(msg, "\n")
		req.Body = strings.TrimSpace(req.Body)
	}
	if req.Title == "" {
		return usageErr("nothing to send: give a message or pipe one on stdin")
	}
	if *alert {
		req.Type = "alert"
	}
	c, err := newClient()
	if err != nil {
		return fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	idem := newIdemKey()
	n, err := retry(ctx, func() (*client.Notification, error) { return c.Notify(ctx, req, idem) })
	if err != nil {
		return fail(err)
	}
	if !*quiet {
		fmt.Fprintln(os.Stderr, "sent", n.ID)
	}
	return exitOK
}

func cmdAsk(args []string) int {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	choices := fs.String("choices", "", "")
	timeout := fs.Int("timeout", 900, "")
	body := fs.String("body", "", "")
	source := fs.String("source", os.Getenv("MEOW_SOURCE"), "")
	priority := fs.String("priority", "high", "")
	fs.Bool("wait", true, "") // waiting is the default; accepted for compatibility
	noWait := fs.Bool("no-wait", false, "")
	asJSON := fs.Bool("json", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return usageErr(err.Error())
	}
	text := strings.Join(pos, " ")
	if text == "" {
		return usageErr("ask needs a question")
	}
	req := client.AskRequest{Question: text, Body: *body, Source: *source, Priority: *priority, Timeout: *timeout}
	if *choices != "" {
		for _, ch := range strings.Split(*choices, ",") {
			req.Choices = append(req.Choices, strings.TrimSpace(ch))
		}
	}
	c, err := newClient()
	if err != nil {
		return fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	idem := newIdemKey()
	q, err := retry(ctx, func() (*client.Question, error) { return c.Ask(ctx, req, idem) })
	if err != nil {
		return fail(err)
	}
	if *noWait {
		if *asJSON {
			return printJSON(q)
		}
		fmt.Println(q.ID)
		return exitOK
	}
	fmt.Fprintf(os.Stderr, "waiting for an answer on your phone (%s, expires %s)…\n", q.ID, q.ExpiresAt.Local().Format("15:04:05"))
	return waitAndPrint(ctx, c, q.ID, *asJSON)
}

func cmdWait(args []string) int {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return usageErr(err.Error())
	}
	if len(pos) != 1 {
		return usageErr("usage: meow wait <question-id>")
	}
	c, err := newClient()
	if err != nil {
		return fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return waitAndPrint(ctx, c, pos[0], *asJSON)
}

func waitAndPrint(ctx context.Context, c *client.Client, id string, asJSON bool) int {
	q, err := c.Wait(ctx, id, func(err error) {
		fmt.Fprintln(os.Stderr, "meow: connection problem, retrying:", err)
	})
	if errors.Is(err, context.Canceled) {
		// Interrupted: cancel the question so it can't be answered after nobody is waiting.
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, cerr := c.Cancel(cctx, id); cerr == nil {
			fmt.Fprintln(os.Stderr, "\nmeow: interrupted; question cancelled")
		} else {
			fmt.Fprintln(os.Stderr, "\nmeow: interrupted; could not cancel question:", cerr)
		}
		return exitInterrupt
	}
	if err != nil {
		return fail(err)
	}
	if asJSON {
		printJSON(q)
	} else if q.Answer != nil {
		fmt.Println(*q.Answer)
	}
	switch q.Status {
	case "answered":
		return exitOK
	case "rejected":
		return exitRejected
	case "expired":
		fmt.Fprintln(os.Stderr, "meow: question expired without an answer")
		return exitExpired
	default:
		fmt.Fprintln(os.Stderr, "meow: question", q.Status)
		return exitCancelled
	}
}

func printJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fail(err)
	}
	return exitOK
}

func cmdLogin(args []string) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	key := fs.String("key", "", "")
	server := fs.String("server", "", "")
	if _, err := parse(fs, args); err != nil {
		return usageErr(err.Error())
	}
	cfg, err := config.Load()
	if err != nil {
		return fail(err)
	}
	if *server != "" {
		cfg.Server = strings.TrimRight(*server, "/")
	}
	in := bufio.NewReader(os.Stdin)
	if cfg.Server == "" {
		if !stdinIsTerminal() {
			return usageErr("no server configured; pass --server or set MEOW_SERVER")
		}
		fmt.Fprint(os.Stderr, "Meow server (shown in the app under Settings → Server): ")
		line, _ := in.ReadString('\n')
		cfg.Server = strings.TrimRight(strings.TrimSpace(line), "/")
		if cfg.Server == "" {
			return usageErr(client.ErrNoServer.Error())
		}
	}
	k := strings.TrimSpace(*key)
	if k == "" {
		// Prompting keeps the key out of shell history.
		fmt.Fprint(os.Stderr, "Device Key (Meow app → Settings → Device Key): ")
		line, err := in.ReadString('\n')
		if err != nil && line == "" {
			return fail(err)
		}
		k = strings.TrimSpace(line)
	}
	if !strings.HasPrefix(k, "meow_") {
		return usageErr(`a Device Key starts with "meow_"`)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	who, err := client.New(cfg.Server, k).WhoAmI(ctx)
	if err != nil {
		return fail(fmt.Errorf("key check failed: %w", err))
	}
	cfg.Key = k
	if err := config.Save(cfg); err != nil {
		return fail(err)
	}
	p, _ := config.Path()
	fmt.Fprintf(os.Stderr, "logged in as key %s (%s) for device %s; saved to %s\n", who.Key.Hint, strings.Join(who.Key.Scopes, ","), who.Device.ID, p)
	return exitOK
}

func cmdWhoAmI(args []string) int {
	if len(args) > 0 {
		return usageErr("whoami takes no arguments")
	}
	cfg, err := config.Load()
	if err != nil {
		return fail(err)
	}
	c, err := newClient()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	who, err := c.WhoAmI(ctx)
	if err != nil {
		return fail(err)
	}
	server := cfg.Server
	fmt.Printf("server  %s\nkey     %s (%s)\ndevice  %s %s\n", server, who.Key.Hint, strings.Join(who.Key.Scopes, ","), who.Device.ID, who.Device.Name)
	return exitOK
}
