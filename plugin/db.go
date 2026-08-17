package plugin

// DBOption adjusts how the host opens a per-user plugin database.
//
// Options describe intent, not connection-string syntax: "read only" survives a
// change of storage backend, "mode=ro" does not.
type DBOption func(*DBOptions)

// DBOptions is the resolved form of the DBOption list. Hosts read it; plugins
// build it with the option functions below.
type DBOptions struct {
	// ReadOnly opens a database the caller promises not to write to.
	ReadOnly bool
	// NoHooks skips the host's per-connection instrumentation, currently
	// search indexing and the test clock. Use it for bulk or out-of-process
	// work that must not contend with the server for index files.
	NoHooks bool
	// PluginID overrides which plugin's database is opened. Empty means the
	// calling plugin's own. Reserved for privileged plugins; the host rejects
	// it from anyone else.
	PluginID string
}

// ReadOnly opens the database for reading only.
func ReadOnly(o *DBOptions) { o.ReadOnly = true }

// NoHooks opens the database without the host's per-connection instrumentation.
func NoHooks(o *DBOptions) { o.NoHooks = true }

// OfPlugin opens another plugin's database for the same user. Only privileged
// plugins may do this; for anyone else the host returns an error.
func OfPlugin(pluginID string) DBOption {
	return func(o *DBOptions) { o.PluginID = pluginID }
}

// ApplyDBOptions resolves an option list. It is exported for hosts implementing
// PluginContext; plugins have no reason to call it.
func ApplyDBOptions(opts []DBOption) DBOptions {
	var o DBOptions
	for _, opt := range opts {
		opt(&o)
	}

	return o
}
