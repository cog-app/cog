package mount

// SetBuilt sets the hook that runs between building a user's tree and
// caching it.
func SetBuilt(r *Resolver, f func()) { r.built = f }
