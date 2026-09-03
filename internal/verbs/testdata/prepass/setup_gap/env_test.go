package env

import "testing"

func TestRegister(t *testing.T) {
	t.Run("WithSetup", func(t *testing.T) {
		e := NewEnv()
		e.Slots = map[string]int{}
		Register(e, "x")
		if e.Slots["x"] != 0 {
			t.Errorf("want slot 0")
		}
	})
	t.Run("NoSetup", func(t *testing.T) {
		e := NewEnv()
		Register(e, "y")
		e.Slots["y"] = 1 // sibling initializes e.Slots; this one does not
		if e.Slots["y"] != 1 {
			t.Errorf("want 1")
		}
	})
}
