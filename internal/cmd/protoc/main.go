// protoc runs goprotoc with the Connect generator available as
// --connect-go_out.
//
// Usage: go tool protoc <goprotoc arguments>
//
// goprotoc knows protoc-gen-go and protoc-gen-go-grpc by name and finds any
// other generator through --plugin, which wants an executable. go tool -n
// names the one go.mod pins, already built in the build cache, so nothing is
// installed on the PATH or left behind.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "protoc:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	var out bytes.Buffer
	find := exec.Command("go", "tool", "-n", "protoc-gen-connect-go")
	find.Stdout = &out
	find.Stderr = os.Stderr
	if err := find.Run(); err != nil {
		return fmt.Errorf("find protoc-gen-connect-go: %w", err)
	}
	plugin := "--plugin=protoc-gen-connect-go=" + strings.TrimSpace(out.String())

	cmd := exec.Command("go", append([]string{"tool", "goprotoc", plugin}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("goprotoc: %w", err)
	}
	return nil
}
