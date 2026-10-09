'use strict'
const { spawn } = require('node:child_process')

// One fresh process per request. Cancellation waits for its close before edits
// are restored; graceful SIGINT reaches RunAtomicContext and resolver cleanup.
function worker(executable, request, { environment = process.env, signal, timeoutMS = 900000 } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, ['bench-sample'], { env: environment, stdio: ['pipe', 'pipe', 'pipe'] })
    const stdout = [], stderr = []
    let stdoutBytes = 0, stderrBytes = 0, failure, escalation
    const stop = () => {
      failure = new Error(signal?.aborted ? 'matrix cancelled' : 'matrix worker timed out')
      child.kill('SIGINT')
      escalation ??= setTimeout(() => child.kill('SIGKILL'), 5000)
    }
    const timer = setTimeout(stop, timeoutMS)
    signal?.addEventListener('abort', stop, { once: true })
    const capture = (chunks, count, chunk) => {
      if (count + chunk.length > 16 * 1024 * 1024) {
        stop(); failure = new Error('worker output exceeds 16 MiB'); return count
      }
      chunks.push(chunk); return count + chunk.length
    }
    child.stdout.on('data', chunk => { stdoutBytes = capture(stdout, stdoutBytes, chunk) })
    child.stderr.on('data', chunk => { stderrBytes = capture(stderr, stderrBytes, chunk) })
    child.on('error', error => { failure = error })
    child.stdin.on('error', error => { failure ??= error })
    child.on('close', code => {
      clearTimeout(timer); clearTimeout(escalation); signal?.removeEventListener('abort', stop)
      if (failure || code !== 0) return reject(failure ?? new Error(`benchmark worker failed (exit ${code}); inspect local stderr, not a success sample`))
      try { resolve(JSON.parse(Buffer.concat(stdout).toString('utf8'))) } catch { reject(new Error('benchmark worker did not return one JSON object')) }
    })
    child.stdin.end(JSON.stringify(request))
    if (signal?.aborted) stop()
  })
}
module.exports = { worker }
