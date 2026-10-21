package main

import (
	"meow.blue/meow/adapters"
	"meow.blue/meow/message"
	"os"
)

// Legacy regression helpers delegate to the extracted modules; these shims are
// test-only, so command binaries do not retain duplicate formatting policies.
const summaryRunes = message.MaxBody

func summarize(s string) string          { return adapters.Summary(s) }
func hookIdemKey(parts ...string) string { return adapters.IdempotencyKey(parts...) }
func hookTitle(kind string) string       { return adapters.Title(kind, os.Getenv("MEOW_LANGUAGE")) }
