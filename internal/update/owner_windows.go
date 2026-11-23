package update

// keepOwner does nothing: the new binary takes its access from the
// install directory's grants, as the old one did.
func keepOwner(path, of string) error { return nil }
