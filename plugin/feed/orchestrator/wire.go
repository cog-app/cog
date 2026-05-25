package orchestrator

import "git.duckfam.us/jj-software/cog/plugin/feed"

// NewFeedPlugin returns a feed.Plugin with the orchestrator wired in as the
// background runner. Callers in cmd/cog and cog.go should use this instead of
// feed.New() so background polling actually runs.
func NewFeedPlugin() *feed.Plugin {
	return feed.New().WithOrchestrator(Run)
}
