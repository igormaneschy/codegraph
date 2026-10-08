#!/usr/bin/env node
// A deterministic, explicitly call-only graph run. The oracle stays independent.
'use strict'
const fs = require('node:fs')
const path = require('node:path')
const { execFileSync } = require('node:child_process')
const { graphItems, pageOptions, STRICT } = require('./graph-pages')

/** @param {string} outdir @param {string} contents */
function publishAnswers(outdir, contents) {
  const staging = fs.mkdtempSync(path.join(outdir, '.graph-answers-'))
  try {
    const file = path.join(staging, 'answers.json')
    fs.writeFileSync(file, contents, { mode: 0o600, flag: 'wx' })
    fs.renameSync(file, path.join(outdir, 'answers.json'))
  } finally {
    fs.rmSync(staging, { recursive: true, force: true })
  }
}

/** @param {string[]} argv @param {NodeJS.ProcessEnv} environment */
function main(argv, environment) {
  if (argv.length < 2 || argv.length > 3) throw new Error('usage: node eval/graph-answers.js <repo> <outdir> [exe]')
  const [repo, directory, executable] = argv
  const outdir = path.resolve(directory)
  const exe = path.resolve(executable ?? environment.CODEGRAPH_EXE ?? './codegraph')
  const options = pageOptions({
    pageLimit: Number(environment.CODEGRAPH_EVAL_PAGE_LIMIT ?? 500),
    maxPages: Number(environment.CODEGRAPH_EVAL_MAX_PAGES ?? 1000),
    scorer: environment.CODEGRAPH_EVAL_SCORER ?? STRICT,
  })
  const questions = JSON.parse(fs.readFileSync(path.join(outdir, 'questions.json'), 'utf8'))
  validateCallQuestions(questions)
  const runPage = (tool, args) => execFileSync(exe, ['cli', tool, repo, JSON.stringify(args)], {
    encoding: 'utf8', maxBuffer: 64 * 1024 * 1024, timeout: 60000,
  })
  const answers = questions.map(q => ({ id: q.id, mode: 'graph', ...graphItems(q.qn, q.type, runPage, options) }))
  publishAnswers(outdir, JSON.stringify(answers, null, 2) + '\n')
  console.log(JSON.stringify({ scorer: options.scorer, modes: ['graph'], questions: answers.length, answers: path.join(outdir, 'answers.json') }))
}

/** @param {{id: string, type: string, qn: string}[]} questions */
function validateCallQuestions(questions) {
  if (!Array.isArray(questions) || !questions.length) throw new Error('want a nonempty, explicit call-only questions array')
  const seen = new Set()
  for (const q of questions) {
    if (!q || typeof q.id !== 'string' || !q.id || q.id.trim() !== q.id || /[\u0000-\u001f\u007f-\u009f]/.test(q.id) || seen.has(q.id)) throw new Error('want unique nonempty question IDs')
    if (!['callers', 'callees'].includes(q.type)) throw new Error(`question ${q.id}: graph-answers requires an explicit callers/callees-only run; no implicit subset scoring`)
    if (typeof q.qn !== 'string' || !q.qn) throw new Error(`question ${q.id}: want a nonempty QName`)
    seen.add(q.id)
  }
}

if (require.main === module) {
  try { main(process.argv.slice(2), process.env) } catch (error) { console.error(error.message); process.exitCode = 1 }
}
module.exports = { main, publishAnswers, validateCallQuestions }
