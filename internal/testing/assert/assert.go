// Package assert provides testify-style test assertions that call t.Error
// (non-fatal) on failure. Each function returns true if the assertion passed.
package assert

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func formatMsg(msgAndArgs []any) string {
	if len(msgAndArgs) == 0 {
		return ""
	}
	if format, ok := msgAndArgs[0].(string); ok {
		return fmt.Sprintf(format, msgAndArgs[1:]...)
	}
	return fmt.Sprint(msgAndArgs...)
}

func fail(t testing.TB, msg string, extra ...any) {
	t.Helper()
	label := formatMsg(extra)
	if label != "" {
		t.Error(msg + ": " + label)
	} else {
		t.Error(msg)
	}
}

// Equal asserts deep equality between expected and actual.
// Both operands must be the same type, avoiding untyped comparison mistakes.
func Equal[T any](t testing.TB, expected, actual T, msgAndArgs ...any) bool {
	t.Helper()
	if !reflect.DeepEqual(expected, actual) {
		fail(t, fmt.Sprintf("expected %v, got %v", expected, actual), msgAndArgs...)
		return false
	}
	return true
}

// NotEqual asserts that expected and actual are not deeply equal.
func NotEqual[T any](t testing.TB, expected, actual T, msgAndArgs ...any) bool {
	t.Helper()
	if reflect.DeepEqual(expected, actual) {
		fail(t, fmt.Sprintf("expected value to differ from %v", expected), msgAndArgs...)
		return false
	}
	return true
}

// Same asserts that expected == actual using the == operator.
func Same[T comparable](t testing.TB, expected, actual T, msgAndArgs ...any) bool {
	t.Helper()
	if expected != actual {
		fail(t, fmt.Sprintf("expected %v, got %v", expected, actual), msgAndArgs...)
		return false
	}
	return true
}

// NotSame asserts that expected != actual using the != operator.
func NotSame[T comparable](t testing.TB, expected, actual T, msgAndArgs ...any) bool {
	t.Helper()
	if expected == actual {
		fail(t, fmt.Sprintf("expected value to differ from %v", expected), msgAndArgs...)
		return false
	}
	return true
}

// True asserts that value is true.
func True(t testing.TB, value bool, msgAndArgs ...any) bool {
	t.Helper()
	if !value {
		fail(t, "expected true, got false", msgAndArgs...)
		return false
	}
	return true
}

// False asserts that value is false.
func False(t testing.TB, value bool, msgAndArgs ...any) bool {
	t.Helper()
	if value {
		fail(t, "expected false, got true", msgAndArgs...)
		return false
	}
	return true
}

// Nil asserts that obj is nil.
func Nil(t testing.TB, obj any, msgAndArgs ...any) bool {
	t.Helper()
	if !isNil(obj) {
		fail(t, fmt.Sprintf("expected nil, got %v", obj), msgAndArgs...)
		return false
	}
	return true
}

// NotNil asserts that obj is not nil.
func NotNil(t testing.TB, obj any, msgAndArgs ...any) bool {
	t.Helper()
	if isNil(obj) {
		fail(t, "expected non-nil value", msgAndArgs...)
		return false
	}
	return true
}

// NoError asserts that err is nil.
func NoError(t testing.TB, err error, msgAndArgs ...any) bool {
	t.Helper()
	if err != nil {
		fail(t, fmt.Sprintf("unexpected error: %v", err), msgAndArgs...)
		return false
	}
	return true
}

// Error asserts that err is not nil.
func Error(t testing.TB, err error, msgAndArgs ...any) bool {
	t.Helper()
	if err == nil {
		fail(t, "expected an error but got nil", msgAndArgs...)
		return false
	}
	return true
}

// Contains asserts that s contains substr.
func Contains(t testing.TB, s, substr string, msgAndArgs ...any) bool {
	t.Helper()
	if !strings.Contains(s, substr) {
		fail(t, fmt.Sprintf("expected %q to contain %q", s, substr), msgAndArgs...)
		return false
	}
	return true
}

// NotContains asserts that s does not contain substr.
func NotContains(t testing.TB, s, substr string, msgAndArgs ...any) bool {
	t.Helper()
	if strings.Contains(s, substr) {
		fail(t, fmt.Sprintf("expected %q not to contain %q", s, substr), msgAndArgs...)
		return false
	}
	return true
}

// Empty asserts that v is the zero value for its type.
func Empty[T comparable](t testing.TB, v T, msgAndArgs ...any) bool {
	t.Helper()
	var zero T
	if v != zero {
		fail(t, fmt.Sprintf("expected zero value, got %v", v), msgAndArgs...)
		return false
	}
	return true
}

// NotEmpty asserts that v is not the zero value for its type.
func NotEmpty[T comparable](t testing.TB, v T, msgAndArgs ...any) bool {
	t.Helper()
	var zero T
	if v == zero {
		fail(t, "expected non-zero value", msgAndArgs...)
		return false
	}
	return true
}

// isNil reports whether obj is nil or a nil pointer, map, slice, channel,
// function or interface. A value of any other kind is not nil.
func isNil(obj any) bool {
	if obj == nil {
		return true
	}
	switch v := reflect.ValueOf(obj); v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface, reflect.UnsafePointer:
		return v.IsNil()
	}
	return false
}
