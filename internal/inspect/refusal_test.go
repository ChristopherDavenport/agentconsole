package inspect

import (
	"testing"

	"github.com/ChristopherDavenport/agentsession"
	"github.com/ChristopherDavenport/openresponses"
)

// TestARefusalOnlyMessageIsTitledByItsRefusal (#54): a message whose
// only part is a refusal is described by it, not left blank.
func TestARefusalOnlyMessageIsTitledByItsRefusal(t *testing.T) {
	msg := &openresponses.Message{Role: openresponses.RoleAssistant, Content: openresponses.Contents{
		&openresponses.Refusal{Refusal: "I can't help with that."}}}
	if got, want := describeEntry(&agentsession.ItemEntry{Item: msg}), "agent message: refusal: I can't help with that."; got != want {
		t.Errorf("described as %q, want %q", got, want)
	}
}
