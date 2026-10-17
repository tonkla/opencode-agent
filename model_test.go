package main

import (
	"strings"
	"testing"
)

func TestParseModelRef(t *testing.T) {
	cases := []struct {
		in         string
		want       ModelRef
		wantString string
		wantErr    bool
	}{
		{"deepseek-v4.1-flash-free#high",
			ModelRef{Provider: "opencode", ID: "deepseek-v4.1-flash-free", Variant: "high"},
			"deepseek-v4.1-flash-free#high", false},
		{"opencode/deepseek-v4.1-flash-free#high",
			ModelRef{Provider: "opencode", ID: "deepseek-v4.1-flash-free", Variant: "high"},
			"deepseek-v4.1-flash-free#high", false},
		{"openrouter/deepseek-v4:free#high",
			ModelRef{Provider: "openrouter", ID: "deepseek-v4:free", Variant: "high"},
			"deepseek-v4:free#high", false},
		{"openrouter/deepseek/deepseek-v4.1-flash-1017",
			ModelRef{Provider: "openrouter", ID: "deepseek/deepseek-v4.1-flash-1017"},
			"deepseek/deepseek-v4.1-flash-1017", false},
		{"anthropic", ModelRef{Provider: "opencode", ID: "anthropic"}, "anthropic", false},
		{"/noprovider", ModelRef{}, "", true},
		{"provider/", ModelRef{}, "", true},
		{"provider/model#", ModelRef{}, "", true},
		{"", ModelRef{}, "", true},
	}
	for _, c := range cases {
		got, err := ParseModelRef(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q: got %+v want %+v", c.in, got, c.want)
		}
		if got.String() != c.wantString {
			t.Errorf("%q: display gave %q want %q", c.in, got.String(), c.wantString)
		}
	}
}

func TestMatchModel(t *testing.T) {
	catalog := []catalogEntry{
		{Provider: "opencode", ID: "deepseek-v4.1-flash-free", Variants: []catalogVariant{{ID: "high"}}},
		{Provider: "openrouter", ID: "deepseek-v4:free", Variants: []catalogVariant{{ID: "high"}}},
	}
	for _, tc := range []struct {
		ref     ModelRef
		wantErr string
	}{
		{ModelRef{Provider: "opencode", ID: "deepseek-v4.1-flash-free", Variant: "high"}, ""},
		{ModelRef{Provider: "opencode", ID: "deepseek-v4.1-flash-free"}, ""},
		{ModelRef{Provider: "nope", ID: "nope"}, `unknown model "nope/nope"`},
		{ModelRef{Provider: "openrouter", ID: "deepseek-v4:free", Variant: "high"}, `not an OpenCode Zen model`},
		{ModelRef{Provider: "openrouter", ID: "deepseek-v4:free"}, `not an OpenCode Zen model`},
		{ModelRef{Provider: "opencode", ID: "deepseek-v4.1-flash-free", Variant: "bogus"}, `unknown variant "bogus"`},
	} {
		err := matchModel(catalog, tc.ref)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%v: %v", tc.ref, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%v: got %v want %q", tc.ref, err, tc.wantErr)
		}
	}
}

func TestSuggestRefs(t *testing.T) {
	catalog := []catalogEntry{
		{Provider: "opencode", ID: "deepseek-v4.1-flash-free"},
		{Provider: "opencode", ID: "glm-5.3-flash-free"},
		{Provider: "openrouter", ID: "deepseek/deepseek-v4.1-flash-1017"},
		{Provider: "anthropic", ID: "claude-sonnet-4-5"},
	}
	got := SuggestRefs(catalog, "flash-free", 5)
	if len(got) != 2 || got[0] != "deepseek-v4.1-flash-free" {
		t.Errorf("Zen-only substring: %q", got)
	}
	got = SuggestRefs(catalog, "claude", 5)
	if len(got) != 0 {
		t.Errorf("non-Zen must never be suggested: %q", got)
	}
	got = SuggestRefs(catalog, "FLASH", 1)
	if len(got) != 1 {
		t.Errorf("case-insensitive capped: %q", got)
	}
	if got := SuggestRefs(catalog, "zzz", 5); len(got) != 0 {
		t.Errorf("no match: %q", got)
	}
}

func TestFormatCatalog(t *testing.T) {
	catalog := []catalogEntry{
		{Provider: "openrouter", ID: "zzz", Variants: []catalogVariant{{ID: "high"}}},
		{Provider: "opencode", ID: "mimo-v2.6-flash-free"},
		{Provider: "opencode", ID: "beam-free", Variants: []catalogVariant{{ID: "low"}, {ID: "high"}}},
	}
	want := "beam-free [low, high]\nmimo-v2.6-flash-free"
	if got := formatCatalog(catalog); got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if got := formatCatalog(nil); got != "No Zen models available." {
		t.Errorf("empty: %q", got)
	}
}

func TestDescribeModel(t *testing.T) {
	if got := describeModel(sessionModel{}); got != "No model override. The session uses the server default." {
		t.Errorf("default: %q", got)
	}
	got := describeModel(sessionModel{ID: "m", Provider: "p", Variant: "high", HasModel: true, Overridden: true})
	if got != "Model m#high (session override)." {
		t.Errorf("override: %q", got)
	}
	got = describeModel(sessionModel{ID: "m", Provider: "p", HasModel: true})
	if got != "Model m (default)." {
		t.Errorf("default: %q", got)
	}
}
