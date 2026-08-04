package graph

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo) — driver name "sqlite"
)

// Store is the SQLite-backed knowledge graph. Two tables (nodes, edges) plus an
// FTS5 index over node names. The whole "graph" is an adjacency list with
// indexes on edge source/target/type — graph queries are just indexed SQL.
type Store struct {
	db     *sql.DB
	path   string
	snapTx *sql.Tx // optional DEFERRED read txn pinning a pre-wipe graph snapshot
}

const schema = `
CREATE TABLE IF NOT EXISTS nodes (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	project        TEXT NOT NULL,
	label          TEXT NOT NULL,
	name           TEXT NOT NULL,
	qualified_name TEXT NOT NULL,
	file_path      TEXT DEFAULT '',
	start_line     INTEGER DEFAULT 0,
	end_line       INTEGER DEFAULT 0,
	properties     TEXT DEFAULT '{}',
	UNIQUE(project, qualified_name)
);
CREATE INDEX IF NOT EXISTS idx_nodes_label ON nodes(label);
CREATE INDEX IF NOT EXISTS idx_nodes_name  ON nodes(name);
CREATE INDEX IF NOT EXISTS idx_nodes_file  ON nodes(file_path);

CREATE TABLE IF NOT EXISTS edges (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	project    TEXT NOT NULL,
	source_id  INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
	target_id  INTEGER NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
	type       TEXT NOT NULL,
	properties TEXT DEFAULT '{}',
	UNIQUE(source_id, target_id, type)
);
CREATE INDEX IF NOT EXISTS idx_edges_source      ON edges(source_id);
CREATE INDEX IF NOT EXISTS idx_edges_target      ON edges(target_id);
CREATE INDEX IF NOT EXISTS idx_edges_type        ON edges(type);
CREATE INDEX IF NOT EXISTS idx_edges_source_type ON edges(source_id, type);
CREATE INDEX IF NOT EXISTS idx_edges_target_type ON edges(target_id, type);

-- Contentless FTS5: rowid == nodes.id. BM25 ranking comes for free.
CREATE VIRTUAL TABLE IF NOT EXISTS nodes_fts USING fts5(
	name, qualified_name, label, file_path,
	content='', tokenize='unicode61 remove_diacritics 2'
);
`

// Open opens (creating if needed) the graph store at path.
func Open(path string) (*Store, error) {
	db, err := openDatabase(path)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, path: path}, nil
}

func openDatabase(path string) (*sql.DB, error) {
	if err := prepareDatabaseFile(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	closeWithError := func(err error) (*sql.DB, error) {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		return closeWithError(err)
	}
	if _, err := db.Exec(schema); err != nil {
		return closeWithError(fmt.Errorf("schema: %w", err))
	}
	if err := secureDatabaseArtifacts(path); err != nil {
		return closeWithError(err)
	}
	return db, nil
}

func prepareDatabaseFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return secureDatabaseFile(path)
}

func secureDatabaseFile(path string) error {
	if runtime.GOOS == "windows" {
		// Windows ACLs are inherited from the cache directory; chmod does not
		// provide a portable Unix-style ownership mode there.
		return nil
	}
	if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func secureDatabaseArtifacts(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := secureDatabaseFile(path + suffix); err != nil {
			return fmt.Errorf("secure database artifact %q: %w", path+suffix, err)
		}
	}
	return nil
}

func (s *Store) Close() error {
	if s.snapTx != nil {
		_ = s.snapTx.Rollback()
		s.snapTx = nil
	}
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return errors.Join(err, secureDatabaseArtifacts(s.path))
}

// BeginReadSnapshot starts a DEFERRED read transaction that pins the current DB
// snapshot. While active, ForEachCallEdge reads through this txn so a concurrent
// writer (e.g. ReplaceProject on another connection) does not hide pre-wipe CALLS.
func (s *Store) BeginReadSnapshot() error {
	if s.snapTx != nil {
		return fmt.Errorf("read snapshot already active")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	// DEFERRED txns pin their snapshot on the first table read — do it now so a
	// concurrent ReplaceProject cannot hide CALLS before reuse iteration runs.
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM edges`).Scan(&n); err != nil {
		_ = tx.Rollback()
		return err
	}
	s.snapTx = tx
	return nil
}

// EndReadSnapshot ends the read snapshot started by BeginReadSnapshot.
func (s *Store) EndReadSnapshot() error {
	if s.snapTx == nil {
		return nil
	}
	err := s.snapTx.Rollback()
	s.snapTx = nil
	return err
}

func (s *Store) readConn() queryer {
	if s.snapTx != nil {
		return s.snapTx
	}
	return s.db
}

type queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// DBPath returns the filesystem path this store was opened with.
func (s *Store) DBPath() string { return s.path }

// Reopen closes the current connection and opens path (or the same DBPath when empty).
func (s *Store) Reopen(path string) error {
	if path == "" {
		path = s.path
	}
	if err := s.Close(); err != nil {
		return err
	}
	db, err := openDatabase(path)
	if err != nil {
		return err
	}
	s.db = db
	s.path = path
	return nil
}

// Checkpoint finalizes WAL state before an atomic file replacement. It is kept
// explicit rather than part of every Close so ordinary readers do not pay a
// checkpoint cost and snapshot callers retain their existing lifecycle.
func (s *Store) Checkpoint() error {
	if s.db == nil {
		return fmt.Errorf("store is closed")
	}
	var busy, logFrames, checkpointed int
	if err := s.db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		return err
	}
	if busy != 0 || checkpointed != logFrames {
		return fmt.Errorf("incomplete WAL checkpoint: busy=%d log=%d checkpointed=%d", busy, logFrames, checkpointed)
	}
	return secureDatabaseArtifacts(s.path)
}

// ValidateIntegrity checks the complete logical store without changing its
// contents. SQLite's structural check is paired with FTS5's own check, an
// explicit row-id correspondence check between nodes and nodes_fts, and a
// project-consistent endpoint check for every edge.
func (s *Store) ValidateIntegrity() error {
	if s == nil || s.db == nil {
		return errors.New("store is closed")
	}
	rows, err := s.db.Query(`PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("sqlite integrity check: %w", err)
	}
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read sqlite integrity check: %w", err)
		}
		if result != "ok" {
			_ = rows.Close()
			return fmt.Errorf("sqlite integrity check failed: %s", result)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("sqlite integrity check: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close sqlite integrity check: %w", err)
	}

	if err := validateFTS5Integrity(s.db); err != nil {
		return err
	}
	if err := validateFTSNodeRows(s.db); err != nil {
		return err
	}
	if err := validateFTSNodeContent(s.db); err != nil {
		return err
	}
	if err := validateGraphProperties(s.db); err != nil {
		return err
	}
	if err := validateEdgeEndpoints(s.db); err != nil {
		return err
	}
	return nil
}

func validateFTS5Integrity(db *sql.DB) error {
	// FTS5 exposes integrity-check as a special command through INSERT. The
	// command only reads the index; the rollback also guarantees that a driver
	// cannot persist any incidental virtual-table transaction state.
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin FTS5 integrity check: %w", err)
	}
	_, checkErr := tx.Exec(`INSERT INTO nodes_fts(nodes_fts) VALUES('integrity-check')`)
	rollbackErr := tx.Rollback()
	if checkErr != nil {
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return fmt.Errorf("FTS5 integrity check: %v; rollback: %w", checkErr, rollbackErr)
		}
		return fmt.Errorf("FTS5 integrity check: %w", checkErr)
	}
	if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		return fmt.Errorf("rollback FTS5 integrity check: %w", rollbackErr)
	}
	return nil
}

func validateFTSNodeRows(db *sql.DB) error {
	var missingID int64
	err := db.QueryRow(`
		SELECT n.id
		FROM nodes n
		LEFT JOIN nodes_fts f ON f.rowid=n.id
		WHERE f.rowid IS NULL
		ORDER BY n.id
		LIMIT 1`).Scan(&missingID)
	if err == nil {
		return fmt.Errorf("FTS index is missing node row %d", missingID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check FTS rows for missing nodes: %w", err)
	}

	var extraID int64
	err = db.QueryRow(`
		SELECT f.rowid
		FROM nodes_fts f
		LEFT JOIN nodes n ON n.id=f.rowid
		WHERE n.id IS NULL
		ORDER BY f.rowid
		LIMIT 1`).Scan(&extraID)
	if err == nil {
		return fmt.Errorf("FTS index has orphan node row %d", extraID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check FTS rows for orphan nodes: %w", err)
	}
	return nil
}

// validateFTSNodeContent compares the live contentless FTS postings with a
// freshly-built in-transaction index over nodes. Row-id correspondence alone is
// insufficient: a deleted posting can be replaced with a different value under
// the same rowid while SQLite's own integrity check still reports "ok". The
// temporary index never touches the live graph and is rolled back before the
// connection is returned to the pool.
func validateFTSNodeContent(db *sql.DB) (retErr error) {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin FTS content check: %w", err)
	}
	defer func() {
		var cleanupErrs []error
		for _, table := range []string{
			"temp.codegraph_expected_fts",
			"temp.codegraph_live_vocab",
			"temp.codegraph_expected_vocab",
		} {
			if _, err := tx.Exec("DROP TABLE IF EXISTS " + table); err != nil {
				cleanupErrs = append(cleanupErrs, fmt.Errorf("drop %s: %w", table, err))
			}
		}
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("rollback FTS content check: %w", err))
		}
		if cleanupErr := errors.Join(cleanupErrs...); cleanupErr != nil {
			if retErr == nil {
				retErr = cleanupErr
			} else {
				retErr = errors.Join(retErr, cleanupErr)
			}
		}
	}()

	if _, err := tx.Exec(`CREATE VIRTUAL TABLE temp.codegraph_expected_fts USING fts5(
		name, qualified_name, label, file_path,
		content='', tokenize='unicode61 remove_diacritics 2'
	)`); err != nil {
		return fmt.Errorf("create expected FTS index: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO temp.codegraph_expected_fts(rowid,name,qualified_name,label,file_path)
		SELECT id,name,qualified_name,label,file_path FROM nodes ORDER BY id`); err != nil {
		return fmt.Errorf("populate expected FTS index: %w", err)
	}
	if _, err := tx.Exec(`CREATE VIRTUAL TABLE temp.codegraph_live_vocab
		USING fts5vocab(main, nodes_fts, 'instance')`); err != nil {
		return fmt.Errorf("inspect live FTS postings: %w", err)
	}
	if _, err := tx.Exec(`CREATE VIRTUAL TABLE temp.codegraph_expected_vocab
		USING fts5vocab(temp, codegraph_expected_fts, 'instance')`); err != nil {
		return fmt.Errorf("inspect expected FTS postings: %w", err)
	}

	missing, extra, err := compareFTSPostings(tx, "temp.codegraph_expected_vocab", "temp.codegraph_live_vocab")
	if err != nil {
		return err
	}
	if missing != 0 || extra != 0 {
		return fmt.Errorf("FTS postings do not match nodes: missing=%d extra=%d", missing, extra)
	}
	return nil
}

// compareFTSPostings compares grouped posting counts rather than raw EXCEPT
// sets. EXCEPT is distinct-set subtraction, so it would erase a duplicate
// (term, doc, column, offset) and certify a damaged FTS index.
func compareFTSPostings(tx *sql.Tx, expectedTable, liveTable string) (missing, extra int, err error) {
	if err := tx.QueryRow(`SELECT COUNT(*) FROM (
		SELECT term, doc, col, offset, COUNT(*) AS posting_count
		FROM ` + expectedTable + `
		GROUP BY term, doc, col, offset
		EXCEPT
		SELECT term, doc, col, offset, COUNT(*) AS posting_count
		FROM ` + liveTable + `
		GROUP BY term, doc, col, offset
	)`).Scan(&missing); err != nil {
		return 0, 0, fmt.Errorf("compare missing FTS postings: %w", err)
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM (
		SELECT term, doc, col, offset, COUNT(*) AS posting_count
		FROM ` + liveTable + `
		GROUP BY term, doc, col, offset
		EXCEPT
		SELECT term, doc, col, offset, COUNT(*) AS posting_count
		FROM ` + expectedTable + `
		GROUP BY term, doc, col, offset
	)`).Scan(&extra); err != nil {
		return 0, 0, fmt.Errorf("compare extra FTS postings: %w", err)
	}
	return missing, extra, nil
}

func validateGraphProperties(db *sql.DB) error {
	for _, table := range []string{"nodes", "edges"} {
		rows, err := db.Query("SELECT id, properties FROM " + table + " ORDER BY id")
		if err != nil {
			return fmt.Errorf("query %s properties: %w", table, err)
		}
		for rows.Next() {
			var id int64
			var properties string
			if err := rows.Scan(&id, &properties); err != nil {
				_ = rows.Close()
				return fmt.Errorf("read %s properties: %w", table, err)
			}
			if _, err := canonicalGraphProperties(properties); err != nil {
				_ = rows.Close()
				return fmt.Errorf("invalid JSON properties in %s row %d: %w", table, id, err)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read %s properties: %w", table, err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close %s properties: %w", table, err)
		}
	}
	return nil
}

func validateEdgeEndpoints(db *sql.DB) error {
	var edgeID int64
	var edgeProject, sourceProject, targetProject sql.NullString
	err := db.QueryRow(`
		SELECT e.id, e.project, src.project, tgt.project
		FROM edges e
		LEFT JOIN nodes src ON src.id=e.source_id
		LEFT JOIN nodes tgt ON tgt.id=e.target_id
		WHERE src.id IS NULL OR tgt.id IS NULL
		   OR src.project != e.project OR tgt.project != e.project
		ORDER BY e.id
		LIMIT 1`).Scan(&edgeID, &edgeProject, &sourceProject, &targetProject)
	if err == nil {
		return fmt.Errorf("edge %d has inconsistent endpoints: edge project=%q source project=%q target project=%q", edgeID, edgeProject.String, sourceProject.String, targetProject.String)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check edge endpoints: %w", err)
	}
	return nil
}

// LogicalGraphDigest returns a deterministic digest of one project's logical
// graph. SQLite row IDs, inode identity, WAL frames, and raw database bytes are
// deliberately excluded so equivalent graphs hash alike across rebuilds.
func (s *Store) LogicalGraphDigest(project string) (string, error) {
	if s == nil || s.db == nil {
		return "", errors.New("store is closed")
	}
	h := sha256.New()
	digestGraphRecord(h, "graph", "logical-graph-v1")

	nodeRows, err := s.db.Query(`
		SELECT project, label, name, qualified_name, file_path, start_line, end_line, properties
		FROM nodes
		WHERE project=?
		ORDER BY project, label, qualified_name, name, file_path, start_line, end_line, properties`, project)
	if err != nil {
		return "", fmt.Errorf("query graph nodes for digest: %w", err)
	}
	for nodeRows.Next() {
		var nodeProject, label, name, qualifiedName, filePath, properties string
		var startLine, endLine int
		if err := nodeRows.Scan(&nodeProject, &label, &name, &qualifiedName, &filePath, &startLine, &endLine, &properties); err != nil {
			_ = nodeRows.Close()
			return "", fmt.Errorf("read graph node for digest: %w", err)
		}
		canonicalProperties, err := canonicalGraphProperties(properties)
		if err != nil {
			_ = nodeRows.Close()
			return "", fmt.Errorf("canonicalize properties for node %q: %w", qualifiedName, err)
		}
		digestGraphRecord(h, "node", nodeProject, label, name, qualifiedName, filePath,
			strconv.Itoa(startLine), strconv.Itoa(endLine), canonicalProperties)
	}
	if err := nodeRows.Err(); err != nil {
		_ = nodeRows.Close()
		return "", fmt.Errorf("read graph nodes for digest: %w", err)
	}
	if err := nodeRows.Close(); err != nil {
		return "", fmt.Errorf("close graph nodes for digest: %w", err)
	}

	edgeRows, err := s.db.Query(`
		SELECT e.project, src.qualified_name, tgt.qualified_name, e.type, e.properties
		FROM edges e
		LEFT JOIN nodes src ON src.id=e.source_id
		LEFT JOIN nodes tgt ON tgt.id=e.target_id
		WHERE e.project=?
		ORDER BY e.project, src.qualified_name, tgt.qualified_name, e.type, e.properties, e.source_id, e.target_id`, project)
	if err != nil {
		return "", fmt.Errorf("query graph edges for digest: %w", err)
	}
	for edgeRows.Next() {
		var edgeProject, edgeType, properties string
		var sourceQN, targetQN sql.NullString
		if err := edgeRows.Scan(&edgeProject, &sourceQN, &targetQN, &edgeType, &properties); err != nil {
			_ = edgeRows.Close()
			return "", fmt.Errorf("read graph edge for digest: %w", err)
		}
		if !sourceQN.Valid || !targetQN.Valid {
			_ = edgeRows.Close()
			return "", errors.New("cannot digest graph edge with missing endpoint")
		}
		canonicalProperties, err := canonicalGraphProperties(properties)
		if err != nil {
			_ = edgeRows.Close()
			return "", fmt.Errorf("canonicalize properties for edge %q -> %q: %w", sourceQN.String, targetQN.String, err)
		}
		digestGraphRecord(h, "edge", edgeProject, sourceQN.String, targetQN.String, edgeType, canonicalProperties)
	}
	if err := edgeRows.Err(); err != nil {
		_ = edgeRows.Close()
		return "", fmt.Errorf("read graph edges for digest: %w", err)
	}
	if err := edgeRows.Close(); err != nil {
		return "", fmt.Errorf("close graph edges for digest: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func canonicalGraphProperties(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		if raw == "" {
			return "", nil
		}
		return "", errors.New("empty JSON")
	}
	raw = trimmed
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return "", errors.New("multiple JSON values")
		}
		return "", err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func digestGraphRecord(h hash.Hash, kind string, fields ...string) {
	digestGraphField(h, kind)
	digestGraphField(h, strconv.Itoa(len(fields)))
	for _, field := range fields {
		digestGraphField(h, field)
	}
}

func digestGraphField(h hash.Hash, value string) {
	var encodedLength [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(encodedLength[:], uint64(len(value)))
	_, _ = h.Write(encodedLength[:n])
	_, _ = h.Write([]byte(value))
}

// ReplaceProject wipes a project's nodes/edges/FTS so a re-index is clean.
// (Incremental indexing — only changed files — is a later milestone.)
func (s *Store) ReplaceProject(project string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Contentless FTS5 rejects a plain DELETE; rows are removed via the special
	// 'delete' command, fed the originally-indexed column values (still present in
	// nodes at this point) so the right terms are purged. Must run before the
	// nodes rows are deleted.
	if _, err := tx.Exec(`INSERT INTO nodes_fts(nodes_fts, rowid, name, qualified_name, label, file_path)
		SELECT 'delete', id, name, qualified_name, label, file_path FROM nodes WHERE project=?`, project); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM nodes WHERE project=?`, project); err != nil {
		return err
	}
	// edges cascade via FK, but be explicit in case FKs are off.
	if _, err := tx.Exec(`DELETE FROM edges WHERE project=?`, project); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return secureDatabaseArtifacts(s.path)
}

// InsertNodes inserts nodes and assigns IDs, keeping the FTS index in sync.
func (s *Store) InsertNodes(nodes []Node) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	insNode, err := tx.Prepare(`INSERT OR IGNORE INTO nodes
		(project,label,name,qualified_name,file_path,start_line,end_line,properties)
		VALUES (?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer insNode.Close()
	insFTS, err := tx.Prepare(`INSERT INTO nodes_fts(rowid,name,qualified_name,label,file_path) VALUES (?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer insFTS.Close()

	for _, n := range nodes {
		props := "{}"
		if len(n.Props) > 0 {
			if b, err := json.Marshal(n.Props); err == nil {
				props = string(b)
			}
		}
		res, err := insNode.Exec(n.Project, n.Label, n.Name, n.QualifiedName, n.FilePath, n.StartLine, n.EndLine, props)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil || id == 0 {
			continue // duplicate qualified_name (INSERT OR IGNORE) — skip FTS
		}
		if _, err := insFTS.Exec(id, n.Name, n.QualifiedName, string(n.Label), n.FilePath); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return secureDatabaseArtifacts(s.path)
}

// FunctionSpan is a lightweight function/method row for caller attribution during
// CALLS resolution — much smaller than a full Node (no properties JSON).
type FunctionSpan struct {
	QualifiedName string
	FilePath      string
	StartLine     int
	EndLine       int
}

// RubyCallTarget is a singleton method that the Ruby static resolver may use as
// a CALLS target. Owner is the source-level constant path (for example,
// "Payments::Gateway"), not a file-qualified graph name.
type RubyCallTarget struct {
	QualifiedName string
	Owner         string
	Name          string
}

// FunctionSpans returns every Function/Method span in a project. The indexing
// pipeline loads this instead of keeping all nodes in RAM for CALLS/SIMILAR.
func (s *Store) FunctionSpans(project string) ([]FunctionSpan, error) {
	rows, err := s.db.Query(`SELECT qualified_name, file_path, start_line, end_line
		FROM nodes WHERE project=? AND label IN ('Function','Method')
		ORDER BY file_path, start_line`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FunctionSpan
	for rows.Next() {
		var sp FunctionSpan
		if err := rows.Scan(&sp.QualifiedName, &sp.FilePath, &sp.StartLine, &sp.EndLine); err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// RubySingletonCallTargets returns singleton methods whose source declaration
// establishes a static target owner. The resolver deliberately uses only this
// narrow set: an absolute constant receiver identifies one runtime object without
// needing receiver type inference.
func (s *Store) RubySingletonCallTargets(project string) ([]RubyCallTarget, error) {
	rows, err := s.db.Query(`SELECT qualified_name, json_extract(properties, '$.ruby_static_call_target_owner'), name
		FROM nodes
		WHERE project=? AND label='Method'
		  AND json_extract(properties, '$.lang')='ruby'
		  AND json_extract(properties, '$.ruby_static_call_target_owner') IS NOT NULL`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RubyCallTarget
	for rows.Next() {
		var target RubyCallTarget
		if err := rows.Scan(&target.QualifiedName, &target.Owner, &target.Name); err != nil {
			return nil, err
		}
		out = append(out, target)
	}
	return out, rows.Err()
}

// RubyAnalysisCurrent reports whether every indexed Ruby File node was built with
// the requested Ruby analysis version. It lets parser and resolver upgrades
// invalidate an otherwise hash-identical Ruby graph exactly once.
func (s *Store) RubyAnalysisCurrent(project string, version int) (bool, error) {
	var rubyFiles, current int
	err := s.db.QueryRow(`SELECT
		COUNT(*),
		COALESCE(SUM(CASE WHEN json_extract(properties, '$.ruby_analysis_version')=? THEN 1 ELSE 0 END), 0)
		FROM nodes
		WHERE project=? AND label='File' AND json_extract(properties, '$.lang')='ruby'`, version, project).Scan(&rubyFiles, &current)
	if err != nil {
		return false, err
	}
	return rubyFiles == current, nil
}

// InsertEdges resolves source/target qualified names to node IDs and inserts.
// QN→id resolution is done once in memory (was one correlated subquery per edge —
// O(edges) two-table lookups). Edges whose endpoints don't exist are dropped.
func (s *Store) InsertEdges(edges []Edge) (inserted, dropped int, err error) {
	if len(edges) == 0 {
		return 0, 0, nil
	}

	project := edges[0].Project
	idByQN := make(map[string]int64)
	rows, err := s.db.Query(`SELECT qualified_name, id FROM nodes WHERE project=?`, project)
	if err != nil {
		return 0, 0, err
	}
	for rows.Next() {
		var qn string
		var id int64
		if err := rows.Scan(&qn, &id); err != nil {
			rows.Close()
			return 0, 0, err
		}
		idByQN[qn] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	ins, err := tx.Prepare(`INSERT OR IGNORE INTO edges(project,source_id,target_id,type,properties) VALUES (?,?,?,?,?)`)
	if err != nil {
		return 0, 0, err
	}
	defer ins.Close()

	for _, e := range edges {
		sid, ok1 := idByQN[e.SourceQN]
		tid, ok2 := idByQN[e.TargetQN]
		if !ok1 || !ok2 {
			dropped++
			continue
		}
		props := "{}"
		if len(e.Props) > 0 {
			if b, err := json.Marshal(e.Props); err == nil {
				props = string(b)
			}
		}
		res, err := ins.Exec(e.Project, sid, tid, string(e.Type), props)
		if err != nil {
			return 0, 0, err
		}
		if aff, _ := res.RowsAffected(); aff > 0 {
			inserted++
		} else {
			dropped++ // duplicate (unique constraint)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	if err := secureDatabaseArtifacts(s.path); err != nil {
		return 0, 0, err
	}
	return inserted, dropped, nil
}

// DeleteEdgesByType removes one relationship class from a project. The indexer
// uses this only to discard successful resolver edges when another resolver
// scope failed during a first degraded build; it never fabricates a partial
// CALLS graph.
func (s *Store) DeleteEdgesByType(project string, edgeType EdgeType) error {
	_, err := s.db.Exec(`DELETE FROM edges WHERE project=? AND type=?`, project, string(edgeType))
	if err != nil {
		return err
	}
	return secureDatabaseArtifacts(s.path)
}

// TopByInboundCalls returns the nodes with the most inbound CALLS edges — the
// call hubs. These make the most discriminating benchmark questions ("who calls
// X"): a real caller set the grep baseline has to reconstruct by hand.
func (s *Store) TopByInboundCalls(project string, limit int) ([]Node, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT ` + ftsCols("n.") + ` FROM edges e
		JOIN nodes n ON n.id = e.target_id
		WHERE e.project=? AND e.type='CALLS'
		GROUP BY e.target_id
		ORDER BY COUNT(*) DESC, n.qualified_name ASC
		LIMIT ?`
	rows, err := s.db.Query(q, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// TopByOutboundCalls returns the nodes that call the most other nodes — useful
// for "what does X call" (callees) benchmark/quality questions.
func (s *Store) TopByOutboundCalls(project string, limit int) ([]Node, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT ` + ftsCols("n.") + ` FROM edges e
		JOIN nodes n ON n.id = e.source_id
		WHERE e.project=? AND e.type='CALLS'
		GROUP BY e.source_id
		ORDER BY COUNT(*) DESC, n.qualified_name ASC
		LIMIT ?`
	rows, err := s.db.Query(q, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// HubCount is a call hub plus how many distinct callers point at it.
type HubCount struct {
	Node    Node
	Callers int
}

// CallHubs returns the most-called nodes with their inbound-CALLS count — the call
// hotspots for get_architecture (TopByInboundCalls without the count loses the metric).
func (s *Store) CallHubs(project string, limit int) ([]HubCount, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT ` + ftsCols("n.") + `, COUNT(*) FROM edges e
		JOIN nodes n ON n.id = e.target_id
		WHERE e.project=? AND e.type='CALLS'
		GROUP BY e.target_id
		ORDER BY COUNT(*) DESC, n.qualified_name ASC
		LIMIT ?`
	rows, err := s.db.Query(q, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HubCount
	for rows.Next() {
		var n Node
		var props string
		var count int
		if err := rows.Scan(&n.ID, &n.Project, &n.Label, &n.Name, &n.QualifiedName, &n.FilePath, &n.StartLine, &n.EndLine, &props, &count); err != nil {
			return nil, err
		}
		if props != "" {
			_ = json.Unmarshal([]byte(props), &n.Props)
		}
		out = append(out, HubCount{Node: n, Callers: count})
	}
	return out, rows.Err()
}

// TopByComplexity returns Function/Method nodes ranked by stored cyclomatic
// complexity (properties.complexity), highest first — the complexity hotspots.
func (s *Store) TopByComplexity(project string, limit int) ([]Node, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT ` + ftsCols("n.") + ` FROM nodes n
		WHERE n.project=? AND n.label IN ('Function','Method')
		AND json_extract(n.properties,'$.complexity') IS NOT NULL
		ORDER BY CAST(json_extract(n.properties,'$.complexity') AS INTEGER) DESC, n.qualified_name ASC
		LIMIT ?`
	rows, err := s.db.Query(q, project, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// LabelCounts / EdgeTypeCounts / LanguageCounts are the headline aggregates for
// get_architecture: nodes per label, edges per type, and File nodes per language.
func (s *Store) LabelCounts(project string) (map[string]int, error) {
	return s.countBy(`SELECT label, COUNT(*) FROM nodes WHERE project=? GROUP BY label`, project)
}

func (s *Store) EdgeTypeCounts(project string) (map[string]int, error) {
	return s.countBy(`SELECT type, COUNT(*) FROM edges WHERE project=? GROUP BY type`, project)
}

func (s *Store) LanguageCounts(project string) (map[string]int, error) {
	return s.countBy(`SELECT json_extract(properties,'$.lang'), COUNT(*) FROM nodes WHERE project=? AND label='File' GROUP BY 1`, project)
}

// FileSymbolCounts returns symbol count per file (File nodes excluded) — the query
// layer folds these into per-directory package stats.
func (s *Store) FileSymbolCounts(project string) (map[string]int, error) {
	return s.countBy(`SELECT file_path, COUNT(*) FROM nodes WHERE project=? AND label<>'File' GROUP BY file_path`, project)
}

// countBy runs a `SELECT key, COUNT(*)` grouped query into a map, skipping NULL keys.
func (s *Store) countBy(q, project string) (map[string]int, error) {
	rows, err := s.db.Query(q, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k sql.NullString
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		if k.Valid {
			out[k.String] = n
		}
	}
	return out, rows.Err()
}

// FunctionsWithoutInboundCalls returns Function/Method nodes that no in-graph
// CALLS edge points at — the raw candidate set for the dead-code hint. It is only
// the graph half of the answer: the query layer still drops entry points
// (exported, decorated, main/init, tests) before reporting, because those have no
// in-graph caller by design, not because they're dead.
func (s *Store) FunctionsWithoutInboundCalls(project string) ([]Node, error) {
	// source_id <> n.id ignores self-edges: a function reachable only by its own
	// recursion is still unreachable from the rest of the repo, so it stays dead.
	q := `SELECT ` + ftsCols("n.") + ` FROM nodes n
		WHERE n.project=? AND n.label IN ('Function','Method')
		AND NOT EXISTS (
			SELECT 1 FROM edges e WHERE e.target_id = n.id AND e.source_id <> n.id AND e.type='CALLS'
		)
		ORDER BY n.file_path ASC, n.start_line ASC`
	rows, err := s.db.Query(q, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// SampleByLabel returns a deterministic sample of nodes of a given label
// (ordered by qualified name) — used to pick "where is X defined" questions.
func (s *Store) SampleByLabel(project, label string, limit int) ([]Node, error) {
	if limit <= 0 {
		limit = 10
	}
	q := `SELECT ` + nodeCols + ` FROM nodes
		WHERE project=? AND label=? ORDER BY qualified_name LIMIT ?`
	rows, err := s.db.Query(q, project, label, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// FileHashes returns the stored sha256 content hash of every File node in the
// project, keyed by repo-relative path. The basis for incremental indexing:
// comparing these against the files currently on disk yields the change set.
func (s *Store) FileHashes(project string) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT file_path, properties FROM nodes WHERE project=? AND label=?`,
		project, string(LabelFile))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var path, props string
		if err := rows.Scan(&path, &props); err != nil {
			return nil, err
		}
		if props == "" {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal([]byte(props), &p); err != nil {
			return nil, fmt.Errorf("decode properties for file %q: %w", path, err)
		}
		if h, ok := p["sha256"].(string); ok {
			out[path] = h
		}
	}
	return out, rows.Err()
}

// CallEdge is a stored CALLS edge plus its caller's file path — enough for
// incremental indexing to decide, by scope, which edges to reuse across a re-index.
type CallEdge struct {
	SourceQN   string
	TargetQN   string
	SourceFile string
	Props      map[string]any
}

// ForEachCallEdge streams CALLS edges without materializing the full set. fn is
// invoked once per edge; returning a non-nil error stops iteration.
func (s *Store) ForEachCallEdge(project string, fn func(CallEdge) error) error {
	rows, err := s.readConn().Query(`SELECT src.qualified_name, tgt.qualified_name, src.file_path, e.properties
		FROM edges e
		JOIN nodes src ON src.id = e.source_id
		JOIN nodes tgt ON tgt.id = e.target_id
		WHERE e.project=? AND e.type='CALLS'`, project)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ce CallEdge
		var props string
		if err := rows.Scan(&ce.SourceQN, &ce.TargetQN, &ce.SourceFile, &props); err != nil {
			return err
		}
		if props != "" {
			_ = json.Unmarshal([]byte(props), &ce.Props)
		}
		if err := fn(ce); err != nil {
			return err
		}
	}
	return rows.Err()
}

// CallEdges returns every CALLS edge in the project with its caller's file path.
// Read before a re-index so unchanged scopes' edges can be kept instead of
// re-resolved (the expensive scip / go+VTA pass).
func (s *Store) CallEdges(project string) ([]CallEdge, error) {
	var out []CallEdge
	err := s.ForEachCallEdge(project, func(ce CallEdge) error {
		out = append(out, ce)
		return nil
	})
	return out, err
}

// Stats returns node/edge counts for a project.
func (s *Store) Stats(project string) (nodes, edges int, err error) {
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE project=?`, project).Scan(&nodes); err != nil {
		return 0, 0, err
	}
	err = s.db.QueryRow(`SELECT COUNT(*) FROM edges WHERE project=?`, project).Scan(&edges)
	return
}

func scanNode(rows *sql.Rows) (Node, error) {
	var n Node
	var props string
	if err := rows.Scan(&n.ID, &n.Project, &n.Label, &n.Name, &n.QualifiedName, &n.FilePath, &n.StartLine, &n.EndLine, &props); err != nil {
		return n, err
	}
	if props != "" {
		_ = json.Unmarshal([]byte(props), &n.Props)
	}
	return n, nil
}

const nodeCols = `id,project,label,name,qualified_name,file_path,start_line,end_line,properties`

// Search runs a BM25 FTS query over node names/qualified names. `label` filters
// by node kind when non-empty.
func (s *Store) Search(project, query, label string, limit int) ([]SearchHit, error) {
	if limit <= 0 {
		limit = 25
	}
	q := `SELECT ` + ftsCols("n.") + `, fts.rank
		FROM nodes_fts fts JOIN nodes n ON n.id = fts.rowid
		WHERE nodes_fts MATCH ? AND n.project = ?`
	args := []any{ftsQuery(query), project}
	if label != "" {
		q += ` AND n.label = ?`
		args = append(args, label)
	}
	q += ` ORDER BY fts.rank LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hits []SearchHit
	for rows.Next() {
		var n Node
		var props string
		var rank float64
		if err := rows.Scan(&n.ID, &n.Project, &n.Label, &n.Name, &n.QualifiedName, &n.FilePath, &n.StartLine, &n.EndLine, &props, &rank); err != nil {
			return nil, err
		}
		if props != "" {
			_ = json.Unmarshal([]byte(props), &n.Props)
		}
		hits = append(hits, SearchHit{Node: n, Rank: rank})
	}
	return hits, rows.Err()
}

func ftsCols(prefix string) string {
	cols := strings.Split(nodeCols, ",")
	for i := range cols {
		cols[i] = prefix + cols[i]
	}
	return strings.Join(cols, ",")
}

// ftsQuery makes a user string safe-ish for FTS5 by quoting each token as a
// prefix match. Keeps things forgiving for agent-supplied queries.
func ftsQuery(q string) string {
	fields := strings.Fields(q)
	for i, f := range fields {
		f = strings.ReplaceAll(f, `"`, "")
		fields[i] = `"` + f + `"*`
	}
	if len(fields) == 0 {
		return `""`
	}
	return strings.Join(fields, " OR ")
}

// Neighbors returns nodes connected to the given qualified name. direction is
// "out" (callees/dependencies), "in" (callers/dependents) or "both".
// edgeType filters by relationship when non-empty.
func (s *Store) Neighbors(project, qualifiedName, direction, edgeType string, limit int) ([]Node, error) {
	if limit <= 0 {
		// callers/callees/neighbors/similar are exhaustive relationship queries, so a
		// low cap turns a recall ceiling into a wrong answer (gh-cli's iostreams.Test
		// has 448 callers). 500 covers real hubs while staying bounded against a
		// pathological "called everywhere" symbol; callers can pass an explicit limit.
		limit = 500
	}
	// The type filter is embedded per-SELECT so it also applies to the "both"
	// UNION (one clause in each arm) — that's what lets `similar` ask for just
	// SIMILAR_TO neighbors in both directions. edgeType="" leaves it off entirely,
	// so plain `neighbors` still returns every edge kind.
	typeClause := ""
	if edgeType != "" {
		typeClause = ` AND e.type=?`
	}
	endpoint := func(args []any) []any {
		args = append(args, project, qualifiedName)
		if edgeType != "" {
			args = append(args, edgeType)
		}
		return args
	}
	var q string
	var args []any
	switch direction {
	case "in":
		q = `SELECT ` + ftsCols("n.") + ` FROM edges e
			JOIN nodes n ON n.id = e.source_id
			JOIN nodes t ON t.id = e.target_id
			WHERE t.project=? AND t.qualified_name=?` + typeClause
		args = endpoint(nil)
	case "both":
		q = `SELECT ` + ftsCols("n.") + ` FROM edges e
			JOIN nodes n ON n.id = e.target_id JOIN nodes src ON src.id = e.source_id
			WHERE src.project=? AND src.qualified_name=?` + typeClause + `
			UNION
			SELECT ` + ftsCols("n.") + ` FROM edges e
			JOIN nodes n ON n.id = e.source_id JOIN nodes tgt ON tgt.id = e.target_id
			WHERE tgt.project=? AND tgt.qualified_name=?` + typeClause
		args = endpoint(endpoint(nil))
	default: // "out"
		q = `SELECT ` + ftsCols("n.") + ` FROM edges e
			JOIN nodes n ON n.id = e.target_id
			JOIN nodes s ON s.id = e.source_id
			WHERE s.project=? AND s.qualified_name=?` + typeClause
		args = endpoint(nil)
	}
	q += ` LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Snippet reads the source lines [start,end] for a node from disk. repoRoot is
// the repository root; filePath must be repo-relative (as stored on nodes). Paths
// outside the root — including .. segments and absolute paths — are rejected.
func Snippet(repoRoot, filePath string, start, end int) (string, error) {
	abs, err := resolveRepoFile(repoRoot, filePath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	if start < 1 {
		start = 1
	}
	if end > len(lines) || end == 0 {
		end = len(lines)
	}
	if start > end {
		return "", fmt.Errorf("bad range %d-%d", start, end)
	}
	return strings.Join(lines[start-1:end], "\n"), nil
}

// resolveRepoFile maps a repo-relative path to an absolute path confined under
// repoRoot. Used by Snippet so MCP/CLI callers cannot escape the indexed tree.
func resolveRepoFile(repoRoot, filePath string) (string, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", fmt.Errorf("repo root: %w", err)
	}
	rel := filepath.FromSlash(filePath)
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute paths are not allowed")
	}
	rel = filepath.Clean(rel)
	if rel == "." || rel == "" {
		return "", fmt.Errorf("empty file path")
	}
	full := filepath.Join(root, rel)
	full, err = filepath.Abs(full)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	// filepath.Rel on Windows accepts case-insensitive roots; good enough for confinement.
	out, err := filepath.Rel(root, full)
	if err != nil {
		return "", fmt.Errorf("path outside repository root")
	}
	if out == ".." || strings.HasPrefix(out, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path outside repository root")
	}
	return full, nil
}
