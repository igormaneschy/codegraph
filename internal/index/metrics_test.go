package index

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Lordymine/codegraph/internal/scip"
	scippb "github.com/scip-code/scip/bindings/go/scip"
)

func TestRunMetrics_NoOpAndScopeReuse(t *testing.T) {
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "main.go", "package fixture\nfunc Run() {}\n")
	writeSecurityFile(t, root, "gateway.rb", "class Gateway\n def self.authorize\n end\nend\n")
	db := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	first, err := RunAtomic(db, root)
	if err != nil {
		t.Fatal(err)
	}
	assertRunMetrics(t, first, "rebuild", "success")
	if !slices.Contains(first.Metrics.InvalidationReasons, "new-index") {
		t.Fatalf("reasons=%v", first.Metrics.InvalidationReasons)
	}
	noop, err := RunAtomic(db, root)
	if err != nil || !noop.Reused {
		t.Fatalf("noop=%+v err=%v", noop, err)
	}
	assertRunMetrics(t, noop, "noop", "success")
	if noop.Metrics.StagedBytes != 0 || len(noop.Metrics.InvalidationReasons) != 0 || !slices.Equal(noop.Metrics.ReusedScopes, []string{"ruby-static[ruby]"}) {
		t.Fatalf("noop metrics=%+v", noop.Metrics)
	}
	writeSecurityFile(t, root, "main.go", "package fixture\nfunc Run() {}\nfunc Added() {}\n")
	changed, err := RunAtomic(db, root)
	if err != nil || changed.Reused {
		t.Fatalf("changed=%+v err=%v", changed, err)
	}
	assertRunMetrics(t, changed, "rebuild", "success")
	if !slices.Contains(changed.Metrics.InvalidationReasons, "source-changed") || !slices.Equal(changed.Metrics.ReusedScopes, []string{"ruby-static[ruby]"}) {
		t.Fatalf("incremental metrics=%+v", changed.Metrics)
	}
}

func TestRunMetrics_GoEnvironmentViewsCountEvenOnNoOp(t *testing.T) {
	root := securityPhysicalTempDir(t)
	module := "module example.test/metrics\ngo 1.26\n"
	source := "package fixture\nfunc Run() {}\n"
	writeSecurityFile(t, root, "go.mod", module)
	writeSecurityFile(t, root, "source.go", source)
	db := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	built, err := RunAtomic(db, root)
	if err != nil || built.Status != StatusHealthy {
		t.Fatalf("build=%+v err=%v", built, err)
	}
	// Initial, validating and handoff observations each create a Go env view;
	// the resolver snapshot then writes the source and module once more.
	if built.Metrics.StagedFiles != 5 || built.Metrics.StagedBytes != int64(4*len(module)+len(source)) {
		t.Fatalf("build staging=%+v", built.Metrics)
	}
	noop, err := RunAtomic(db, root)
	if err != nil || !noop.Reused {
		t.Fatalf("noop=%+v err=%v", noop, err)
	}
	if noop.Metrics.StagedFiles != 2 || noop.Metrics.StagedBytes != int64(2*len(module)) {
		t.Fatalf("no-op must count both Go environment views: %+v", noop.Metrics)
	}
	for _, phase := range noop.Metrics.Phases {
		if phase.Phase == "staging" || phase.Phase == "definitions" {
			t.Fatalf("no-op entered pipeline: %+v", phase)
		}
	}
}

func TestRunMetrics_StagingDegradationAndFailedRefresh(t *testing.T) {
	root := securityPhysicalTempDir(t)
	inputs := map[string]string{"app.ts": "export function Run() {}\n", "tsconfig.json": "{}\n", "node_modules/demo/index.d.ts": "export function Call(): void;\n"}
	var bytes int64
	for rel, content := range inputs {
		writeSecurityFile(t, root, rel, content)
		bytes += int64(len(content))
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("npm_config_registry", "https://fixture-secret.invalid")
	db := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	first, err := RunAtomic(db, root)
	if err != nil || first.Status != StatusDegraded {
		t.Fatalf("degraded=%+v err=%v", first, err)
	}
	assertRunMetrics(t, first, "rebuild", "success")
	if first.Metrics.StagedFiles != len(inputs) || first.Metrics.StagedBytes != bytes {
		t.Fatalf("staging=%+v want %d files, %d bytes", first.Metrics, len(inputs), bytes)
	}
	before := digestOf(t, db, first.Project)
	failed, err := RunAtomic(db, root)
	var failure *ResolverFailure
	if !errors.As(err, &failure) || digestOf(t, db, first.Project) != before {
		t.Fatalf("refresh=%+v err=%v", failed, err)
	}
	assertRunMetrics(t, failed, "rebuild", "failed")
	if !slices.Contains(failed.Metrics.InvalidationReasons, "ts-runtime-inputs-unobserved") {
		t.Fatalf("reasons=%v", failed.Metrics.InvalidationReasons)
	}
	if strings.Contains(failed.MetricsSummary(), "fixture-secret") {
		t.Fatal("metrics expose environment")
	}
}

func TestRunMetrics_PreservesSCIPResourcesOnCancellation(t *testing.T) {
	root := securityPhysicalTempDir(t)
	writeSecurityFile(t, root, "app.ts", "export function Run() {}\n")
	writeSecurityFile(t, root, "tsconfig.json", "{}\n")
	original := scipRunAndRead
	t.Cleanup(func() { scipRunAndRead = original })
	scipRunAndRead = cancelledSCIPMetrics
	db := filepath.Join(securityPhysicalTempDir(t), "graph.db")
	result, err := RunAtomic(db, root)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled SCIP err=%v", err)
	}
	assertRunMetrics(t, result, "rebuild", "cancelled")
	if result.Metrics.ScipInvocations != 1 || result.Metrics.ScipPeakRSSBytes != 42*1024*1024 {
		t.Fatalf("cancelled run lost resource metrics: %+v", result.Metrics)
	}
	if _, err := ReadManifest(db); err == nil {
		t.Fatal("cancelled run published a manifest")
	}
}

func cancelledSCIPMetrics(context.Context, string, string, *scip.ExecutionEnvironment) (*scippb.Index, scip.RunStats, error) {
	return nil, scip.RunStats{PeakRSSBytes: 42 * 1024 * 1024}, context.Canceled
}

func TestRunMetrics_CancellationAndInvalidRoot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := RunAtomicContext(ctx, filepath.Join(t.TempDir(), "graph.db"), t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	assertRunMetrics(t, result, "undecided", "cancelled")
	missing := filepath.Join(t.TempDir(), "absent")
	result, err = RunAtomic(filepath.Join(t.TempDir(), "graph.db"), missing)
	if err == nil {
		t.Fatal("missing root accepted")
	}
	assertRunMetrics(t, result, "undecided", "failed")
}

func TestMetricsSummaryBoundsAndQuotesScopeNames(t *testing.T) {
	metrics := RunMetrics{Decision: "noop", Outcome: "success", Duration: time.Second}
	for i := 0; i < 50; i++ {
		metrics.ReusedScopes = append(metrics.ReusedScopes, "scope\nforged=1"+strings.Repeat("scope", 1000))
	}
	text := (Result{Metrics: metrics}).MetricsSummary()
	if len(text) > 10000 || strings.Count(text, "reused_scope=") != 20 || !strings.Contains(text, "reused_scopes_omitted=30") || strings.Contains(text, "\nforged=1") || !strings.Contains(text, "\\nforged=1") || !strings.Contains(text, "truncated=true") {
		t.Fatalf("unbounded or unescaped summary: %s", text)
	}
}

func assertRunMetrics(t *testing.T, result Result, decision, outcome string) {
	t.Helper()
	metrics := result.Metrics
	if metrics.Decision != decision || metrics.Outcome != outcome || metrics.Duration <= 0 || len(metrics.Phases) == 0 {
		t.Fatalf("metrics=%+v want %s/%s", metrics, decision, outcome)
	}
	var sum int64
	seen := map[string]bool{}
	for _, phase := range metrics.Phases {
		if phase.Duration < 0 || phase.Phase == "" || seen[phase.Phase] {
			t.Fatalf("invalid phase=%+v", phase)
		}
		seen[phase.Phase] = true
		sum += int64(phase.Duration)
	}
	if sum > int64(metrics.Duration) {
		t.Fatalf("overlapping timings: sum=%d total=%d", sum, metrics.Duration)
	}
	if !strings.Contains(result.MetricsSummary(), "staged_bytes=") {
		t.Fatal("missing readable metrics")
	}
}
