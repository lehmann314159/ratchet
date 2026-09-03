package store

import "testing"

func TestLookup(t *testing.T) {
	t.Run("FirstValue", func(t *testing.T) {
		got := Lookup("x")
		if got[0] != "y" {
			t.Errorf("got %q, want y", got[0])
		}
	})
}
