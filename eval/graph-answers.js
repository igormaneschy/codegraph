// Deterministic graph-mode answers for the call questions (0 agents). The graph
// answer to "who calls X" / "what X calls" is exactly `codegraph cli callers/callees`
// — on cobra the 9 graph-responder agents reproduced this verbatim, so we skip the
// responder agents here and spend the budget on the oracle (the part that needs
// independent reasoning). Usage: node graph-answers.js <repo> <outdir> [exe]
//
// The compact ref tools page: a `# has_more=... cursor=...` trailer ends every
// answer, and a hub with thousands of callers is cut by the 32 KiB byte budget
// long before `limit`. A single-page read under-counts the answer and reports
// calls=1, so this script walks the trailer to has_more=false, de-duplicating
// across pages, and charges every invocation and every output byte it read.
const { execFileSync } = require('child_process')
const fs = require('fs')
const path = require('path')

// Configurable executable: env var, then argv, then a bare `codegraph` on PATH.
const EXE = process.env.CODEGRAPH_EXE || process.argv[4] || 'codegraph'
const REPO = path.resolve(process.argv[2])
const OUT = process.argv[3]

// pageLimit keeps each request bounded; the walker follows the cursor regardless.
const pageLimit = Number(process.env.CODEGRAPH_EVAL_PAGE || 500)
// maxPages is a safety valve against a server that never reports has_more=false.
const maxPages = Number(process.env.CODEGRAPH_EVAL_MAX_PAGES || 1000)

const questions = JSON.parse(fs.readFileSync(path.join(OUT, 'questions.json'), 'utf8'))
const callQs = questions.filter(q => q.type === 'callers' || q.type === 'callees')

// parseTrailer reads the `# has_more=<bool> cursor=<tok> generation=<gen>` line.
function parseTrailer(out) {
  const line = out.split('\n').find(l => l.startsWith('# '))
  const fields = {}
  if (line) {
    for (const part of line.slice(2).trim().split(/\s+/)) {
      const eq = part.indexOf('=')
      if (eq > 0) fields[part.slice(0, eq)] = part.slice(eq + 1)
    }
  }
  return fields
}

// graphNames walks every page for one qn and returns the de-duplicated names plus
// the real call count and an output-byte-based token estimate.
function graphNames(qn, type) {
  const tool = type === 'callers' ? 'callers' : 'callees'
  const names = []
  const seen = new Set()
  let cursor = ''
  let calls = 0
  let chars = 0
  for (let page = 0; page < maxPages; page++) {
    const args = { qualified_name: qn, limit: pageLimit }
    if (cursor) args.cursor = cursor
    const out = execFileSync(EXE, ['cli', tool, REPO, JSON.stringify(args)], {
      encoding: 'utf8',
      maxBuffer: 64 * 1024 * 1024,
    })
    calls++
    chars += out.length
    for (const line of out.split('\n')) {
      if (!line || line.startsWith('#')) continue
      const name = line.split('\t')[1]
      if (name && !seen.has(name)) {
        seen.add(name)
        names.push(name)
      }
    }
    const trailer = parseTrailer(out)
    if (trailer.has_more !== 'true') break
    if (!trailer.cursor || trailer.cursor === '-') break
    cursor = trailer.cursor
  }
  return { names, calls, tokens: Math.round(chars / 4) }
}

const answers = callQs.map(q => {
  const r = graphNames(q.qn, q.type)
  return { id: q.id, mode: 'graph', items: r.names, tokens: r.tokens, calls: r.calls }
})
fs.writeFileSync(path.join(OUT, 'answers.json'), JSON.stringify(answers, null, 2))
console.log('wrote', path.join(OUT, 'answers.json'), '-',
  answers.map(a => `${a.id}=${a.items.length}/${a.calls}call`).join(' '))
