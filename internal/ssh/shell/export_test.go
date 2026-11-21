package shell

import "testing"

// The POSIX parser hands export to the interpreter as a plain command, which
// it does not implement; bash syntax declares the variable.
func TestExport(t *testing.T) {
	sh, _ := newTestShell(t)
	out, stderr, status := runStatus(t, sh, "export FOO=bar; echo $FOO; env | grep FOO")
	if status != 0 || out != "bar\nFOO=bar\n" {
		t.Fatalf("status %d, out %q, stderr %q", status, out, stderr)
	}
}
