package main

import (
	"flag"
	"reflect"
	"testing"
)

func TestParseInterleavedFlags(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		pos     []string
		choices string
		noWait  bool
	}{
		{[]string{"Deploy?", "--choices", "approve,reject"}, []string{"Deploy?"}, "approve,reject", false},
		{[]string{"--choices=a,b", "Which", "db?", "--no-wait"}, []string{"Which", "db?"}, "a,b", true},
		{[]string{"--", "--not-a-flag"}, []string{"--not-a-flag"}, "", false},
		{[]string{"-"}, []string{"-"}, "", false},
	} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		choices := fs.String("choices", "", "")
		noWait := fs.Bool("no-wait", false, "")
		pos, err := parse(fs, tc.args)
		if err != nil {
			t.Fatalf("parse(%q): %v", tc.args, err)
		}
		if !reflect.DeepEqual(pos, tc.pos) || *choices != tc.choices || *noWait != tc.noWait {
			t.Errorf("parse(%q) = %q choices=%q noWait=%v", tc.args, pos, *choices, *noWait)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, args := range [][]string{{"--bogus"}, {"--choices"}} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.String("choices", "", "")
		if _, err := parse(fs, args); err == nil {
			t.Errorf("parse(%q) succeeded, want error", args)
		}
	}
}

func TestSendWithoutKey(t *testing.T) {
	t.Setenv("MEOW_CONFIG", t.TempDir()+"/config.json")
	t.Setenv("MEOW_KEY", "")
	if code := run([]string{"hello"}); code != exitError {
		t.Fatalf("exit = %d, want %d", code, exitError)
	}
	if code := run([]string{"ask"}); code != exitUsage {
		t.Fatalf("ask without question exit = %d, want %d", code, exitUsage)
	}
}
