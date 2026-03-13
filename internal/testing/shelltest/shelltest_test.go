package shelltest_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/shelltest"
)

func TestPromptAndEcho(t *testing.T) {
	tm := shelltest.New(t)
	tm.WaitPrompt()

	tm.TypeLine("echo hello")
	tm.WaitText("hello")
}

func TestPwd(t *testing.T) {
	tm := shelltest.New(t)
	tm.WaitPrompt()

	tm.TypeLine("pwd")
	tm.WaitText("/")
}

func TestMultipleCommands(t *testing.T) {
	tm := shelltest.New(t)
	tm.WaitPrompt()

	tm.TypeLine("echo first")
	tm.WaitText("first")

	tm.TypeLine("echo second")
	tm.WaitText("second")
}
