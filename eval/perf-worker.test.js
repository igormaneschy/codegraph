'use strict'
const test = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { worker } = require('./perf-worker')

class FakeWorkerExecutable {
  constructor(t, body) {
    const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'codegraph-worker-'))
    t.after(() => fs.rmSync(directory, { recursive: true, force: true }))
    this.path = path.join(directory, 'fake-worker')
    fs.writeFileSync(this.path, `#!${process.execPath}\n${body}\n`, { mode: 0o700 })
  }
}

test('worker preserves split UTF-8 output and admits only one JSON artifact', async t => {
  const executable = new FakeWorkerExecutable(t, `process.stdin.resume(); process.stdin.on('end',()=>{ const text=Buffer.from(JSON.stringify({symbol:'méthode'})); const offset=text.indexOf(Buffer.from('é'))+1; process.stdout.write(text.subarray(0,offset)); setTimeout(()=>process.stdout.end(text.subarray(offset)),20) })`)
  assert.deepEqual(await worker(executable.path, { operation: 'sample' }), { symbol: 'méthode' })
  const invalid = new FakeWorkerExecutable(t, `process.stdin.resume(); process.stdin.on('end',()=>process.stdout.end('{} {}'))`)
  await assert.rejects(worker(invalid.path, {}), /one JSON/)
})

test('timeout and pre-cancellation stop worker before rejecting', async t => {
  const executable = new FakeWorkerExecutable(t, `process.stdin.resume(); setInterval(()=>{},1000); process.on('SIGINT',()=>process.exit(130))`)
  await assert.rejects(worker(executable.path, {}, { timeoutMS: 50 }), /timed out/)
  const controller = new AbortController(); controller.abort()
  await assert.rejects(worker(executable.path, {}, { signal: controller.signal }), /cancelled/)
})

test('worker rejects failed launches and failed process exits', async t => {
  await assert.rejects(worker('/does-not-exist-codegraph', {}), /ENOENT/)
  const executable = new FakeWorkerExecutable(t, `process.stdin.resume(); process.stdin.on('end',()=>process.exit(7))`)
  await assert.rejects(worker(executable.path, {}), /exit 7/)
})
