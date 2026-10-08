package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/Lordymine/codegraph/internal/quality"
	"github.com/Lordymine/codegraph/internal/securefile"
)

func cmdQualityScoreArgs(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: codegraph quality score <dir> [--scorer qualified-name-v1|name-v1] [--modes graph,baseline]")
	}
	flags := flag.NewFlagSet("quality score", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	scorer := flags.String("scorer", string(quality.ScorerStrict), "versioned identity scorer")
	modes := flags.String("modes", "graph,baseline", "complete expected mode matrix")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected quality score arguments %q: flags follow the run directory", flags.Args())
	}
	options := quality.EvaluationOptions{Scorer: quality.Scorer(*scorer), Modes: strings.Split(*modes, ",")}
	return cmdQualityScoreWithOptions(args[0], options)
}

func cmdQualityScore(dir string) error {
	return cmdQualityScoreWithOptions(dir, quality.EvaluationOptions{})
}

func cmdQualityScoreWithOptions(dir string, options quality.EvaluationOptions) error {
	if err := securefile.MkdirAllPrivate(dir); err != nil {
		return fmt.Errorf("prepare private quality directory: %w", err)
	}
	var questions []quality.Question
	var truths []quality.Truth
	var answers []quality.Answer
	if err := readJSON(filepath.Join(dir, "questions.json"), &questions); err != nil {
		return err
	}
	if err := readJSON(filepath.Join(dir, "truth.json"), &truths); err != nil {
		return err
	}
	if err := readJSON(filepath.Join(dir, "answers.json"), &answers); err != nil {
		return err
	}
	report, err := quality.Report(questions, truths, answers, options)
	if err != nil {
		return fmt.Errorf("invalid quality run in %q: %w", dir, err)
	}
	if err := writePrivate(filepath.Join(dir, "report.md"), []byte(report)); err != nil {
		return err
	}
	fmt.Print(report)
	return nil
}
