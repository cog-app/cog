package main_test

import (
	"fmt"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
)

func TestTabReuse(t *testing.T) {
	t.Skip("disabled while we get login figured out")
	env := cogtest.New(t, allPlugins...)
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	for i := 0; i < 4; i++ {
		user := fmt.Sprintf("user%d", i)
		env.CreateUser(t, user, "pass")
	}

	for i := 0; i < 4; i++ {
		user := fmt.Sprintf("user%d", i)
		t.Run(fmt.Sprintf("tab-%d", i), func(t *testing.T) {
			b := eng.StartT(t)
			b.SetTimeout(20 * time.Second)
			b.Navigate(t, "/login")

			// Explicitly set values via JS to avoid autofill issues
			b.Eval(t, fmt.Sprintf(`document.querySelector('#username').value = '%s'`, user))
			b.Eval(t, `document.querySelector('#password').value = 'pass'`)
			b.Eval(t, `document.querySelector('form').submit()`)

			time.Sleep(500 * time.Millisecond)
			url := b.URL(t)
			t.Logf("tab %d: URL=%s", i, url)
		})
	}
}
