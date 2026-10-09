package index

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeGoDependencyFixture builds an application module whose dependency is a
// separate local module outside the repository root, so dependency bytes are
// controllable without touching the real module cache.
func writeGoDependencyFixture(t *testing.T) (root, dependency string) {
	t.Helper()
	root = securityPhysicalTempDir(t)
	dependency = securityPhysicalTempDir(t)
	writeSecurityFile(t, dependency, "go.mod", "module example.test/dep\n\ngo 1.26\n")
	writeSecurityFile(t, dependency, "dep.go", "package dep\n\nfunc Value() int { return 1 }\n")
	writeSecurityFile(t, root, "go.mod", "module example.test/app\n\ngo 1.26\n\nrequire example.test/dep v0.0.0\n\nreplace example.test/dep => "+dependency+"\n")
	writeSecurityFile(t, root, "main.go", "package app\n\nimport \"example.test/dep\"\n\nfunc Run() int { return dep.Value() }\n")
	return root, dependency
}

func TestGoDependencyInputs_CertifyUnchangedAndInvalidateOnDependencyEdit(t *testing.T) {
	root, dependency := writeGoDependencyFixture(t)
	db := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	first, err := RunAtomic(db, root)
	if err != nil || first.Reused || first.Status != StatusHealthy {
		t.Fatalf("first index=%+v err=%v", first, err)
	}
	manifest, err := ReadManifest(db)
	if err != nil || manifest.ResolverInputs.GoDependencies == nil {
		t.Fatalf("dependency inputs were not observed: %+v err=%v", manifest.ResolverInputs, err)
	}
	if manifest.ResolverInputs.GoDependencies.Files == 0 || !validSHA256(manifest.ResolverInputs.GoDependencies.Digest) {
		t.Fatalf("empty or invalid dependency identity: %+v", manifest.ResolverInputs.GoDependencies)
	}
	if slices.Contains(manifest.ResolverInputs.NoReuseReasons, "go-dependency-inputs-unobserved") {
		t.Fatal("observed dependency inputs kept the unobserved reason")
	}
	second, err := RunAtomic(db, root)
	if err != nil || !second.Reused {
		t.Fatalf("unchanged repository was not a certified no-op: %+v err=%v", second, err)
	}
	writeSecurityFile(t, dependency, "dep.go", "package dep\n\nfunc Value() int { return 2 }\n")
	third, err := RunAtomic(db, root)
	if err != nil || third.Reused {
		t.Fatalf("edited dependency was reused: %+v err=%v", third, err)
	}
}

func TestGoDependencyInputs_AddedFileAndMissingDependencyInvalidate(t *testing.T) {
	root, dependency := writeGoDependencyFixture(t)
	db := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	if first, err := RunAtomic(db, root); err != nil || first.Reused {
		t.Fatalf("first index=%+v err=%v", first, err)
	}
	writeSecurityFile(t, dependency, "extra.go", "package dep\n\nfunc Extra() int { return 3 }\n")
	second, err := RunAtomic(db, root)
	if err != nil || second.Reused {
		t.Fatalf("added dependency file was reused: %+v err=%v", second, err)
	}
	if err := os.RemoveAll(dependency); err != nil {
		t.Fatal(err)
	}
	third, err := RunAtomic(db, root)
	if third.Reused {
		t.Fatalf("missing dependency directory was certified as unchanged: %+v err=%v", third, err)
	}
}

func TestGoDependencyInputs_UnreadableDependencyFailsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode-000 files")
	}
	root, dependency := writeGoDependencyFixture(t)
	db := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	if first, err := RunAtomic(db, root); err != nil || first.Reused {
		t.Fatalf("first index=%+v err=%v", first, err)
	}
	unreadable := filepath.Join(dependency, "dep.go")
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })
	second, err := RunAtomic(db, root)
	if second.Reused {
		t.Fatalf("unreadable dependency was certified as unchanged: %+v err=%v", second, err)
	}
}

func TestGoDependencyInputs_ProbeFailureFallsBackToEnumeration(t *testing.T) {
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "go.mod", "module example.test/probe\n\ngo 1.26\n\nrequire example.test/ghost v1.0.0\n")
	writeSecurityFile(t, root, "main.go", "package probe\n\nfunc Run() int { return 1 }\n")
	db := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	first, err := RunAtomic(db, root)
	if err != nil || first.Reused || first.Status != StatusHealthy {
		t.Fatalf("first index=%+v err=%v", first, err)
	}
	manifest, err := ReadManifest(db)
	if err != nil || manifest.ResolverInputs.GoDependencies == nil {
		t.Fatalf("probe failure did not fall back to enumeration: %+v err=%v", manifest.ResolverInputs, err)
	}
	if slices.Contains(manifest.ResolverInputs.NoReuseReasons, "go-dependency-inputs-unobserved") {
		t.Fatal("unimported require blocked certification")
	}
	second, err := RunAtomic(db, root)
	if err != nil || !second.Reused {
		t.Fatalf("unchanged probe fixture was not a certified no-op: %+v err=%v", second, err)
	}
}

func TestGoDependencyInputs_VendorBytesCertifyAndInvalidate(t *testing.T) {
	root, _ := writeGoDependencyFixture(t)
	command := exec.Command("go", "mod", "vendor")
	command.Dir = root
	command.Env = append(os.Environ(), "GOPROXY=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go mod vendor: %v (%s)", err, output)
	}
	db := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	first, err := RunAtomic(db, root)
	if err != nil || first.Reused || first.Status != StatusHealthy {
		t.Fatalf("first vendor index=%+v err=%v", first, err)
	}
	manifest, err := ReadManifest(db)
	if err != nil || manifest.ResolverInputs.GoDependencies == nil {
		t.Fatalf("vendored dependency inputs were not certified: %+v err=%v", manifest.ResolverInputs, err)
	}
	second, err := RunAtomic(db, root)
	if err != nil || !second.Reused {
		t.Fatalf("unchanged vendor tree was not a certified no-op: %+v err=%v", second, err)
	}
	writeSecurityFile(t, root, "vendor/example.test/dep/dep.go", "package dep\n\nfunc Value() int { return 9 }\n")
	third, err := RunAtomic(db, root)
	if err != nil || third.Reused {
		t.Fatalf("edited vendor bytes were reused: %+v err=%v", third, err)
	}
}

func TestParseGoListDependencyFiles_FiltersRootStdlibAndGenerated(t *testing.T) {
	root := "/work/repo"
	goroot := "/tools/go"
	output := strings.Join([]string{
		"true\t/tools/go/src/fmt\tprint.go\t\t\t\t\t",
		"false\t/work/repo\tmain.go\t\t\t\t\t",
		"false\t/cache/mod/example.com/dep@v1.0.0\tdep.go gen.go\tcgo.go\tc.c\tcxx.cc\th.h\ts.s",
		"false\t/cache/mod/example.com/dep@v1.0.0\t\t\t\t\t\t",
		"false\t/cache/mod/example.com/dep@v1.0.0\t/tmp/gocache/generated-d\t\t\t\t\t",
	}, "\n")
	files, err := parseGoListDependencyFiles(root, goroot, output)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/cache/mod/example.com/dep@v1.0.0/c.c",
		"/cache/mod/example.com/dep@v1.0.0/cgo.go",
		"/cache/mod/example.com/dep@v1.0.0/cxx.cc",
		"/cache/mod/example.com/dep@v1.0.0/dep.go",
		"/cache/mod/example.com/dep@v1.0.0/gen.go",
		"/cache/mod/example.com/dep@v1.0.0/h.h",
		"/cache/mod/example.com/dep@v1.0.0/s.s",
	}
	if !slices.Equal(files, want) {
		t.Fatalf("files=%v want=%v", files, want)
	}
}

func TestGoListModArgs_KeepVendorAndPreventRewrite(t *testing.T) {
	if got := goListModArgs(map[string]string{"GOFLAGS": "-mod=vendor"}); !slices.Equal(got, []string{"-mod=vendor"}) {
		t.Fatalf("vendor selection lost: %v", got)
	}
	if got := goListModArgs(map[string]string{"GOFLAGS": "--mod=mod"}); !slices.Equal(got, []string{"-mod=readonly"}) {
		t.Fatalf("module rewrite not prevented: %v", got)
	}
	if got := goListModArgs(map[string]string{}); got != nil {
		t.Fatalf("default mode must stay with the go command default: %v", got)
	}
}

func TestGoSourceSpecials_LiteralsNoLongerBlockDependencyCertification(t *testing.T) {
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "go.mod", "module example.test/literals\n\ngo 1.26\n")
	writeSecurityFile(t, root, "main.go", "package main\n\nvar flag = \"C\"\nvar pattern = `//go:embed assets/*.txt`\n\nfunc main() {}\n")
	scan, err := scanRepositoryContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{"go-cgo-external-inputs-unobserved", "go-embed-inputs-unobserved"} {
		if slices.Contains(scan.manifest.ResolverInputs.NoReuseReasons, reason) {
			t.Fatalf("literal source blocked certification with %q: %v", reason, scan.manifest.ResolverInputs.NoReuseReasons)
		}
	}
}

func TestGoSourceSpecials_RealCgoImportKeepsExternalInputReason(t *testing.T) {
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "go.mod", "module example.test/cgo\n\ngo 1.26\n")
	writeSecurityFile(t, root, "main.go", "package main\n\n/*\n#include <stdlib.h>\n*/\nimport \"C\"\n\nfunc main() { C.free(nil) }\n")
	scan, err := scanRepositoryContext(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(scan.manifest.ResolverInputs.NoReuseReasons, "go-cgo-external-inputs-unobserved") {
		t.Fatalf("real cgo import lost the external input reason: %v", scan.manifest.ResolverInputs.NoReuseReasons)
	}
}
