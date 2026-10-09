package index

import (
	"bytes"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
)

// goSourceSpecialInputs reports whether a Go source actually contains an
// //go:embed directive or an import "C" declaration. The previous byte
// heuristic matched those sequences inside strings and raw strings, so
// unrelated files (including this repository's own flag table and embed
// fixtures) disabled no-op for the whole repository. A parse failure keeps the
// candidate bytes' verdicts: false negatives would certify unknown inputs,
// false positives merely pay for a rebuild.
func goSourceSpecialInputs(filename string, content []byte) (embedDirective, cgoImport bool) {
	candidateEmbed := bytes.Contains(content, []byte("//go:embed"))
	candidateCgo := bytes.Contains(content, []byte(`"C"`))
	if !candidateEmbed && !candidateCgo {
		return false, false
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), filename, content, parser.ParseComments)
	if err != nil {
		return candidateEmbed, candidateCgo
	}
	if candidateCgo {
		for _, imported := range parsed.Imports {
			name, unquoteErr := strconv.Unquote(imported.Path.Value)
			if unquoteErr == nil && name == "C" {
				cgoImport = true
				break
			}
		}
	}
	if candidateEmbed {
		for _, group := range parsed.Comments {
			for _, comment := range group.List {
				if isGoEmbedDirective(comment.Text) {
					embedDirective = true
					break
				}
			}
			if embedDirective {
				break
			}
		}
	}
	return embedDirective, cgoImport
}

func isGoEmbedDirective(comment string) bool {
	arguments, ok := strings.CutPrefix(comment, "//go:embed")
	return ok && arguments != "" && startsGoEmbedWhitespace(arguments)
}
