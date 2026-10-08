package quality

import (
	"fmt"
	"math"
	"path"
	"strconv"
	"strings"
)

// Evaluation labels the method and complete mode matrix alongside its scores.
type Evaluation struct {
	Scorer     Scorer         `json:"scorer"`
	Modes      []string       `json:"modes"`
	Scores     []Score        `json:"scores"`
	Aggregates map[string]Agg `json:"aggregates"`
}

// Evaluate validates the whole run before grading, never inferring completeness
// from the answers that happen to be present. Example: Evaluate(qs, truths,
// answers, EvaluationOptions{}) uses exact QNs and expects graph+baseline.
func Evaluate(qs []Question, truths []Truth, answers []Answer, options EvaluationOptions) (Evaluation, error) {
	plan, err := validateEvaluation(qs, truths, answers, options)
	if err != nil {
		return Evaluation{}, err
	}
	var scores []Score
	for _, q := range qs {
		for _, mode := range plan.options.Modes {
			scores = append(scores, scoreAnswer(q, plan.truths[q.ID], plan.answers[answerKey{q.ID, mode}], plan.options.Scorer))
		}
	}
	aggregates, err := aggregateScores(qs, scores, plan.answers)
	if err != nil {
		return Evaluation{}, err
	}
	return Evaluation{Scorer: plan.options.Scorer, Modes: plan.options.Modes, Scores: scores, Aggregates: aggregates}, nil
}

func scoreAnswer(q Question, truth Truth, answer Answer, scorer Scorer) Score {
	score := Score{ID: q.ID, Mode: answer.Mode, Type: q.Type}
	switch q.Type {
	case TypeOpen:
		score.Quality = *answer.Judge
	case TypeDefinition:
		if scorer == ScorerLegacy {
			score.Quality = b2f(matchDefinition(answer.Items, truth.Items))
		} else {
			score.Quality = b2f(strictDefinition(answer.Items, truth.Items))
		}
	default:
		if scorer == ScorerLegacy {
			score.Precision, score.Recall, score.Quality = f1(answer.Items, truth.Items)
		} else {
			score.Precision, score.Recall, score.Quality = setF1(exactSet(answer.Items), exactSet(truth.Items))
		}
		return score
	}
	score.Precision, score.Recall = score.Quality, score.Quality
	return score
}

func aggregateScores(qs []Question, scores []Score, answers map[answerKey]Answer) (map[string]Agg, error) {
	counts := map[QType]int{}
	for _, q := range qs {
		counts[q.Type]++
	}
	aggregates := map[string]Agg{}
	for _, score := range scores {
		aggregate := aggregates[score.Mode]
		if aggregate.ByType == nil {
			aggregate = Agg{Mode: score.Mode, ByType: map[QType]float64{}}
		}
		if err := addScore(&aggregate, score, answers[answerKey{score.ID, score.Mode}]); err != nil {
			return nil, err
		}
		aggregates[score.Mode] = aggregate
	}
	for mode, aggregate := range aggregates {
		aggregate.MeanQuality /= float64(aggregate.N)
		for typ, sum := range aggregate.ByType {
			aggregate.ByType[typ] = sum / float64(counts[typ])
		}
		aggregates[mode] = aggregate
	}
	return aggregates, nil
}

func addScore(aggregate *Agg, score Score, answer Answer) error {
	if answer.Tokens > math.MaxInt-aggregate.TotalTokens || answer.Calls > math.MaxInt-aggregate.TotalCalls {
		return fmt.Errorf("cost overflow for question %q in mode %q: tokens=%d calls=%d cannot be aggregated as int", answer.ID, answer.Mode, answer.Tokens, answer.Calls)
	}
	aggregate.N++
	aggregate.MeanQuality += score.Quality
	aggregate.ByType[score.Type] += score.Quality
	aggregate.TotalTokens += answer.Tokens
	aggregate.TotalCalls += answer.Calls
	return nil
}

// f1 retains the explicitly selected legacy name-normalization semantics.
func f1(answer, truth []string) (p, r, f float64) {
	return setF1(toSet(answer), toSet(truth))
}

func setF1(A, T map[string]bool) (p, r, f float64) {
	if len(T) == 0 {
		if len(A) == 0 {
			return 1, 1, 1 // correctly said "nothing"
		}
		return 0, 1, 0 // claimed callers where there are none
	}
	tp := 0
	for x := range A {
		if T[x] {
			tp++
		}
	}
	if len(A) > 0 {
		p = float64(tp) / float64(len(A))
	}
	r = float64(tp) / float64(len(T))
	if p+r > 0 {
		f = 2 * p * r / (p + r)
	}
	return p, r, f
}

func toSet(xs []string) map[string]bool {
	s := map[string]bool{}
	for _, x := range xs {
		if n := normName(x); n != "" {
			s[n] = true
		}
	}
	return s
}

// normName reduces a symbol reference to its bare identifier, lowercased, so the
// many shapes agents emit all compare equal:
//
//	getActiveCode
//	Service.getActiveCode            ->  getactivecode
//	x.getActiveCode()                ->  getactivecode
//	path/file.tsx:Button             ->  button         (name after the path)
//	Name (path/file.tsx:42)          ->  name           (trailing location annotation)
//	Name @ path/file.tsx:42          ->  name
//
// Crucial: the trailing-annotation forms must be stripped FIRST, otherwise the
// last ':' segment is a LINE NUMBER, not the name. (Collisions between same-named
// methods in different files fold together — an accepted approximation, see
// docs/QUALITY.md.)
func normName(s string) string {
	s = strings.TrimSpace(s)
	// 1) drop any trailing location annotation the responder appended.
	for _, sep := range []string{" (", " @ ", " -> ", " => ", " - ", "\t"} {
		if i := strings.Index(s, sep); i >= 0 {
			s = s[:i]
		}
	}
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "()"))
	// 2) "path/file.tsx:Name" -> "Name" (only when the tail is an identifier, not
	// a line number). Without a path, still split a trailing "file:Name".
	if c := strings.LastIndex(s, ":"); c >= 0 && isIdentStart(s[c+1:]) {
		s = s[c+1:]
	}
	// 3) "Owner.method" -> "method".
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSuffix(s, "()")
	return strings.ToLower(strings.TrimSpace(s))
}

func isIdentStart(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	b := s[0]
	return b == '_' || b == '$' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// matchDefinition is true if the answer points at the same file (by basename)
// and a line within ±3 of any truth entry. A missing line matches on file alone.
func matchDefinition(answer, truth []string) bool {
	for _, a := range answer {
		af, al := splitFileLine(a)
		if af == "" {
			continue
		}
		for _, t := range truth {
			tf, tl := splitFileLine(t)
			if tf == "" || path.Base(af) != path.Base(tf) {
				continue
			}
			if al == 0 || tl == 0 || abs(al-tl) <= 3 {
				return true
			}
		}
	}
	return false
}

func splitFileLine(s string) (file string, line int) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\\", "/"))
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return s, 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(s[i+1:])); err == nil {
		return strings.TrimSpace(s[:i]), n
	}
	return s, 0
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}
