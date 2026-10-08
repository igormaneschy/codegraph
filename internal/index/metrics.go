package index

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Lordymine/codegraph/internal/scip"
)

// RunMetrics describes actual work in one run, including unsuccessful runs.
// Timings and counters are diagnostic only: they never enter a manifest or
// freshness identity. Staging counts successful payload writes (including Go
// environment views), not allocated filesystem blocks or directory/link entries.
type RunMetrics struct {
	Duration            time.Duration `json:"duration_ns"`
	Phases              []PhaseTiming `json:"phases"`
	StagedFiles         int           `json:"staged_files"`
	StagedBytes         int64         `json:"staged_bytes"`
	ScipInvocations     int           `json:"scip_invocations"`
	ScipPeakRSSBytes    uint64        `json:"scip_peak_rss_bytes"`
	Decision            string        `json:"decision"`
	Outcome             string        `json:"outcome"`
	InvalidationReasons []string      `json:"invalidation_reasons,omitempty"`
	ReusedScopes        []string      `json:"reused_scopes,omitempty"`
}

type PhaseTiming struct {
	Phase    string        `json:"phase"`
	Duration time.Duration `json:"duration_ns"`
}

// The collector is local to one sequential pipeline; worker goroutines do not
// mutate it. A private context key propagates it to snapshot-copy helpers without
// global state or making standalone helpers require a metrics sink.
type runMetricsKey struct{}

func metricsFor(ctx context.Context) *RunMetrics {
	metrics, _ := ctx.Value(runMetricsKey{}).(*RunMetrics)
	return metrics
}

type phaseClock struct {
	metrics *RunMetrics
	phase   string
	started time.Time
}

func newPhaseClock(ctx context.Context, phase string) *phaseClock {
	clock := &phaseClock{metrics: metricsFor(ctx)}
	clock.next(phase)
	return clock
}

func (clock *phaseClock) next(phase string) {
	clock.stop()
	clock.phase = phase
	clock.started = time.Now()
}

func (clock *phaseClock) stop() {
	if clock.metrics == nil || clock.phase == "" {
		return
	}
	elapsed := time.Since(clock.started)
	for i := range clock.metrics.Phases {
		if clock.metrics.Phases[i].Phase == clock.phase {
			clock.metrics.Phases[i].Duration += elapsed
			clock.phase = ""
			return
		}
	}
	clock.metrics.Phases = append(clock.metrics.Phases, PhaseTiming{Phase: clock.phase, Duration: elapsed})
	clock.phase = ""
}

func recordSCIPRun(ctx context.Context, stats scip.RunStats) {
	if metrics := metricsFor(ctx); metrics != nil {
		metrics.ScipInvocations++
		metrics.ScipPeakRSSBytes = max(metrics.ScipPeakRSSBytes, stats.PeakRSSBytes)
	}
}

func recordStagedWrite(ctx context.Context, bytes int) {
	if metrics := metricsFor(ctx); metrics != nil {
		metrics.StagedFiles++
		metrics.StagedBytes += int64(bytes)
	}
}

type freshnessDecision struct {
	unchanged       bool
	existingGraph   bool
	graphUnreadable bool
	manifestTrusted bool
	rubyCurrent     bool
	changes         Changes
	uncertified     []string
}

func recordIndexDecision(ctx context.Context, decision freshnessDecision) {
	metrics := metricsFor(ctx)
	if metrics == nil {
		return
	}
	metrics.Decision = "rebuild"
	if decision.unchanged {
		metrics.Decision = "noop"
		return
	}
	reasons := slices.Clone(decision.uncertified)
	if !decision.existingGraph {
		reasons = append(reasons, "new-index")
	}
	if decision.changes.Any() {
		reasons = append(reasons, "source-changed")
	}
	if decision.graphUnreadable {
		reasons = append(reasons, "graph-unreadable")
	}
	// A generic trust miss is deliberately not labelled "config changed": it
	// can also be a missing/corrupt manifest or failed integrity certification.
	if decision.existingGraph && !decision.manifestTrusted {
		reasons = append(reasons, "manifest-untrusted")
	}
	if decision.existingGraph && !decision.rubyCurrent {
		reasons = append(reasons, "ruby-analysis-outdated")
	}
	slices.Sort(reasons)
	metrics.InvalidationReasons = slices.Compact(reasons)
}

func finishRunMetrics(metrics *RunMetrics, result Result, err error, started time.Time) {
	metrics.Duration = time.Since(started)
	metrics.Outcome = "success"
	if err != nil {
		metrics.Outcome = "failed"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		metrics.Outcome = "cancelled"
	}
	for _, scope := range result.Resolver.Scopes {
		if result.Reused || scope.Reused {
			metrics.ReusedScopes = append(metrics.ReusedScopes, formatResolverScopeKey(resolverScopeKey(scope.Resolver, scope.Scope)))
		}
	}
	slices.Sort(metrics.ReusedScopes)
}

// MetricsSummary renders bounded work diagnostics, never raw environment/source bytes.
// Example: fmt.Println(result.MetricsSummary()) after RunAtomic.
func (result Result) MetricsSummary() string {
	metrics := result.Metrics
	if metrics.Decision == "" {
		return ""
	}
	var text strings.Builder
	fmt.Fprintf(&text, "decision=%s outcome=%s duration=%s staged_files=%d staged_bytes=%d\n", metrics.Decision, metrics.Outcome, metrics.Duration.Round(time.Microsecond), metrics.StagedFiles, metrics.StagedBytes)
	reasons := strings.Join(metrics.InvalidationReasons, ",")
	if reasons == "" {
		reasons = "none"
	}
	fmt.Fprintf(&text, "invalidation=%s reused_scopes=%d scip_scopes=%d scip_invocations=%d scip_peak_rss_bytes=%d\n", reasons, len(metrics.ReusedScopes), result.ScipScopes, metrics.ScipInvocations, max(result.ScipPeakRSS, metrics.ScipPeakRSSBytes))
	for _, phase := range metrics.Phases {
		fmt.Fprintf(&text, "phase=%s duration=%s\n", phase.Phase, phase.Duration.Round(time.Microsecond))
	}
	const maxScopeLines = 20
	for _, scope := range metrics.ReusedScopes[:min(len(metrics.ReusedScopes), maxScopeLines)] {
		const maxScopeBytes = 240
		fmt.Fprintf(&text, "reused_scope=%s truncated=%v\n", strconv.Quote(scope[:min(len(scope), maxScopeBytes)]), len(scope) > maxScopeBytes)
	}
	if len(metrics.ReusedScopes) > maxScopeLines {
		fmt.Fprintf(&text, "reused_scopes_omitted=%d\n", len(metrics.ReusedScopes)-maxScopeLines)
	}
	return strings.TrimSuffix(text.String(), "\n")
}
