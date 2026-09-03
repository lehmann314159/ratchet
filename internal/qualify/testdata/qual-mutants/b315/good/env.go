package main

type Environment struct {
	Slots  map[string]int
	Values []int64
}

func NewEnvironment() *Environment {
	return &Environment{Slots: make(map[string]int), Values: []int64{}}
}
