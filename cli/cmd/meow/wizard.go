package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"meow.blue/meow/cli/internal/config"
	"meow.blue/meow/client"
)

// cmdSetupWizard is "meow setup" without an agent: check the server, log in,
// connect every agent found on this computer, then send a test notification.
// Each agent still shows its changes and asks before writing.
func cmdSetupWizard(args []string) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	server := fs.String("server", "", "")
	yes := fs.Bool("yes", false, "")
	if _, err := parse(fs, args); err != nil {
		return usageErr(err.Error())
	}
	cfg, err := config.Load()
	if err != nil {
		return fail(err)
	}
	in := bufio.NewReader(os.Stdin)

	// 1. Server
	if *server != "" {
		cfg.Server = strings.TrimRight(strings.TrimSpace(*server), "/")
	}
	if cfg.Server == "" {
		if !stdinIsTerminal() {
			return usageErr("no server configured; pass --server https://your-meow-server")
		}
		fmt.Fprint(os.Stderr, "Meow server (connection page, or Settings → Server): ")
		line, _ := in.ReadString('\n')
		cfg.Server = strings.TrimRight(strings.TrimSpace(line), "/")
	}
	if err := checkServer(cfg.Server); err != nil {
		fmt.Fprintf(os.Stderr, "✗ %s\n", err)
		return exitError
	}
	fmt.Fprintf(os.Stderr, "✓ Server %s is reachable\n", cfg.Server)

	// 2. Device Key: keep a saved one that still works on this server.
	var who client.WhoAmI
	if cfg.Key != "" {
		who, err = checkKey(cfg.Server, cfg.Key)
	}
	if cfg.Key == "" || err != nil {
		if err != nil {
			fmt.Fprintf(os.Stderr, "  The saved Device Key doesn't work here (%s).\n", err)
		}
		if !stdinIsTerminal() {
			return usageErr(`no working Device Key; run "meow login" first`)
		}
		// Read from the prompt, never from flags, so the key stays out of shell history.
		fmt.Fprint(os.Stderr, "Device Key (Meow app → Connect your computer, or Settings → Device Key (tap to copy)): ")
		line, _ := in.ReadString('\n')
		key := strings.TrimSpace(line)
		if !strings.HasPrefix(key, "meow_") {
			fmt.Fprintln(os.Stderr, `✗ A Device Key starts with "meow_". Copy it again from the app.`)
			return exitError
		}
		if who, err = checkKey(cfg.Server, key); err != nil {
			fmt.Fprintf(os.Stderr, "✗ %s\n", err)
			return exitError
		}
		cfg.Key = key
	}
	if err := config.Save(cfg); err != nil {
		return fail(err)
	}
	fmt.Fprintf(os.Stderr, "✓ Logged in to device %q\n", who.Device.Name)
	if !who.HasPushRegistration() {
		fmt.Fprintln(os.Stderr, "! This iPhone hasn't registered for notifications yet. Open Meow and allow notifications for system alerts. Messages can still appear in the app inbox.")
	}

	// 3. Agents
	agents := detectAgents()
	var detected []string
	for _, agent := range agents {
		detected = append(detected, agent.name)
	}
	if len(detected) > 0 {
		fmt.Fprintln(os.Stderr, "Detected agents: "+strings.Join(detected, ", "))
	}
	for _, supported := range []agentInfo{{"claude", "Claude Code"}, {"codex", "Codex"}} {
		found := false
		for _, agent := range agents {
			if agent.id == supported.id {
				found = true
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "Not detected: %s. To connect manually: meow setup %s\n", supported.name, supported.id)
		}
	}
	if len(agents) == 0 {
		fmt.Fprintln(os.Stderr, "\nNo Claude Code or Codex setup found on this computer.")
		fmt.Fprintln(os.Stderr, `Install one, then run "meow setup claude" or "meow setup codex".`)
		sendSetupTest(cfg, "Meow CLI")
		return exitOK
	}
	var connected, failed []string
	for _, a := range agents {
		fmt.Fprintln(os.Stderr)
		sub := []string{a.id, "--no-test"}
		if *yes {
			sub = append(sub, "--yes")
		}
		if code := cmdSetup(sub); code != exitOK {
			failed = append(failed, a.name) // keep going with the other agents
			continue
		}
		// Declining the changes also exits 0; only a recorded install counts.
		if c, err := config.LoadFile(); err == nil && c.Installed[a.id] != "" {
			connected = append(connected, a.name)
		}
	}

	// 4. Verify CLI delivery separately from agent hook authorization.
	fmt.Fprintln(os.Stderr)
	if len(connected) > 0 {
		sendSetupTest(cfg, strings.Join(connected, " and "))
		fmt.Fprintln(os.Stderr, "CLI connection configured. The setup test does not verify agent hooks; finish a real task and confirm its result on your iPhone.")
		for _, agent := range connected {
			if agent == "Codex" {
				status, _ := codexHookStatus()
				fmt.Fprintln(os.Stderr, "Codex: "+status)
			}
		}
	}
	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "✗ Not connected: %s. Fix the problem above and run \"meow setup\" again.\n", strings.Join(failed, ", "))
		return exitError
	}
	if len(connected) == 0 {
		fmt.Fprintln(os.Stderr, "Nothing was connected.")
	}
	return exitOK
}

// cmdDoctor runs the wizard's checks without changing anything.
func cmdDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	if _, err := parse(fs, args); err != nil {
		return usageErr(err.Error())
	}
	cfg, err := config.Load()
	if err != nil {
		return fail(err)
	}
	ok := true
	report := func(good bool, msg string) {
		mark := "✓"
		if !good {
			mark, ok = "✗", false
		}
		fmt.Fprintf(os.Stderr, "%s %s\n", mark, msg)
	}

	if cfg.Server == "" {
		report(false, `No server configured. Run "meow setup".`)
		return exitError
	}
	if err := checkServer(cfg.Server); err != nil {
		report(false, err.Error())
		return exitError
	}
	report(true, "Server "+cfg.Server+" is reachable")

	if cfg.Key == "" {
		report(false, `No Device Key saved. Run "meow setup" or "meow login".`)
	} else if who, err := checkKey(cfg.Server, cfg.Key); err != nil {
		report(false, err.Error())
	} else {
		report(true, fmt.Sprintf("Device Key works for device %q", who.Device.Name))
		report(who.HasPushRegistration(), map[bool]string{
			true:  "The iPhone is registered for notifications",
			false: "The iPhone hasn't registered for notifications. Open Meow and allow notifications.",
		}[who.HasPushRegistration()])
	}

	agents := detectAgents()
	if len(agents) == 0 {
		fmt.Fprintln(os.Stderr, "- No Claude Code or Codex setup found")
	}
	for _, a := range agents {
		installed := cfg.Installed[a.id] != ""
		msg := a.name + " is connected"
		if !installed {
			msg = a.name + ` is not connected. Run "meow setup ` + a.id + `".`
		}
		if a.id == "codex" && installed {
			status, observed := codexHookStatus()
			fmt.Fprintln(os.Stderr, "- "+status)
			if codexLegacyConfigured() {
				report(false, "Old Meow notify configuration found; run meow setup codex to clean it and use lifecycle hooks")
			} else {
				report(observed, "Codex lifecycle notifications: "+status)
			}
		} else {
			report(installed, msg)
		}
	}
	if !ok {
		return exitError
	}
	return exitOK
}

type agentInfo struct{ id, name string }

// detectAgents finds agents by their config directory or their command.
func detectAgents() []agentInfo {
	var found []agentInfo
	for _, a := range []struct {
		agentInfo
		dir     func() (string, error)
		command string
	}{
		{agentInfo{"claude", "Claude Code"}, claudeDir, "claude"},
		{agentInfo{"codex", "Codex"}, codexDir, "codex"},
	} {
		if d, err := a.dir(); err == nil {
			if fi, err := os.Stat(d); err == nil && fi.IsDir() {
				found = append(found, a.agentInfo)
				continue
			}
		}
		if _, err := exec.LookPath(a.command); err == nil {
			found = append(found, a.agentInfo)
		}
	}
	return found
}

// checkServer asks /healthz and turns failures into a next step.
func checkServer(server string) error {
	u, err := url.Parse(server)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("%q is not a server address; it should look like https://meow.example.com", server)
	}
	c := http.Client{Timeout: 10 * time.Second}
	resp, err := c.Get(server + "/healthz")
	if err != nil {
		return diagnoseConnError(u.Hostname(), err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s, so it doesn't look like a Meow server. Check the address", server, resp.Status)
	}
	return nil
}

func diagnoseConnError(host string, err error) error {
	var (
		unknownCA  x509.UnknownAuthorityError
		unknownCAP *x509.UnknownAuthorityError
		verifyErr  *tls.CertificateVerificationError
		hostErr    x509.HostnameError
		hostErrP   *x509.HostnameError
		invalid    x509.CertificateInvalidError
		invalidP   *x509.CertificateInvalidError
		dnsErr     *net.DNSError
	)
	switch {
	case errors.As(err, &unknownCA), errors.As(err, &unknownCAP):
		return fmt.Errorf("the TLS certificate of %s isn't trusted (self-signed or private CA). Use a certificate from a public CA, such as the one Caddy gets automatically", host)
	case errors.As(err, &hostErr), errors.As(err, &hostErrP):
		return fmt.Errorf("the TLS certificate isn't for %s. Check that the address matches the server's domain", host)
	case errors.As(err, &invalid), errors.As(err, &invalidP):
		return fmt.Errorf("the TLS certificate of %s is invalid or expired. Check the server's certificate renewal", host)
	case errors.As(err, &verifyErr):
		return fmt.Errorf("the TLS certificate of %s couldn't be verified (%v). Check the server's certificate", host, verifyErr.Err)
	case errors.As(err, &dnsErr):
		return fmt.Errorf("can't find %s. Check the spelling and that its DNS record exists", host)
	case errors.Is(err, syscall.ECONNREFUSED):
		return fmt.Errorf("%s refused the connection. Check that the server is running and the port is open", host)
	}
	hint := "Check the address, the firewall, and that the server is running"
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback()) {
		hint = "This is a local address: make sure this computer is on the same network or VPN as the server"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%s didn't answer in time. %s", host, hint)
	}
	return fmt.Errorf("can't reach %s (%v). %s", host, err, hint)
}

// checkKey verifies a Device Key and says what to do when it fails.
func checkKey(server, key string) (client.WhoAmI, error) {
	if err := rejectMaskedDeviceKey(key); err != nil {
		return client.WhoAmI{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	who, err := client.New(server, key).WhoAmI(ctx)
	var ae *client.Error
	if errors.As(err, &ae) && (ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden) {
		return client.WhoAmI{}, errors.New("the Device Key isn't valid on this server (wrong server, or the key was rotated). Copy it again from Meow → Connect your computer, or Settings → Device Key (tap to copy)")
	}
	if err != nil {
		return client.WhoAmI{}, err
	}
	return *who, nil
}

// stdinIsTerminal reports whether a person can answer prompts. A mode check
// isn't enough: /dev/null is also a character device.
func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// A display abbreviation is never a credential. Explain the copy mistake before
// making an HTTP request or persisting it as a saved key.
func rejectMaskedDeviceKey(key string) error {
	if strings.Contains(key, "…") || strings.Contains(key, "...") {
		return errors.New(`this Device Key is masked or incomplete. Tap Device Key in Meow Settings, or use "Copy Device Key" during setup, instead of copying the shortened text`)
	}
	return nil
}
