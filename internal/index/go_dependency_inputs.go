package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Lordymine/codegraph/internal/securefile"
)

const goDependencyInputVersion = "go-dependency-inputs-v1"

// goListDependencyTemplate selects, per resolved package: the standard-library
// flag, the package directory, and the source files the build consumes.
// Generated cgo files arrive as absolute GOCACHE paths and are derived inputs;
// the C sources and headers they derive from are listed by the cgo columns.
const goListDependencyTemplate = `{{.Standard}}{{"\t"}}{{.Dir}}{{"\t"}}{{join .CompiledGoFiles " "}}{{"\t"}}{{join .CgoFiles " "}}{{"\t"}}{{join .CFiles " "}}{{"\t"}}{{join .CXXFiles " "}}{{"\t"}}{{join .HFiles " "}}{{"\t"}}{{join .SFiles " "}}`

// GoDependencyInputs certifies the dependency source bytes the Go build
// consumes under the observed environment and build tags. Only a digest is
// persisted: any change to the file set, a module version, or a file byte
// changes the digest and forces a rebuild.
type GoDependencyInputs struct {
	Version string `json:"version"`
	Digest  string `json:"digest"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes,omitempty"`
}

// observeGoDependencyInputs enumerates the packages the Go build resolves for
// this repository and hashes the dependency files it consumes. Files inside the
// repository (source hashing) and inside GOROOT (toolchain identity) are not
// part of the digest. Any failure returns an error; callers must keep the
// conservative rebuild/no-reuse decision instead of certifying stale bytes.
func observeGoDependencyInputs(ctx context.Context, root string, goEnvironment map[string]string) (*GoDependencyInputs, error) {
	enumerate := true
	if !goVendorModulesPresent(root) {
		modules, err := goListDependencyModules(ctx, root, goEnvironment)
		if err != nil {
			return nil, err
		}
		// A dependency-free repository needs no per-package enumeration.
		enumerate = len(modules) != 0
	}
	var files []string
	if enumerate {
		output, err := runGoList(ctx, root, goEnvironment, "list", "-deps", "-compiled", "-test", "-f", goListDependencyTemplate, "./...")
		if err != nil {
			return nil, err
		}
		files, err = parseGoListDependencyFiles(root, goEnvironment["GOROOT"], output)
		if err != nil {
			return nil, err
		}
	}
	digest, count, bytes, err := hashGoDependencyFiles(ctx, files)
	if err != nil {
		return nil, err
	}
	return &GoDependencyInputs{Version: goDependencyInputVersion, Digest: digest, Files: count, Bytes: bytes}, nil
}

// goVendorModulesPresent reports whether the root build reads a vendor tree.
// `go list -m all` cannot compute the module list against a vendor directory,
// so vendored repositories go straight to the package enumeration, which
// resolves the vendored packages in place (repository bytes, already hashed by
// the resolver input plan).
func goVendorModulesPresent(root string) bool {
	_, err := os.Lstat(filepath.Join(root, "vendor", "modules.txt"))
	return err == nil
}

// goListDependencyModules reports the modules outside the main module.
func goListDependencyModules(ctx context.Context, root string, goEnvironment map[string]string) ([]string, error) {
	output, err := runGoList(ctx, root, goEnvironment, "list", "-m", "-f", "{{if not .Main}}{{.Path}}{{end}}", "all")
	if err != nil {
		return nil, err
	}
	var modules []string
	for _, line := range strings.Split(output, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			modules = append(modules, line)
		}
	}
	return modules, nil
}

// runGoList runs the go command with the frozen effective environment and a
// forced offline read: the observation must never download, rewrite go.mod, or
// mutate the module cache. Failures are the caller's fail-closed signal.
func runGoList(ctx context.Context, root string, goEnvironment map[string]string, args ...string) (string, error) {
	// #nosec G204 -- fixed go command; arguments are fixed strings, and the only
	// derived values are -mod=vendor/-mod=readonly from the admitted GOFLAGS grammar.
	command := exec.CommandContext(ctx, "go", append(goListModArgs(goEnvironment), args...)...)
	command.Dir = root
	command.Env = append(goEnvironmentForRoot(goEnvironment, root, root), "GOPROXY=off")
	command.WaitDelay = 2 * time.Second
	output, err := command.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", errors.New("go dependency inputs could not be enumerated; inspect the module cache and Go settings")
		}
		return "", fmt.Errorf("enumerate Go dependency inputs: %w", err)
	}
	return string(output), nil
}

// goListModArgs keeps the observation read-only while preserving the vendor
// selection admitted through GOFLAGS. Other modes keep the go command default.
func goListModArgs(goEnvironment map[string]string) []string {
	fields, err := splitGoFlags(goEnvironment["GOFLAGS"])
	if err != nil {
		return []string{"-mod=readonly"}
	}
	mode := ""
	for _, field := range fields {
		flag, flagErr := parseGoBuildFlag(field)
		if flagErr == nil && flag.name == "mod" {
			mode = flag.value
		}
	}
	switch mode {
	case "vendor":
		return []string{"-mod=vendor"}
	case "mod":
		// Selection is unchanged; only the rewrite permission is removed.
		return []string{"-mod=readonly"}
	default:
		return nil
	}
}

// parseGoListDependencyFiles keeps the dependency bytes the build consumes:
// non-standard packages outside the repository and GOROOT, with their Go, cgo
// and C sources. Absolute entries are generated cgo files under GOCACHE and are
// derived from the listed cgo inputs plus the observed environment.
func parseGoListDependencyFiles(root, goroot, output string) ([]string, error) {
	seen := make(map[string]bool)
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		columns := strings.Split(line, "\t")
		if len(columns) != 8 {
			return nil, errors.New("go dependency enumeration returned an unexpected record")
		}
		if columns[0] == "true" {
			continue // standard library: covered by the toolchain identity
		}
		dir := columns[1]
		if dir == "" {
			continue
		}
		if _, within := resolverPathWithin(root, dir); within {
			continue // repository sources are hashed by the source observation
		}
		if pathWithinBase(goroot, dir) {
			continue
		}
		for _, column := range columns[2:] {
			for _, name := range strings.Fields(column) {
				if name == "" || filepath.IsAbs(name) {
					continue
				}
				seen[filepath.Join(dir, name)] = true
			}
		}
	}
	files := make([]string, 0, len(seen))
	for file := range seen {
		files = append(files, file)
	}
	sort.Strings(files)
	return files, nil
}

func pathWithinBase(base, candidate string) bool {
	if base == "" {
		return false
	}
	rel, err := filepath.Rel(base, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// hashGoDependencyFiles streams a canonical digest over the sorted file set.
// Each file contributes its path and its own sha256, so the digest changes when
// a file appears, disappears, or changes bytes.
func hashGoDependencyFiles(ctx context.Context, files []string) (string, int, int64, error) {
	stream := sha256.New()
	var total int64
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return "", 0, 0, err
		}
		file, err := securefile.OpenRead(path)
		if err != nil {
			return "", 0, 0, fmt.Errorf("read Go dependency input: %w", err)
		}
		fileDigest := sha256.New()
		written, copyErr := io.Copy(fileDigest, file)
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return "", 0, 0, fmt.Errorf("read Go dependency input: %w", err)
		}
		stream.Write([]byte(path))
		stream.Write([]byte{0})
		stream.Write([]byte(hex.EncodeToString(fileDigest.Sum(nil))))
		stream.Write([]byte{'\n'})
		total += written
	}
	return hex.EncodeToString(stream.Sum(nil)), len(files), total, nil
}
