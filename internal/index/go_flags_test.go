package index

import (
	"slices"
	"strings"
	"testing"
)

func flagsScan(flags string) repositoryScan {
	return repositoryScan{manifest: Manifest{GoEnvironment: GoEnvironmentIdentity{Available: true}}, goEnvironment: map[string]string{"GOFLAGS": flags}}
}

func TestGoFlagsRejectUnadmittedPathsInEveryGoSpelling(t *testing.T) {
	for _, flags := range []string{
		"-overlay=/never-open/private.env", "--overlay=/never-open/private.env",
		`"-overlay=/never open/private.env"`, `'--overlay=/never open/private.env'`,
		"--modfile=/never-open/private.env", `'--modfile=/never open/private.env'`,
		"-C=/never-open", `"--C=/never open"`,
	} {
		t.Run(flags, func(t *testing.T) {
			_, err := goInputReuseReasons(flagsScan(flags))
			if err == nil || !strings.Contains(err.Error(), "not admitted") {
				t.Fatalf("flag spelling was admitted: %v", err)
			}
			if strings.Contains(err.Error(), "never open") || strings.Contains(err.Error(), "private.env") {
				t.Fatal("flag value leaked into admission diagnostic")
			}
		})
	}
}

func TestGoFlagsExternalAndFutureInputsCannotCertifyReuse(t *testing.T) {
	for _, flags := range []string{
		"-toolexec=/tools/wrapper", `'--toolexec=/tools/wrapper secret-token'`,
		"--pkgdir=/packages", "-pgo=/profiles/input.pprof", "-pgo=auto",
		"-gcflags=all=-importcfg=/private/input", "--asmflags=all=-I/private", "-ldflags=-extld=/tools/linker", "-gccgoflags=-I/private",
		"-futureflag=secret-token", "-race", "-compiler=other",
	} {
		t.Run(flags, func(t *testing.T) {
			reasons, err := goInputReuseReasons(flagsScan(flags))
			if err != nil || !slices.Contains(reasons, "go-build-flags-inputs-unobserved") {
				t.Fatalf("uncertified flags accepted: reasons=%v err=%v", reasons, err)
			}
		})
	}
	for _, flags := range []string{"--compiler=gccgo", `"-compiler=gccgo"`, `'--compiler=gccgo'`} {
		reasons, err := goInputReuseReasons(flagsScan(flags))
		if err != nil || !slices.Contains(reasons, "go-external-compiler-inputs-unobserved") {
			t.Fatalf("gccgo reason=%v err=%v", reasons, err)
		}
	}
}

func TestGoFlagsMalformedFieldsFailWithoutLeakingValues(t *testing.T) {
	for _, flags := range []string{`"secret-token`, "secret-token", "-tags=good secret-token", "---secret-token=value", "--=secret-token", "-", "--"} {
		_, err := goInputReuseReasons(flagsScan(flags))
		if err == nil {
			t.Fatal("malformed flags accepted")
		}
		if strings.Contains(err.Error(), "secret-token") {
			t.Fatal("raw malformed flag leaked")
		}
	}
}

func TestGoFlagsAdmittedIdentityOnlyValues(t *testing.T) {
	for _, flags := range []string{"", "-tags=alternate", `"--tags=alternate other"`, "\t-tags=alternate\r\n-p=2", "--mod=readonly -trimpath --compiler=gc -pgo=off", "-mod=vendor", "-mod=mod", "-trimpath=false -buildvcs=false"} {
		reasons, err := goInputReuseReasons(flagsScan(flags))
		if err != nil || len(reasons) != 0 {
			t.Fatalf("identity-only flags rejected: reasons=%v err=%v", reasons, err)
		}
	}
	for _, flags := range []string{"-tags", "-p=zero", "-p=0", "-trimpath=bad", "-mod=invalid", "-buildvcs=true"} {
		reasons, err := goInputReuseReasons(flagsScan(flags))
		if err == nil && len(reasons) == 0 {
			t.Fatal("unproved flag value certified")
		}
	}
}

func TestGoExternalCacheEffectiveCommandCannotCertifyReuse(t *testing.T) {
	scan := flagsScan("")
	scan.goEnvironment["GOCACHEPROG"] = "/cache/driver --credential=secret-token"
	reasons, err := goInputReuseReasons(scan)
	if err != nil || !slices.Contains(reasons, "go-external-cache-inputs-unobserved") {
		t.Fatalf("cache reason=%v err=%v", reasons, err)
	}
	if strings.Contains(strings.Join(reasons, ","), "secret-token") {
		t.Fatal("cache command leaked into reasons")
	}
}
