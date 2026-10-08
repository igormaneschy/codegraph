package index

import (
	"context"

	"github.com/Lordymine/codegraph/internal/scip"
)

// TSEnvironmentIdentity persists only opaque evidence, never raw variables or
// auth/config values. Available does not certify npm/SCIP's external closure.
type TSEnvironmentIdentity struct {
	Version   string `json:"version,omitempty"`
	Digest    string `json:"digest,omitempty"`
	Available bool   `json:"available,omitempty"`
}

func observeTSEnvironment(ctx context.Context) (TSEnvironmentIdentity, *scip.ExecutionEnvironment, error) {
	environment, err := scip.ObserveExecutionEnvironment(ctx)
	if err != nil {
		return TSEnvironmentIdentity{}, nil, err
	}
	digest, err := environment.Digest()
	if err != nil {
		return TSEnvironmentIdentity{}, nil, err
	}
	return TSEnvironmentIdentity{Version: "ts-env-v1", Digest: digest, Available: environment.Available()}, environment, nil
}
