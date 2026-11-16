package pipeline

// SetAfterEncode makes w call f once an item's edges are encoded, before it
// records them: where a change to the item's inputs races the encode.
func (w *Edges) SetAfterEncode(f func()) { w.afterEncode = f }
