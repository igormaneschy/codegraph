package quality

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var sourceBoundary = regexp.MustCompile(`(?i)\.(?:go|tsx?|jsx?|mjs|cjs|rb|rake|ru|rbi)(?:\.|$)`)

// Identity is deliberately not normalized. The oracle derives declarations from
// source; accepting a bare name/project prefix and guessing its identity would
// reintroduce homonym collisions or make the graph its own ground truth.
func validateQualifiedName(qn string) error {
	if !utf8.ValidString(qn) || strings.ContainsFunc(qn, unicode.IsControl) {
		return fmt.Errorf("want a UTF-8 repository-qualified name without control characters")
	}
	file := qualifiedNameFile(qn)
	if file == "" {
		return fmt.Errorf("want a project-stripped repository QName such as src/file.go.Owner.Method or lib/file.rb.Owner#method")
	}
	return validateRelativeFile(file)
}

func qualifiedNameFile(qn string) string {
	// Symbol suffixes are opaque: TS quoted/computed method names may contain
	// slashes or spaces. Never interpret those as directories or normalize them.
	match := sourceBoundary.FindStringIndex(qn)
	if match == nil {
		return ""
	}
	boundary := match[1]
	if qn[boundary-1] != '.' {
		return qn
	}
	if boundary == len(qn) {
		return ""
	}
	return qn[:boundary-1]
}

func validateRelativeFile(file string) error {
	if !utf8.ValidString(file) || file == "" || file == "." || path.IsAbs(file) || path.Clean(file) != file || file == ".." || strings.HasPrefix(file, "../") || strings.ContainsAny(file, "\\:") || strings.ContainsFunc(file, unicode.IsControl) {
		return fmt.Errorf("want a canonical UTF-8 repository-relative path without project prefix, drive, traversal or controls")
	}
	return nil
}

func validateLocation(location string) error {
	file, number, found := strings.Cut(location, ":")
	if !found {
		return fmt.Errorf("want a declaration location relpath:positive-line")
	}
	if err := validateRelativeFile(file); err != nil {
		return err
	}
	line, err := strconv.Atoi(number)
	if err != nil || line <= 0 || strconv.Itoa(line) != number {
		return fmt.Errorf("want a positive canonical declaration line, got %q", number)
	}
	return nil
}

func exactSet(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[item] = true
	}
	return set
}

func strictDefinition(answer, truth []string) bool {
	return len(answer) == 1 && answer[0] == truth[0]
}
