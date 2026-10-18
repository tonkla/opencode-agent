package main

import (
	"strings"
	"testing"
)

func TestParseSessionUsage(t *testing.T) {
	raw := `{"data":{"id":"s","model":{"id":"deepseek-v4.1-flash-free","providerID":"opencode","variant":"high"},"tokens":{"input":12345,"output":678,"reasoning":9,"cache":{"read":1,"write":2}}}}`
	u, err := parseSessionUsage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !u.HasModel || u.ModelID != "deepseek-v4.1-flash-free" || u.Variant != "high" {
		t.Errorf("model identity lost: %+v", u)
	}
	if u.Input != 12345 || u.Output != 678 {
		t.Errorf("counters lost: %+v", u)
	}
	if u.used() != 13023 || u.Limit != 0 {
		t.Errorf("derived fields wrong: %+v", u)
	}
}

func TestParseSessionUsageWithoutModel(t *testing.T) {
	raw := `{"data":{"id":"s","tokens":{"input":1,"output":2}}}`
	u, err := parseSessionUsage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if u.HasModel {
		t.Errorf("missing model must leave HasModel false: %+v", u)
	}
}

func TestParseSessionUsageRejectsBadJSON(t *testing.T) {
	if _, err := parseSessionUsage([]byte("{nope")); err == nil {
		t.Error("expected JSON error")
	}
}

func TestModelLimit(t *testing.T) {
	catalog := []catalogEntry{
		{Provider: "opencode", ID: "a"},
		{Provider: "opencode", ID: "b"},
		{Provider: "other", ID: "b"},
	}
	catalog[1].Limit.Context = 200000
	if got, ok := modelLimit(catalog, "opencode", "b"); !ok || got != 200000 {
		t.Errorf("hit: (%d,%v)", got, ok)
	}
	if _, ok := modelLimit(catalog, "opencode", "a"); ok {
		t.Error("zero limit must report false")
	}
	if _, ok := modelLimit(catalog, "opencode", "missing"); ok {
		t.Error("missing model must report false")
	}
	if _, ok := modelLimit(catalog, "other", "b"); ok {
		t.Error("other provider shares no window")
	}
}

func TestFormatUsage(t *testing.T) {
	u := sessionUsage{Input: 12345, Output: 678, ModelID: "deepseek-v4.1-flash-free",
		Variant: "high", HasModel: true, Limit: 1048576, HasLimit: true}
	want := "Tokens in: 12,345, out: 678. Context left: 98% on deepseek-v4.1-flash-free#high."
	if got := formatUsage(u); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestFormatUsageOmitsDefaultVariant(t *testing.T) {
	u := sessionUsage{Input: 0, Output: 0, ModelID: "m", Variant: "default", HasModel: true, Limit: 100, HasLimit: true}
	want := "Tokens in: 0, out: 0. Context left: 100% on m."
	if got := formatUsage(u); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestFormatUsageWithoutLimit(t *testing.T) {
	u := sessionUsage{Input: 7, Output: 8}
	if got := formatUsage(u); !strings.HasPrefix(got, "Tokens in: 7, out: 8.") || strings.Contains(got, "Context") {
		t.Errorf("model-less usage must skip the context line: %q", got)
	}
}

func TestUsageLeftFloorsAtZero(t *testing.T) {
	u := sessionUsage{Input: 90, Output: 20, Limit: 100, HasLimit: true}
	if u.left() != 0 {
		t.Errorf("over-budget left must floor at zero, got %d", u.left())
	}
}

func TestCommas(t *testing.T) {
	for in, want := range map[int64]string{
		0: "0", 7: "7", 999: "999", 1000: "1,000",
		12345: "12,345", 1048576: "1,048,576", -1234567: "-1,234,567",
	} {
		if got := commas(in); got != want {
			t.Errorf("commas(%d) = %q, want %q", in, got, want)
		}
	}
}
