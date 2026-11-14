package webtest

import (
	"fmt"
	"time"

	"github.com/go-rod/rod"
)

// waitQuiet waits until p has loaded, has no request in flight, and its
// document has gone d without a mutation. It is rod's WaitStable with the
// DOM leg done in the page.
//
// WaitStable's DOM leg captures a full DOM snapshot every d and diffs it
// against the last one in Go. That costs Chrome a document walk and the test
// binary a longest-common-subsequence over every string on the page, at
// every element lookup, and on a machine running every package's tests at
// once it was most of a browser test's CPU. A MutationObserver sees the same
// changes for free.
func waitQuiet(p *rod.Page, d time.Duration) error {
	ctx := p.GetContext()
	for {
		err := quietOnce(p, d)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		// A navigation destroyed the document the observer was in. Wait on
		// the one that replaced it.
		time.Sleep(10 * time.Millisecond)
	}
}

// quietJS resolves once the document has gone ms milliseconds without a
// mutation.
const quietJS = `(ms) => new Promise((resolve) => {
	let timer;
	const obs = new MutationObserver(() => {
		clearTimeout(timer);
		timer = setTimeout(done, ms);
	});
	function done() {
		obs.disconnect();
		resolve();
	}
	obs.observe(document, {subtree: true, childList: true, attributes: true, characterData: true});
	timer = setTimeout(done, ms);
})`

func quietOnce(p *rod.Page, d time.Duration) error {
	// Load first and alone: rod installs its JS helpers in a new document
	// on the first evaluation there, and two at once race on its map.
	if err := p.WaitLoad(); err != nil {
		return fmt.Errorf("wait load: %w", err)
	}
	idle := make(chan struct{})
	go func() {
		defer close(idle)
		p.WaitRequestIdle(d, nil, nil, nil)()
	}()
	_, err := p.Evaluate(rod.Eval(quietJS, d.Milliseconds()).ByPromise())
	<-idle
	if err != nil {
		return fmt.Errorf("wait for the document to go quiet: %w", err)
	}
	return nil
}
