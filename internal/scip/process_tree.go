package scip

import (
	"context"
)

// processTreeController is the platform-neutral lifecycle used by the SCIP
// runner. Each supported OS supplies its own start/contain/wait implementation;
// callers never reach into platform-specific handles or termination APIs.
type processTreeController interface {
	start(context.Context) error
	wait() error
	pid() int
	terminate() error
	close() error
}
