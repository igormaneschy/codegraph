package index

import (
	"context"
	"slices"
	"testing"
)

func TestGoEnvironment_UnavailableToolDisablesCertification(t *testing.T) {
	root := writeGoBuildTagFixture(t)
	t.Setenv("PATH", securityPhysicalTempDir(t))
	scan, err := scanRepositoryContext(context.Background(), root)
	if err != nil || scan.manifest.GoEnvironment.Available || !validSHA256(scan.manifest.GoEnvironment.Digest) {
		t.Fatalf("unavailable environment identity=%+v err=%v", scan.manifest.GoEnvironment, err)
	}
	if !slices.Contains(scan.manifest.ResolverInputs.NoReuseReasons, "go-environment-unavailable") {
		t.Fatal("unavailable environment was not excluded from certification")
	}
}

func TestGoInputCoverage_UnobservedInputsAreNotReuseCertificates(t *testing.T) {
	for _, reason := range []string{"go-workspace-inputs-unobserved", "go-gopath-inputs-unobserved", "go-external-driver-inputs-unobserved", "go-external-compiler-inputs-unobserved"} {
		t.Run(reason, func(t *testing.T) {
			scan := repositoryScan{manifest: Manifest{GoEnvironment: GoEnvironmentIdentity{Available: true}}, goEnvironment: make(map[string]string)}
			switch reason {
			case "go-workspace-inputs-unobserved":
				scan.goEnvironment["GOWORK"] = "/workspace/go.work"
			case "go-gopath-inputs-unobserved":
				scan.goEnvironment["GO111MODULE"] = "off"
			case "go-external-driver-inputs-unobserved":
				scan.goEnvironment["GOPACKAGESDRIVER"] = "/tools/custom-driver"
			case "go-external-compiler-inputs-unobserved":
				scan.goEnvironment["GOFLAGS"] = "-compiler=gccgo"
			}
			reasons, err := goInputReuseReasons(scan)
			if err != nil || !slices.Contains(reasons, reason) {
				t.Fatalf("coverage reasons=%v err=%v, want %q", reasons, err, reason)
			}
		})
	}
}
