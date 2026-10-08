'use strict'

/** @typedef {{pageLimit?: number, maxPages?: number, scorer?: string}} PageOptions */
/** @typedef {{qualified_name: string, limit: number, cursor?: string}} PageArgs */

const STRICT = 'qualified-name-v1'
const LEGACY = 'name-v1'

/** @param {PageOptions} options */
function pageOptions(options) {
  const pageLimit = options.pageLimit ?? 500
  const maxPages = options.maxPages ?? 1000
  const scorer = options.scorer ?? STRICT
  if (!Number.isSafeInteger(pageLimit) || pageLimit < 1 || pageLimit > 2000) {
    throw new Error(`invalid page limit ${pageLimit}: want an integer in [1,2000]`)
  }
  if (!Number.isSafeInteger(maxPages) || maxPages < 1) {
    throw new Error(`invalid max pages ${maxPages}: want a positive safe integer`)
  }
  if (![STRICT, LEGACY].includes(scorer)) throw new Error(`invalid scorer ${scorer}: want ${STRICT} or ${LEGACY}`)
  return { pageLimit, maxPages, scorer }
}

/** @param {string} line */
function trailerFields(line) {
  const fields = Object.create(null)
  for (const token of line.slice(2).trim().split(/\s+/)) {
    const at = token.indexOf('=')
    const key = token.slice(0, at)
    if (at <= 0 || Object.hasOwn(fields, key)) throw new Error(`malformed or duplicate trailer field ${token}`)
    fields[key] = token.slice(at + 1)
  }
  if (!['true', 'false'].includes(fields.has_more) || !fields.cursor || !fields.generation) {
    throw new Error('incomplete trailer: want has_more, cursor and generation')
  }
  if ((fields.has_more === 'true') === (fields.cursor === '-')) throw new Error('cursor disagrees with has_more')
  return fields
}

/** @param {string} output */
function parseRefPage(output) {
  const lines = output.split(/\r?\n/).filter(line => line !== '')
  const trailers = lines.filter(line => line.startsWith('# '))
  if (trailers.length !== 1 || lines.at(-1) !== trailers[0]) throw new Error('missing, duplicated or nonterminal page trailer')
  const fields = trailerFields(trailers[0])
  const refs = lines.slice(0, -1).map(line => {
    const columns = line.split('\t')
    if (columns.length !== 4 || columns.some(column => column === '')) throw new Error(`malformed TSV ref ${JSON.stringify(line)}`)
    return columns
  })
  if (!/^(0|[1-9]\d*)$/.test(fields.shown) || Number(fields.shown) !== refs.length) throw new Error('trailer shown count disagrees with refs')
  if (fields.has_more === 'true' && refs.length === 0) throw new Error('nonterminal page has no refs')
  return { refs, cursor: fields.cursor, generation: fields.generation, more: fields.has_more === 'true' }
}

/**
 * Read every page or fail: a partial walk must never become a scored answer.
 * @param {string} qn
 * @param {string} type
 * @param {(tool: string, args: PageArgs) => string} runPage
 * @param {PageOptions} [options]
 */
function graphItems(qn, type, runPage, options = {}) {
  const { pageLimit, maxPages, scorer } = pageOptions(options)
  if (typeof qn !== 'string' || !qn || !['callers', 'callees'].includes(type)) throw new Error('want a nonempty qn and callers/callees type')
  const items = new Set()
  const cursors = new Set()
  let cursor = ''
  let generation
  let bytes = 0
  for (let calls = 1; calls <= maxPages; calls++) {
    const args = { qualified_name: qn, limit: pageLimit }
    if (cursor) args.cursor = cursor
    const output = runPage(type, args)
    bytes += Buffer.byteLength(output, 'utf8')
    const page = parseRefPage(output)
    if (generation !== undefined && generation !== page.generation) throw new Error('served generation changed during evaluation')
    generation = page.generation
    for (const ref of page.refs) items.add(ref[scorer === STRICT ? 3 : 1])
    if (!page.more) return { items: [...items], calls, tokens: Math.round(bytes / 4) }
    if (cursors.has(page.cursor)) throw new Error('nonprogressing evaluation cursor')
    cursors.add(page.cursor)
    cursor = page.cursor
  }
  throw new Error(`evaluation exceeded ${maxPages} pages before a terminal trailer`)
}

module.exports = { STRICT, LEGACY, graphItems, pageOptions, parseRefPage }
