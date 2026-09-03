package reg

import "testing"

func TestNormalize(t *testing.T) {
	t.Run("Table", func(t *testing.T) {
		// Test-setup bug: write to a nil map, unrelated to the bead's code.
		var counts map[string]int
		counts["a"] = 1
		if counts["a"] != 1 {
			t.Errorf("want 1")
		}
	})
}
