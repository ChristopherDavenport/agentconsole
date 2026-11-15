// Package incomplete copies a response's incomplete details, for the
// client's copy of a live run end and the view's of the record's, so
// neither shares the details' extension members with what it read.
package incomplete

import "github.com/ChristopherDavenport/openresponses"

// Copy returns a copy of d sharing nothing with it, its extension
// members (decoded JSON values) copied deep, and nil for nil.
func Copy(d *openresponses.IncompleteDetails) *openresponses.IncompleteDetails {
	if d == nil {
		return nil
	}
	c := *d
	if d.Extra != nil {
		c.Extra = copyJSON(d.Extra).(map[string]any)
	}
	return &c
}

// copyJSON deep-copies a decoded JSON value: its maps and slices are
// copied, anything else is a value already.
func copyJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		c := make(map[string]any, len(x))
		for k, e := range x {
			c[k] = copyJSON(e)
		}
		return c
	case []any:
		c := make([]any, len(x))
		for i, e := range x {
			c[i] = copyJSON(e)
		}
		return c
	}
	return v
}
