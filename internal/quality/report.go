package quality

import (
	"fmt"
	"sort"
	"strings"
)

// Report renders a labelled complete-run comparison or no report on failure.
// Example: Report(qs, truths, answers, EvaluationOptions{}) uses strict identity.
func Report(qs []Question, truths []Truth, answers []Answer, options EvaluationOptions) (string, error) {
	evaluation, err := Evaluate(qs, truths, answers, options)
	if err != nil {
		return "", err
	}
	var report strings.Builder
	writeReportIntro(&report, evaluation, len(qs))
	modes := modesOf(evaluation.Aggregates)
	writeCostSummary(&report, evaluation.Aggregates, modes)
	writeTypeSummary(&report, evaluation.Aggregates, modes)
	writeQuestionSummary(&report, qs, evaluation.Scores, modes)
	report.WriteString("\n> Quality = set F1 vs independent oracle truth for callers/callees, declaration location match for definition, finite LLM-judge (0–100%) for open. Strict identity is case-sensitive QN/full-path:exact-line; legacy identity is normalized name/basename:±3-lines. See docs/QUALITY.md.\n")
	return report.String(), nil
}

func writeReportIntro(report *strings.Builder, evaluation Evaluation, questions int) {
	report.WriteString("# codegraph quality harness\n\n")
	fmt.Fprintf(report, "scorer=%s · questions=%d · modes=%s\n\n", evaluation.Scorer, questions, markdownCell(strings.Join(evaluation.Modes, ",")))
	if evaluation.Scorer == ScorerLegacy {
		report.WriteString("> Legacy identity scoring: names/basenames can collapse homonyms. Not comparable to qualified-name-v1.\n\n")
	}
}

func writeCostSummary(report *strings.Builder, aggregates map[string]Agg, modes []string) {
	report.WriteString("## Answer quality vs cost\n\n")
	report.WriteString("| mode | mean quality | tokens | tool calls |\n|---|--:|--:|--:|\n")
	for _, mode := range modes {
		aggregate := aggregates[mode]
		fmt.Fprintf(report, "| **%s** | %.0f%% | %d | %d |\n", markdownCell(mode), 100*aggregate.MeanQuality, aggregate.TotalTokens, aggregate.TotalCalls)
	}
}

func writeTypeSummary(report *strings.Builder, aggregates map[string]Agg, modes []string) {
	report.WriteString("\n## Quality by question type\n\n")
	report.WriteString("| mode | callers | callees | definition | open |\n|---|--:|--:|--:|--:|\n")
	for _, mode := range modes {
		report.WriteString("| **" + markdownCell(mode) + "** |")
		for _, typ := range []QType{TypeCallers, TypeCallees, TypeDefinition, TypeOpen} {
			quality, exists := aggregates[mode].ByType[typ]
			fmt.Fprintf(report, " %s |", pctQuality(quality, exists))
		}
		report.WriteByte('\n')
	}
}

func writeQuestionSummary(report *strings.Builder, qs []Question, scores []Score, modes []string) {
	report.WriteString("\n## Per question\n\n| id | type |")
	for _, mode := range modes {
		fmt.Fprintf(report, " %s |", markdownCell(mode))
	}
	report.WriteString("\n|---|---|" + strings.Repeat("--:|", len(modes)) + "\n")
	byKey := map[answerKey]Score{}
	for _, score := range scores {
		byKey[answerKey{score.ID, score.Mode}] = score
	}
	for _, q := range qs {
		fmt.Fprintf(report, "| `%s` | %s |", markdownCell(q.ID), q.Type)
		for _, mode := range modes {
			fmt.Fprintf(report, " %.0f%% |", 100*byKey[answerKey{q.ID, mode}].Quality)
		}
		report.WriteByte('\n')
	}
}

func pctQuality(quality float64, exists bool) string {
	if !exists {
		return "–"
	}
	return fmt.Sprintf("%.0f%%", 100*quality)
}

func markdownCell(text string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "|", "&#124;", "`", "&#96;").Replace(text)
}

// modesOf keeps graph then baseline first; additional declared modes are sorted.
func modesOf(aggregates map[string]Agg) []string {
	var modes, other []string
	for _, mode := range []string{"graph", "baseline"} {
		if _, exists := aggregates[mode]; exists {
			modes = append(modes, mode)
		}
	}
	for mode := range aggregates {
		if mode != "graph" && mode != "baseline" {
			other = append(other, mode)
		}
	}
	sort.Strings(other)
	return append(modes, other...)
}
