package pipeline

import "time"

// SetAfterEncode makes w call f once an item's edges are encoded, before it
// records them: where a change to the item's inputs races the encode.
func (w *Edges) SetAfterEncode(f func()) { w.afterEncode = f }

// SetBudget sets how many items one run takes, and how long it may encode.
func (w *Edges) SetBudget(items int, d time.Duration) { w.limit, w.budget = items, d }
