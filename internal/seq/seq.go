// Package seq provides generic helpers for working with iter.Seq2[T, error].
package seq

import (
	"errors"
	"iter"
)

// ErrEmpty is returned by Peek when the iterator has no elements.
var ErrEmpty = errors.New("empty iterator")

// Collect drains an iterator into a slice, stopping on the first error.
func Collect[T any](seq iter.Seq2[T, error]) ([]T, error) {
	var result []T
	for v, err := range seq {
		if err != nil {
			return result, err
		}
		result = append(result, v)
	}
	return result, nil
}

// Peek returns the first element, a new iterator that yields all elements
// (starting from the first), and any error. Returns ErrEmpty if the iterator
// is empty.
func Peek[T any](seq iter.Seq2[T, error]) (T, iter.Seq2[T, error], error) {
	var zero T
	next, stop := iter.Pull2(seq)
	first, err, ok := next()
	if !ok {
		stop()
		return zero, empty[T](), ErrEmpty
	}
	if err != nil {
		stop()
		return zero, empty[T](), err
	}
	resumed := func(yield func(T, error) bool) {
		if !yield(first, nil) {
			stop()
			return
		}
		for {
			v, err, ok := next()
			if !ok {
				return
			}
			if !yield(v, err) {
				stop()
				return
			}
			if err != nil {
				return
			}
		}
	}
	return first, resumed, nil
}

// Filter returns a new iterator that only yields elements for which fn returns true.
func Filter[T any](seq iter.Seq2[T, error], fn func(T) bool) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for v, err := range seq {
			if err != nil {
				yield(v, err)
				return
			}
			if fn(v) {
				if !yield(v, nil) {
					return
				}
			}
		}
	}
}

// ToMap builds a map from an iterator using key and value extraction functions.
func ToMap[T any, K comparable, V any](seq iter.Seq2[T, error], key func(T) K, val func(T) V) (map[K]V, error) {
	m := make(map[K]V)
	for v, err := range seq {
		if err != nil {
			return m, err
		}
		m[key(v)] = val(v)
	}
	return m, nil
}

func empty[T any]() iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {}
}
