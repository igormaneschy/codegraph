'use strict'
const test = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { spawnSync } = require('node:child_process')
const { publishAnswers, validateCallQuestions } = require('./graph-answers')

class FakeGraphExecutable {
  constructor(directory, output) {
    this.file = path.join(directory, 'fake-codegraph')
    fs.writeFileSync(this.file, `#!/usr/bin/env node\nconst assert = require('node:assert/strict'); const args = process.argv.slice(2); assert.equal(args[0], 'cli'); assert.equal(args[1], 'callers'); assert.equal(args[2], '/fixture/repo'); const payload = JSON.parse(args[3]); assert.equal(payload.qualified_name, 'src/target.go.Target'); assert.equal(payload.limit, 500); process.stdout.write(${JSON.stringify(output)});\n`, { mode: 0o700 })
  }
}

function runDirectory(t) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'codegraph-eval-'))
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
  fs.writeFileSync(path.join(directory, 'questions.json'), JSON.stringify([{ id: 'callers-01', type: 'callers', qn: 'src/target.go.Target' }]))
  return directory
}
function invoke(directory, executable, options = {}) {
  return spawnSync(process.execPath, [path.join(__dirname, 'graph-answers.js'), '/fixture/repo', directory, executable], {
    encoding: 'utf8', env: { ...process.env, CODEGRAPH_EVAL_SCORER: 'qualified-name-v1', CODEGRAPH_EVAL_PAGE_LIMIT: '500', CODEGRAPH_EVAL_MAX_PAGES: '1000', ...options },
  })
}

test('CLI uses tool-first argv, emits exact QNs, and replaces artifacts privately', t => {
  const directory = runDirectory(t)
  const output = 'Method\tRun\tsrc/a.go:1\tsrc/a.go.Owner.Run\nMethod\tRun\tsrc/b.go:2\tsrc/b.go.Owner.Run\n# has_more=false cursor=- generation=g1 shown=2\n'
  const executable = new FakeGraphExecutable(directory, output)
  const destination = path.join(directory, 'answers.json')
  fs.writeFileSync(destination, 'old', { mode: 0o644 })
  const result = invoke(directory, executable.file)
  assert.equal(result.status, 0, result.stderr)
  assert.equal(JSON.parse(result.stdout).scorer, 'qualified-name-v1')
  const answers = JSON.parse(fs.readFileSync(destination, 'utf8'))
  assert.deepEqual(answers[0].items, ['src/a.go.Owner.Run', 'src/b.go.Owner.Run'])
  assert.equal(answers[0].calls, 1)
  assert.equal(answers[0].tokens, Math.round(Buffer.byteLength(output) / 4))
  assert.equal(fs.statSync(destination).mode & 0o777, 0o600)
})

test('nonterminal capped walk fails nonzero and preserves existing artifact', t => {
  const directory = runDirectory(t)
  const executable = new FakeGraphExecutable(directory, 'Method\tRun\tsrc/a.go:1\tsrc/a.go.Run\n# has_more=true cursor=next generation=g1 shown=1\n')
  const destination = path.join(directory, 'answers.json')
  fs.writeFileSync(destination, 'keep existing\n')
  const result = invoke(directory, executable.file, { CODEGRAPH_EVAL_MAX_PAGES: '1' })
  assert.equal(result.status, 1)
  assert.match(result.stderr, /exceeded 1 pages/)
  assert.equal(fs.readFileSync(destination, 'utf8'), 'keep existing\n')
})

test('atomic publication replaces a destination symlink without following it', t => {
  const directory = runDirectory(t)
  const outside = path.join(directory, 'outside.json')
  fs.writeFileSync(outside, 'keep outside')
  fs.symlinkSync(outside, path.join(directory, 'answers.json'))
  publishAnswers(directory, '[]\n')
  assert.equal(fs.readFileSync(outside, 'utf8'), 'keep outside')
  assert.equal(fs.lstatSync(path.join(directory, 'answers.json')).isSymbolicLink(), false)
  assert.deepEqual(fs.readdirSync(directory).sort(), ['answers.json', 'outside.json', 'questions.json'])
})

test('question subset, missing QN and duplicate IDs are never inferred', () => {
  for (const questions of [null, [], [{ id: 'open-01', type: 'open', qn: 'src/a.go.Run' }], [{ id: 'callers-01', type: 'callers' }], [{ id: 'x', type: 'callers', qn: 'src/a.go.Run' }, { id: 'x', type: 'callers', qn: 'src/a.go.Run' }]]) {
    assert.throws(() => validateCallQuestions(questions))
  }
})
