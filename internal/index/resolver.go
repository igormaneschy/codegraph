package index

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// IndexStatus is the trust status of the graph produced by an indexing run.
// A degraded graph is structural-only: it is useful for navigation, but it is
// not allowed to present a resolver failure as a healthy fresh graph.
type IndexStatus string

const (
	StatusHealthy  IndexStatus = "healthy"
	StatusDegraded IndexStatus = "degraded"
	StatusStale    IndexStatus = "stale"
)

// ResolverScopeStatus is the auditable outcome for one resolver scope. Reused
// scopes are recorded too, so a report distinguishes work that was deliberately
// skipped from a scope that was attempted and failed.
type ResolverScopeStatus struct {
	Resolver  string `json:"resolver"`
	Scope     string `json:"scope"`
	Attempted bool   `json:"attempted"`
	Succeeded bool   `json:"succeeded"`
	Failed    bool   `json:"failed"`
	Reused    bool   `json:"reused"`
	Error     string `json:"error,omitempty"`
}

// ResolverReport is persisted in a committed manifest and returned with every
// non-no-op result. The report is intentionally per-scope: a count alone would
// hide a partially resolved monorepo.
type ResolverReport struct {
	Scopes []ResolverScopeStatus `json:"scopes,omitempty"`
}

// HasFailures reports whether any attempted resolver scope failed.
func (r ResolverReport) HasFailures() bool {
	for _, scope := range r.Scopes {
		if scope.Failed {
			return true
		}
	}
	return false
}

// Validate rejects an ambiguous persisted scope outcome. A manifest must never
// turn an attempted-but-unrecorded resolver result into a healthy no-op.
func (r ResolverReport) Validate() error {
	seen := make(map[string]struct{}, len(r.Scopes))
	for _, scope := range r.Scopes {
		if strings.TrimSpace(scope.Resolver) == "" {
			return fmt.Errorf("resolver scope has no resolver name")
		}
		key := scope.Resolver + "\x00" + scope.Scope
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate resolver scope %q", displayResolverScope(scope.Scope))
		}
		seen[key] = struct{}{}
		hasError := strings.TrimSpace(scope.Error) != ""
		if scope.Reused {
			if scope.Attempted || scope.Succeeded || scope.Failed || hasError {
				return fmt.Errorf("reused resolver scope %q has an attempted outcome", displayResolverScope(scope.Scope))
			}
			continue
		}
		if !scope.Attempted {
			return fmt.Errorf("resolver scope %q has no terminal outcome", displayResolverScope(scope.Scope))
		}
		if scope.Succeeded && scope.Failed {
			return fmt.Errorf("resolver scope %q is both successful and failed", displayResolverScope(scope.Scope))
		}
		if scope.Succeeded {
			if hasError {
				return fmt.Errorf("successful resolver scope %q has an error", displayResolverScope(scope.Scope))
			}
			continue
		}
		if !scope.Failed || !hasError {
			return fmt.Errorf("resolver scope %q has an incomplete outcome", displayResolverScope(scope.Scope))
		}
	}
	return nil
}

// ValidateExpected requires a terminal outcome for exactly the resolver scopes
// that apply to the current repository. A syntactically valid report with a
// missing TypeScript scope, or with a scope from a removed project, is not a
// freshness certificate.
func (r ResolverReport) ValidateExpected(expected map[string]struct{}) error {
	if err := r.Validate(); err != nil {
		return err
	}
	actual := make(map[string]struct{}, len(r.Scopes))
	for _, scope := range r.Scopes {
		actual[resolverScopeKey(scope.Resolver, scope.Scope)] = struct{}{}
	}
	for _, key := range sortedResolverScopeKeys(expected) {
		if _, ok := actual[key]; !ok {
			return fmt.Errorf("resolver report is missing expected scope %q", formatResolverScopeKey(key))
		}
	}
	for _, key := range sortedResolverScopeKeys(actual) {
		if _, ok := expected[key]; !ok {
			return fmt.Errorf("resolver report contains unexpected scope %q", formatResolverScopeKey(key))
		}
	}
	return nil
}

func resolverScopeKey(resolver, scope string) string {
	return resolver + "\x00" + scope
}

func formatResolverScopeKey(key string) string {
	resolver, scope, ok := strings.Cut(key, "\x00")
	if !ok {
		return key
	}
	return resolver + "[" + displayResolverScope(scope) + "]"
}

func sortedResolverScopeKeys(scopes map[string]struct{}) []string {
	keys := make([]string, 0, len(scopes))
	for key := range scopes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// expectedResolverScopeKeys is shared by the produced-report and persisted-
// manifest boundaries. Source-only repositories intentionally have no resolver
// scope until a usable resolver configuration exists.
func expectedResolverScopeKeys(root string, files []SourceFile, tsdirs []string) map[string]struct{} {
	expected := make(map[string]struct{}, len(tsdirs)+2)
	for _, dir := range tsdirs {
		expected[resolverScopeKey("scip-typescript", dir)] = struct{}{}
	}
	if hasGo(files) && hasGoResolverConfig(root) {
		expected[resolverScopeKey("go-vta", "go")] = struct{}{}
	}
	if hasRuby(files) {
		expected[resolverScopeKey("ruby-static", "ruby")] = struct{}{}
	}
	return expected
}

func hasRuby(files []SourceFile) bool {
	for _, file := range files {
		if file.Lang == LangRuby {
			return true
		}
	}
	return false
}

func validateResolverStatus(status IndexStatus, report ResolverReport) error {
	if err := report.Validate(); err != nil {
		return err
	}
	switch status {
	case StatusHealthy:
		if report.HasFailures() {
			return errors.New("healthy manifest contains failed resolver scope")
		}
	case StatusDegraded:
		if !report.HasFailures() {
			return errors.New("degraded manifest has no failed resolver scope")
		}
	}
	return nil
}

// Summary renders deterministic, bounded context for CLI/MCP status messages.
func (r ResolverReport) Summary() string {
	var failed []string
	for _, scope := range r.Scopes {
		if !scope.Failed {
			continue
		}
		item := scope.Resolver + "[" + displayResolverScope(scope.Scope) + "]"
		if scope.Error != "" {
			item += ": " + scope.Error
		}
		failed = append(failed, item)
	}
	if len(failed) == 0 {
		return "no resolver failures"
	}
	return strings.Join(failed, "; ")
}

func displayResolverScope(scope string) string {
	if scope == "" {
		return "root"
	}
	return scope
}

// ResolverFailure is returned only after a build has established that a
// resolver outcome is incomplete. RunAtomic can therefore leave the previous
// graph untouched while callers still receive the complete failure context.
type ResolverFailure struct {
	Report ResolverReport
}

func (e *ResolverFailure) Error() string {
	if e == nil {
		return "resolver failed"
	}
	return fmt.Sprintf("resolver failed: %s", e.Report.Summary())
}
