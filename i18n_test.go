package main

import (
	"regexp"
	"slices"
	"testing"
)

// verbs are the fmt verbs of a message, in order.
var verbs = regexp.MustCompile(`%[-+# 0]*[0-9]*(?:\.[0-9]+)?[a-zA-Z%]`)

func TestTranslationsKeepVerbs(t *testing.T) {
	for en, zh := range zhHans {
		if got, want := verbs.FindAllString(zh, -1), verbs.FindAllString(en, -1); !slices.Equal(got, want) {
			t.Errorf("%q has verbs %q, its translation %q has %q", en, want, zh, got)
		}
	}
}

func TestTrIn(t *testing.T) {
	for _, c := range []struct{ locale, msg, want string }{
		{"en", "Open yesman", "Open yesman"},
		{"zh-Hans", "Open yesman", "打开 yesman"},
		{"zh-Hans", "an untranslated message", "an untranslated message"},
	} {
		if got := trIn(c.locale, c.msg); got != c.want {
			t.Errorf("trIn(%q, %q) = %q, want %q", c.locale, c.msg, got, c.want)
		}
	}
	if got := trIn("zh-Hans", "yesman: %d working, %d waiting for you", 2, 1); got != "yesman：2 个在工作，1 个在等你" {
		t.Errorf("formatted message = %q", got)
	}
	if got := trIn("en", "%s stopped", "demo/lead"); got != "demo/lead stopped" {
		t.Errorf("formatted message = %q", got)
	}
}

func TestInUseText(t *testing.T) {
	// Tests run without an app, so tr says English.
	for _, reason := range []string{"a worktree is checked out of it", "3 worktrees are checked out of it", "a branch has commits that no remote has"} {
		if got := inUseText(reason); got != reason {
			t.Errorf("inUseText(%q) = %q", reason, got)
		}
	}
}

func TestLanguageChoices(t *testing.T) {
	for _, c := range []struct{ tag, want string }{
		{"", languageSystem},
		{"en", "en"},
		{"en-GB", "en"},
		{"zh-Hans", "zh-Hans"},
		{"zh-Hans-CN", "zh-Hans"},
		{"zh-Hant-TW", languageSystem}, // not written in Traditional Chinese
	} {
		if got := chosenLanguage(c.tag); got != c.want {
			t.Errorf("chosenLanguage(%q) = %q, want %q", c.tag, got, c.want)
		}
	}
	for _, c := range []struct {
		choice, tag string
		ok          bool
	}{
		{languageSystem, "", true},
		{"en", "en", true},
		{"zh-Hans", "zh-Hans", true},
		{"ja", "ja", false},
		{"", "", false},
	} {
		if tag, ok := languageTag(c.choice); tag != c.tag || ok != c.ok {
			t.Errorf("languageTag(%q) = %q, %v; want %q, %v", c.choice, tag, ok, c.tag, c.ok)
		}
	}
}
