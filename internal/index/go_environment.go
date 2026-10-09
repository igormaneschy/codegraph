package index

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/modfile"

	"github.com/Lordymine/codegraph/internal/securefile"
)

// Only a digest is persisted: flags and proxy/auth configuration can contain
// credentials. Effective values stay in memory and are injected into go/packages.
type GoEnvironmentIdentity struct {
	Version   string `json:"version,omitempty"`
	Digest    string `json:"digest,omitempty"`
	Available bool   `json:"available,omitempty"`
}

func observeGoEnvironment(ctx context.Context, root string, inputs []InputFingerprint) (identity GoEnvironmentIdentity, values map[string]string, retErr error) {
	private, err := goEnvironmentView(ctx, root, inputs)
	if err != nil {
		return identity, nil, err
	}
	defer func() { retErr = errors.Join(retErr, private.Cleanup()) }()
	cmd := exec.CommandContext(ctx, "go", "env", "-json")
	cmd.Dir = private.Path()
	cmd.Env = goEnvironmentForRoot(nil, root, private.Path())
	if err := validateProcessGoFlags(cmd.Env); err != nil {
		return identity, nil, err
	}
	cmd.WaitDelay = 2 * time.Second
	controlInputs, err := observeGoControlInputs(ctx, cmd)
	if err != nil {
		return identity, nil, err
	}
	if _, err := goFlagReuseReasons(controlInputs["GOFLAGS"]); err != nil {
		return identity, nil, err
	}
	output, commandErr := cmd.Output()
	if err := ctx.Err(); err != nil {
		return identity, nil, err
	}
	if err := private.Verify(); err != nil {
		return identity, nil, err
	}
	identity.Version = "go-env-v1"
	values = controlInputs
	if commandErr == nil {
		if err := json.Unmarshal(output, &values); err != nil || values == nil {
			return identity, nil, errors.New("go env returned invalid environment JSON; expected a settings object")
		}
		identity.Available = values["GOVERSION"] != "" && values["GOOS"] != "" && values["GOARCH"] != ""
	}
	// This is a derived compiler command containing a random go-build debug
	// prefix. Its semantic inputs are already in the effective environment.
	delete(values, "GOGCCFLAGS")
	// go/packages consumes this process setting, although go env does not
	// report it. Freeze it alongside the build tool's effective settings.
	values["GOPACKAGESDRIVER"] = os.Getenv("GOPACKAGESDRIVER")
	normalizeGoEnvironmentPaths(values, private.Path(), root)
	identity.Digest, err = goEnvironmentDigest(values)
	return identity, values, err
}

// Named control observation avoids NewBuilder's external cache initialization.
// Keep the settings even when costly full observation fails to initialize that
// cache; an unavailable environment is never a reuse certificate.
func observeGoControlInputs(ctx context.Context, command *exec.Cmd) (map[string]string, error) {
	// #nosec G204 -- same Go executable already resolved by exec.CommandContext;
	// arguments are fixed environment queries, never a shell or operator command.
	probe := exec.CommandContext(ctx, command.Path, "env", "-json", "GOFLAGS", "GOCACHEPROG")
	probe.Dir, probe.Env, probe.WaitDelay = command.Dir, command.Env, command.WaitDelay
	output, err := probe.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	values := make(map[string]string)
	if err != nil {
		var failed *exec.ExitError
		if errors.As(err, &failed) {
			return nil, errors.New("effective Go control inputs could not be observed and are not admitted; inspect GOFLAGS/GOENV/toolchain settings")
		}
		return values, nil
	}
	if err := json.Unmarshal(output, &values); err != nil || values == nil {
		return nil, errors.New("go env returned invalid control input JSON; expected a settings object")
	}
	return values, nil
}

func goEnvironmentView(ctx context.Context, root string, inputs []InputFingerprint) (private *securefile.PrivateDirectory, retErr error) {
	if isPrivateEnvironmentFile(os.Getenv("GOENV")) {
		return nil, errors.New("GOENV points to a private environment file; explicit admission is required")
	}
	private, err := securefile.MkdirTempPrivate(filepath.Dir(root), ".codegraph-go-env-")
	if err != nil {
		return nil, err
	}
	for _, input := range inputs {
		if !isGoMetadata(input.Path) {
			continue
		}
		if err := copyResolverFile(ctx, root, private.Path(), input.Path, input.SHA256); err != nil {
			return nil, errors.Join(err, private.Cleanup())
		}
	}
	return private, nil
}

func isGoMetadata(rel string) bool {
	switch filepath.Base(rel) {
	case "go.mod", "go.sum", "go.work", "go.work.sum":
		return true
	default:
		return false
	}
}

func normalizeGoEnvironmentPaths(values map[string]string, from, to string) {
	for _, key := range []string{"GOMOD", "GOWORK"} {
		if rel, ok := resolverPathWithin(from, values[key]); ok {
			values[key] = filepath.Join(to, filepath.FromSlash(rel))
		}
	}
}

func goEnvironmentDigest(values map[string]string) (string, error) {
	observed := make(map[string]string, len(values))
	for key, value := range values {
		observed["effective:"+key] = value
	}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GO") || strings.HasPrefix(key, "CGO") || key == "PATH" || key == "CC" || key == "CXX" || key == "FC" || key == "PKG_CONFIG" {
			observed["process:"+key] = value
		}
	}
	encoded, err := json.Marshal(observed)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func goEnvironmentForRoot(values map[string]string, root, snapshot string) []string {
	merged := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		merged[key] = value
	}
	for key, value := range values {
		merged[key] = value
	}
	normalizeGoEnvironmentPaths(merged, root, snapshot)
	if values["GOVERSION"] != "" {
		merged["GOENV"] = "off"
		merged["GOTOOLCHAIN"] = values["GOVERSION"]
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+merged[key])
	}
	return result
}

func goInputReuseReasons(scan repositoryScan) ([]string, error) {
	reasons := make(map[string]bool)
	if !scan.manifest.GoEnvironment.Available {
		reasons["go-environment-unavailable"] = true
	}
	if scan.goEnvironment["GOWORK"] != "" && scan.goEnvironment["GOWORK"] != "off" {
		reasons["go-workspace-inputs-unobserved"] = true
	}
	if scan.goEnvironment["GO111MODULE"] == "off" {
		reasons["go-gopath-inputs-unobserved"] = true
	}
	if driver := scan.goEnvironment["GOPACKAGESDRIVER"]; driver != "" && driver != "off" {
		reasons["go-external-driver-inputs-unobserved"] = true
	}
	flagReasons, err := goFlagReuseReasons(scan.goEnvironment["GOFLAGS"])
	if err != nil {
		return nil, err
	}
	for reason := range flagReasons {
		reasons[reason] = true
	}
	if scan.goEnvironment["GOCACHEPROG"] != "" {
		reasons["go-external-cache-inputs-unobserved"] = true
	}
	if err := observeGoReuseLimits(scan, reasons); err != nil {
		return nil, err
	}
	var result []string
	for reason := range reasons {
		result = append(result, reason)
	}
	sort.Strings(result)
	return result, nil
}

func observeGoReuseLimits(scan repositoryScan, reasons map[string]bool) error {
	root := scan.manifest.CanonicalRoot
	for _, input := range scan.manifest.Inputs {
		if filepath.Base(input.Path) != "go.mod" {
			continue
		}
		content, err := securefile.ReadFile(filepath.Join(root, input.Path))
		if err != nil {
			return err
		}
		module, err := modfile.Parse(input.Path, content, nil)
		if hashBytes(content) != input.SHA256 {
			return fmt.Errorf("go metadata %q changed during observation", input.Path)
		}
		if err != nil || len(module.Require) > 0 || len(module.Replace) > 0 {
			reasons["go-dependency-inputs-unobserved"] = true
		}
	}
	return observeGoSourceReuseLimits(scan, reasons)
}

func observeGoSourceReuseLimits(scan repositoryScan, reasons map[string]bool) error {
	expected := sourceObservationHashes(scan.sourceObservations)
	for _, file := range scan.files {
		if file.Lang != LangGo {
			continue
		}
		content, err := securefile.ReadFile(file.AbsPath)
		if err != nil {
			return err
		}
		if hashBytes(content) != expected[file.RelPath] {
			return fmt.Errorf("go source %q changed during observation", file.RelPath)
		}
		// A potential directive also disables reuse when it appears in a raw
		// string: false negatives would certify unknown inputs; false positives
		// merely pay for a rebuild until a complete compiler-input plan exists.
		if bytes.Contains(content, []byte("//go:embed")) {
			reasons["go-embed-inputs-unobserved"] = true
		}
		if bytes.Contains(content, []byte(`"C"`)) {
			reasons["go-cgo-external-inputs-unobserved"] = true
		}
	}
	return nil
}
