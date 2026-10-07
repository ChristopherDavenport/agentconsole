package toolview

import (
	"reflect"
	"testing"
)

func TestTextIsOneSpanALine(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []Line
	}{
		{"", nil},
		{"\n", nil},
		{"a", []Line{{S(Dim, "a")}}},
		{"a\nb\n", []Line{{S(Dim, "a")}, {S(Dim, "b")}}},
		{"a\n\nb", []Line{{S(Dim, "a")}, {S(Dim, "")}, {S(Dim, "b")}}},
	} {
		if got := Text(Dim, tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Text(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestALinesStringIsItsText(t *testing.T) {
	l := Line{S(Removed, "- a"), S(Plain, " "), S(Added, "+ b")}
	if got := l.String(); got != "- a + b" {
		t.Errorf("String() = %q", got)
	}
}
