package dis

import "testing"

func TestDisassemble(t *testing.T) {
	t.Run("ConstPush", func(t *testing.T) {
		got := Disassemble([]int{0, 10})
		want := []string{"PUSH_CONST 10"}
		if len(got) != len(want) || (len(got) > 0 && got[0] != want[0]) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}
