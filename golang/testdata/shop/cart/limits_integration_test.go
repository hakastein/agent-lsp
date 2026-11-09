//go:build integration

package cart

import "testing"

// TestLimits runs with the tag integration only. MinItems is a word of this comment.
func TestLimits(t *testing.T) {
	if MaxItems < 1 {
		t.Fatal("MinItems")
	}
}
