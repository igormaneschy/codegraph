// Package bench measures the project's core bet — token efficiency — the same
// headline the upstream codebase-memory-mcp reports (10x fewer tokens, 2.1x
// fewer tool calls on structural questions; see docs/UPSTREAM.md).
//
// For each structural question ("who calls X?") it compares three strategies:
//
//	graph         — one callers/callees query, returns compact refs (1 tool call)
//	baseline-win  — grep X, then read a ±window around each match (1+N calls)
//	baseline-file — grep X, then read each whole matched file (1+N calls)
//
// The graph wins because it has already resolved the enclosing caller of every
// match and filtered out definitions/imports/homonyms — exactly the work grep
// dumps onto the agent. The reported number is a RATIO (baseline/graph), so the
// rough token estimate cancels out. We do NOT measure answer quality here: that
// needs an LLM-as-judge harness and would be a romantic number, not an
// engineered one. This file measures only what is deterministic.
package bench

import (
	"os"
	"sort"
	"strings"

	"github.com/Lordymine/codegraph/internal/graph"
	"github.com/Lordymine/codegraph/internal/index"
	"github.com/Lordymine/codegraph/internal/query"
)

// Method stamps the wire format the graph side meters. Historical headline
// numbers stay tagged with the method that produced them (tsv-uncapped-v0
// before P2) and must not be compared directly with paged-wire-v1 runs.
const Method = "paged-wire-v1"

// ProductPageRefs is the page size the MCP/CLI tools serve by default; the
// benchmark meters that same size unless a test overrides it.
const ProductPageRefs = 500

// windowRadius is how many lines above and below a grep match the efficient
// baseline agent reads to see the enclosing function. ±10 is generous to the
// baseline (more context than strictly needed) — it makes the graph's win a
// floor, not an inflated ceiling.
const windowRadius = 10

// EstimateTokens approximates LLM token count as bytes/4 (the classic ~4
// chars/token rule). The absolute value is rough, but the same function meters
// both sides of every comparison, so the RATIO — the only number we report — is
// stable under the approximation.
func EstimateTokens(s string) int {
	n := (len(s) + 3) / 4
	if n < 1 {
		return 1
	}
	return n
}

// Question is one structural query to benchmark.
type Question struct {
	Kind string // "callers" | "callees"
	QN   string // qualified name to query
	Name string // short symbol name the grep baseline searches for
}

// Cost is tokens + tool-calls for one strategy answering one Question.
type Cost struct {
	Tokens int
	Calls  int
}

// Outcome is the full three-way comparison for one Question.
type Outcome struct {
	Question     Question
	Graph        Cost
	BaselineWin  Cost // grep + read ±window around each match
	BaselineFile Cost // grep + read whole file per match
	MatchFiles   int  // distinct files the grep baseline had to open
	GraphResults int  // nodes the graph returned
	// Completeness against an independent oracle (see oracle.go). Oracle < 0
	// means unknown (e.g. `codegraph bench` on a real repo, where no oracle
	// exists); otherwise Complete reports whether the graph returned exactly
	// the oracle's caller set — never just "the graph agrees with itself".
	Oracle      int
	Complete    bool
	IndexStatus string // manifest status behind this outcome (healthy/degraded)
}

// QuestionsFromHubs turns call hubs into "who calls X" questions.
func QuestionsFromHubs(hubs []graph.Node) []Question {
	qs := make([]Question, 0, len(hubs))
	for _, h := range hubs {
		qs = append(qs, Question{Kind: "callers", QN: h.QualifiedName, Name: h.Name})
	}
	return qs
}

// Corpus is the repo's source loaded once, reused across every question's grep.
type Corpus struct {
	files []srcFile
}

type srcFile struct {
	rel   string
	lines []string
}

// LoadCorpus reads every discoverable source file into memory (the same files
// the indexer sees), so the baseline's grep is apples-to-apples with the graph.
func LoadCorpus(root string) (*Corpus, error) {
	files, err := index.Discover(root)
	if err != nil {
		return nil, err
	}
	c := &Corpus{}
	for _, f := range files {
		data, err := os.ReadFile(f.AbsPath)
		if err != nil {
			continue
		}
		c.files = append(c.files, srcFile{rel: f.RelPath, lines: strings.Split(string(data), "\n")})
	}
	return c, nil
}

// grep returns, per file index, the 0-based line numbers where name appears as a
// whole identifier token (mimicking `grep -w name`).
func (c *Corpus) grep(name string) map[int][]int {
	hits := map[int][]int{}
	for fi := range c.files {
		for li, line := range c.files[fi].lines {
			if containsWord(line, name) {
				hits[fi] = append(hits[fi], li)
			}
		}
	}
	return hits
}

// baselineCost models the two grep-based agents answering one question.
func (c *Corpus) baselineCost(name string) (win, file Cost) {
	hits := c.grep(name)
	var grepOut strings.Builder
	winTokens, fileTokens, nFiles := 0, 0, 0
	for fi, lis := range hits {
		nFiles++
		f := c.files[fi]
		for _, li := range lis {
			grepOut.WriteString(f.rel)
			grepOut.WriteByte(':')
			grepOut.WriteString(f.lines[li]) // the matched line (lineno cost is negligible)
			grepOut.WriteByte('\n')
		}
		winTokens += EstimateTokens(windowText(f.lines, lis, windowRadius))
		fileTokens += EstimateTokens(strings.Join(f.lines, "\n"))
	}
	grepTokens := EstimateTokens(grepOut.String())
	win = Cost{Tokens: grepTokens + winTokens, Calls: 1 + nFiles}
	file = Cost{Tokens: grepTokens + fileTokens, Calls: 1 + nFiles}
	return win, file
}

// graphCostPages runs the actual graph query and meters it in the SAME paged
// wire format the MCP/CLI tools return — every real page, trailer lines
// included — so the reported ratio reflects the real product, not a
// measurement trick. Calls counts one round-trip per page walked. The product
// default (500) is what ships; smaller sizes exist so tests can prove a hub
// question returns the identical set at any page size — shrinking the page
// must never manufacture a cheaper-looking answer by truncating it.
func graphCostPages(eng *query.Engine, q Question, pageSize int) (Cost, map[string]bool, error) {
	var (
		tokens int
		calls  int
		cursor string
	)
	set := map[string]bool{}
	for {
		var (
			page query.RefPage
			err  error
		)
		switch q.Kind {
		case "callees":
			page, err = eng.CalleesPage(q.QN, pageSize, cursor)
		default: // "callers"
			page, err = eng.CallersPage(q.QN, pageSize, cursor)
		}
		if err != nil {
			return Cost{}, nil, err
		}
		tokens += EstimateTokens(page.WireText())
		calls++
		for _, r := range page.Refs {
			set[r.QualifiedName] = true
		}
		if !page.HasMore {
			break
		}
		cursor = page.Cursor
	}
	return Cost{Tokens: tokens, Calls: calls}, set, nil
}

// RunOne benchmarks a single question across all three strategies. The oracle
// is unknown (-1): no completeness claim.
func RunOne(eng *query.Engine, c *Corpus, q Question) (Outcome, error) {
	return RunOneWithOracle(eng, c, q, nil)
}

// RunOneWithOracle benchmarks one question and checks completeness against an
// independent oracle set (qualified names; nil = unknown, no claim). Complete
// is true only when the graph returns exactly the oracle set — same question
// semantics on both sides, no truncation win.
func RunOneWithOracle(eng *query.Engine, c *Corpus, q Question, oracle map[string]bool) (Outcome, error) {
	g, set, err := graphCostPages(eng, q, ProductPageRefs)
	if err != nil {
		return Outcome{}, err
	}
	win, file := c.baselineCost(q.Name)
	o := Outcome{
		Question: q, Graph: g, BaselineWin: win, BaselineFile: file,
		MatchFiles: win.Calls - 1, GraphResults: len(set),
		Oracle: -1,
	}
	if oracle != nil {
		o.Oracle = len(oracle)
		o.Complete = len(set) == len(oracle)
		if o.Complete {
			for qn := range set {
				// Oracle keys are project-relative (see oracle.go); strip the
				// served prefix before comparing — same question semantics.
				if !oracle[query.StripProjectPrefix(qn)] {
					o.Complete = false
					break
				}
			}
		}
	}
	return o, nil
}

// Summary aggregates outcomes. Two aggregate ratios are reported because they
// answer different questions: the MEDIAN per-query ratio (typical case, robust
// to one common-named outlier) and the TOTAL/TOTAL ratio (the "10x" headline:
// total tokens an agent spends across the whole question set).
type Summary struct {
	N                  int
	MedianRatioWin     float64
	MedianRatioFile    float64
	TotalRatioWin      float64
	TotalRatioFile     float64
	CallRatioWin       float64 // baseline-win tool-calls / graph tool-calls (total)
	GraphTokens        int
	BaselineWinTokens  int
	BaselineFileTokens int
	GraphCalls         int
	BaselineWinCalls   int
	BaselineFileCalls  int
}

// Summarize folds outcomes into aggregate ratios.
func Summarize(outs []Outcome) Summary {
	var s Summary
	s.N = len(outs)
	var ratWin, ratFile []float64
	for _, o := range outs {
		s.GraphTokens += o.Graph.Tokens
		s.BaselineWinTokens += o.BaselineWin.Tokens
		s.BaselineFileTokens += o.BaselineFile.Tokens
		s.GraphCalls += o.Graph.Calls
		s.BaselineWinCalls += o.BaselineWin.Calls
		s.BaselineFileCalls += o.BaselineFile.Calls
		ratWin = append(ratWin, ratio(o.BaselineWin.Tokens, o.Graph.Tokens))
		ratFile = append(ratFile, ratio(o.BaselineFile.Tokens, o.Graph.Tokens))
	}
	s.MedianRatioWin = median(ratWin)
	s.MedianRatioFile = median(ratFile)
	s.TotalRatioWin = ratio(s.BaselineWinTokens, s.GraphTokens)
	s.TotalRatioFile = ratio(s.BaselineFileTokens, s.GraphTokens)
	s.CallRatioWin = ratio(s.BaselineWinCalls, s.GraphCalls)
	return s
}

func ratio(a, b int) float64 {
	if b == 0 {
		b = 1
	}
	return float64(a) / float64(b)
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := append([]float64(nil), xs...)
	sort.Float64s(cp)
	mid := len(cp) / 2
	if len(cp)%2 == 1 {
		return cp[mid]
	}
	return (cp[mid-1] + cp[mid]) / 2
}

// windowText returns the union of ±radius line windows around each match index.
func windowText(lines []string, idxs []int, radius int) string {
	if len(lines) == 0 {
		return ""
	}
	keep := make([]bool, len(lines))
	for _, li := range idxs {
		lo, hi := li-radius, li+radius
		if lo < 0 {
			lo = 0
		}
		if hi >= len(lines) {
			hi = len(lines) - 1
		}
		for k := lo; k <= hi; k++ {
			keep[k] = true
		}
	}
	var b strings.Builder
	for k, on := range keep {
		if on {
			b.WriteString(lines[k])
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func containsWord(line, word string) bool {
	if word == "" {
		return false
	}
	from := 0
	for from <= len(line)-len(word) {
		i := strings.Index(line[from:], word)
		if i < 0 {
			return false
		}
		i += from
		leftOK := i == 0 || !isIdentChar(line[i-1])
		rj := i + len(word)
		rightOK := rj >= len(line) || !isIdentChar(line[rj])
		if leftOK && rightOK {
			return true
		}
		from = i + 1
	}
	return false
}

func isIdentChar(b byte) bool {
	return b == '_' || b == '$' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
