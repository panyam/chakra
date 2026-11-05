package mod

import (
	"context"
	"io"
)

// Kind is documented.
type Kind int

const (
	KindA Kind = iota
	KindB
	kindC
)

const Untyped = 3

var ErrX = io.EOF

var Hook func(ctx context.Context, n int) error

type Base struct{ Name string }

type Thing[T any] struct {
	Base
	*io.PipeReader
	ID, Alt T
	hidden  int
	Fn      func(ctx context.Context) (out string, err error)
}

type hidden struct{}

func (hidden) Exported() {}

func (t *Thing[T]) Get(ctx context.Context, key string) (T, error) { var z T; return z, nil }

func (t Thing[T]) Len() int { return 0 }

func New[T comparable](v T, opts ...string) *Thing[T] { return nil }

type Source interface {
	io.Closer
	List(ctx context.Context) ([]string, error)
	private()
}

type Number interface{ ~int | ~float64 }

type Alias = Base

func unexported() {}
