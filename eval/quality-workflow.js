// Workflow-harness script (wrapped by the host, not a standalone Node module).
// Workflow({scriptPath: <this file>, args: {repo, exe, outdir, scorer?}})
// Default scorer is qualified-name-v1. All grading still requires independent
// source-derived truth; loading/writing agents do not certify artifact contents.
export const meta = {
  name: 'codegraph-quality-run',
  description: 'Independent oracle + graph/baseline responders + complete-run admission',
  phases: [
    { title: 'Oracle', detail: 'independent truth per question (grep/read only)' },
    { title: 'Answer', detail: 'graph-only and grep-only responders' },
    { title: 'Judge', detail: 'finite scores against independent open rubrics' },
    { title: 'Write', detail: 'write truth.json + answers.json; CLI validates the run' },
  ],
}

/** @typedef {{id:string, type:string, prompt:string, symbol:string, qn:string, file:string, line:number}} EvalQuestion */
/** @typedef {{items?:string[], notes?:string, text?:string, calls?:number, tokens_approx?:number, score?:number}} AgentResult */
const A = typeof args === 'string' ? JSON.parse(args) : args
const REPO = A.repo
const EXE = A.exe
const OUT = A.outdir
for (const [field, value] of Object.entries({ repo: REPO, exe: EXE, outdir: OUT })) {
  if (typeof value !== 'string' || !value) throw new Error(`missing or invalid workflow argument ${field}`)
}
const SCORER = A.scorer ?? 'qualified-name-v1'
if (!['qualified-name-v1', 'name-v1'].includes(SCORER)) throw new Error(`invalid scorer ${SCORER}`)
const IDENTITY = SCORER === 'qualified-name-v1'
  ? 'Use exact project-stripped repository qualified names: file plus declaration owner/symbol (src/file.go.Owner.Method, src/file.ts.Owner.method; Ruby lib/file.rb.Owner#instance_method versus Owner.singleton_method, preserving :: namespaces). Preserve case, full relative paths and homonyms. Derive declaration identity from source, not an index. No bare names, location annotations, project prefix or guessed owner.'
  : 'Legacy name-v1: use de-duplicated symbol names (not qualified names). This deliberately permits homonym collisions.'

const QUESTION = { type: 'object', properties: { id: { type: 'string' }, type: { type: 'string' }, lang: { type: 'string' }, symbol: { type: 'string' }, qn: { type: 'string' }, file: { type: 'string' }, line: { type: 'integer' }, prompt: { type: 'string' } }, required: ['id', 'type', 'prompt', 'qn'] }
const QUESTIONS = { type: 'object', properties: { questions: { type: 'array', items: QUESTION } }, required: ['questions'] }
const ITEMS = { type: 'array', items: { type: 'string' } }
const ORACLE_STRUCT = { type: 'object', properties: { items: ITEMS, notes: { type: 'string' } }, required: ['items'] }
const ORACLE_OPEN = { type: 'object', properties: { notes: { type: 'string' } }, required: ['notes'] }
const RESPONDER = { type: 'object', properties: { items: ITEMS, text: { type: 'string' }, calls: { type: 'integer', minimum: 0 }, tokens_approx: { type: 'integer', minimum: 0 } }, required: ['calls', 'tokens_approx'] }
const JUDGE = { type: 'object', properties: { score: { type: 'number', minimum: 0, maximum: 1 }, reason: { type: 'string' } }, required: ['score'] }

/** @param {AgentResult} result @param {string} context */
function explicitItems(result, context) {
  if (!result || !Array.isArray(result.items) || result.items.some(item => typeof item !== 'string' || !item.trim())) {
    throw new Error(`${context}: want an explicit items array; [] is known empty, null/missing is not evidence`)
  }
  return result.items
}

/** @param {AgentResult} result @param {string} field @param {string} context */
function requiredText(result, field, context) {
  if (!result || typeof result[field] !== 'string' || !result[field].trim()) throw new Error(`${context}: want nonempty ${field}`)
  return result[field]
}

/** @param {EvalQuestion} q */
function oracleStructPrompt(q) {
  const common = `Establish GROUND TRUTH independently. Repo: ${REPO}. Use ONLY grep/ripgrep + reading source, NEVER the codegraph tool. Target is ${q.qn}; ${q.file}:${q.line} is a selection hint, not oracle evidence. Verify declaration and owner yourself. Missing/wrong truth corrupts the benchmark.`
  if (q.type === 'definition') return `${common}\nFind the exact DECLARATION of this qualified symbol, not a same-named symbol or usage. Return items=["full/relpath:exact-declaration-line"] (one entry); notes=declaration evidence.`
  if (q.type === 'callees') return `${common}\nList every function/method/hook/component invoked DIRECTLY by this target that is DEFINED IN THIS REPO. Exclude stdlib/third-party callees, imports without calls and type-only references. KEEP intra-repo func-value/dynamic-dispatch field invocations (e.g. RunE) as honest potential misses. Exclude nested-callback calls belonging to a different function. ${IDENTITY} Return items=deduplicated callees; notes=verification/exclusions. If uncertain or source unavailable, fail explicitly; do not fabricate [].`
  return `${common}\nFind EVERY direct caller of this exact target. Open each grep hit and resolve bindings; reject same-named other owners/files, imports, comments, strings and type-only refs. Record each ENCLOSING caller's declaration identity. ${IDENTITY} Return items=deduplicated callers; notes=verification. If uncertain or source unavailable, fail explicitly; do not fabricate [].`
}

/** @param {EvalQuestion} q */
function oracleOpenPrompt(q) {
  return `Establish an independent rubric for ${q.qn}. Repo: ${REPO}. Verify source at ${q.file}:${q.line} and surroundings with grep/read ONLY, never codegraph. Return nonempty notes with concrete responsibilities, callers and dependencies required in a correct 2-4 sentence answer. If source unavailable, fail rather than invent a rubric.`
}

/** @param {string} value */
function shellQuote(value) { return `'${value.replaceAll("'", "'\\''")}'` }

/** @param {string} tool @param {object} payload */
function cliCommand(tool, payload) {
  return `${shellQuote(EXE)} cli ${tool} ${shellQuote(REPO)} ${shellQuote(JSON.stringify(payload))}`
}

/** @param {string} tool @param {EvalQuestion} q */
function pageHint(tool, q) {
  const payload = tool === 'search' ? { query: q.symbol, limit: 200 } : { qualified_name: q.qn, limit: 200 }
  const identityColumn = SCORER === 'qualified-name-v1' ? '4th column = exact project-stripped QNs' : '2nd column = legacy names'
  const extraction = tool === 'search'
    ? `Pick ONLY the node whose 4th column is exactly ${q.qn}; return its 3rd column (full relpath:exact-line), not a basename homonym.`
    : `items = ${identityColumn}, de-duplicated across ALL pages. Do not shorten or fold case.`
  return `Run ${cliCommand(tool, payload)}. TSV refs end with # has_more=<bool> cursor=<tok> generation=<gen> shown=<n>. Follow ${cliCommand(tool, { ...payload, cursor: '<cursor>' })} until has_more=false (a byte cut can happen before limit=200). Keep limit and query identical; require progress/stable generation and a terminal trailer, otherwise fail. ${extraction} Count every invocation.`
}

/** @param {EvalQuestion} q */
function graphPrompt(q) {
  const hint = q.type === 'open'
    ? `Use callers/callees/search for ${q.qn}, then explain it in 2-4 sentences as text.`
    : pageHint(q.type === 'definition' ? 'search' : q.type, q)
  return `Answer using ONLY ${shellQuote(EXE)} cli <tool> ${shellQuote(REPO)} '<json>' via Bash, where tool is search/callers/callees/snippet. NO grep, source reads or other tools. Question: ${q.prompt}\n${hint}\nReturn explicit items for structural answers (known empty=[]), text for open, nonnegative integer calls and tokens_approx (~UTF-8 output bytes / 4). Count ALL invocations. Do not invent empty results after tool failures. Scorer=${SCORER}.`
}

/** @param {EvalQuestion} q */
function baselinePrompt(q) {
  const shape = q.type === 'definition' ? 'Return one full relpath:exact-declaration-line.' : IDENTITY
  return `Answer with ONLY grep/ripgrep and source reads, NEVER codegraph. Repo: ${REPO}. Target identity: ${q.qn}. Question: ${q.prompt}\n${shape} Use your own source-derived declaration identities. Return explicit items for structural questions, text for open, nonnegative integer calls (all tool invocations) and tokens_approx (~UTF-8 output bytes / 4). If tools fail/identity is uncertain, fail explicitly; do not fabricate [].`
}

/** @param {EvalQuestion} q @param {string} notes @param {{text:string}} answer */
function judgePrompt(q, notes, answer) {
  return `Grade against an independent rubric. Question: ${q.prompt}\nReference:\n${notes}\nAnswer:\n${answer.text}\nReturn finite score 0..1 + reason: 1 fully correct/complete, .5 partial, 0 wrong/explicit abstention. Never clamp or invent a score after failure.`
}

/** @param {EvalQuestion} q @param {string} mode @param {AgentResult} result */
function answerRecord(q, mode, result) {
  if (!result || !Number.isSafeInteger(result.calls) || result.calls < 0 || !Number.isSafeInteger(result.tokens_approx) || result.tokens_approx < 0) throw new Error(`${q.id}/${mode}: missing or invalid nonnegative integer costs`)
  const record = { id: q.id, mode, tokens: result.tokens_approx, calls: result.calls }
  if (q.type === 'open') record.text = requiredText(result, 'text', `${q.id}/${mode}`)
  else record.items = explicitItems(result, `${q.id}/${mode}`)
  return record
}

/** @param {EvalQuestion[]} questions */
function validateQuestions(questions) {
  if (!Array.isArray(questions) || !questions.length) throw new Error('missing/empty question set')
  const seen = new Set()
  for (const q of questions) {
    if (!q || typeof q.id !== 'string' || !q.id || q.id.trim() !== q.id || /[\u0000-\u001f\u007f-\u009f]/.test(q.id) || seen.has(q.id) || !['callers', 'callees', 'definition', 'open'].includes(q.type) || typeof q.qn !== 'string' || !q.qn) throw new Error(`invalid or duplicate question ${JSON.stringify(q)}`)
    seen.add(q.id)
  }
}

const loaded = await agent(`Read ${OUT}/questions.json and return its exact array as {"questions": [...]} WITHOUT dropping/editing any fields or rows.`, { schema: QUESTIONS, label: 'load-questions', phase: 'Oracle' })
const Q = loaded?.questions
validateQuestions(Q)
const results = await pipeline(Q,
  async q => {
    if (q.type === 'open') {
      const oracle = await agent(oracleOpenPrompt(q), { schema: ORACLE_OPEN, label: `oracle:${q.id}`, phase: 'Oracle', effort: 'high' })
      return { q, truth: { id: q.id, notes: requiredText(oracle, 'notes', `oracle:${q.id}`) } }
    }
    const oracle = await agent(oracleStructPrompt(q), { schema: ORACLE_STRUCT, label: `oracle:${q.id}`, phase: 'Oracle', effort: 'high' })
    return { q, truth: { id: q.id, items: explicitItems(oracle, `oracle:${q.id}`), notes: oracle.notes ?? '' } }
  },
  async prev => {
    const q = prev.q
    const responses = await parallel([
      () => agent(graphPrompt(q), { schema: RESPONDER, label: `graph:${q.id}`, phase: 'Answer' }),
      () => agent(baselinePrompt(q), { schema: RESPONDER, label: `baseline:${q.id}`, phase: 'Answer' }),
    ])
    return { ...prev, answers: [answerRecord(q, 'graph', responses[0]), answerRecord(q, 'baseline', responses[1])] }
  },
  async prev => {
    if (prev.q.type !== 'open') return prev
    const judged = await parallel(prev.answers.map(answer => async () => {
      const judge = await agent(judgePrompt(prev.q, prev.truth.notes, answer), { schema: JUDGE, label: `judge:${prev.q.id}:${answer.mode}`, phase: 'Judge', effort: 'high' })
      if (!judge || !Number.isFinite(judge.score) || judge.score < 0 || judge.score > 1) throw new Error(`${prev.q.id}/${answer.mode}: missing or invalid judge score`)
      return { ...answer, judge: judge.score }
    }))
    return { ...prev, answers: judged }
  }
)

/** @param {EvalQuestion[]} questions @param {object[]} rows */
function completeResults(questions, rows) {
  if (!Array.isArray(rows) || rows.length !== questions.length) throw new Error('incomplete or duplicate workflow results')
  for (const q of questions) {
    const matches = rows.filter(row => row?.q?.id === q.id)
    if (matches.length !== 1 || matches[0].truth?.id !== q.id) throw new Error('incomplete or duplicate workflow results')
    const answers = matches[0].answers
    if (!Array.isArray(answers) || answers.length !== 2 || ['graph', 'baseline'].some(mode => answers.filter(answer => answer?.id === q.id && answer.mode === mode).length !== 1)) throw new Error('incomplete or duplicate workflow mode matrix')
  }
}

// The CLI rechecks the actual files against the actual questions; the host's
// load/write agents cannot certify file contents or independent oracle accuracy.
completeResults(Q, results)
const truth = results.map(result => result.truth)
const answers = results.flatMap(result => result.answers)
const written = await agent(`Use Write to create exactly these files verbatim, without modifying JSON.\n${OUT}/truth.json:\n${JSON.stringify(truth, null, 2)}\n${OUT}/answers.json:\n${JSON.stringify(answers, null, 2)}\nThen reply done.`, { label: 'write-results', phase: 'Write' })
if (!written) throw new Error('write-results failed; no completed evaluation is claimed')
log(`quality artifacts prepared: scorer=${SCORER}, truths=${truth.length}, answers=${answers.length}; validate with codegraph quality score`)
return { repo: REPO, scorer: SCORER, questions: Q.length, truths: truth.length, answers: answers.length }
