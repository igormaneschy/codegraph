package index

import (
	"slices"
	"testing"
)

func TestGoFlagGrammarMatchesWholeFieldQuotesNotShellEscapes(t *testing.T) {
	cases := []struct {
		raw    string
		fields []string
	}{
		{"", nil},
		{"\t-one\r\n--two=three", []string{"-one", "--two=three"}},
		{`"--tags=a b" '-p=2'`, []string{"--tags=a b", "-p=2"}},
		{`-tags='a'`, []string{"-tags='a'"}},
		{`"-tags=a\\b"`, []string{`-tags=a\\b`}},
		{`"-tags=a"-p=2`, []string{"-tags=a", "-p=2"}},
		{"-tags=a\u00a0-p=2", []string{"-tags=a\u00a0-p=2"}},
		{`""`, []string{""}},
	}
	for _, item := range cases {
		fields, err := splitGoFlags(item.raw)
		if err != nil || !slices.Equal(fields, item.fields) {
			t.Fatalf("fields=%v expected=%v err=%v", fields, item.fields, err)
		}
	}
	for _, raw := range []string{`"-tags=a`, "'-p=2"} {
		if _, err := splitGoFlags(raw); err == nil {
			t.Fatal("unterminated quote accepted")
		}
	}
}

func TestGoFlagProcessAdmissionUsesCapturedVector(t *testing.T) {
	if err := validateProcessGoFlags([]string{"IGNORED=value", "GOFLAGS='--overlay=/unopened/private.env'"}); err == nil {
		t.Fatal("captured unadmitted flags accepted")
	}
	for _, environment := range [][]string{nil, {"PATH=/tools"}, {"GOFLAGS=-tags=alternate"}, {"GOFLAGS=--toolexec=/wrapper"}} {
		if err := validateProcessGoFlags(environment); err != nil {
			t.Fatalf("process classification=%v", err)
		}
	}
}

func TestGoIdentityFlagValueBoundariesNeverCertifyInvalidValues(t *testing.T) {
	for _, raw := range []string{"-p=-1", "-p=999999999999999999999999", "-trimpath=not-bool", "-tags=", "-mod=", "-compiler=", "-pgo=auto", "-buildvcs=auto"} {
		reasons, err := goFlagReuseReasons(raw)
		if err != nil || !reasons["go-build-flags-inputs-unobserved"] {
			t.Fatalf("unproved value admitted: reasons=%v err=%v", reasons, err)
		}
	}
}
