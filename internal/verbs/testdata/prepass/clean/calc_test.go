package calc

import "testing"

func TestAdd(t *testing.T) {
	t.Run("Sum", func(t *testing.T) {
		if got := Add(2, 3); got != 5 {
			t.Errorf("got %d, want 5", got)
		}
	})
	t.Run("Zero", func(t *testing.T) {
		if got := Add(0, 0); got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})
}
