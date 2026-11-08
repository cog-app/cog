// Package feedpb is FeedService, the API native Feed clients use: the same
// views, item state and management the web UI has, over gRPC, gRPC-Web or
// Connect on Cog's main port.
package feedpb

//go:generate go tool protoc --go_out=. --go_opt=paths=source_relative --connect-go_out=. --connect-go_opt=paths=source_relative feed.proto
