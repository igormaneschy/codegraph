'use strict'
const test = require('node:test')
const assert = require('node:assert/strict')
const { graphItems, parseRefPage, pageOptions, LEGACY } = require('./graph-pages')

class FakePageRunner {
  constructor(outputs) { this.outputs = outputs; this.invocations = [] }
  run(tool, args) {
    this.invocations.push({ tool, args })
    assert.ok(this.outputs.length, 'unexpected invocation')
    return this.outputs.shift()
  }
}
function ref(qn, name = 'Run') { return `Method\t${name}\tsrc/file.go:12\t${qn}\n` }
function page(refs, more = false, cursor = '-', generation = 'g1') {
  return refs.join('') + `# has_more=${more} cursor=${cursor} generation=${generation} shown=${refs.length}\n`
}

test('walks 720 homonyms, deduplicating exact QNs and retaining limits', () => {
  const refs = Array.from({ length: 720 }, (_, i) => ref(`src/file${i}.go.Owner.Run`))
  const first = page(refs.slice(0, 500), true, 'next')
  const second = page([...refs.slice(500), refs[0]])
  const runner = new FakePageRunner([first, second])
  const answer = graphItems('src/target.go.Target', 'callers', (tool, args) => runner.run(tool, args), { pageLimit: 500 })
  assert.equal(answer.items.length, 720)
  assert.equal(answer.calls, 2)
  assert.equal(answer.tokens, Math.round(Buffer.byteLength(first + second, 'utf8') / 4))
  assert.deepEqual(runner.invocations, [
    { tool: 'callers', args: { qualified_name: 'src/target.go.Target', limit: 500 } },
    { tool: 'callers', args: { qualified_name: 'src/target.go.Target', limit: 500, cursor: 'next' } },
  ])
})

test('known empty terminal page is evidence, UTF-8 costs are bytes, legacy explicit', () => {
  assert.deepEqual(graphItems('src/a.go.Target', 'callees', () => page([])), { items: [], calls: 1, tokens: Math.round(Buffer.byteLength(page([])) / 4) })
  const output = page([ref('lib/a.rb.Owner#méthode', 'méthode'), ref('lib/b.rb.Owner#méthode', 'méthode')])
  const answer = graphItems('lib/target.rb.Owner#target', 'callers', () => output, { scorer: LEGACY })
  assert.deepEqual(answer.items, ['méthode'])
  assert.equal(answer.tokens, Math.round(Buffer.byteLength(output, 'utf8') / 4))
})

test('rejects incomplete, malformed and inconsistent pages', () => {
  const validRef = ref('src/a.go.Run')
  const invalid = [
    '', validRef, validRef + '# has_more=false cursor=-\n',
    validRef + '# has_more=maybe cursor=- generation=g1 shown=1\n',
    validRef + '# has_more=true cursor=- generation=g1 shown=1\n',
    validRef + '# has_more=false cursor=next generation=g1 shown=1\n',
    validRef + '# has_more=false cursor=- generation=g1 shown=2\n',
    validRef + '# has_more=false cursor=- generation=g1 shown=1 shown=1\n',
    page([]) + validRef, page([]) + page([]), page([], true, 'next'),
    'Run\tsrc/a.go.Run\n# has_more=false cursor=- generation=g1 shown=1\n',
  ]
  for (const output of invalid) assert.throws(() => parseRefPage(output), undefined, output)
})

test('page cap, cycles, generation changes and executable failure are errors', () => {
  const output = page([ref('src/a.go.Run')], true, 'next')
  assert.throws(() => graphItems('src/a.go.Target', 'callers', () => output, { maxPages: 1 }), /exceeded 1 pages/)
  assert.throws(() => graphItems('src/a.go.Target', 'callers', () => output), /nonprogressing/)
  const runner = new FakePageRunner([output, page([], false, '-', 'g2')])
  assert.throws(() => graphItems('src/a.go.Target', 'callers', (tool, args) => runner.run(tool, args)), /generation changed/)
  assert.throws(() => graphItems('src/a.go.Target', 'callers', () => { throw new Error('executable failed') }), /executable failed/)
})

test('page options and query type are admitted explicitly', () => {
  for (const pageLimit of [0, -1, 1.5, 2001, NaN, Infinity]) assert.throws(() => pageOptions({ pageLimit }), /invalid page limit/)
  for (const maxPages of [0, -1, 1.5, NaN, Infinity]) assert.throws(() => pageOptions({ maxPages }), /invalid max pages/)
  assert.throws(() => pageOptions({ scorer: 'unknown' }), /invalid scorer/)
  assert.throws(() => graphItems('', 'callers', () => ''), /nonempty qn/)
  assert.throws(() => graphItems('src/a.go.Target', 'open', () => ''), /callers\/callees/)
})

module.exports = { ref, page }
