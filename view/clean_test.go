package view_test

import (
	"testing"

	"github.com/ChristopherDavenport/agentconsole/view"
)

func TestClean(t *testing.T) {
	for in, want := range map[string]string{
		"plain text, ünïcode ✓ 日本\nsecond\tline": "plain text, ünïcode ✓ 日本\nsecond\tline",
		"":                                    "",
		"👍🏽 cafe\u0301 \u200d zwj":            "👍🏽 cafe\u0301 \u200d zwj",
		"\x1b[2K\r▶ approved":                 "[2K▶ approved",
		"a\x1b]0;title\x07b":                  "a]0;titleb",
		"bell\x07 back\x08\x08 del\x7f":       "bell back del",
		"c1\u009b31m\u0085x\u0080\u009f":      "c131mx",
		"rtl \u202eevil\u202c \u2066x\u2069":  "rtl evil x",
		"\u061carabic \u200eltr\u200f \u202a": "arabic ltr ",
		"nul\x00byte":                         "nulbyte",
		"cr\r\nlf":                            "cr\nlf",
		"\x1b":                                "",
		"\xff invalid":                        "\ufffd invalid",
		"\xff then \x1b[1m":                   "\ufffd then [1m",
		"raw \x9b31m c1":                      "raw \ufffd31m c1",
	} {
		if got := view.Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}
