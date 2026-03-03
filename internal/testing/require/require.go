// Package require provides testify-style test assertions that call t.Fatal
// (fatal) on failure. The API mirrors the assert package.
package require

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
		t.Fatal(msg + ": " + label)
	} else {
		t.Fatal(msg)
	}
}

// Equal asserts deep equality between expected and actual.
// Both operands must be the same type, avoiding untyped comparison mistakes.
func Equal[T any](t testing.TB, expected, actual T, msgAndArgs ...any) {
	t.Helper()
	if !reflect.DeepEqual(expected, actual) {
		fail(t, fmt.Sprintf("expected %v, got %v", expected, actual), msgAndArgs...)
	}
}

// NotEqual asserts that expected and actual are not deeply equal.
func NotEqual[T any](t testing.TB, expected, actual T, msgAndArgs ...any) {
	t.Helper()
	if reflect.DeepEqual(expected, actual) {
		fail(t, fmt.Sprintf("expected value to differ from %v", expected), msgAndArgs...)
	}
}

// Same asserts that expected == actual using the == operator.
func Same[T comparable](t testing.TB, expected, actual T, msgAndArgs ...any) {
	t.Helper()
	if expected != actual {
		fail(t, fmt.Sprintf("expected %v, got %v", expected, actual), msgAndArgs...)
	}
}

// NotSame asserts that expected != actual using the != operator.
func NotSame[T comparable](t testing.TB, expected, actual T, msgAndArgs ...any) {
	t.Helper()
	if expected == actual {
		fail(t, fmt.Sprintf("expected value to differ from %v", expected), msgAndArgs...)
	}
}

// True asserts that value is true.
func True(t testing.TB, value bool, msgAndArgs ...any) {
	t.Helper()
	if !value {
		fail(t, "expected true, got false", msgAndArgs...)
	}
}

// False asserts that value is false.
func False(t testing.TB, value bool, msgAndArgs ...any) {
	t.Helper()
	if value {
		fail(t, "expected false, got true", msgAndArgs...)
	}
}

// Nil asserts that obj is nil.
func Nil(t testing.TB, obj any, msgAndArgs ...any) {
	t.Helper()
	if obj != nil && !reflect.ValueOf(obj).IsNil() {
		fail(t, fmt.Sprintf("expected nil, got %v", obj), msgAndArgs...)
	}
}

// NotNil asserts that obj is not nil.
func NotNil(t testing.TB, obj any, msgAndArgs ...any) {
	t.Helper()
	if obj == nil || reflect.ValueOf(obj).IsNil() {
		fail(t, "expected non-nil value", msgAndArgs...)
	}
}

// NoError asserts that err is nil.
func NoError(t testing.TB, err error, msgAndArgs ...any) {
	t.Helper()
	if err != nil {
		fail(t, fmt.Sprintf("unexpected error: %v", err), msgAndArgs...)
	}
}

// Error asserts that err is not nil.
func Error(t testing.TB, err error, msgAndArgs ...any) {
	t.Helper()
	if err == nil {
		fail(t, "expected an error but got nil", msgAndArgs...)
	}
}

// Contains asserts that s contains substr.
func Contains(t testing.TB, s, substr string, msgAndArgs ...any) {
	t.Helper()
	if !strings.Contains(s, substr) {
		fail(t, fmt.Sprintf("expected %q to contain %q", s, substr), msgAndArgs...)
	}
}

// NotContains asserts that s does not contain substr.
func NotContains(t testing.TB, s, substr string, msgAndArgs ...any) {
	t.Helper()
	if strings.Contains(s, substr) {
		fail(t, fmt.Sprintf("expected %q not to contain %q", s, substr), msgAndArgs...)
	}
}

// Empty asserts that v is the zero value for its type.
func Empty[T comparable](t testing.TB, v T, msgAndArgs ...any) {
	t.Helper()
	var zero T
	if v != zero {
		fail(t, fmt.Sprintf("expected zero value, got %v", v), msgAndArgs...)
	}
}

// NotEmpty asserts that v is not the zero value for its type.
func NotEmpty[T comparable](t testing.TB, v T, msgAndArgs ...any) {
	t.Helper()
	var zero T
	if v == zero {
		fail(t, "expected non-zero value", msgAndArgs...)
	}
}
