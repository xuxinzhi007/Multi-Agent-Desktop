package main

import (
	"strings"
	"testing"
)

func TestSlashHelpListsCommands(t *testing.T) {
	text := slashHelp(cliSlashCommands())
	for _, name := range []string{"/help", "/new", "/resume", "/status", "/name", "/model", "/stop", "/quit"} {
		if !strings.Contains(text, name) {
			t.Fatalf("help missing %s\n%s", name, text)
		}
	}
}

func TestSplitSlash(t *testing.T) {
	name, arg := splitSlash("/new 前端")
	if name != "/new" || arg != "前端" {
		t.Fatalf("got %q %q", name, arg)
	}
	name, arg = splitSlash("/quit")
	if name != "/quit" || arg != "" {
		t.Fatalf("got %q %q", name, arg)
	}
}
