package quality

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Scorer versions identity semantics, not resolver or graph analysis.
type Scorer string

const (
	ScorerStrict Scorer = "qualified-name-v1"
	ScorerLegacy Scorer = "name-v1"
)

type EvaluationOptions struct {
	Scorer Scorer
	Modes  []string // nil selects graph+baseline; an explicit empty list is invalid
}

type answerKey struct{ id, mode string }

type evaluationPlan struct {
	options EvaluationOptions
	truths  map[string]Truth
	answers map[answerKey]Answer
}

func normalizedOptions(options EvaluationOptions) (EvaluationOptions, error) {
	if options.Scorer == "" {
		options.Scorer = ScorerStrict
	}
	if options.Scorer != ScorerStrict && options.Scorer != ScorerLegacy {
		return options, fmt.Errorf("invalid scorer %q: want %q or %q", options.Scorer, ScorerStrict, ScorerLegacy)
	}
	if options.Modes == nil {
		options.Modes = []string{"graph", "baseline"}
	}
	if len(options.Modes) == 0 {
		return options, fmt.Errorf("empty modes: declare at least one expected mode")
	}
	seen := map[string]bool{}
	for _, mode := range options.Modes {
		if !validRunID(mode) || seen[mode] {
			return options, fmt.Errorf("invalid or duplicate mode %q: want unique nonempty mode names without surrounding whitespace or controls", mode)
		}
		seen[mode] = true
	}
	options.Modes = slices.Clone(options.Modes)
	return options, nil
}

func validateEvaluation(qs []Question, truths []Truth, answers []Answer, options EvaluationOptions) (evaluationPlan, error) {
	options, err := normalizedOptions(options)
	if err != nil {
		return evaluationPlan{}, err
	}
	questions, err := indexedQuestions(qs)
	if err != nil {
		return evaluationPlan{}, err
	}
	truthIndex, err := indexedTruths(qs, questions, truths, options.Scorer)
	if err != nil {
		return evaluationPlan{}, err
	}
	answerIndex, err := indexedAnswers(qs, questions, answers, options)
	if err != nil {
		return evaluationPlan{}, err
	}
	return evaluationPlan{options: options, truths: truthIndex, answers: answerIndex}, nil
}

func indexedQuestions(qs []Question) (map[string]Question, error) {
	if len(qs) == 0 {
		return nil, fmt.Errorf("empty question set: want at least one independently graded question")
	}
	index := make(map[string]Question, len(qs))
	for _, q := range qs {
		if !validRunID(q.ID) {
			return nil, fmt.Errorf("invalid question ID %q: want nonempty ID without surrounding whitespace or controls", q.ID)
		}
		if _, exists := index[q.ID]; exists {
			return nil, fmt.Errorf("duplicate question ID %q", q.ID)
		}
		switch q.Type {
		case TypeCallers, TypeCallees, TypeDefinition, TypeOpen:
		default:
			return nil, fmt.Errorf("question %q has unsupported type %q: want callers, callees, definition or open", q.ID, q.Type)
		}
		index[q.ID] = q
	}
	return index, nil
}

func indexedTruths(qs []Question, questions map[string]Question, truths []Truth, scorer Scorer) (map[string]Truth, error) {
	index := make(map[string]Truth, len(truths))
	for _, truth := range truths {
		q, exists := questions[truth.ID]
		if !exists {
			return nil, fmt.Errorf("truth references unknown question %q", truth.ID)
		}
		if _, exists := index[truth.ID]; exists {
			return nil, fmt.Errorf("duplicate truth for question %q", truth.ID)
		}
		if err := validateTruth(q, truth, scorer); err != nil {
			return nil, err
		}
		index[truth.ID] = truth
	}
	for _, q := range qs {
		if _, exists := index[q.ID]; !exists {
			return nil, fmt.Errorf("missing truth for question %q", q.ID)
		}
	}
	return index, nil
}

func validateTruth(q Question, truth Truth, scorer Scorer) error {
	if q.Type == TypeOpen {
		if strings.TrimSpace(truth.Notes) == "" {
			return fmt.Errorf("truth for open question %q needs independent rubric notes", q.ID)
		}
		return nil
	}
	if err := validateItems(q, truth.Items, scorer, "truth"); err != nil {
		return err
	}
	if q.Type == TypeDefinition && len(truth.Items) != 1 {
		return fmt.Errorf("definition truth for question %q has %d locations: want exactly one declaration location", q.ID, len(truth.Items))
	}
	return nil
}

func indexedAnswers(qs []Question, questions map[string]Question, answers []Answer, options EvaluationOptions) (map[answerKey]Answer, error) {
	index := make(map[answerKey]Answer, len(answers))
	for _, answer := range answers {
		q, exists := questions[answer.ID]
		if !exists {
			return nil, fmt.Errorf("answer references unknown question %q in mode %q", answer.ID, answer.Mode)
		}
		if !slices.Contains(options.Modes, answer.Mode) {
			return nil, fmt.Errorf("answer for question %q has unexpected mode %q: expected %v", answer.ID, answer.Mode, options.Modes)
		}
		key := answerKey{answer.ID, answer.Mode}
		if _, exists := index[key]; exists {
			return nil, fmt.Errorf("duplicate answer for question %q in mode %q", answer.ID, answer.Mode)
		}
		if err := validateAnswer(q, answer, options.Scorer); err != nil {
			return nil, err
		}
		index[key] = answer
	}
	for _, q := range qs {
		for _, mode := range options.Modes {
			if _, exists := index[answerKey{q.ID, mode}]; !exists {
				return nil, fmt.Errorf("missing answer for question %q in mode %q", q.ID, mode)
			}
		}
	}
	return index, nil
}

func validateAnswer(q Question, answer Answer, scorer Scorer) error {
	if answer.Tokens < 0 || answer.Calls < 0 {
		return fmt.Errorf("answer for question %q in mode %q has tokens=%d calls=%d: want nonnegative costs", q.ID, answer.Mode, answer.Tokens, answer.Calls)
	}
	if q.Type != TypeOpen {
		return validateItems(q, answer.Items, scorer, "answer in mode "+answer.Mode)
	}
	if strings.TrimSpace(answer.Text) == "" {
		return fmt.Errorf("answer for open question %q in mode %q needs text (including explicit abstention)", q.ID, answer.Mode)
	}
	if answer.Judge == nil || math.IsNaN(*answer.Judge) || math.IsInf(*answer.Judge, 0) || *answer.Judge < 0 || *answer.Judge > 1 {
		return fmt.Errorf("answer for open question %q in mode %q has judge=%v: want a finite score in [0,1]", q.ID, answer.Mode, judgeValue(answer.Judge))
	}
	return nil
}

func judgeValue(judge *float64) string {
	if judge == nil {
		return "missing"
	}
	return fmt.Sprintf("%g", *judge)
}

func validateItems(q Question, items []string, scorer Scorer, source string) error {
	if items == nil {
		return fmt.Errorf("%s for question %q needs an explicit items array: [] means known empty, null/missing is not evidence", source, q.ID)
	}
	for _, item := range items {
		if strings.TrimSpace(item) == "" {
			return fmt.Errorf("%s for question %q has empty item %q", source, q.ID, item)
		}
		if scorer == ScorerLegacy {
			continue
		}
		var err error
		if q.Type == TypeDefinition {
			err = validateLocation(item)
		} else {
			err = validateQualifiedName(item)
		}
		if err != nil {
			return fmt.Errorf("%s for question %q has item %q: %w", source, q.ID, item, err)
		}
	}
	return nil
}

func validRunID(id string) bool {
	return utf8.ValidString(id) && id != "" && strings.TrimSpace(id) == id && !strings.ContainsFunc(id, unicode.IsControl)
}
