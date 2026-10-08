'use strict'
const test = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor
const workflow = new AsyncFunction('args', 'agent', 'parallel', 'pipeline', 'log',
  fs.readFileSync(path.join(__dirname, 'quality-workflow.js'), 'utf8').replace('export const meta =', 'const meta ='))

class FakeQualityHost {
  constructor({ failure, response, pipelineTransform, scorer } = {}) {
    this.failure = failure
    this.response = response
    this.pipelineTransform = pipelineTransform ?? (rows => rows)
    this.scorer = scorer
    this.prompts = new Map()
    this.writes = 0
    this.questions = ['callers', 'callees', 'definition', 'open'].map(type => ({
      id: `${type}-01`, type, qn: 'src/a.go.Owner.Run', symbol: 'Run', file: 'src/a.go', line: 12, prompt: `Question ${type}`,
    }))
  }
  async agent(prompt, options) {
    this.prompts.set(options.label, prompt)
    if (options.label === this.failure) return this.response
    if (options.label === 'load-questions') return { questions: this.questions }
    if (options.label === 'write-results') { this.writes++; this.writePrompt = prompt; return 'done' }
    if (options.label.startsWith('judge:')) return { score: .8 }
    const type = options.label.split(':')[1].split('-')[0]
    if (options.label.startsWith('oracle:')) return type === 'open' ? { notes: 'Independent responsibility/caller/dependency rubric' } : { items: type === 'definition' ? ['src/a.go:12'] : ['src/b.go.Caller'] }
    return type === 'open' ? { text: 'Explanation', calls: 2, tokens_approx: 12 } : { items: type === 'definition' ? ['src/a.go:12'] : ['src/b.go.Caller'], calls: 1, tokens_approx: 6 }
  }
  async pipeline(questions, ...stages) {
    const rows = []
    for (const question of questions) {
      let row = question
      for (const stage of stages) row = await stage(row)
      rows.push(row)
    }
    return this.pipelineTransform(rows)
  }
  run() {
    return workflow({ repo: "/repo/it's", exe: '/bin/codegraph', outdir: '/run', scorer: this.scorer },
      (prompt, options) => this.agent(prompt, options), jobs => Promise.all(jobs.map(job => job())),
      (questions, ...stages) => this.pipeline(questions, ...stages), () => {})
  }
}

test('complete workflow produces the matrix and independent, homonym-safe prompts', async () => {
  const host = new FakeQualityHost()
  const result = await host.run()
  assert.equal(result.scorer, 'qualified-name-v1')
  assert.equal(result.questions, 4)
  assert.equal(result.truths, 4)
  assert.equal(result.answers, 8)
  assert.equal(host.writes, 1)
  assert.match(host.prompts.get('oracle:callers-01'), /NEVER the codegraph tool/)
  assert.match(host.prompts.get('oracle:callers-01'), /resolve bindings/)
  assert.match(host.prompts.get('baseline:callers-01'), /NEVER codegraph/)
  assert.match(host.prompts.get('baseline:callers-01'), /project-stripped repository qualified names/)
  const graph = host.prompts.get('graph:callers-01')
  assert.match(graph, /4th column = exact project-stripped QNs/)
  assert.match(graph, /"limit":200,"cursor":"<cursor>"/)
  assert.match(graph, /'\/repo\/it'\\''s'/)
  assert.match(host.prompts.get('graph:definition-01'), /4th column is exactly src\/a.go.Owner.Run/)
  assert.match(host.writePrompt, /"judge": 0.8/)
})

test('explicit legacy producer asks for names rather than QN column', async () => {
  const host = new FakeQualityHost({ scorer: 'name-v1' })
  await host.run()
  assert.match(host.prompts.get('graph:callers-01'), /2nd column = legacy names/)
  assert.match(host.prompts.get('baseline:callers-01'), /Legacy name-v1/)
})

test('failed loader/oracle/responders/judges never become empty evidence or zero scores', async () => {
  const cases = [
    { failure: 'load-questions', response: null },
    { failure: 'load-questions', response: { questions: [] } },
    { failure: 'oracle:callers-01', response: null },
    { failure: 'oracle:callees-01', response: {} },
    { failure: 'oracle:open-01', response: { notes: '' } },
    { failure: 'graph:callers-01', response: null },
    { failure: 'baseline:callers-01', response: { calls: 0, tokens_approx: 0 } },
    { failure: 'graph:open-01', response: { calls: 0, tokens_approx: 0, text: '' } },
    { failure: 'graph:callers-01', response: { items: [], calls: -1, tokens_approx: 1 } },
    { failure: 'graph:callers-01', response: { items: [], calls: 0, tokens_approx: NaN } },
    ...[null, { score: NaN }, { score: Infinity }, { score: 1.1 }, { score: -.1 }].map(response => ({ failure: 'judge:open-01:graph', response })),
  ]
  for (const options of cases) {
    const host = new FakeQualityHost(options)
    await assert.rejects(host.run(), undefined, JSON.stringify(options))
    assert.equal(host.writes, 0)
  }
})

test('known empty structural items and zero judge are valid evidence', async () => {
  for (const options of [
    { failure: 'oracle:callers-01', response: { items: [] } },
    { failure: 'graph:callers-01', response: { items: [], calls: 0, tokens_approx: 0 } },
    { failure: 'judge:open-01:graph', response: { score: 0 } },
  ]) {
    const host = new FakeQualityHost(options)
    await host.run()
    assert.equal(host.writes, 1)
  }
})

test('missing and duplicated pipeline rows are errors, not silently filtered', async () => {
  for (const pipelineTransform of [rows => rows.slice(1), rows => [...rows.slice(1), rows[1]], rows => [null, ...rows.slice(1)]]) {
    const host = new FakeQualityHost({ pipelineTransform })
    await assert.rejects(host.run(), /incomplete or duplicate workflow results/)
    assert.equal(host.writes, 0)
  }
})

test('missing truth and duplicated modes from the host are errors', async () => {
  for (const pipelineTransform of [
    rows => { rows[0].truth = undefined; return rows },
    rows => { rows[0].answers.pop(); return rows },
    rows => { rows[0].answers[1] = rows[0].answers[0]; return rows },
  ]) {
    const host = new FakeQualityHost({ pipelineTransform })
    await assert.rejects(host.run(), /incomplete or duplicate workflow/)
    assert.equal(host.writes, 0)
  }
})

test('writer failure and invalid scorer are not claimed as completed runs', async () => {
  await assert.rejects(new FakeQualityHost({ failure: 'write-results', response: null }).run(), /write-results failed/)
  await assert.rejects(new FakeQualityHost({ scorer: 'unknown' }).run(), /invalid scorer/)
})
