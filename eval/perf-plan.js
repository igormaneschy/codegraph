'use strict'
const path = require('node:path')

const METHOD = 'production-matrix-v1'
const SCENARIOS = ['cold', 'unchanged', 'source-edit', 'config-edit', 'degraded-recovery']

function validatePlan(plan) {
  if (!plan || plan.method !== METHOD || !Number.isSafeInteger(plan.repetitions) || plan.repetitions < 1 || plan.repetitions > 100) throw new Error('want production-matrix-v1 and repetitions in [1,100]')
  if (!Array.isArray(plan.datasets) || !plan.datasets.length) throw new Error('want nonempty datasets')
  const seen = new Set()
  for (const dataset of plan.datasets) {
    if (!dataset || !/^[a-z0-9][a-z0-9-]*$/.test(dataset.id) || seen.has(dataset.id)) throw new Error('want unique lowercase dataset IDs')
    seen.add(dataset.id)
    if (!['go', 'ts'].includes(dataset.lang) || !/^[a-f0-9]{40}$/.test(dataset.commit) || !path.isAbsolute(dataset.root) || dataset.disposable !== true) throw new Error(`${dataset.id}: want Go/TS, full commit SHA, absolute disposable root`)
    for (const edit of [dataset.source, dataset.config]) validateEdit(edit)
    if (typeof dataset.degradedSuffix !== 'string' || !dataset.degradedSuffix) throw new Error(`${dataset.id}: want explicit degraded-source suffix`)
    if (!Array.isArray(dataset.queries) || !dataset.queries.length) throw new Error(`${dataset.id}: want explicit queries`)
    for (const query of [...dataset.queries, dataset.probe]) {
      if (!query || !['callers', 'callees', 'search'].includes(query.tool) || typeof query.target !== 'string' || !query.target || !Number.isSafeInteger(query.limit) || query.limit < 1 || query.limit > 2000 || (query.min_results !== undefined && (!Number.isSafeInteger(query.min_results) || query.min_results < 0))) throw new Error(`${dataset.id}: invalid query`)
    }
    if (!dataset.probe || dataset.probe.tool !== 'callees' || !Array.isArray(dataset.probe.expected) || !dataset.probe.expected.length) throw new Error(`${dataset.id}: want independent nonempty probe CALLS`)
  }
  return plan
}

function validateEdit(edit) {
  if (!edit || typeof edit.path !== 'string' || !edit.path || path.posix.normalize(edit.path) !== edit.path || path.posix.isAbsolute(edit.path) || edit.path === '..' || edit.path.startsWith('../') || /[\\:\x00-\x1f]/.test(edit.path)) throw new Error('want canonical confined edit path')
  if (edit.path.split('/').some(part => part.startsWith('.env') || ['node_modules', 'vendor', '.git'].includes(part))) throw new Error('private/dependency paths cannot be edited')
  if (!/^[a-f0-9]{64}$/.test(edit.sha256) || typeof edit.suffix !== 'string' || !edit.suffix) throw new Error('want exact SHA-256 and nonempty edit suffix')
}

function percentile(values, fraction) {
  if (!values.length || values.some(value => !Number.isFinite(value) || value < 0)) throw new Error('want nonempty finite nonnegative samples')
  const sorted = [...values].sort((a, b) => a - b)
  return sorted[Math.max(0, Math.ceil(fraction * sorted.length) - 1)]
}

function summarize(rows) {
  const groups = new Map()
  for (const row of rows) {
    const key = `${row.dataset}/${row.scenario}`
    if (!groups.has(key)) groups.set(key, [])
    groups.get(key).push(row.sample)
  }
  return [...groups].map(([key, samples]) => ({
    group: key, n: samples.length,
    wall_ns: distribution(samples.map(sample => sample.wall_ns)),
    go_alloc_bytes: distribution(samples.map(sample => sample.go_alloc_bytes)),
    self_peak_rss_bytes: distribution(samples.map(sample => sample.self_peak_rss_bytes)),
    staged_bytes: distribution(samples.map(sample => sample.metrics.staged_bytes)),
    decisions: samples.map(sample => sample.metrics.decision),
  }))
}
function distribution(values) { return { p50: percentile(values, .5), p95: percentile(values, .95) } }

module.exports = { METHOD, SCENARIOS, validatePlan, percentile, summarize }
