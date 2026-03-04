// Package plugpb contains the gRPC service definition for Cog plugins.
package plugpb

//go:generate go tool goprotoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative plugin.proto
