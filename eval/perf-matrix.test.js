'use strict'
const test = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { METHOD, SCENARIOS, validatePlan, percentile, summarize } = require('./perf-plan')
const { scenario, measure, publishReport, assertSample } = require('./perf-matrix')

function dataset() {
  const input = { path: 'a.go', sha256: 'a'.repeat(64), suffix: 'probe' }
  return { id: 'fixture', lang: 'go', root: '/disposable', commit: 'a'.repeat(40), disposable: true, source: input, config: { ...input, path: 'go.mod' }, degradedSuffix: 'invalid', queries: [{ tool: 'search', target: 'run', limit: 1 }], probe: { tool: 'callees', target: 'a.go.probe', limit: 1, expected: ['a.go.target'] } }
}
function sample(status = 'healthy', queries = []) {
  return { method: METHOD, status, graph_digest: 'b'.repeat(64), wall_ns: 10, go_alloc_bytes: 20, go_mallocs: 1, self_peak_rss_bytes: 30, files: 1, nodes: 2, edges: 1, metrics: { outcome: 'success', decision: 'rebuild', staged_bytes: 40 }, queries: queries.map(query => ({ ...query, store_equivalent: true, expected_verified: query.expected !== undefined })) }
}
class FakeMatrixWorker {
  constructor({ failure, mismatch, lostEditReply } = {}) { this.requests = []; this.failure = failure; this.mismatch = mismatch; this.lostEditReply = lostEditReply; this.activeEdit = false }
  async run(request, options = {}) {
    this.requests.push({ request, options })
    if (request.operation === 'edit') {
      if (request.edit.restore) { this.activeEdit = false; return { original_sha: request.edit.expected_sha } }
      this.activeEdit = true
      if (this.lostEditReply) throw new Error('lost edit reply')
      return { original_sha: 'a'.repeat(64), edited_sha: 'c'.repeat(64) }
    }
    if (this.failure && request.queries.length) throw new Error('measured failure')
    const result = sample(request.database.endsWith('measured.db') && this.activeEdit && request.queries.length === 0 ? 'degraded' : 'healthy', request.queries)
    if (this.mismatch && request.database.endsWith('reference.db')) result.graph_digest = 'd'.repeat(64)
    return result
  }
}
function tempDirectory(t) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'codegraph-matrix-'))
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  return directory
}

test('plan requires pinned explicit disposable datasets and bounded input shapes', () => {
  const plan = { method: METHOD, repetitions: 2, datasets: [dataset()] }
  assert.equal(validatePlan(plan), plan)
  for (const mutate of [p => { p.method = 'other' }, p => { p.repetitions = 0 }, p => { p.datasets = [] }, p => { p.datasets.push(p.datasets[0]) }, p => { p.datasets[0].commit = 'main' }, p => { p.datasets[0].disposable = false }, p => { p.datasets[0].source.path = '.env' }, p => { p.datasets[0].source.path = '../outside' }, p => { p.datasets[0].queries[0].limit = 0 }]) {
    const invalid = structuredClone(plan); mutate(invalid)
    assert.throws(() => validatePlan(invalid))
  }
})

test('nearest-rank percentiles retain sample count and do not mutate rows', () => {
  const values = [5, 1, 4, 2, 3]
  assert.equal(percentile(values, .5), 3); assert.equal(percentile(values, .95), 5)
  assert.deepEqual(values, [5, 1, 4, 2, 3])
  assert.throws(() => percentile([],.5)); assert.throws(() => percentile([NaN],.5))
  const rows = [{ dataset: 'fixture', scenario: 'unchanged', sample: sample() }]
  assert.equal(summarize(rows)[0].n, 1)
  assert.deepEqual(summarize(rows)[0].decisions, ['rebuild'])
})

test('source/config edit, operation failure and lost edit reply always restore', async t => {
  for (const options of [{}, { failure: true }, { lostEditReply: true }]) {
    const worker = new FakeMatrixWorker(options)
    const run = (request, configuration) => worker.run(request, configuration)
    const operation = scenario(dataset(), 'source-edit', tempDirectory(t), run)
    if (options.failure || options.lostEditReply) await assert.rejects(operation)
    else { const row = await operation; assert.equal(row.full_rebuild_equivalent, true) }
    assert.equal(worker.activeEdit, false)
    assert.equal(worker.requests.at(-1).request.edit.restore, true)
  }
})

test('full rebuild mismatch aborts and restores; degraded seed must be observed', async t => {
  const worker = new FakeMatrixWorker({ mismatch: true })
  await assert.rejects(scenario(dataset(), 'source-edit', tempDirectory(t), (request, options) => worker.run(request, options)), /differs from full rebuild/)
  assert.equal(worker.activeEdit, false)
  const recovery = new FakeMatrixWorker()
  const row = await scenario(dataset(), 'degraded-recovery', tempDirectory(t), (request, options) => recovery.run(request, options))
  assert.equal(row.sample.status, 'healthy')
  assert.equal(recovery.activeEdit, false)
  await assert.rejects(scenario(dataset(), 'degraded-recovery', tempDirectory(t), async request => request.operation === 'edit' ? {} : sample()), /want degraded/)
})

test('matrix produces all five scenarios per repetition, including actual rebuild unchanged', async t => {
  const worker = new FakeMatrixWorker()
  const rows = await measure({ repetitions: 2, datasets: [dataset()] }, tempDirectory(t), (request, options) => worker.run(request, options))
  assert.equal(rows.length, 10)
  assert.deepEqual([...new Set(rows.map(row => row.scenario))], SCENARIOS)
  assert.equal(rows.filter(row => row.scenario === 'unchanged').every(row => row.sample.metrics.decision === 'rebuild'), true)
})

test('invalid worker data and cancelled matrix cannot produce final reports', async t => {
  assert.throws(() => assertSample({ ...sample(), status: 'stale' }, 'healthy'))
  assert.throws(() => assertSample({ ...sample(), queries: [{}] }, 'healthy'))
  const controller = new AbortController(); controller.abort()
  await assert.rejects(measure({ repetitions: 1, datasets: [dataset()] },tempDirectory(t),async () => sample(),controller.signal), /cancelled/)
})

test('publication is private and replaces a destination symlink without following it', t => {
  const directory = tempDirectory(t), outside = path.join(directory, 'outside')
  fs.writeFileSync(outside, 'preserved')
  fs.symlinkSync(outside, path.join(directory, 'matrix.json'))
  publishReport(directory, { method: METHOD, rows: [] })
  assert.equal(fs.readFileSync(outside, 'utf8'), 'preserved')
  assert.equal(fs.statSync(path.join(directory, 'matrix.json')).mode & 0o777, 0o600)
  assert.deepEqual(JSON.parse(fs.readFileSync(path.join(directory, 'matrix.json'), 'utf8')), { method: METHOD, rows: [] })
})
