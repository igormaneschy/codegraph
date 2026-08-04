package graph

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestReplaceProject_AllowsReindex is a regression test for the contentless-FTS5
// bug: ReplaceProject used `DELETE FROM nodes_fts`, which SQLite rejects on a
// contentless FTS5 table, so the SECOND index of a repo failed with
// "cannot DELETE from contentless fts5 table". A re-index must succeed and leave
// the FTS index searchable.
func TestReplaceProject_AllowsReindex(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	nodes := []Node{{
		Project: "p", Label: LabelFunction, Name: "Foo",
		QualifiedName: "p:f.go.Foo", FilePath: "f.go", StartLine: 1, EndLine: 3,
	}}
	if err := s.InsertNodes(nodes); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// The re-index path that used to fail.
	if err := s.ReplaceProject("p"); err != nil {
		t.Fatalf("replace project: %v", err)
	}
	if err := s.InsertNodes(nodes); err != nil {
		t.Fatalf("second insert: %v", err)
	}

	hits, err := s.Search("p", "Foo", "", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1 (FTS must survive a reindex)", len(hits))
	}
}

func TestForEachCallEdge_Streams(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	nodes := []Node{
		{Project: "p", Label: LabelFunction, Name: "A", QualifiedName: "p:a.go.A", FilePath: "a.go"},
		{Project: "p", Label: LabelFunction, Name: "B", QualifiedName: "p:a.go.B", FilePath: "a.go"},
	}
	if err := s.InsertNodes(nodes); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.InsertEdges([]Edge{
		{Project: "p", SourceQN: "p:a.go.A", TargetQN: "p:a.go.B", Type: EdgeCalls},
	}); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := s.ForEachCallEdge("p", func(ce CallEdge) error {
		n++
		if ce.SourceQN != "p:a.go.A" || ce.TargetQN != "p:a.go.B" || ce.SourceFile != "a.go" {
			t.Fatalf("unexpected edge %+v", ce)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("got %d edges, want 1", n)
	}
}

func TestRubyAnalysisCurrent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	t.Run("no Ruby files", func(t *testing.T) {
		current, err := s.RubyAnalysisCurrent("empty", 1)
		if err != nil {
			t.Fatal(err)
		}
		if !current {
			t.Error("a project without Ruby files must not require a Ruby analysis migration")
		}
	})

	if err := s.InsertNodes([]Node{{
		Project: "old", Label: LabelFile, Name: "old.rb", QualifiedName: "old:old.rb", FilePath: "old.rb",
		Props: map[string]any{"lang": "ruby"},
	}}); err != nil {
		t.Fatal(err)
	}
	current, err := s.RubyAnalysisCurrent("old", 1)
	if err != nil {
		t.Fatal(err)
	}
	if current {
		t.Error("Ruby file without an analysis version must invalidate the old graph")
	}

	if err := s.InsertNodes([]Node{{
		Project: "legacy", Label: LabelFile, Name: "legacy.rb", QualifiedName: "legacy:legacy.rb", FilePath: "legacy.rb",
		Props: map[string]any{"lang": "ruby", "ruby_static_calls_version": 1},
	}}); err != nil {
		t.Fatal(err)
	}
	current, err = s.RubyAnalysisCurrent("legacy", 1)
	if err != nil {
		t.Fatal(err)
	}
	if current {
		t.Error("legacy static-call metadata must invalidate the graph for Ruby analysis upgrades")
	}

	if err := s.InsertNodes([]Node{{
		Project: "current", Label: LabelFile, Name: "current.rb", QualifiedName: "current:current.rb", FilePath: "current.rb",
		Props: map[string]any{"lang": "ruby", "ruby_analysis_version": 1},
	}}); err != nil {
		t.Fatal(err)
	}
	current, err = s.RubyAnalysisCurrent("current", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !current {
		t.Error("Ruby file at the current analysis version must permit a no-op")
	}
}

// TestStore_ReadSnapshotPreservesPreWipeCalls pins the Run reuse path: a second
// connection with an active read snapshot still sees CALLS edges after ReplaceProject
// wipes them on the writer connection.
func TestStore_ReadSnapshotPreservesPreWipeCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	nodes := []Node{
		{Project: "p", Label: LabelFunction, Name: "A", QualifiedName: "p:a.go.A", FilePath: "a.go"},
		{Project: "p", Label: LabelFunction, Name: "B", QualifiedName: "p:a.go.B", FilePath: "a.go"},
	}
	if err := writer.InsertNodes(nodes); err != nil {
		t.Fatal(err)
	}
	if _, _, err := writer.InsertEdges([]Edge{
		{Project: "p", SourceQN: "p:a.go.A", TargetQN: "p:a.go.B", Type: EdgeCalls},
	}); err != nil {
		t.Fatal(err)
	}

	reader, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := reader.BeginReadSnapshot(); err != nil {
		t.Fatal(err)
	}
	defer reader.EndReadSnapshot()

	if err := writer.ReplaceProject("p"); err != nil {
		t.Fatal(err)
	}
	if err := writer.InsertNodes(nodes[:1]); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := reader.ForEachCallEdge("p", func(ce CallEdge) error {
		n++
		if ce.SourceQN != "p:a.go.A" || ce.TargetQN != "p:a.go.B" {
			t.Fatalf("unexpected edge %+v", ce)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("snapshot saw %d CALLS edges, want 1 (pre-wipe graph)", n)
	}
}

func TestStore_Reopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	nodes := []Node{{
		Project: "p", Label: LabelFunction, Name: "Foo",
		QualifiedName: "p:f.go.Foo", FilePath: "f.go",
	}}
	if err := s.InsertNodes(nodes); err != nil {
		t.Fatal(err)
	}
	if s.DBPath() != path {
		t.Fatalf("DBPath = %q, want %q", s.DBPath(), path)
	}
	if err := s.Reopen(path); err != nil {
		t.Fatal(err)
	}
	n, _, err := s.Stats("p")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("after Reopen: nodes=%d, want 1", n)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStore_PrivateDatabaseArtifactsAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InsertNodes([]Node{{
		Project: "p", Label: LabelFunction, Name: "Private", QualifiedName: "p:private.go.Private",
		FilePath: "private.go",
	}}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			info, err := os.Stat(path + suffix)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				t.Fatalf("stat artifact %q: %v", suffix, err)
			}
			if got := info.Mode().Perm(); got != 0o600 {
				t.Fatalf("artifact %q mode=%o, want 600", suffix, got)
			}
		}
	}
	if err := s.Reopen(path); err != nil {
		t.Fatalf("reopen private store: %v", err)
	}
	n, _, err := s.Stats("p")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reopened nodes=%d, want 1", n)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("database mode after close=%o, want 600", got)
		}
	}
}

// TestStore_CheckpointRejectsBusyReader keeps a WAL snapshot open while a
// second connection appends a frame. The checkpoint must report SQLite's
// result row instead of pretending that TRUNCATE completed; the WAL remains
// available until the reader releases its snapshot.
func TestStore_CheckpointRejectsBusyReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	if err := writer.InsertNodes([]Node{{
		Project: "p", Label: LabelFunction, Name: "Before", QualifiedName: "p:a.go.Before", FilePath: "a.go",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := reader.BeginReadSnapshot(); err != nil {
		t.Fatal(err)
	}
	if err := writer.InsertNodes([]Node{{
		Project: "p", Label: LabelFunction, Name: "After", QualifiedName: "p:a.go.After", FilePath: "a.go",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("writer append did not leave a WAL fixture: %v", err)
	}

	if err := writer.Checkpoint(); err == nil {
		t.Fatal("busy reader checkpoint unexpectedly succeeded")
	} else if !strings.Contains(err.Error(), "WAL checkpoint") {
		t.Fatalf("checkpoint error = %v, want result-row validation", err)
	}
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("failed busy checkpoint removed the WAL sidecar: %v", err)
	}

	if err := reader.EndReadSnapshot(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Checkpoint(); err != nil {
		t.Fatalf("checkpoint after reader release: %v", err)
	}
}

func TestStore_LogicalGraphDigestIsStableAcrossInsertionOrder(t *testing.T) {
	makeStore := func(t *testing.T, path string, nodes []Node, edges []Edge) string {
		t.Helper()
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.InsertNodes(nodes); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
		if _, _, err := store.InsertEdges(edges); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
		digest, err := store.LogicalGraphDigest("p")
		closeErr := store.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		return digest
	}

	nodes := []Node{
		{Project: "p", Label: LabelFunction, Name: "A", QualifiedName: "p:a.go.A", FilePath: "a.go", StartLine: 1, EndLine: 3, Props: map[string]any{"z": 2, "a": "one"}},
		{Project: "p", Label: LabelFunction, Name: "B", QualifiedName: "p:b.go.B", FilePath: "b.go", StartLine: 4, EndLine: 6, Props: map[string]any{"a": "two"}},
	}
	edges := []Edge{{
		Project: "p", SourceQN: "p:a.go.A", TargetQN: "p:b.go.B", Type: EdgeCalls,
		Props: map[string]any{"confidence": "high"},
	}}
	first := makeStore(t, filepath.Join(t.TempDir(), "first.db"), nodes, edges)
	second := makeStore(t, filepath.Join(t.TempDir(), "second.db"),
		[]Node{nodes[1], nodes[0]}, []Edge{edges[0]})
	if first != second {
		t.Fatalf("digest changed with insertion order: first=%s second=%s", first, second)
	}
}

func TestStore_ValidateIntegrityDetectsFTSAndEndpointCorruption(t *testing.T) {
	t.Run("fts row loss", func(t *testing.T) {
		store, err := Open(filepath.Join(t.TempDir(), "fts.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if err := store.InsertNodes([]Node{{
			Project: "p", Label: LabelFunction, Name: "A", QualifiedName: "p:a.go.A", FilePath: "a.go",
		}}); err != nil {
			t.Fatal(err)
		}
		before, err := store.LogicalGraphDigest("p")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ValidateIntegrity(); err != nil {
			t.Fatalf("healthy graph integrity: %v", err)
		}
		after, err := store.LogicalGraphDigest("p")
		if err != nil {
			t.Fatal(err)
		}
		if before != after {
			t.Fatalf("integrity validation mutated graph digest: before=%s after=%s", before, after)
		}
		if _, err := store.db.Exec(`INSERT INTO nodes_fts(nodes_fts, rowid, name, qualified_name, label, file_path)
			SELECT 'delete', id, name, qualified_name, label, file_path FROM nodes WHERE qualified_name='p:a.go.A'`); err != nil {
			t.Fatal(err)
		}
		if err := store.ValidateIntegrity(); err == nil {
			t.Fatal("FTS row loss was accepted")
		}
	})

	t.Run("fts postings replaced under the same rowid", func(t *testing.T) {
		store, err := Open(filepath.Join(t.TempDir(), "fts-content.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if err := store.InsertNodes([]Node{
			{Project: "p", Label: LabelFunction, Name: "A", QualifiedName: "p:a.go.A", FilePath: "a.go"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.ValidateIntegrity(); err != nil {
			t.Fatalf("healthy graph integrity: %v", err)
		}
		if _, err := store.db.Exec(`INSERT INTO nodes_fts(nodes_fts, rowid, name, qualified_name, label, file_path)
			SELECT 'delete', id, name, qualified_name, label, file_path
			FROM nodes WHERE qualified_name='p:a.go.A'`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`INSERT INTO nodes_fts(rowid,name,qualified_name,label,file_path)
			SELECT id, 'Wrong', 'p:a.go.Wrong', label, file_path
			FROM nodes WHERE qualified_name='p:a.go.A'`); err != nil {
			t.Fatal(err)
		}
		if err := store.ValidateIntegrity(); err == nil {
			t.Fatal("FTS postings replaced under the same rowid were accepted")
		}
	})

	t.Run("duplicate posting under the same rowid", func(t *testing.T) {
		store, err := Open(filepath.Join(t.TempDir(), "fts-duplicate.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		tx, err := store.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`CREATE TEMP TABLE expected_duplicate_vocab(term TEXT, doc INTEGER, col TEXT, offset INTEGER)`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`CREATE TEMP TABLE live_duplicate_vocab(term TEXT, doc INTEGER, col TEXT, offset INTEGER)`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO expected_duplicate_vocab VALUES ('duplicate', 7, 'name', 0)`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO live_duplicate_vocab VALUES
			('duplicate', 7, 'name', 0), ('duplicate', 7, 'name', 0)`); err != nil {
			t.Fatal(err)
		}
		missing, extra, err := compareFTSPostings(tx, "temp.expected_duplicate_vocab", "temp.live_duplicate_vocab")
		if err != nil {
			t.Fatal(err)
		}
		if missing != 1 || extra != 1 {
			t.Fatalf("duplicate posting counts: missing=%d extra=%d, want 1/1", missing, extra)
		}
	})

	t.Run("edge project mismatch", func(t *testing.T) {
		store, err := Open(filepath.Join(t.TempDir(), "edge.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if err := store.InsertNodes([]Node{
			{Project: "p", Label: LabelFunction, Name: "A", QualifiedName: "p:a.go.A", FilePath: "a.go"},
			{Project: "p", Label: LabelFunction, Name: "B", QualifiedName: "p:b.go.B", FilePath: "b.go"},
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.InsertEdges([]Edge{{
			Project: "p", SourceQN: "p:a.go.A", TargetQN: "p:b.go.B", Type: EdgeCalls,
		}}); err != nil {
			t.Fatal(err)
		}
		if err := store.ValidateIntegrity(); err != nil {
			t.Fatalf("healthy edge graph integrity: %v", err)
		}
		if _, err := store.db.Exec(`UPDATE edges SET project='other' WHERE type='CALLS'`); err != nil {
			t.Fatal(err)
		}
		if err := store.ValidateIntegrity(); err == nil {
			t.Fatal("edge project mismatch was accepted")
		}
	})
}
