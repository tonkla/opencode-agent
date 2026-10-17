package main

import "testing"

func TestMemoryTarget(t *testing.T) {
	file, rest := memoryTarget("project uses chi")
	if file != memoryFile || rest != "project uses chi" {
		t.Errorf("bare text must target MEMORY.md, got (%q,%q)", file, rest)
	}
	file, rest = memoryTarget("user: prefers terse replies")
	if file != userFile || rest != "prefers terse replies" {
		t.Errorf("user: prefix must target USER.md, got (%q,%q)", file, rest)
	}
}
