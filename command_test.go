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
	for prefix, want := range map[string]string{
		"soul: direct, no sycophancy":    soulFile,
		"identity: keeper bot":           identityFile,
		"memory: project uses chi":       memoryFile,
		"SOUL: uppercase still matches":  soulFile,
		"note: this colon is content":    memoryFile,
		"http://x is not a prefix match": memoryFile,
	} {
		file, _ := memoryTarget(prefix)
		if file != want {
			t.Errorf("%q must target %s, got %s", prefix, want, file)
		}
	}
	if _, rest := memoryTarget("user:"); rest != "" {
		t.Errorf("bare prefix must leave empty rest, got %q", rest)
	}
}
