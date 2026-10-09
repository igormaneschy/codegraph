package index

import "testing"

func TestGoSourceSpecialInputs_ReportRealDirectivesNotLiterals(t *testing.T) {
	cases := []struct {
		name   string
		source string
		embed  bool
		cgo    bool
	}{
		{"cgo literal", "package p\n\nvar flag = \"C\"\n", false, false},
		{"embed literal", "package p\n\nvar pattern = `//go:embed assets/*.txt`\n", false, false},
		{"cgo import", "package p\n\n/*\n#include <stdlib.h>\n*/\nimport \"C\"\n\nfunc Run() {}\n", false, true},
		{"embed directive", "package p\n\nimport \"embed\"\n\n//go:embed assets/*.txt\nvar assets embed.FS\n", true, false},
		{"directive-like block comment", "package p\n\n/* //go:embed assets/*.txt */\nvar x int\n", false, false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			embed, cgo := goSourceSpecialInputs("fixture.go", []byte(item.source))
			if embed != item.embed || cgo != item.cgo {
				t.Fatalf("embed=%v cgo=%v want embed=%v cgo=%v", embed, cgo, item.embed, item.cgo)
			}
		})
	}
}

func TestGoSourceSpecialInputs_ParseFailureKeepsCandidateBytes(t *testing.T) {
	embed, cgo := goSourceSpecialInputs("broken.go", []byte("package p\n\n//go:embed assets\nvar x = \"C\"\nfunc {\n"))
	if !embed || !cgo {
		t.Fatalf("parse failure must stay conservative: embed=%v cgo=%v", embed, cgo)
	}
}
