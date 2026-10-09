#!/usr/bin/env node
'use strict'
const fs = require('node:fs')
const path = require('node:path')
const os = require('node:os')
const crypto = require('node:crypto')
const { execFileSync } = require('node:child_process')
const { worker } = require('./perf-worker')
const { METHOD, SCENARIOS, validatePlan, summarize } = require('./perf-plan')
const { publishAnswers } = require('./graph-answers')

function git(root, args) { return execFileSync('git', ['-C', root, ...args], { encoding: 'utf8', timeout: 60000 }).trim() }

function admitDatasets(plan, output) {
  const roots = new Set()
  for (const dataset of plan.datasets) {
    const root = fs.realpathSync(dataset.root)
    if (root !== dataset.root || roots.has(root) || root === fs.realpathSync(path.join(__dirname, '..')) || output === root || output.startsWith(root + path.sep)) throw new Error(`${dataset.id}: duplicate/nonphysical/served root or output inside root`)
    roots.add(root)
    if (git(root, ['rev-parse', 'HEAD']) !== dataset.commit || git(root, ['status', '--porcelain', '--untracked-files=no'])) throw new Error(`${dataset.id}: wrong revision or dirty tracked checkout`)
    const tracked = git(root, ['ls-files', '-z']).split('\0')
    if (tracked.some(file => file.split('/').some(part => part.startsWith('.env')))) throw new Error(`${dataset.id}: tracked private .env input requires separate admission; not read`)
    for (const input of [dataset.source, dataset.config]) {
      if (!tracked.includes(input.path)) throw new Error(`${dataset.id}: edit must address a pinned tracked input`)
    }
  }
}

function assertSample(sample, expectedStatus) {
  if (!sample || sample.method !== METHOD || sample.status !== expectedStatus || sample.metrics?.outcome !== 'success' || !['noop', 'rebuild'].includes(sample.metrics?.decision) || !/^[a-f0-9]{64}$/.test(sample.graph_digest)) throw new Error(`invalid worker result; want ${expectedStatus} production sample`)
  for (const key of ['wall_ns', 'go_alloc_bytes', 'go_mallocs', 'self_peak_rss_bytes', 'files', 'nodes', 'edges']) {
    if (!Number.isSafeInteger(sample[key]) || sample[key] < 0) throw new Error(`invalid sample ${key}`)
  }
  if (!Array.isArray(sample.queries) || sample.queries.some(query => query.store_equivalent !== true)) throw new Error('incomplete or unequal query walks')
  return sample
}

async function scenario(dataset, kind, directory, run, signal) {
  const database = path.join(directory, 'measured.db')
  let activeEdit
  const invoke = (request, options = {}) => run(request, { signal, ...options })
  const sampleRequest = (db, queries = []) => ({ operation: 'sample', root: dataset.root, database: db, queries })
  const edit = async (input, suffix) => {
    const request = { root: dataset.root, path: input.path, backup: path.join(directory, 'original'), suffix, expected_sha: input.sha256 }
    activeEdit = { ...request, restore: true }
    return invoke({ operation: 'edit', edit: request })
  }
  const restore = async () => {
    if (!activeEdit) return
    await run({ operation: 'edit', edit: activeEdit }, {})
    activeEdit = undefined
  }
  try {
    if (kind !== 'cold' && kind !== 'degraded-recovery') assertSample(await invoke(sampleRequest(database)), 'healthy')
    if (kind === 'degraded-recovery') {
      if (dataset.lang === 'go') await edit(dataset.source, dataset.degradedSuffix)
      const options = dataset.lang === 'ts' ? { environment: { ...process.env, PATH: '' } } : {}
      assertSample(await invoke(sampleRequest(database), options), 'degraded')
      await restore()
    }
    let evidence
    if (kind === 'source-edit') evidence = await edit(dataset.source, dataset.source.suffix)
    if (kind === 'config-edit') evidence = await edit(dataset.config, dataset.config.suffix)
    const queries = kind === 'source-edit' ? [...dataset.queries, dataset.probe] : dataset.queries
    const sample = assertSample(await invoke(sampleRequest(database, queries)), 'healthy')
    if (sample.queries.length !== queries.length) throw new Error('worker dropped requested query walks')
    if (kind === 'source-edit' && sample.queries.at(-1).expected_verified !== true) throw new Error('source probe CALLS not verified')
    const reference = assertSample(await invoke(sampleRequest(path.join(directory, 'reference.db'))), 'healthy')
    if (sample.graph_digest !== reference.graph_digest) throw new Error(`${dataset.id}/${kind}: incremental/recovery graph differs from full rebuild`)
    return { sample, evidence, full_rebuild_equivalent: true, reference_digest: reference.graph_digest }
  } finally { await restore() }
}

async function measure(plan, output, run, signal) {
  const rows = []
  for (const dataset of plan.datasets) {
    for (let repetition = 1; repetition <= plan.repetitions; repetition++) {
      for (const kind of SCENARIOS) {
        if (signal?.aborted) throw new Error('matrix cancelled')
        const directory = fs.mkdtempSync(path.join(output, `${dataset.id}-${kind}-`))
        const result = await scenario(dataset, kind, directory, run, signal)
        rows.push({ dataset: dataset.id, commit: dataset.commit, scenario: kind, repetition, ...result })
        console.error(`${dataset.id}/${kind} repetition=${repetition} decision=${result.sample.metrics.decision}`)
        fs.rmSync(directory, { recursive: true, force: true })
      }
    }
  }
  return rows
}

function provenance(executable) {
  const version = execFileSync(executable, ['version'], { encoding: 'utf8', timeout: 60000 })
  return { binary_sha256: crypto.createHash('sha256').update(fs.readFileSync(executable)).digest('hex'), build: version, platform: os.platform(), arch: os.arch(), cpus: os.cpus().length, cpu_model: os.cpus()[0]?.model, orchestrator_node: process.version, cache_policy: 'cold codegraph databases; warm OS/Go/npm/dependency caches; no cache flushing', allocation_method: 'Go TotalAlloc/Mallocs delta around RunAtomic only; excludes cgo/children', rss_method: 'OS RUSAGE_SELF high-water includes fresh worker startup, excludes children; SCIP sampled tree RSS is separate' }
}

function publishReport(output, report) {
  // Reuse the private staging/rename primitive, then atomically install the
  // complete matrix as one artifact (raw rows + summary + provenance).
  const staging = fs.mkdtempSync(path.join(output, '.matrix-report-'))
  try {
    publishAnswers(staging, JSON.stringify(report, null, 2) + '\n')
    fs.renameSync(path.join(staging, 'answers.json'), path.join(output, 'matrix.json'))
  } finally { fs.rmSync(staging, { recursive: true, force: true }) }
}

async function main(argv) {
  if (argv.length !== 4 || argv[3] !== '--allow-edits') throw new Error('usage: node eval/perf-matrix.js <plan.json> <output-dir> <codegraph-exe> --allow-edits (disposable clones only)')
  const [planPath, outputPath, exePath] = argv
  const plan = validatePlan(JSON.parse(fs.readFileSync(planPath, 'utf8')))
  fs.mkdirSync(outputPath, { recursive: true, mode: 0o700 })
  const output = fs.realpathSync(outputPath), executable = fs.realpathSync(exePath)
  admitDatasets(plan, output)
  const controller = new AbortController()
  const cancel = () => controller.abort()
  process.on('SIGINT', cancel); process.on('SIGTERM', cancel)
  try {
    const run = (request, options) => worker(executable, request, options)
    for (const dataset of plan.datasets) {
      for (const input of [dataset.source, dataset.config]) {
        await run({ operation: 'edit', edit: { root: dataset.root, path: input.path, backup: path.join(output, 'unused-backup'), expected_sha: input.sha256, check_only: true } }, { signal: controller.signal })
      }
    }
    const rows = await measure(plan, output, run, controller.signal)
    admitDatasets(plan, output)
    publishReport(output, { method: METHOD, provenance: provenance(executable), datasets: plan.datasets.map(({ id, commit, lang, source, config }) => ({ id, commit, lang, source_sha256: source.sha256, config_sha256: config.sha256 })), rows, summary: summarize(rows), limitations: 'two small pilot datasets; low-N p95 descriptive; query equivalence is not independent full-repo recall; no answer-quality or universal gain claim' })
  } finally { process.removeListener('SIGINT', cancel); process.removeListener('SIGTERM', cancel) }
}

if (require.main === module) main(process.argv.slice(2)).catch(error => { console.error(error.message); process.exitCode = 1 })
module.exports = { admitDatasets, assertSample, scenario, measure, publishReport, main }
