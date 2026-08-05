package grpc

import spicestarter "github.com/spice-framework/spice/starter"

// Manifest returns gRPC starter compatibility and review metadata.
func Manifest() spicestarter.Manifest {
	return spicestarter.Must(spicestarter.Spec{
		Schema:    spicestarter.Schema,
		ID:        "github.com/spice-framework/spice/starter/grpc",
		Version:   "0.1.0-dev",
		Module:    "github.com/spice-framework/spice",
		SpiceAPI:  spicestarter.APIVersion,
		MinimumGo: "1.26",
		License:   "Apache-2.0",
		Review:    "docs/dependency-reviews/grpc-go.md",
		Activation: spicestarter.Activation{
			Mode: spicestarter.ActivationExplicitConstructor,
			EntryPoints: []spicestarter.EntryPoint{
				{
					Package: "github.com/spice-framework/spice/starter/grpc",
					Symbol:  "OpenServer",
				},
				{
					Package: "github.com/spice-framework/spice/starter/grpc",
					Symbol:  "OpenClient",
				},
			},
		},
		Capabilities: []string{
			"rpc.grpc.client",
			"rpc.grpc.server",
		},
		Dependencies: []spicestarter.Dependency{{
			Module:  "google.golang.org/grpc",
			Version: "v1.82.1",
			License: "Apache-2.0",
		}},
	})
}
