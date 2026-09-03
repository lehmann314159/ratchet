package env

// Env and Register are this bead's stubs.
type Env struct {
	Slots map[string]int
}

func NewEnv() *Env { return &Env{} }

func Register(e *Env, name string) {}
