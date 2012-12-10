package main

import (
	"fmt"
	"github.com/runningwild/cgf/core"
	"time"
)

type Game interface {
	core.Game
}

type Event interface {
	core.Event
}

type FooGame struct {
	A, B int
}

func (f *FooGame) Think() {
	f.A++
	f.B += 2
}
func (f *FooGame) Copy() interface{} {
	f2 := *f
	return &f2
}
func (f *FooGame) OverwriteWith(_f2 interface{}) {
	*f = *_f2.(*FooGame)
}

func main() {
	f := FooGame{}
	s, err := core.MakeServer(&f, nil)
	if err != nil {
		println(err.Error())
		return
	}
	for i := 0; i < 100; i++ {
		time.Sleep(time.Millisecond * 10)
		f := s.CurrentState().(*FooGame)
		fmt.Printf("State: %v\n", f)
	}
}
