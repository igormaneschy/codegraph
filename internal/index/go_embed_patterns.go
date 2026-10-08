package index

import (
	"bytes"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// goEmbedPatterns reads real comments, not lookalike directives in strings.
// Invalid sources/arguments remain the compiler's diagnostic responsibility;
// the conservative embed reuse exclusion still applies to their source bytes.
func goEmbedPatterns(filename string, content []byte) []string {
	if !bytes.Contains(content, []byte("//go:embed")) {
		return nil
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), filename, content, parser.ParseComments)
	if err != nil {
		return nil
	}
	hasEmbed := false
	for _, imported := range parsed.Imports {
		name, err := strconv.Unquote(imported.Path.Value)
		hasEmbed = hasEmbed || err == nil && name == "embed"
	}
	if !hasEmbed {
		return nil
	}
	var patterns []string
	for _, group := range parsed.Comments {
		for _, comment := range group.List {
			patterns = append(patterns, goEmbedCommentPatterns(comment.Text)...)
		}
	}
	return patterns
}

func goEmbedCommentPatterns(comment string) []string {
	arguments, ok := strings.CutPrefix(comment, "//go:embed")
	if !ok || arguments == "" || !startsGoEmbedWhitespace(arguments) {
		return nil
	}
	var patterns []string
	for arguments = strings.TrimSpace(arguments); arguments != ""; arguments = strings.TrimSpace(arguments) {
		pattern, rest, err := nextGoEmbedPattern(arguments)
		if err != nil {
			return nil
		}
		patterns = append(patterns, pattern)
		arguments = rest
	}
	return patterns
}

func startsGoEmbedWhitespace(text string) bool {
	first, _ := utf8.DecodeRuneInString(text)
	return unicode.IsSpace(first)
}

func nextGoEmbedPattern(arguments string) (string, string, error) {
	if arguments[0] != '"' && arguments[0] != '`' {
		end := strings.IndexFunc(arguments, unicode.IsSpace)
		if end < 0 {
			return arguments, "", nil
		}
		return arguments[:end], arguments[end:], nil
	}
	quoted, err := strconv.QuotedPrefix(arguments)
	if err != nil {
		return "", "", err
	}
	pattern, err := strconv.Unquote(quoted)
	rest := arguments[len(quoted):]
	if rest != "" && !startsGoEmbedWhitespace(rest) {
		return "", "", strconv.ErrSyntax
	}
	return pattern, rest, err
}
