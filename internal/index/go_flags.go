package index

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const goFlagWhitespace = " \t\r\n"

type goBuildFlag struct {
	name     string
	value    string
	hasValue bool
}

func validateProcessGoFlags(environment []string) error {
	for _, entry := range environment {
		if raw, ok := strings.CutPrefix(entry, "GOFLAGS="); ok {
			_, err := goFlagReuseReasons(raw)
			return err
		}
	}
	return nil
}

// splitGoFlags mirrors cmd/internal/quoted.Split, not shell tokenization:
// whole-field quotes delimit values; inner quotes and backslashes are literal.
func splitGoFlags(raw string) ([]string, error) {
	var fields []string
	for remaining := strings.TrimLeft(raw, goFlagWhitespace); remaining != ""; remaining = strings.TrimLeft(remaining, goFlagWhitespace) {
		field, tail, err := takeGoFlagField(remaining)
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
		remaining = tail
	}
	return fields, nil
}

func takeGoFlagField(remaining string) (string, string, error) {
	if remaining[0] == '\'' || remaining[0] == '"' {
		end := strings.IndexByte(remaining[1:], remaining[0])
		if end < 0 {
			return "", "", errors.New("GOFLAGS has an unterminated whole-field quote; expected a closing quote")
		}
		return remaining[1 : end+1], remaining[end+2:], nil
	}
	end := strings.IndexAny(remaining, goFlagWhitespace)
	if end < 0 {
		return remaining, "", nil
	}
	return remaining[:end], remaining[end:], nil
}

func parseGoBuildFlag(field string) (goBuildFlag, error) {
	if !strings.HasPrefix(field, "-") || field == "-" || field == "--" || strings.HasPrefix(field, "---") || strings.HasPrefix(field, "-=") || strings.HasPrefix(field, "--=") {
		return goBuildFlag{}, errors.New("expected a standalone -flag or -flag=value (one/two leading dashes)")
	}
	name, value, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(field, "-"), "-"), "=")
	return goBuildFlag{name: name, value: value, hasValue: hasValue}, nil
}

func goFlagReuseReasons(raw string) (map[string]bool, error) {
	fields, err := splitGoFlags(raw)
	if err != nil {
		return nil, err
	}
	reasons := make(map[string]bool)
	for position, field := range fields {
		flag, err := parseGoBuildFlag(field)
		if err != nil {
			return nil, fmt.Errorf("GOFLAGS field %d: %w", position+1, err)
		}
		if err := classifyGoBuildFlag(flag, reasons); err != nil {
			return nil, err
		}
	}
	return reasons, nil
}

func classifyGoBuildFlag(flag goBuildFlag, reasons map[string]bool) error {
	switch flag.name {
	case "overlay", "modfile", "C":
		return fmt.Errorf("go -%s inputs are not admitted to the resolver snapshot; remove this GOFLAGS flag", flag.name)
	case "compiler":
		if flag.value == "gccgo" {
			reasons["go-external-compiler-inputs-unobserved"] = true
		}
	}
	// A versioned finite allowlist avoids certifying a newly added flag whose
	// input files/commands we do not observe. Values stay out of diagnostics.
	if !identityOnlyGoBuildFlag(flag) {
		reasons["go-build-flags-inputs-unobserved"] = true
	}
	return nil
}

func identityOnlyGoBuildFlag(flag goBuildFlag) bool {
	switch flag.name {
	case "tags":
		return flag.hasValue && flag.value != ""
	case "p":
		count, err := strconv.Atoi(flag.value)
		return err == nil && count > 0
	case "trimpath":
		if !flag.hasValue {
			return true
		}
		_, err := strconv.ParseBool(flag.value)
		return err == nil
	default:
		return identityOnlyGoFlagValue(flag)
	}
}

func identityOnlyGoFlagValue(flag goBuildFlag) bool {
	switch flag.name {
	case "mod":
		return flag.value == "readonly" || flag.value == "vendor" || flag.value == "mod"
	case "compiler":
		return flag.value == "gc"
	case "pgo":
		return flag.value == "off"
	case "buildvcs":
		return flag.value == "false"
	default:
		return false
	}
}
