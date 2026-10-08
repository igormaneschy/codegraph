package graph

import (
	"database/sql"
	"fmt"
)

// RefNode is the compact node projection: exactly the columns a compact Ref
// needs (label, name, qualified name, file, lines). Search and neighbor answers
// never use the properties JSON, so selecting and decoding it is pure overhead
// on the hottest query paths (P3). Ordering and paging stay identical to the
// full-node queries.
type RefNode struct {
	Label         NodeLabel
	Name          string
	QualifiedName string
	FilePath      string
	StartLine     int
	EndLine       int
}

// refNodeCols is the compact projection over a nodes alias `n`.
const refNodeCols = `n.label,n.name,n.qualified_name,n.file_path,n.start_line,n.end_line`

func scanRefNode(rows *sql.Rows) (RefNode, error) {
	var r RefNode
	err := rows.Scan(&r.Label, &r.Name, &r.QualifiedName, &r.FilePath, &r.StartLine, &r.EndLine)
	return r, err
}

// searchSelect builds the shared FTS join for a search page over the given
// column list. The caller appends ORDER BY / LIMIT / OFFSET.
func searchSelect(cols, project, query, label string) (string, []any) {
	q := `SELECT ` + cols + ` FROM nodes_fts fts JOIN nodes n ON n.id = fts.rowid
		WHERE nodes_fts MATCH ? AND n.project = ?`
	args := []any{ftsQuery(query), project}
	if label != "" {
		q += ` AND n.label = ?`
		args = append(args, label)
	}
	return q, args
}

// SearchRefs is SearchPage without the properties decode: it returns only the
// compact ref columns. Ranking (BM25 rank, node id) and paging are identical, so
// the resulting refs are the same.
func (s *Store) SearchRefs(project, query, label string, limit, offset int) ([]RefNode, error) {
	if limit <= 0 {
		limit = 25
	}
	if offset < 0 {
		return nil, fmt.Errorf("invalid offset %d: want a non-negative row offset", offset)
	}
	q, args := searchSelect(refNodeCols, project, query, label)
	q += ` ORDER BY fts.rank, n.id LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []RefNode
	for rows.Next() {
		r, err := scanRefNode(rows)
		if err != nil {
			return nil, err
		}
		refs = append(refs, r)
	}
	return refs, rows.Err()
}

// neighborSelect builds the shared JOIN/UNION body for the neighbor queries over
// the given column list. The type filter and the keyset `after` filter are
// embedded per-SELECT so they also apply to the "both" UNION; edgeType="" and
// after="" leave them off entirely.
func neighborSelect(cols, project, qualifiedName, direction, edgeType, after string) (string, []any, error) {
	filter := ""
	if edgeType != "" {
		filter += ` AND e.type=?`
	}
	if after != "" {
		// The rows are ordered by n.qualified_name, so this is one page's exact
		// continuation without an OFFSET rescan (P2).
		filter += ` AND n.qualified_name > ?`
	}
	endpoint := func(args []any) []any {
		args = append(args, project, qualifiedName)
		if edgeType != "" {
			args = append(args, edgeType)
		}
		if after != "" {
			args = append(args, after)
		}
		return args
	}
	var q string
	var args []any
	switch direction {
	case "in":
		q = `SELECT ` + cols + ` FROM edges e
			JOIN nodes n ON n.id = e.source_id
			JOIN nodes t ON t.id = e.target_id
			WHERE t.project=? AND t.qualified_name=?` + filter
		args = endpoint(nil)
	case "both":
		q = `SELECT ` + cols + ` FROM edges e
			JOIN nodes n ON n.id = e.target_id JOIN nodes src ON src.id = e.source_id
			WHERE src.project=? AND src.qualified_name=?` + filter + `
			UNION
			SELECT ` + cols + ` FROM edges e
			JOIN nodes n ON n.id = e.source_id JOIN nodes tgt ON tgt.id = e.target_id
			WHERE tgt.project=? AND tgt.qualified_name=?` + filter
		args = endpoint(endpoint(nil))
	case "out":
		q = `SELECT ` + cols + ` FROM edges e
			JOIN nodes n ON n.id = e.target_id
			JOIN nodes s ON s.id = e.source_id
			WHERE s.project=? AND s.qualified_name=?` + filter
		args = endpoint(nil)
	default:
		return "", nil, fmt.Errorf("invalid direction %q: want \"in\", \"out\", or \"both\"", direction)
	}
	return q, args, nil
}

// NeighborRefs is NeighborsPage without the properties decode: it returns only
// the compact ref columns, with the same JOIN/UNION, ordering, paging, and
// dedup semantics (P3).
func (s *Store) NeighborRefs(project, qualifiedName, direction, edgeType string, limit, offset int) ([]RefNode, error) {
	if limit <= 0 {
		limit = 500
	}
	if offset < 0 {
		return nil, fmt.Errorf("invalid offset %d: want a non-negative row offset", offset)
	}
	q, args, err := neighborSelect(refNodeCols, project, qualifiedName, direction, edgeType, "")
	if err != nil {
		return nil, err
	}
	q += ` ORDER BY n.qualified_name LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []RefNode
	for rows.Next() {
		r, err := scanRefNode(rows)
		if err != nil {
			return nil, err
		}
		refs = append(refs, r)
	}
	return refs, rows.Err()
}

// NeighborRefsAfter is the keyset form of NeighborRefs: rows are ordered by
// n.qualified_name and the page continues strictly after `after`, so a deep
// page costs the same as the first instead of rescanning an OFFSET (P2).
// An empty `after` starts at the first row.
func (s *Store) NeighborRefsAfter(project, qualifiedName, direction, edgeType, after string, limit int) ([]RefNode, error) {
	if limit <= 0 {
		limit = 500
	}
	q, args, err := neighborSelect(refNodeCols, project, qualifiedName, direction, edgeType, after)
	if err != nil {
		return nil, err
	}
	q += ` ORDER BY n.qualified_name LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []RefNode
	for rows.Next() {
		r, err := scanRefNode(rows)
		if err != nil {
			return nil, err
		}
		refs = append(refs, r)
	}
	return refs, rows.Err()
}
