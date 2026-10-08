package index

import (
	"reflect"
	"testing"
)

func TestGoEmbedPatterns_OnlyImportedRealCommentDirectives(t *testing.T) {
	for _, source := range []string{
		"package p\nimport _ \"embed\"\nvar text = `//go:embed private.txt`\n",
		"package p\n//go:embed private.txt\nvar text string\n",
		"package p\nimport _ \"embed\"\n//go:embedx private.txt\nvar text string\n",
		"package p\nimport _ \"embed\"\n/* //go:embed private.txt */\nvar text string\n",
		"package p\nimport _ \"embed\"\n//go:embed private.txt\ninvalid syntax\n",
	} {
		if patterns := goEmbedPatterns("source.go", []byte(source)); len(patterns) != 0 {
			t.Errorf("lookalike/invalid source selected %v", patterns)
		}
	}
}

func TestGoEmbedPatterns_ParseQuotedMultipleRepeatedArguments(t *testing.T) {
	source := "package p\nimport \"embed\"\n//go:embed one.txt \"space name.txt\" `raw name.txt`\n//go:embed all:assets\nvar files embed.FS\n"
	want := []string{"one.txt", "space name.txt", "raw name.txt", "all:assets"}
	if actual := goEmbedPatterns("source.go", []byte(source)); !reflect.DeepEqual(actual, want) {
		t.Fatalf("patterns=%v want=%v", actual, want)
	}
}

func TestGoEmbedPatterns_InvalidArgumentsAreLeftToCompiler(t *testing.T) {
	for _, comment := range []string{"//go:embed", "//go:embed \"unterminated", "//go:embed \"quoted\"tail"} {
		if patterns := goEmbedCommentPatterns(comment); len(patterns) != 0 {
			t.Errorf("invalid directive %q selected %v", comment, patterns)
		}
	}
}

func TestGoEmbedInputs_MissingPatternProducesExplicitDegradedFirstGraph(t *testing.T) {
	root := writeGoEmbedFixture(t, "missing.txt")
	db := securityPhysicalTempDir(t) + "/graph.db"
	result, err := RunAtomic(db, root)
	if err != nil || result.Status != StatusDegraded || len(result.Resolver.Scopes) != 1 || !result.Resolver.Scopes[0].Failed {
		t.Fatalf("missing active embed pattern result=%+v err=%v, want explicit resolver failure", result, err)
	}
}
