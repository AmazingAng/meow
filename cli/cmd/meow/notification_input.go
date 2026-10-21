package main

import (
	"flag"
	"fmt"
	"io"
	"meow.blue/meow/adapters"
	"meow.blue/meow/message"
	"os"
	"strings"
)

func jsonNotificationInput(fs *flag.FlagSet, pos []string) (message.Notification, error) {
	var conflict string
	fs.Visit(func(f *flag.Flag) {
		if f.Name != "json" && f.Name != "quiet" && f.Name != "idempotency-key" {
			conflict = f.Name
		}
	})
	if conflict != "" {
		return message.Notification{}, fmt.Errorf("--json cannot be combined with --%s", conflict)
	}
	input := []byte(strings.Join(pos, " "))
	if len(input) == 0 {
		info, err := os.Stdin.Stat()
		if err != nil {
			return message.Notification{}, err
		}
		if info.Mode()&os.ModeCharDevice == 0 {
			input, err = io.ReadAll(io.LimitReader(os.Stdin, message.MaxJSONBytes+1))
			if err != nil {
				return message.Notification{}, err
			}
		}
	}
	result, err := adapters.Build("generic", input, adapters.Options{})
	return result.Notification, err
}
