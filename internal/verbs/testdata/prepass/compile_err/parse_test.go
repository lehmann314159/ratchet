package parse

import "testing"

func TestParse(t *testing.T) {
	// Compile error in the locked test file: wrong type.
	var want string = 5
	got, _ := Parse("5")
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}
