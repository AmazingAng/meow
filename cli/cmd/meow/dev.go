package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"meow.blue/meow/cli/internal/config"
	"meow.blue/meow/client"
)

const devUsage = `meow dev — stand in for the iOS app during local development

  meow dev register [--server URL] [--name NAME]
      register a device, save its Device Key as the CLI login and print the
      device token (mdt_…) used to act as the phone
  meow dev inbox
      list the device's notifications
  meow dev reply <question-id> <answer>
      answer a question as the phone would

The dev commands read the device token from $MEOW_DEVICE_TOKEN.
`

func cmdDev(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, devUsage)
		return exitUsage
	}
	switch args[0] {
	case "register":
		return devRegister(args[1:])
	case "inbox":
		return devInbox(args[1:])
	case "reply":
		return devReply(args[1:])
	}
	fmt.Fprint(os.Stderr, devUsage)
	return exitUsage
}

func deviceClient() (*client.Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	tok := os.Getenv("MEOW_DEVICE_TOKEN")
	if !strings.HasPrefix(tok, "mdt_") {
		return nil, fmt.Errorf(`set MEOW_DEVICE_TOKEN to the device token printed by "meow dev register"`)
	}
	return client.New(cfg.Server, tok), nil
}

func devRegister(args []string) int {
	fs := flag.NewFlagSet("dev register", flag.ContinueOnError)
	server := fs.String("server", "", "")
	name := fs.String("name", "dev", "")
	if _, err := parse(fs, args); err != nil {
		return usageErr(err.Error())
	}
	cfg, err := config.LoadFile()
	if err != nil {
		return fail(err)
	}
	if *server != "" {
		cfg.Server = strings.TrimRight(*server, "/")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	reg, err := client.New(cfg.Server, "").Register(ctx, *name)
	if err != nil {
		return fail(err)
	}
	cfg.Key = reg.Key.Secret
	if err := config.Save(cfg); err != nil {
		return fail(err)
	}
	fmt.Fprintf(os.Stderr, "registered device %s; Device Key %s saved as the CLI login\n", reg.Device.ID, reg.Key.Hint)
	fmt.Fprintln(os.Stderr, "to act as the phone, run:")
	fmt.Printf("export MEOW_DEVICE_TOKEN=%s\n", reg.DeviceToken)
	return exitOK
}

func devInbox(args []string) int {
	if len(args) > 0 {
		return usageErr("inbox takes no arguments")
	}
	c, err := deviceClient()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ns, _, err := c.Inbox(ctx, "", 20)
	if err != nil {
		return fail(err)
	}
	for _, n := range ns {
		extra := n.PushStatus
		if q := n.Question; q != nil {
			extra = q.ID + " " + q.Status
			if q.Answer != nil {
				extra += ": " + *q.Answer
			}
		}
		src := n.Source
		if src == "" {
			src = "-"
		}
		fmt.Printf("%s  %-8s  %-12s  %s  [%s]\n", n.CreatedAt.Local().Format("01-02 15:04:05"), n.Type, src, n.Title, extra)
	}
	return exitOK
}

func devReply(args []string) int {
	if len(args) < 2 {
		return usageErr("usage: meow dev reply <question-id> <answer>")
	}
	c, err := deviceClient()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	q, err := c.Respond(ctx, args[0], strings.Join(args[1:], " "), newIdemKey())
	if err != nil {
		return fail(err)
	}
	fmt.Fprintln(os.Stderr, "question", q.ID, "is now", q.Status)
	return exitOK
}
