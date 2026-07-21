package index

import (
	"testing"

	"github.com/Lordymine/codegraph/internal/graph"
)

// Contract tests for the IMPORTS pass. They pin the resolution behavior: relative
// specifiers resolve to the right repo file (extensions, parent dirs, index
// files, re-exports); package and unresolved imports produce no edge (honest
// precision).

func hasImport(edges []graph.Edge, src, tgt string) bool {
	for _, e := range edges {
		if e.Type == graph.EdgeImports && e.SourceQN == src && e.TargetQN == tgt {
			return true
		}
	}
	return false
}

func TestImports_TS_RelativeSameDir(t *testing.T) {
	edges := resolveImports("p", []fileSrc{
		{RelPath: "src/foo.ts", Lang: LangTS, Data: []byte("import { B } from './bar'\n")},
		{RelPath: "src/bar.ts", Lang: LangTS, Data: []byte("export class B {}\n")},
	})
	if !hasImport(edges, "p:src/foo.ts", "p:src/bar.ts") {
		t.Fatalf("missing IMPORTS foo.ts->bar.ts; got %+v", edges)
	}
}

func TestImports_TS_ParentDir(t *testing.T) {
	edges := resolveImports("p", []fileSrc{
		{RelPath: "src/a/b.ts", Lang: LangTS, Data: []byte("import { U } from '../lib/util'\n")},
		{RelPath: "src/lib/util.ts", Lang: LangTS, Data: []byte("export const U = 1\n")},
	})
	if !hasImport(edges, "p:src/a/b.ts", "p:src/lib/util.ts") {
		t.Fatalf("missing IMPORTS b.ts->util.ts; got %+v", edges)
	}
}

func TestImports_TS_IndexFile(t *testing.T) {
	edges := resolveImports("p", []fileSrc{
		{RelPath: "src/foo.ts", Lang: LangTS, Data: []byte("import { B } from './bar'\n")},
		{RelPath: "src/bar/index.ts", Lang: LangTS, Data: []byte("export class B {}\n")},
	})
	if !hasImport(edges, "p:src/foo.ts", "p:src/bar/index.ts") {
		t.Fatalf("missing IMPORTS foo.ts->bar/index.ts; got %+v", edges)
	}
}

func TestImports_TS_TsxExtension(t *testing.T) {
	edges := resolveImports("p", []fileSrc{
		{RelPath: "src/app.ts", Lang: LangTS, Data: []byte("import { C } from './Comp'\n")},
		{RelPath: "src/Comp.tsx", Lang: LangTSX, Data: []byte("export const C = 1\n")},
	})
	if !hasImport(edges, "p:src/app.ts", "p:src/Comp.tsx") {
		t.Fatalf("missing IMPORTS app.ts->Comp.tsx; got %+v", edges)
	}
}

func TestImports_TS_ReExportResolved(t *testing.T) {
	edges := resolveImports("p", []fileSrc{
		{RelPath: "src/index.ts", Lang: LangTS, Data: []byte("export { B } from './bar'\n")},
		{RelPath: "src/bar.ts", Lang: LangTS, Data: []byte("export class B {}\n")},
	})
	if !hasImport(edges, "p:src/index.ts", "p:src/bar.ts") {
		t.Fatalf("missing re-export IMPORTS index.ts->bar.ts; got %+v", edges)
	}
}

func TestImports_TS_ExternalPackageDropped(t *testing.T) {
	edges := resolveImports("p", []fileSrc{
		{RelPath: "src/foo.ts", Lang: LangTS, Data: []byte("import { Injectable } from '@nestjs/common'\n")},
	})
	if len(edges) != 0 {
		t.Fatalf("external import must not produce an edge; got %+v", edges)
	}
}

func TestImports_TS_UnresolvedRelativeDropped(t *testing.T) {
	edges := resolveImports("p", []fileSrc{
		{RelPath: "src/foo.ts", Lang: LangTS, Data: []byte("import { B } from './missing'\n")},
	})
	if len(edges) != 0 {
		t.Fatalf("unresolved relative import must drop; got %+v", edges)
	}
}

func TestImports_RubyRequireRelative(t *testing.T) {
	edges := resolveImports("p", []fileSrc{
		{RelPath: "app/models/user.rb", Lang: LangRuby, Data: []byte("require_relative '../services/profile'\nrequire_relative '../services/profile.rb'\nrequire_relative '../missing/file'\nrequire_relative './user'\nrequire 'json'\nrequire_relative dynamic_path\n")},
		{RelPath: "app/services/profile.rb", Lang: LangRuby, Data: []byte("class Profile; end\n")},
	})
	if !hasImport(edges, "p:app/models/user.rb", "p:app/services/profile.rb") {
		t.Fatalf("missing Ruby require_relative edge; got %+v", edges)
	}
	if len(edges) != 1 {
		t.Fatalf("only one resolved non-self require_relative edge expected; got %+v", edges)
	}
}

func TestImports_RubyRequireConfiguredLoadPath(t *testing.T) {
	edges := resolveImports("p", []fileSrc{
		{RelPath: "config/application.rb", Lang: LangRuby, Data: []byte(`config.add_autoload_paths_to_load_path = true
config.autoload_paths << Rails.root.join("lib")
`)},
		{RelPath: "app/services/runner.rb", Lang: LangRuby, Data: []byte(`require "widgets/profile"
require "json"
require dynamic_path
require "./local"
`)},
		{RelPath: "lib/widgets/profile.rb", Lang: LangRuby, Data: []byte("class Profile; end\n")},
	})
	if !hasImport(edges, "p:app/services/runner.rb", "p:lib/widgets/profile.rb") {
		t.Fatalf("missing configured Ruby require edge; got %+v", edges)
	}
	if len(edges) != 1 {
		t.Fatalf("only the configured local require should resolve; got %+v", edges)
	}
}

func TestImports_RubyRequireNeedsProvenLoadPath(t *testing.T) {
	files := []fileSrc{
		{RelPath: "config/application.rb", Lang: LangRuby, Data: []byte(`config.autoload_paths << Rails.root.join("lib")
`)},
		{RelPath: "app/services/runner.rb", Lang: LangRuby, Data: []byte(`require "widgets/profile"`)},
		{RelPath: "lib/widgets/profile.rb", Lang: LangRuby, Data: []byte("class Profile; end\n")},
	}
	if edges := resolveImports("p", files); len(edges) != 0 {
		t.Fatalf("require without a proven Ruby load path must drop; got %+v", edges)
	}

	files[0].Data = []byte(`config.add_autoload_paths_to_load_path = true
config.add_autoload_paths_to_load_path = false
config.autoload_paths << Rails.root.join("lib")
`)
	if edges := resolveImports("p", files); len(edges) != 0 {
		t.Fatalf("ambiguous load-path config must drop; got %+v", edges)
	}
}

func TestImports_RubyRequireHeredocMustNotMatch(t *testing.T) {
	files := []fileSrc{
		{RelPath: "config/application.rb", Lang: LangRuby, Data: []byte(`<<~CONFIG
config.add_autoload_paths_to_load_path = true
config.autoload_paths << Rails.root.join("lib")
CONFIG
`)},
		{RelPath: "lib/widgets/profile.rb", Lang: LangRuby, Data: []byte("class Profile; end\n")},
		{RelPath: "app/services/runner.rb", Lang: LangRuby, Data: []byte(`require "widgets/profile"`)},
	}
	if edges := resolveImports("p", files); len(edges) != 0 {
		t.Fatalf("heredoc content must not enable require; got %+v", edges)
	}
}

func TestImports_RubyRequireConditionalFalseMustDisable(t *testing.T) {
	files := []fileSrc{
		{RelPath: "config/application.rb", Lang: LangRuby, Data: []byte(`if some_condition
  config.add_autoload_paths_to_load_path = true
  config.autoload_paths << Rails.root.join("lib")
end
if other_condition
  config.add_autoload_paths_to_load_path = false
end
`)},
		{RelPath: "lib/widgets/profile.rb", Lang: LangRuby, Data: []byte("class Profile; end\n")},
		{RelPath: "app/services/runner.rb", Lang: LangRuby, Data: []byte(`require "widgets/profile"`)},
	}
	if edges := resolveImports("p", files); len(edges) != 0 {
		t.Fatalf("conditional false guards must disable require; got %+v", edges)
	}
}

func TestImports_RubyRequireStringLiteralMustNotMatch(t *testing.T) {
	files := []fileSrc{
		{RelPath: "config/application.rb", Lang: LangRuby, Data: []byte(`str = "config.add_autoload_paths_to_load_path = true"
str = 'config.autoload_paths << Rails.root.join("lib")'
`)},
		{RelPath: "lib/widgets/profile.rb", Lang: LangRuby, Data: []byte("class Profile; end\n")},
		{RelPath: "app/services/runner.rb", Lang: LangRuby, Data: []byte(`require "widgets/profile"`)},
	}
	if edges := resolveImports("p", files); len(edges) != 0 {
		t.Fatalf("string literal content must not enable require; got %+v", edges)
	}
}

func TestImports_RubyRequireConditionalAutoloadPathDropped(t *testing.T) {
	files := []fileSrc{
		{RelPath: "config/application.rb", Lang: LangRuby, Data: []byte(`config.add_autoload_paths_to_load_path = true
if some_guard
  config.autoload_paths << Rails.root.join("lib")
end
`)},
		{RelPath: "lib/widgets/profile.rb", Lang: LangRuby, Data: []byte("class Profile; end\n")},
		{RelPath: "app/services/runner.rb", Lang: LangRuby, Data: []byte(`require "widgets/profile"`)},
	}
	if edges := resolveImports("p", files); len(edges) != 0 {
		t.Fatalf("conditional autoload path must be dropped; got %+v", edges)
	}
}

func TestImports_RubyRequireConditionalElseMustAlsoDrop(t *testing.T) {
	files := []fileSrc{
		{RelPath: "config/application.rb", Lang: LangRuby, Data: []byte(`config.add_autoload_paths_to_load_path = true
if cond
  config.autoload_paths << Rails.root.join("lib1")
else
  config.autoload_paths << Rails.root.join("lib2")
end
`)},
		{RelPath: "lib/widgets/profile.rb", Lang: LangRuby, Data: []byte("class Profile; end\n")},
		{RelPath: "app/services/runner.rb", Lang: LangRuby, Data: []byte(`require "widgets/profile"`)},
	}
	if edges := resolveImports("p", files); len(edges) != 0 {
		t.Fatalf("conditional autoload path in else must be dropped; got %+v", edges)
	}
}
