package scip

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/Lordymine/codegraph/internal/securefile"
)

// ExecutionEnvironment is transient: settings may contain credentials and
// must never be serialized into an index manifest or resolver report.
type ExecutionEnvironment struct {
	Variables []string           `json:"-"`
	Node      ExecutableIdentity `json:"-"`
	NPX       ExecutableIdentity `json:"-"`
}

type ExecutableIdentity struct {
	Path   string
	SHA256 string
}

// ObserveExecutionEnvironment identifies installed tools without launching
// Node (which could execute NODE_OPTIONS preloads merely to report a version).
func ObserveExecutionEnvironment(ctx context.Context) (*ExecutionEnvironment, error) {
	ctx = nonNilContext(ctx)
	variables := sortedProcessEnvironment()
	node, err := observeExecutable(ctx, "node")
	if err != nil {
		return nil, err
	}
	npx, err := observeExecutable(ctx, "npx")
	if err != nil {
		return nil, err
	}
	if !slices.Equal(variables, sortedProcessEnvironment()) {
		return nil, errors.New("SCIP process environment changed during observation")
	}
	return &ExecutionEnvironment{Variables: variables, Node: node, NPX: npx}, nil
}

func sortedProcessEnvironment() []string {
	variables := os.Environ()
	sort.Strings(variables)
	return variables
}

func observeExecutable(ctx context.Context, name string) (ExecutableIdentity, error) {
	if err := ctx.Err(); err != nil {
		return ExecutableIdentity{}, err
	}
	path, err := exec.LookPath(name)
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, exec.ErrDot) {
		return ExecutableIdentity{}, nil
	}
	if err != nil {
		return ExecutableIdentity{}, fmt.Errorf("locate SCIP runtime %q: %w", name, err)
	}
	// Tool installations are outside the repository admission namespace. Resolve
	// their launcher aliases once; private snapshots never use EvalSymlinks.
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ExecutableIdentity{}, err
	}
	physical, err = filepath.Abs(physical)
	if err != nil {
		return ExecutableIdentity{}, err
	}
	digest, err := executableDigest(ctx, physical)
	if err != nil {
		return ExecutableIdentity{}, fmt.Errorf("observe SCIP runtime %q: %w", name, err)
	}
	return ExecutableIdentity{Path: physical, SHA256: digest}, nil
}

func executableDigest(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	file, err := securefile.OpenRead(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, readErr := io.Copy(hash, file)
	if err := errors.Join(readErr, file.Close(), ctx.Err()); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Digest hashes settings in memory; its caller persists only this opaque value.
func (environment *ExecutionEnvironment) Digest() (string, error) {
	encoded, err := json.Marshal(struct {
		Variables []string
		Node, NPX ExecutableIdentity
	}{environment.Variables, environment.Node, environment.NPX})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (environment *ExecutionEnvironment) Available() bool {
	return environment != nil && environment.Node.Path != "" && environment.NPX.Path != ""
}

func (environment *ExecutionEnvironment) Verify(ctx context.Context) error {
	if !environment.Available() {
		return errors.New("observed Node/npx runtime is unavailable")
	}
	for _, executable := range []ExecutableIdentity{environment.Node, environment.NPX} {
		digest, err := executableDigest(ctx, executable.Path)
		if err != nil {
			return fmt.Errorf("verify observed SCIP runtime: %w", err)
		}
		if digest != executable.SHA256 {
			return errors.New("observed SCIP runtime executable changed before/during invocation")
		}
	}
	return nil
}

func (environment *ExecutionEnvironment) command(packageArguments []string) (string, []string) {
	_, args := npx(packageArguments...)
	if strings.EqualFold(filepath.Ext(environment.NPX.Path), ".js") {
		return environment.Node.Path, append([]string{environment.NPX.Path}, append([]string{"--yes"}, packageArguments...)...)
	}
	if strings.EqualFold(filepath.Ext(environment.NPX.Path), ".cmd") {
		return "cmd", append([]string{"/c", environment.NPX.Path}, append([]string{"--yes"}, packageArguments...)...)
	}
	return environment.NPX.Path, args
}
