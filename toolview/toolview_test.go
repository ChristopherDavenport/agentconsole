package toolview

import (
	"reflect"
	"testing"

	"github.com/ChristopherDavenport/agentconsole/view"
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

type bareRenderer struct{}

func (bareRenderer) Head(view.Call) (Line, bool)         { return Line{S(Plain, "$ ls")}, true }
func (bareRenderer) Body(view.Call, bool) ([]Line, bool) { return nil, false }
func (bareRenderer) Bare() bool                          { return true }

// Bare is optional: a renderer that has it is still a Renderer, and one
// that has not is not Bare.
func TestBareIsAnOptionalInterfaceOfARenderer(t *testing.T) {
	var r Renderer = bareRenderer{}
	if b, ok := r.(Bare); !ok || !b.Bare() {
		t.Error("a renderer with Bare() true is not bare")
	}
	var plain Renderer = Renderers{}["x"]
	if _, ok := plain.(Bare); ok {
		t.Error("no renderer is bare")
	}
}
