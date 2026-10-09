# Code review profundo — codegraph

Data: 2026-10-07. Base: `968ea8f5e042d457db51af9450fe7a04cb537a0e`.
Revisão de arquitetura, correção, segurança, concorrência, desempenho, testes e avaliação.
Nenhum código da aplicação foi alterado. Este relatório é o único arquivo adicionado ao checkout.

## Parecer executivo

**A base arquitetural é boa, mas eu não aprovaria uma nova release sem corrigir R01–R06.**
A prioridade é a confiança no grafo, não aumentar paralelismo ou adicionar funcionalidades.

Há **19 achados: 6 de severidade alta, 10 média e 3 baixa**. Não identifiquei um achado crítico comprovado.
Os principais problemas são Windows anunciado mas operacionalmente bloqueado; corrupção de FTS em duplicatas;
invalidação TS insuficiente; identidade incompleta do ambiente de análise; snapshot que não preserva todos
os inputs necessários; e impossibilidade de recuperar um primeiro índice degradado apenas com refresh.

Testes e lint verdes não contradizem esses problemas: faltam regressões para os contraexemplos.
As reproduções usaram fixtures temporárias e arquivos de teste injetados por `go test -overlay`,
sem editar fontes ou indexar manualmente o repositório atendido pelo MCP.

### O que preservar

- SQLite + FTS5 e refs compactas: adequados ao produto; não há justificativa para trocar o banco.
- Resolução delegada a SCIP/VTA; não substituir chamadas ausentes por heurísticas inventadas.
- Construção independente em `.building`, validação e manutenção do grafo anterior em falhas.
- Paginação explícita com geração e continuação, cobertura de similaridade e status degradado visível.
- Guard de sessão serializando consultas completas contra close/reopen.
- CI com dependências/actions fixadas, race detector e verificações de segurança.

## Validação executada

Ambiente: macOS, Apple M1, Go 1.27.0, CGO habilitado; `go.mod` declara Go 1.26.

| Verificação | Resultado |
| --- | --- |
| `gofmt -l .` | Nenhum arquivo listado |
| `go mod verify` | Todos os módulos verificados |
| `go mod tidy -diff` | Sem diff |
| `CGO_ENABLED=1 go vet ./...` | Sem diagnósticos |
| `CGO_ENABLED=1 go build ./...` | Passou |
| `golangci-lint run ./...` | 0 issues; versão local 2.13.1, CI fixa 2.12.2 |
| `go test ./... -coverprofile=/tmp/codegraph-review-coverage.out -timeout 15m` | 13 pacotes passaram; cobertura total 69,6% |
| `go test -race ./... -timeout 20m` | 13 pacotes passaram |
| Diagnósticos isolados via overlay | Reproduções descritas abaixo |
| Benchmarks locais e experimento de GC | Executados; resultados na seção de desempenho |

O pacote `internal/index` levou 235,2 s na execução com cobertura e 449,6 s na execução com race.
Cobertura por pacote: cmd 35,7%; bench 83,7%; gocalls 84,6%; graph 57,6%; index 82,8%;
install 52,1%; mcp 73,8%; memory 38,1%; quality 50,9%; query 66,4%; scip 76,2%;
securefile 52,9%; similar 89,3%.

Limites: não executei Windows/Linux nativos, Go 1.26, testes de queda de energia ou os scanners
remotos de vulnerabilidades. A revisão cobre os fluxos e componentes relevantes, não constitui
prova formal de todas as linhas do repositório. Benchmarks sintéticos não são SLAs de produção.

## Achados de severidade alta

### R01 — Windows publicado, mas as operações necessárias sempre retornam ErrUnsupported

**Local:** `internal/securefile/securefile_windows.go:113–138`, `cmd/codegraph/main.go:145–157`,
`.github/workflows/release.yml:42–43`.

`MkdirAllPrivate`, `WritePrivate`, `WritePrivateTemp` e `MkdirTempPrivate` não têm implementação
Windows. `cachePath` chama a primeira antes de abrir o banco; snapshots e manifests exigem as demais.
Assim, compilar/publicar o executável Windows não significa que index/stats/MCP funcionem.
Os testes de securefile explicitamente aceitam ErrUnsupported, e o CI principal roda somente Ubuntu.

**Melhoria:** implementar operações por handles e ACLs apropriadas, com testes nativos. Até lá,
retirar a promessa/asset de suporte operacional ou documentar explicitamente a indisponibilidade.
Não resolver com fallback inseguro de chmod/path-based writes.
**Regressão:** smoke test nativo de index → no-op → query → refresh nas três plataformas de release.
**Evidência:** caminho incondicional no código; não executei o binário em Windows.

### R02 — INSERT OR IGNORE + LastInsertId corrompe o FTS em duplicatas

**Local:** `internal/graph/store.go:721–750`.

Uma inserção ignorada preserva o último rowid da conexão. `LastInsertId != 0` não prova que o nó
foi inserido. O código insere os termos do nó duplicado no rowid do último nó realmente inserido.

**Reprodução:** inserir `[Alpha, Beta, Alpha]` faz a busca por Alpha retornar também Beta;
`ValidateIntegrity` retorna `FTS postings do not match nodes: missing=0 extra=4`.
No caminho de produção, um arquivo Go válido com `init`, `helper`, `init` faz `RunAtomic`
falhar em `validate built index` com postings extras. O validador impede o commit ruim,
mas repositórios válidos deixam de ser indexáveis. Duplicatas também aparecem em overloads/reaberturas.

**Melhoria:** verificar `RowsAffected` antes de obter o ID/inserir FTS, propagando erros.
Separadamente, definir a política de identidade para múltiplas declarações legais;
ignorar uma segunda declaração não necessariamente representa corretamente o símbolo.
**Regressão:** duplicata intercalada, várias init, overloads TS e reaberturas Ruby; busca e integridade.

### R03 — Invalidação incremental TS não prova a segurança do reuso

**Local:** `internal/index/incremental.go:221–249`, `internal/index/tsdeps.go:137–176`,
`internal/index/imports.go:15–20,395–414`.

Três lacunas relacionadas:

1. Added/Deleted sem arquivo Modified retornam em `len(modifiedTS)==0`, antes do fallback amplo.
   Adicionar somente `b/new.ts` devolve apenas `map[b:true]`, sem o marcador all-TS.
2. IMPORTS é consultado apenas sobre os arquivos originalmente modificados, em um único salto.
   No encadeamento A → B → C, modificar C invalida B e C, mas não A.
   Reexports/tipos e mudanças de binding podem propagar efeitos além do importador direto.
3. O grafo IMPORTS só resolve imports relativos. Aliases `paths`, pacotes/workspaces e outros
   acoplamentos resolvidos pelo TypeScript não constituem dependências completas nesse modelo.

Project references transitivas ajudam, mas não cobrem projetos sem references nem fecham a
transitividade dos importadores adicionados depois. A premissa comentada de que arquivos não
modificados preservam seus bindings não basta para certificar um programa TypeScript.

**Melhoria imediata:** fallback conservador antes do retorno de membership-only; invalidar todos
os escopos quando não houver prova de dependências completa. Depois, usar dependências do programa
TypeScript/resolver e fechamento reverso transitivo, com ownership real de includes/references.
**Regressão:** incremental versus rebuild, exigindo CALLS esperadas, para add-only, delete-only,
shadowing de resolução, aliases sem references, A→B→C e mudança de reexport.
**Evidência:** os dois conjuntos de invalidação incorretos foram reproduzidos estruturalmente;
não executei um oráculo E2E TypeScript para todos esses contraexemplos. O risco de grafo divergente
é decorrente da análise de dependências incompleta, não de uma medição de recall aqui.

### R04 — Freshness não inclui todo o ambiente que define o resultado

**Local:** `internal/index/manifest.go:159–172,558–693,707–741,1154–1165`,
`internal/gocalls/gocalls.go:67–68`.

A identidade cobre arquivos/configs e versões manuais dos bridges, mas não o ambiente efetivo
de build Go. Também não cobre, em geral, conteúdo instalado de dependências abaixo de node_modules/vendor.
Lockfiles representam uma intenção, não uma prova dos bytes que o resolver leu.

**Reprodução forte:** indexar com `GOFLAGS` vazio e depois `GOFLAGS=-tags=alternate` retorna
`reused=true`. O digest mantido difere do digest de um rebuild sob as novas tags.
**Reprodução adicional:** editar uma declaração em `node_modules/local/index.d.ts`, mantendo
lockfiles/configs, produz `sameRepositoryScan=true`.

**Melhoria:** incluir uma identidade tipada do ambiente relevante: toolchain, GOOS/GOARCH,
CGO, tags/GOFLAGS, workspace efetivo e inputs externos admitidos. Para dependências, escolher
um contrato explícito: bytes efetivamente observados/digest de árvore validada ou verificação da
instalação correspondente ao lockfile. Bump de versão da análise ao mudar o contrato.
**Regressão:** tags/ambiente/dependências alterados devem invalidar o índice; rebuild e incremental iguais.

### R05 — Snapshot seguro, mas incompleto para o resolver

**Local:** `internal/index/manifest.go:202–285`, especialmente `269–279`.

O snapshot copia fontes reconhecidas, manifests e apenas node_modules/vendor na raiz.
Arquivos C/headers necessários ao cgo ficam fora. Node_modules próprios de subprojetos também.
Segurança do transporte não garante equivalência com o programa original.

**Reprodução:** módulo Go compilável com `#include "thing.h"` e `thing.c` é indexado como degraded;
o resolver registra `fatal error: 'thing.h' file not found`. A inspeção do snapshot confirma ausência
de ambos. Uma fixture `packages/app/node_modules/local/index.d.ts` também não é copiada.

**Melhoria:** planejar o snapshot a partir dos inputs reais dos programas Go/TS, incluindo arquivos
auxiliares e árvores de dependência por escopo, mantendo confinamento e verificação de identidade.
Não copiar indiscriminadamente todo o checkout como solução definitiva.
**Regressão:** cgo com headers locais; módulos com inputs auxiliares; monorepo com instalação de deps por pacote.

### R06 — Primeiro índice degradado não se recupera com refresh sem editar arquivos

**Local:** `internal/index/prepare.go:160–166`, `internal/index/manifest.go:1168–1191`.

O no-op reutiliza manifests degradados. Se o primeiro resolver falhar temporariamente, consertar
a ferramenta e executar refresh não tenta novamente: fontes/configs continuam iguais.

**Reprodução:** primeira indexação com falha injetada; segunda com resolver disponível:
`first=degraded refresh=degraded reused=true total resolver invocations=1`.

**Melhoria:** não certificar no-op de escopos anteriormente falhos em um refresh que deve reparar o índice.
Como o primeiro grafo degradado descarta CALLS de todos os resolvers, a reconstrução de recuperação
precisa restaurar todos os escopos pertinentes, não apenas o que originalmente falhou.
Um modo force/retry explícito pode complementar a política, com backoff para evitar loops automáticos.
**Regressão:** falha transitória → reparo externo → refresh saudável, sem tocar fontes ou apagar cache.

## Achados de severidade média

### R07 — Métodos Go genéricos perdem CALLS mesmo em índice healthy

**Local:** `internal/index/treesitter.go:221–239`, `internal/gocalls/gocalls.go:175–205`.

O extrator usa `Box[T]` como receiver; o mapper SSA usa o nome nominal `Box` e também rejeita
funções Synthetic, incluindo formas instanciadas que precisam de mapeamento para sua origem.
**Reprodução:** Box[T].Value chama helper; Use chama Box[int].Value. Resultado: `healthy`, CALLS vazias.
**Melhoria:** identidade nominal compartilhada; usar a origem de instâncias genéricas quando houver
uma declaração real conhecida. Preservar o descarte de sintéticos sem origem demonstrável.
**Regressão:** chamadas para/de métodos e funções genéricos; comparar QNs e arestas, não apenas ausência de panic.

### R08 — go.work é reconhecido, mas a carga única ./... falha em workspace sem módulo raiz

**Local:** `internal/index/calls.go:270–286`, `internal/gocalls/gocalls.go:67–68`.

**Reprodução:** raiz com go.work e módulos ./a e ./b produz degraded:
`directory prefix . does not contain modules listed in go.work or their selected dependencies`.
**Melhoria:** enumerar os módulos do workspace e carregar os padrões corretos com contexto comum;
definir também o contrato para repositórios com módulos aninhados e sem config na raiz.
**Regressão:** workspace real, chamadas dentro/entre módulos e alterações no conjunto use.

### R09 — Similaridade usa leitura path-based que contorna securefile

**Local:** `internal/index/similar.go:22–25,62,92`.

Para Ruby e fontes sem resolver externo, o pipeline não cria snapshot. O arquivo pode ser
substituído por symlink após os passes anteriores; `os.ReadFile` então segue o link.
**Reprodução:** a etapa aceita um symlink para arquivo Ruby externo: error=nil, docs=1, complete.
Não demonstrei exfiltração de conteúdo por uma tool: o problema comprovado é a leitura fora do boundary
e influência de bytes externos na análise. Arquivos especiais também merecem rejeição explícita.
**Melhoria:** default de similarReadFile deve ser securefile.ReadFile, com as mesmas garantias dos outros passes.
**Regressão:** substituição de leaf/parent entre passes, symlink externo e arquivo não regular.

### R10 — Cursor de snippet aceita arquivo diferente com o mesmo tamanho

**Local:** `internal/graph/snippet_page.go:70–72`, `internal/query/page.go:349–385`.

Somente o tamanho é comparado; geração do grafo não versiona o arquivo vivo.
**Reprodução:** primeira página de `one\ntwo\nthree\n`; reescrever como `X\nY\ntwo\nthree\n`,
com os mesmos 14 bytes. A continuação retorna Y sem erro, misturando versões e deslocando linhas.
**Melhoria:** definir um contrato real de snapshot/conteúdo. Para garantia exata, usar bytes imutáveis
ou digest verificado; inode/mtime/size podem ser fast reject, mas não são prova exata de conteúdo.
**Regressão:** edição same-size, mudança de quebras, rename com replacement e alteração durante leitura.

### R11 — Publicação da sessão pode informar geração diferente da servida

**Local:** `cmd/codegraph/session.go:302–325,484–500`, também `225–234`.

reopenShared captura o engine sob lock, mas publish lê novamente o manifest do caminho depois de
soltar esse lock. Outro writer pode instalar uma geração entre essas operações.
**Reprodução determinística:** reabrir G1 → writer instala G2 → publish da primeira rodada.
Status ready informa G2, página serve G1 e status não acusa lag. A cobertura de similarity lida do
manifest de disco também pode pertencer ao grafo novo, não ao engine servido.
**Melhoria:** publicar geração/cobertura do snapshot capturado com o engine; ler disco apenas para comparação de lag.
Adotar a identidade do engine também no caminho de reopen após falha.
**Regressão:** barreira entre reopen e publish com writer externo, verificando status == trailer da página.

### R12 — Mudar SkipSimilar não invalida o índice

**Local:** `internal/index/manifest.go:169`, `internal/memory/memory.go:114–122`.

A versão inclui budgets, mas não a decisão de executar/omitir a fase.
**Reprodução:** indexar com CODEGRAPH_SKIP_SIMILAR=1, trocar para 0 e reindexar:
`omitted`, `reused=true`; a fase continua ausente.
**Melhoria:** incluir política efetiva da fase na identidade ou um rebuild/force explícito que efetivamente a reexecute.
**Regressão:** omitted→complete e mudanças de perfil/budget, com cobertura e arestas verificadas.

### R13 — Scripts de avaliação ainda não caminham pelas páginas

**Local:** `eval/graph-answers.js:17–26`, `eval/quality-workflow.js:57–60`.

O primeiro script consulta apenas uma página e registra calls=1/tokens=0. O workflow orienta
limit=200 sem continuação. Isso perde respostas até abaixo de 500 refs se o orçamento de bytes cortar.
O harness Go de benchmark foi migrado; os scripts JS não acompanharam o contrato.
**Melhoria:** consumir trailer/cursor até has_more=false, cobrar todas as chamadas/bytes e usar QNs
canônicas no scoring onde homônimos importam. Tornar o executável configurável, não codegraph.exe fixo.
**Regressão:** hub >500 e corte por bytes; todas as refs e custos devem corresponder às páginas reais.

### R14 — Installer não suporta o formato que escolhe e pode danificar configuração

**Local:** `internal/install/install.go:114–149,162–175,213–226`.

Prefere opencode.jsonc, mas usa json.Unmarshal puro. JSONC válido com comentário é rejeitado.
Conteúdo JSON null provoca panic por mapa nil. Além disso, installOpencode ignora qualquer erro
ReadFile, não só ENOENT, podendo tratar configuração existente não legível como vazia.
Os writes não são atômicos; uma interrupção pode truncar a configuração inteira.
**Melhoria:** validar objeto, suportar JSONC, propagar erros de leitura, merge não destrutivo e escrita
atômica; preservar conteúdo/comentários do usuário quando viável. Não esconder falhas de permissão.
**Regressão:** comentários/trailing commas, null/array, EACCES, config existente e falha de escrita.

### R15 — Rotas NestJS dinâmicas viram uma rota literal inventada

**Local:** `internal/index/routes.go:32–46,82–104`.

String vazia representa tanto ausência de argumento quanto argumento dinâmico não resolvido.
**Reprodução:** @Controller(BASE) + @Get(PATH) gera `GET /` sem saber o valor de BASE/PATH.
**Melhoria:** resultado tri-state: literal, no-arg válido, unknown. Unknown deve omitir Route ou declarar
explicitamente não resolvida, nunca assumir raiz. Arrays de paths exigem suporte validado ou omissão.
**Regressão:** constantes, interpolação, arrays e controller/verb sem argumento.

### R16 — Orçamento de resposta não é um limite duro de recursos

**Local:** `internal/query/architecture.go:37–40`, `internal/graph/snippet_page.go:80–140`,
`internal/query/budget.go:22–42`.

Architecture não usa clamp: limit enorme pode solicitar todos os hotspots/pacotes e renderizar sem
limite de bytes. Snippets aceitam deliberadamente uma linha inteira oversized; ReadString aloca
essa linha inclusive ao pular prefixos ou fazer lookahead. Uma linha gigante pode dominar memória
mesmo quando não será exibida. Esta segunda parte é uma limitação do contrato documentado,
não um acidente de truncamento.
**Melhoria:** clamp de Architecture e teto absoluto de linha/arquivo, com erro orientado a range/fragmento
ou leitura dedicada. Não truncar silenciosamente refs/código. Bounding deve valer também para lookahead e skip.
**Regressão:** limit gigante, linha gigante antes do range, lookahead gigante e corte multibyte.

## Achados de severidade baixa

### R17 — InsertEdges pode criar aresta com projeto inconsistente

**Local:** `internal/graph/store.go:841–890`.

O mapa de endpoints usa o projeto do primeiro item; cada insert usa e.Project sem validar igualdade.
**Reprodução:** lote de dois projetos mantém ambos; ValidateIntegrity detecta edge project=q com endpoints=p.
Os lotes atuais do pipeline são de um projeto, então não reproduzi corrupção no caminho normal.
**Melhoria:** rejeitar lote misto e validar projeto/QNs antes da transação; erro inclui item ofensivo.
**Regressão:** lote misto não deve persistir nenhuma aresta. Também cobrir concorrência de mutações se a API a admitir.

### R18 — detect_changes não detecta mudanças de configuração/ambiente

**Local:** `internal/index/incremental.go:53–107`, `internal/mcp/server.go:260–267`.

Compara só fontes; alterar tsconfig/go.mod/ignore pode produzir `no changes since the last index`,
embora RunAtomic deva reconstruir. O contrato interno diz source-only, mas a mensagem pública é ampla.
**Melhoria:** expor categorias source/config/environment ou afirmar explicitamente source-only;
reusar o plano de scan/freshness ao orientar se o grafo pode ser confiado.
**Regressão:** mudança só de config/ignore, distinguindo ausência de mudança em fontes de índice fresco.

### R19 — Transporte MCP esconde falhas de output e validação incompleta

**Local:** `internal/mcp/server.go:115–138,166–216`.

Encode errors são ignorados; JSON malformado é só logado e não recebe erro de parse.
Campos required das schemas não são validados pelo runtime: qualified_name/file/query vazios
podem virar consulta vazia em vez de erro de parâmetro. Versão do protocolo é fixa, sem negociação.
**Melhoria:** propagação de falhas de transporte, validação tipada por tool, replies apropriados a
requests/notifications e teste de interoperabilidade com a versão MCP suportada. Não é preciso
introduzir um framework inteiro apenas para isso.
**Regressão:** writer que falha, request JSON malformado, ID/notificação e required ausente.

## Melhorias de desempenho — na ordem de retorno provável

### P1 — Remover GC duplo e rever a frequência de Gate

`internal/memory/memory.go:90–93` executa runtime.GC e depois debug.FreeOSMemory.
A implementação/documentação do próprio Go confirma que FreeOSMemory já força GC.
`internal/index/similar.go:83` faz esse gate a cada arquivo, além dos gates por batch/fase.

Experimento **somente em overlay**, sem mudar o checkout: fixture de 100 arquivos Ruby, rebuild
forçado, 3 medições de 3 iterações, depois de as suítes de testes terminarem:

| Variante | Mediana |
| --- | --- |
| Código atual, GC duplo | 545,96 ms/op |
| Somente debug.FreeOSMemory | 458,48 ms/op |

Redução observada de **16,0% no tempo dessa fixture**. Não é ganho universal da aplicação.
O experimento não certifica impacto em peak RSS/hosts limitados. O perfil CPU exploratório também
mostrou custo relevante de GC, syscalls e canonicalização; não transformar porcentagens cum em soma.

**Ação:** primeira mudança pequena é remover o GC redundante; depois testar Gate por fase/batch
ou pressão real, com peak RSS/heap/latência como contratos. Não eliminar os gates indiscriminadamente.

### P2 — Keyset pagination e byte offsets para páginas profundas

LIMIT/OFFSET reduz RAM, mas não elimina scans/skips e sorts repetidos. SnippetPaged reabre o arquivo
e recomeça da linha 1 a cada página, criando strings para todas as linhas ignoradas.

Benchmarks existentes, mesma máquina, sem as suítes concorrentes, 3 repetições de 300 ms:

| Operação | Mediana | Alocações/op |
| --- | --- | --- |
| Snippet primeira página | 0,184 ms | 299 / ~88 KB |
| Snippet a partir da linha 4001 | 0,389 ms | 4299 / ~152 KB |
| DeadCodePage primeira página, 4000 candidatos | 4,80 ms | ~5857 / ~263 KB |
| DeadCodePage após 60 páginas | 7,80 ms | ~6837 / ~273 KB |
| InsertEdges_PhasedReuse, lote 500 | 2,33 ms | ~6339 / ~207 KB |

**Ação:** cursor com último tuple de ordenação para refs/dead_code; cursor de snippet com byte offset
em início de linha, vinculado à identidade de conteúdo resolvida em R10. Consultar EXPLAIN QUERY PLAN
antes de acrescentar índices compostos. Medir enumeração completa, não só uma página.
O benchmark InsertEdges reinserta o mesmo lote, então mede principalmente duplicatas após a primeira
iteração; adicionar benchmarks distintos de inserts novos, duplicates e rebuild do mapa.

### P3 — Não decodificar propriedades que a resposta compacta não utiliza

Search/Neighbors carregam Node completo e fazem json.Unmarshal de properties para depois retornar
apenas nome/QN/label/file/linhas. Hotspots e dead_code têm necessidades diferentes.
**Ação:** projeção SQL/ref dedicada para consultas compactas; manter parsing de props onde necessário.
**Contrato:** mesmas refs, ordenação, paginação e erros, menos bytes/alocações. Não otimizar à custa da integridade.

### P4 — Fazer MaxEdges limitar retenção durante similaridade

`internal/similar/similar.go:237–293` acumula até MaxPairs arestas e só corta para MaxEdges no final.
Com defaults, pode construir 250 mil arestas para conservar 50 mil. Seen e buckets também custam RAM.
**Ação:** retenção limitada durante a execução, preservando seleção/ordem determinística, ou uma política
versionada de early stop. Se mudar o conjunto retido, bump de versão e testes de budgets/cobertura.
Checar cancelamento também durante assinaturas/buckets/sort; hoje o loop de pares é a principal barreira.

### P5 — Evitar canonicalização repetida e staging redundante, sem enfraquecer segurança

CanonicalPath percorre ancestors e lista/stata entradas dos diretórios para recuperar spelling físico.
ValidateRepositoryRoot é repetido em ProjectName, scans, applicability e leitura de configs.
O perfil da fixture mostrou custo relevante nesse caminho. O snapshot também rele e revalida caminhos
várias vezes; não substituir isso por cache global de paths.
**Ação:** um valor interno de root já validado e um plano de inputs por execução; handles/verificação
continuam nos boundaries de leitura. Reusar somente observações cuja identidade continue demonstrada.
Pular leitura de imports para linguagens sem imports suportados e reaproveitar hashes de bytes já lidos.

### P6 — Cache de Architecture por engine/generation

Mesmo immutable graph é agregado a cada pedido: counts, arquivos por diretório, JSON de complexidade,
GROUP BY de hubs e sorts. Cache local ao engine aberto, invalidado em Reopen, é simples e seguro.
Primeiro aplicar o clamp de R16. Não usar o manifest mutável do caminho como chave de um handle antigo.

### P7 — Limites reais de recursos e observabilidade

- Linux usa MemTotal do host, não o limite cgroup: container pequeno pode receber budget grande demais.
- SetMemoryLimit automático pode sobrescrever GOMEMLIMIT informado pelo operador.
- `internal/scip/run.go:150–152` captura stdout/stderr em bytes.Buffer ilimitado, embora só mostre tail.
- `run.go:163` + `rss_linux.go:16–29` medem o PID lançado por npx, não necessariamente o Node pesado
  nem o conjunto dos descendentes; peak RSS pode ser subestimado.

**Ação:** effective memory = limite aplicável do host/container, respeitando configuração explícita;
ring buffer bounded de logs; métricas da árvore/grupo de processos. Expor duração por fase, bytes de
staging, motivo de invalidação, escopos reusados e peak RSS real. Benchmarks sem essas métricas
confundem custo de cópia, resolver, validação e GC.

### P8 — Não fazer agora

Não paralelizar os maiores resolvers sem orçamento conjunto. Não remover validador exato/no-op
fail-closed para produzir um benchmark melhor. Não introduzir watcher, grafo remoto, embeddings ou
Cypher como resposta aos gargalos encontrados. Depois de R03/R04, avaliar cache por conteúdo de
extração/assinaturas: hoje uma edição refaz definições/imports/similaridade de todo o corpus.
Isso é uma evolução maior, condicionada a equivalência incremental versus rebuild.

## Melhorias estruturais

O grafo MCP identificou hotspots reais: runPipelineContext (complexidade 54), runAtomicContext (47),
scanRepositoryContext (41), prepareIndexingContext (32) e callTool (30).
`store.go` tem 1535 linhas; manifest.go 1263; pipeline.go 732; main.go 831; session.go 514.
O objetivo não é obedecer a um limite arbitrário de linhas, mas tornar os invariantes revisáveis.

1. **Separar responsabilidades primeiro em arquivos, não criar dezenas de packages.**
   - graph: connection/schema, node writes/FTS, edge writes, queries, integrity, digest.
   - index: repository observation, dependency/ownership plan, staging, freshness decision, commit/recovery.
   - query: cursor codec, ref paging, dead-code paging, snippet paging.
   - cmd: comando CLI fino; protocolo/lifecycle de sessão como unidade testável.
2. **Plano de indexação explícito e tipado.** Campos para inputs, ambiente, escopos, invalidation reason,
   retries/cobertura. Evitar flags contraditórias espalhadas entre prepare/pipeline/manifest.
   Tornar visível a diferença entre snapshot confiável, resultado parcial e freshness certificada.
3. **Dependências por instância, não hooks globais mutáveis.** Injetar resolvers, filesystem boundaries
   e validator via opções internas/defaults tipados. Hooks globais dificultam testes paralelos e
   futuras múltiplas indexações dentro do mesmo processo. Não requer framework de DI.
4. **Publicar um estado da sessão capturado junto ao engine.** Generation, cobertura e status precisam
   da mesma origem; comparar disco é outra operação. Centralizar transições e ownership de recursos.
5. **Um só pipeline com política de validação explícita.** Run e RunAtomic usam strictness diferentes;
   testes de memória/benchmark pelo caminho leve não são prova do gate de produção. Manter a distinção
   clara e cobrir invariantes também com RunAtomic.
6. **Contrato de durabilidade distinto de atomicidade.** Writes duráveis sincronizam o arquivo,
   mas os renames não sincronizam explicitamente o diretório pai. Definir a garantia para power loss
   e, se exigida, fsync de diretório/ordem de commit e recovery tests. Não afirme crash durability
   apenas porque o rename é atômico ou um teste de erro injetado passou.

## Melhorias dos testes e da avaliação

**Acompanhamento — stress Go (2026-10-09):** o item de `stress_test.go` abaixo
foi corrigido em recorte test-only. A fixture de 280 arquivos exige resultado
healthy/cold, escopo Go tentado e bem-sucedido e exatamente 560 CALLS independentes:
FnI → Fn((I+1)%n) e FnI → localI, com QNs e arquivo de origem exatos. O oracle
rejeita grafo SQL só com DEFINES, ausência/excesso/duplicação de CALLS e bindings
errados mesmo com a mesma contagem. Casos n=1 e n=2 cobrem self-call/ciclo; erros
de leitura e estados/escopos inválidos falham. Integridade/oracle são checados fora
do bloco PeakHeap; sampler, ceiling, skip policy e corpus não mudaram. O teste
continua medindo `Run`, não certificação estrita `RunAtomic`. Contrato/evidências:
`VALIDATION_GO_STRESS_CALLS.md`; sem mudança de runtime/identidade nem claim de ganho.
Os bullets seguintes preservam o diagnóstico da revisão original.

- R01–R16 precisam de regressões comportamentais, não só aumento de cobertura percentual.
- `TestCallEdges_GenericsDoNotCrash` garante um call não genérico sobrevive; não valida calls genéricas.
- `internal/index/tsdeps_test.go:210–272` compara digests mas não exige índice healthy nem um
  conjunto significativo de CALLS. As fixtures precisam realmente alterar bindings/arestas,
  não apenas constantes que mantêm a mesma relação. Evitar equivalência vacuosa.
- `internal/index/stress_test.go:103–105` usa EdgesKept total como prova de CALLS. DEFINES sozinhas
  podem satisfazer a condição. Asserção precisa contar EdgeCalls e validar terminal status.
- Testes de tsdeps invocam SCIP real sem seam/flag de integração; portanto a promessa de que Node
  não é necessário para testar não corresponde a toda a suíte atual. Separar testes determinísticos
  de integração e instalar/pinar Node/SCIP explicitamente no job adequado.
- CI deve testar plataformas de release em runtime; build-only não detecta ErrUnsupported.
- Manter barreiras determinísticas nas corridas; race detector detecta data races, não R11
  (corrida lógica entre dois estados individualmente protegidos).
- Evaluate aceita respostas ausentes/duplicadas sem um contrato forte de completude do run.
  Validar IDs únicos, verdade disponível e matriz mode×question completa antes de agregar.
  Separar scorer permissivo de nomes de um scorer estrito por QN: basename/nome simples colapsam homônimos.
- Benchmarks: medir cold/no-op/edit/config/degraded-recovery, índices pequenos/grandes e full walks;
  incluir p50/p95, peak RSS e bytes. Registrar versões/ambiente, separar método antigo do paginado.

## Ordem sugerida de execução

| Entrega | Escopo | Critério de conclusão |
| --- | --- | --- |
| 1. Correção de storage/segurança | R02, R09, R17 | Duplicatas/projetos/symlinks rejeitados ou representados sem corrupção |
| 2. Freshness e dependências | R03, R04, R05, R06, R12 | Contraexemplos recuperam; incremental == rebuild com CALLS esperadas |
| 3. Suporte operacional | R01, R07, R08, R14, R15 | Smoke tests nativos e fixtures de linguagem reais |
| 4. Contrato de consulta/avaliação | R10, R11, R13, R16, R18, R19 | Snapshot/geração corretos; pages completas e custos reais |
| 5. Performance incremental | P1 → P2/P3 → P4/P5/P6/P7 | Ganho medido sem piorar RSS, recall, integridade ou determinismo |

Bloquear publicação do asset Windows desde já; implementar o suporte pode ser uma entrega separada.
Não misturar todas as mudanças em um PR. Cada alteração semântica de resolver/invalidação precisa
atualizar a identidade de análise e `docs/ARCHITECTURE.md`; fechar milestones no roadmap depois da validação.

## Artefatos locais da revisão

Logs e experimentos ficaram fora do checkout, sob `/tmp/codegraph-review-*`:
`tests.log`, `race.log`, `vet.log`, `lint.log`, `coverage.out`, `repros.log`, `repros-2.log` até
`repros-6.log`, `idle-bench.log`, `idle-double-gc.log`, `idle-single-gc.log`, `cpu.pprof` e `pprof.log`.
Os arquivos `*-test.go` correspondentes usam suffix `_test.go` nas fixtures de overlay;
`overlay.json` permite repetir os diagnósticos no checkout revisado:

```bash
CGO_ENABLED=1 go test -overlay=/tmp/codegraph-review-overlay.json \
  ./internal/graph ./internal/index ./internal/query ./internal/install ./cmd/codegraph \
  -run '^TestReview' -v -count=1 -timeout 3m
```

Esses são **diagnósticos** que registram o comportamento defeituoso, não regressões integradas que
já falham quando ele ocorre. Arquivos de /tmp são temporários; a evidência relevante está resumida
neste relatório. O experimento de GC altera apenas uma cópia temporária da implementação via overlay;
o código original permaneceu intacto.

## Acompanhamento das correções — lote 1 (R02, R09, R17)

Contrato de validação definido antes da implementação:

- [x] R02: duplicatas intercaladas e entre lotes preservam a primeira declaração,
  sem hits falsos no FTS; `ValidateIntegrity` passa. Declarações Go `init`,
  overloads TS e reaberturas Ruby não corrompem o storage.
- [x] R09: ambas as APIs de similaridade rejeitam substituição de leaf/parent
  por symlink e arquivos não regulares, sem produzir arestas/cobertura completa.
- [x] R17: lote misto ou com projeto/QNs vazios é rejeitado antes da transação;
  nenhuma aresta do lote persiste. Endpoints inexistentes continuam sendo descartados.
- [x] Gates: gofmt, dependências, build, vet, testes, race e lint.

Limites: manter `UNIQUE(project, qualified_name)` e a política first-wins vigente.
Representar todas as declarações legais é trabalho separado; este lote não muda
resolvers, invalidação incremental, suporte Windows ou performance.

Validação local: macOS/arm64, Go 1.27.0, CGO habilitado, golangci-lint 2.13.1
(CI: Go 1.26 e lint 2.12.2). `go mod verify`, `go mod tidy -diff`, build, vet,
suítes completas com cobertura e race passaram; lint reportou 0 issues. As novas
regressões foram reexecutadas com e sem race após os últimos ajustes de fixtures.
A fixture de overloads abstratos TS foi validada manualmente com TypeScript 5.9.3
(`tsc --strict --noEmit`); não foi adicionada dependência Node aos novos testes.
Self-review de código e testes: sem novos achados neste lote. Windows/Linux
nativos e as demais correções do relatório continuam pendentes.

Logs locais: `/tmp/codegraph-fixes-lot1-{red,tests,race}.log`, resultado dos gates
em `/tmp/codegraph-fixes-lot1-gates.status`; cobertura em
`/tmp/codegraph-fixes-lot1-coverage.out`. O relatório original acima permanece
como registro da revisão sobre a base indicada, não como descrição pós-correção.

## Acompanhamento das correções — lote 2a (R03, R06, R12)

Contrato definido antes da implementação:

- [x] R03: qualquer mudança de fonte TS/JS (inclusive add-only/delete-only)
  invalida todos os escopos TS/JS; sem mudanças TS/JS, o reuso continua possível.
  Uma nova versão da política força reconstrução de índices antigos.
- [x] R03: incremental e rebuild preservam CALLS esperadas para aliases,
  reexports e cadeia A→B→C, com índice healthy; equivalência de digest não pode
  passar apenas porque ambos os grafos não têm CALLS.
- [x] R06: primeiro índice degradado → reparo externo → refresh saudável, sem
  editar fontes, restaura todos os escopos. Uma nova falha preserva o grafo
  degradado anterior e retorna erro visível; um índice healthy pode fazer no-op.
- [x] R12: alternar a política efetiva de similaridade invalida o índice;
  omitted→complete restaura arestas e complete→omitted remove as arestas.
- [x] Gates: gofmt, dependências, build, vet, testes, race e lint.

R04/R05 ficam no lote 2b: ambiente e bytes admitidos precisam usar o mesmo plano
observado e verificado de inputs no scan e no snapshot. Não ampliar o snapshot
indiscriminadamente nem certificar dependências apenas pelos lockfiles.

Validação local: macOS/arm64, Go 1.27.0, Node 26.0.0, SCIP TypeScript 0.4.0,
CGO habilitado e golangci-lint 2.13.1. Dependências, build, vet, suítes completas
com cobertura e race passaram. Os quatro oráculos reais também passaram com e
sem race (`-tags integration`); vet e lint foram executados nos dois modos.
A fixture transitive-reexport falha contra a implementação original via overlay:
ela reutiliza o escopo `app`, conserva o callee `left` em vez de `right` e diverge
do rebuild. Com a política conservadora, a chamada esperada e o digest coincidem.

As integrações externas agora são opt-in, com job separado no CI que fixa Node
26.0.0, SCIP TypeScript 0.4.0 e actions por SHA. YAML validado localmente; o job
remoto e a combinação Linux/Go 1.26 ainda precisam executar no CI. Invalidação
ampla pode aumentar o custo de edits TS; não foi feita alegação de ganho de
performance. Self-review de código e testes: sem novos achados neste recorte.

Logs: `/tmp/codegraph-fixes-lot2a-{red,tests,race,integration,integration-race,
original-oracle}.log`; status em `/tmp/codegraph-fixes-lot2a-gates.status` e
cobertura em `/tmp/codegraph-fixes-lot2a-coverage.out`. O overlay de comparação
fica em `/tmp/codegraph-fixes-lot2a-original-overlay.json` e não alterou o checkout.

## Acompanhamento — lote 2b, inputs admitidos (R04/R05)

Contrato antes da implementação:

- [x] Ambiente Go efetivo (incluindo GOFLAGS/tags, GOOS/GOARCH, CGO, toolchain,
  workspace e configuração persistida) participa da identidade sem persistir
  valores potencialmente secretos. Mudança de tags altera CALLS esperadas;
  incremental e rebuild coincidem, sem no-op indevido.
- [x] Um plano tipado observa arquivos auxiliares Go admitidos e árvores locais
  de `node_modules`/`vendor`, inclusive abaixo de subprojetos. Bytes, membership
  e targets de links locais participam da identidade; links externos são rejeitados.
- [x] Snapshot usa somente esse plano: mudança de bytes/link antes do staging
  falha, novas entradas não observadas não entram, e cgo com C/header locais
  produz índice healthy com CALLS próprias esperadas.
- [x] Inputs Go ainda não certificados (dependências externas, workspaces,
  embed/cgo externos) desabilitam no-op/reuso de CALLS. Não prometer snapshot
  completo desses ambientes. `.env` não é lido por observação/staging.
- [x] Gates: gofmt, dependências, build, vet, testes, race e lint.

Este recorte não encerra R04/R05: recuperar certificação para inputs externos e
suportar demais inputs auxiliares requer um plano real de programa. O fallback
conservador pode custar rebuilds; não esconder o limite atrás de lockfiles.

Implementado em `resolver_inputs.go`, `resolver_snapshot_inputs.go`,
`resolver_input_validation.go` e `go_environment.go`, com handoff tipado e ambiente
explícito no bridge Go. Manifest v3, analysis-v2 e go-vta-resolver-v3 invalidam a
certificação anterior. `go env` usa uma visão privada/verificada de metadata;
`packages.Config.Env` congela os settings observados, desativa releitura de GOENV
e fixa a toolchain selecionada. GOGCCFLAGS contém um prefixo aleatório de debug
do go-build: excluí-lo da identidade evita invalidar toda observação, mantendo
os inputs semânticos que o geram. Valores efetivos não são persistidos.

Novas regressões passaram contra o código corrigido e os seis cenários originais
falharam antes da implementação (`/tmp/codegraph-fixes-lot2b-red.log`). Os oráculos
assertam CALLS default/alternate, igualdade incremental/rebuild, cgo healthy com
CALLS próprias, congelamento após mutação tardia do processo, mudança de GOENV
persistido, bytes/topologia/links de dependências, staging sem entradas novas,
rejeição de `.env` antes de leitura, e preservação do graph live após dependência
externa insegura ou overlay não admitido. Sem Node na suíte padrão.

Self-review de código/testes: sem achados bloqueantes após remover helpers mortos,
tratar cleanup e desabilitar certificação para driver/compilador Go externo.
Limites conhecidos não foram marcados como corrigidos: cobertura de inputs Go
externos/auxiliares, embed/workspaces e identidade de runtime/config externa TS
continuam abertos; CI remoto Linux/Go 1.26 e Windows não foram verificados.

Validação final local (macOS/arm64, Go 1.27.0, CGO=1): gofmt, diff-check,
`go mod verify`, tidy-diff, build, vet, suíte completa com coverage, race completo,
integrações SCIP reais com race, e lint padrão/integration passaram. Lint local
2.13.1, versus 2.12.2 no CI. Coverage de `internal/index`: 83,9%; cobertura não
substitui os oráculos positivos. Logs:
`/tmp/codegraph-fixes-lot2b-{tests,race,integration-race}.log` e profile
`/tmp/codegraph-fixes-lot2b-coverage.out`. Sem commit solicitado/realizado.

## Contrato — lote 2c, assets Go embed (R05)

- [x] Módulo Go com embed literal, glob, diretório, patterns quoted/múltiplos e
  `all:` produz índice healthy e CALLS próprias esperadas; assets aninhados
  são transportados como inputs, não novos nós de fontes.
- [x] Seleção segue patterns relativos ao pacote; recursão exclui hidden/underscore
  sem `all:`, preserva matches diretos hidden e respeita fronteiras de módulos/VCS.
  Assets não referenciados não são copiados nem lidos.
- [x] Bytes/membership de assets selecionados alteram a observação; alteração
  de bytes ou symlink antes de staging falha, preservando graph live. Arquivos
  novos posteriores à observação não entram no snapshot.
- [x] Leitura de diretórios usa descriptors no-follow. Symlinks selecionados e
  `.env` são rejeitados sem leitura de conteúdo; cancelamento não produz plano.
- [x] Fonte com string literal `//go:embed` não admite assets. Fonte com erro,
  pattern inválido/sem match continua sujeita ao diagnóstico do resolver; não
  inventar CALLS. A união conservadora também observa directives inativas.
- [x] Manter `go-embed-inputs-unobserved`: transportar assets locais não prova
  fechamento completo do programa. Workspace, inputs externos e runtime TS
  permanecem fora deste recorte. Sem performance/release claims.
- [x] Gates locais completos: default, race, integrações reais, vet/build/lint.

Implementação: `go_embed_{patterns,filesystem,inputs}.go`, plano v2 e bridge Go v4.
`securefile.ReadDir` retém a leitura no descriptor no-follow; Windows/targets sem
suporte retornam ErrUnsupported, sem ampliar a promessa de R01. O parser usa
Go parser/strconv.QuotedPrefix; não depende de ast.ParseDirective.
Fixtures geradas com assets reais assertam CALLS e seleção equivalente a `go list`
para os quatro layouts relevantes, além de raiz com metacharacters, fronteiras
module/VCS, mutação de bytes/membership, symlink tardio, glob com parent inseguro,
`.env`, cancelamento e preservação do graph live. Só os arquivos-fonte descobertos
são contados; assets não são adicionados como nós por esta implementação.

O teste negativo descobriu que `LoadAllSyntax` sozinho não força o driver a
resolver embed: missing.txt era healthy. Adicionar `NeedEmbedFiles` agora devolve
falha explícita ao primeiro índice e impede certificar o programa inválido. Os
oráculos healthy com CALLS não falhavam no código anterior; os de seleção do
snapshot e freshness falharam (`/tmp/codegraph-fixes-lot2c-red.log`). Não usar
healthy/digest vazio como prova de cobertura de assets.

Self-review: PASS após preservar erros de FS.Glob (que os ignora por padrão),
usar errors.Is para erros embrulhados e rejeitar matches inválidos em vez de
apagá-los do programa. Restrições: união conservadora de directives inativas,
no-op/reuso embed ainda bloqueado, programa externo/workspace/runtime TS não
certificado. R04/R05 seguem abertos.

Validação final local macOS/arm64, Go 1.27.0: default+coverage, race completo,
SCIP real+race, build, vet, mod verify/tidy-diff, lint padrão/integration e gofmt
passaram. A primeira suíte com coverage atingiu o timeout de 360s durante gates
paralelos; sem processos restantes, a reexecução isolada com timeout explícito
passou (internal/index 66,974s, coverage 84,3%). Logs
`/tmp/codegraph-fixes-lot2c-{tests,race,integration-race}.log`, profile
`/tmp/codegraph-fixes-lot2c-coverage.out`. Não afirmar causa do timeout nem
validação remota Linux/Go 1.26/Windows. Sem commit.

## Contrato — lote 2d, ambiente/runtime SCIP TypeScript (R04)

- [x] Observar ambiente de execução e identidades dos executáveis Node/npx sem
  iniciar Node nem ler configurações/segredos externos durante o scan. Persistir
  apenas digest tipado; alterações de PATH/NODE_OPTIONS/NODE_PATH/npm settings
  ou bytes de executável alteram a identidade mesmo sem editar fontes.
- [x] Injetar ambiente e launcher observados em todas as invocações SCIP;
  mutação tardia do ambiente não muda settings do subprocesso. Heap cap continua
  aplicado sem alterar a observação capturada.
- [x] Runtime externo/cache/config npm não certificados registram motivo e
  impedem no-op/CALLS reuse para índices com escopos TS: refresh saudável deve
  tentar novamente os resolvers. Não tratar versão fixada como prova dos bytes.
- [x] Falta de Node/npx continua falha explícita do resolver, não requisito para
  compilar/rodar a suíte Go padrão. Fixtures Go usam launchers/ambientes falsos.
- [x] Drift de runtime antes do handoff falha e preserva graph live; substituição
  detectada antes/depois da invocação não é certificada. Instalações de toolchain
  são confiadas durante execução, como Go; não prometer exec atomic por descriptor.
- [x] Gates default/race/SCIP real, build/vet/mod/lint/gofmt. R04/R05 continuam
  abertos para fechamento do programa e workspaces; sem claims de performance.

Implementação: `internal/scip/environment.go` (`ObserveExecutionEnvironment`,
`ExecutionEnvironment.Verify`, `Digest`, `command`), `internal/scip/run.go`
(`RunAndReadWithEnvironmentContext`, `nodeEnvFor`), `internal/index/ts_environment.go`
(`ts-env-v1`), `internal/index/manifest.go` (campo `TSEnvironment` + digest na
identidade) e injeção em `internal/index/{manifest,pipeline,calls}.go`. O bridge
TS passa a `scip-typescript-bridge-v2`, invalidando a certificação anterior. A
observação ordena `os.Environ()` e lê path + sha256 de Node/npx via
`securefile.OpenRead` sem lançar Node (um preload de `NODE_OPTIONS` não roda só
para se identificar); muda de PATH a bytes de executável alteram o digest sem
tocar fontes. Em escopos TS, `ts-runtime-inputs-unobserved` entra em
`no_reuse_reasons`, então nenhum índice TS/JS certifica no-op ou reuso de CALLS e
cada refresh explícito reexecuta os resolvers SCIP; a versão fixada do pacote não
é tratada como prova dos bytes. Ausência de Node/npx vira falha explícita de
resolver, não requisito da suíte Go padrão (fixtures usam launchers falsos).

Regressões adicionadas: `TestObserveExecutionEnvironment_TracksLauncherBytes`,
`TestExecutionEnvironment_VerifyDetectsExecutableSubstitution`,
`TestRunAndReadWithEnvironmentContext_RejectsUnavailableRuntime`,
`TestRunAndReadWithEnvironmentContext_InjectsCapturedRuntime` (prova launcher e
PATH capturados, mutação tardia e heap cap) em `internal/scip/environment_test.go`;
e `TestTSEnvironment_ProcessSettingsChangeObservation` (PATH/NODE_OPTIONS/
NODE_PATH/npm settings), `TestTSEnvironment_LauncherByteChangeInvalidatesIdentity`,
`TestTSEnvironment_IdentityDoesNotPersistRuntimeSettings`,
`TestTSEnvironment_MissingRuntimeFailsResolverExplicitly` e
`TestTSEnvironment_UncertifiedRuntimeNeverReusesHealthyCalls` em
`internal/index/ts_environment_test.go`. O drift antes do handoff reusa
`sameManifestFingerprint` (o mesmo predicado verificado pelo teste de bytes do
launcher): a observação de runtime entra na re-scan do handoff. A re-hash
depois da invocação é coberta pelo teste unitário de `Verify`; não há promessa de
exec atômico por descriptor nem teste de corrida entre substituição e exec.

Limites: R04/R05 seguem abertos — o fechamento do programa Go, workspaces/GOPATH
e o runtime/cache/config npm externo ao repositório continuam não certificados; a
identidade apenas elimina reuso indevido, não promete snapshot completo. Windows
permanece bloqueado por R01 (a leitura por `securefile` retorna `ErrUnsupported`,
como as demais operações). Validar `node`/`npx` antes e depois de cada escopo
re-hasheia os executáveis por escopo; não foram feitas alegações de desempenho.

Validação local: macOS/arm64 (M1), Go 1.27.0, Node 26.0.0, npx 11.12.1, SCIP
TypeScript 0.4.0, CGO habilitado, golangci-lint 2.13.1 (CI: Go 1.26 e lint
2.12.2). `gofmt` (dois arquivos escritos fora de formato no fim da sessão anterior
foram corrigidos), `go mod verify`, `go mod tidy -diff`, `go build`, `go vet`,
`go test ./...` e `go test -race ./...` passaram; a integração real com race
(`go test -race -tags integration ./internal/index -run '^TestTSInvalidation_RealResolver'`)
passou e o lint reportou 0 issues. Os dois testes comportamentais falhavam antes
da implementação (`/tmp/codegraph-fixes-lot2d-red.log`: settings não observados e
`reused=true` na segunda rodada); os testes novos dependem da API introduzida e
não compilam sem ela. Cobertura: `internal/index` 84,2%, `internal/scip` 76,8%
(cobertura não substitui os oráculos positivos). Logs:
`/tmp/codegraph-fixes-lot2d-{red,tests,race,integration-race,lint}.log` e
cobertura em `/tmp/codegraph-fixes-lot2d-coverage.out`. Self-review de
código/testes: sem achados bloqueantes. Windows/Linux nativos e o CI remoto
Linux/Go 1.26 não foram verificados. Sem commit solicitado/realizado.

## Contrato — lote 3, suporte operacional (R01/R07/R08/R14/R15)

- [x] R01: o asset Windows deixa de ser publicado e a indisponibilidade fica
  explícita (README/CONTRIBUTING). Nenhum fallback inseguro de chmod/path-based
  write é introduzido. A implementação nativa por handles/ACL continua entrega
  separada, com smoke test Windows exigido antes de readicionar o asset.
- [x] R07: receiver genérico vira nome nominal (`Box[T]` → `Box`) e instâncias
  SSA (incluindo wrappers) mapeiam para a origem declarada quando existe, sem
  ressuscitar sintéticos sem declaração. Chamadas para/de métodos e funções
  genéricos e chamadas dentro de método genérico geram CALLS esperadas.
- [x] R08: `go.work` enumera cada módulo `use` e carrega todos num único contexto
  (`./<dir>/...`), em vez do `./...` de raiz; chamadas dentro e entre módulos
  resolvem. O contrato de módulos aninhados sem config na raiz fica documentado.
- [x] R14: o installer aceita JSONC (comentários e trailing commas), valida que o
  documento e o campo `mcp` são objetos, propaga erros de leitura diferentes de
  ENOENT, faz merge não destrutivo e escrita atômica.
- [x] R15: o argumento de rota é tri-state (ausente/literal/unknown). Base ou
  caminho unknown omitem o Route; `@Get('')` literal continua sendo a raiz. Nunca
  inventa `GET /`.
- [x] Gates default/race/SCIP real, build/vet/mod/lint/gofmt.

Implementação:
- R01: `.github/workflows/release.yml` (matriz sem `windows-latest`), `README.md`,
  `CONTRIBUTING.md`. `internal/securefile` mantém `ErrUnsupported` no Windows.
- R07: `internal/index/treesitter.go` (`goReceiver` desembrulha `generic_type`) e
  `internal/gocalls/gocalls.go` (`funcToQN` mapeia sintético→`Origin()` antes do
  teste de `Pkg`). Fixture `internal/gocalls/testdata/generics` cobre método
  genérico, função genérica e chamada dentro de corpo genérico.
- R08: `internal/gocalls/gocalls.go` (`loadPatterns` a partir de
  `modfile.ParseWork`; entradas `../`/absolutas fora do snapshot são ignoradas) e
  fixture `internal/gocalls/testdata/workspace` (`a` e `b`, chamada cross-module).
- R14: `internal/install/install.go` (`parseOpencodeConfig`, `stripJSONComments`,
  `stripJSONTrailingCommas`, `writeFileAtomic`, propagação de erro de leitura).
- R15: `internal/index/routes.go` (`routeArg` tri-state, `decoratorPath`).

Identidade: `analysis-v3` e `go-vta-resolver-v5` invalidam a certificação anterior
(mudança de QN de receiver genérico e de conjunto de nós Route/CALLS).

Regressões: `TestGoGenericMethodQualifiedName` (QN nominal e prop `receiver`);
`TestCallEdges_GenericsDoNotCrash` estendido (arestas genéricas esperadas);
`TestCallEdges_GoWorkspaceLoadsEveryModule` (patterns por módulo + aresta
cross-module); `TestMergeOpencodeConfig_AcceptsJSONC`,
`TestMergeOpencodeConfig_RejectsNonObjectDocuments`,
`TestInstallOpencode_PropagatesReadErrors`, `TestInstallOpencode_MergesIntoExistingJSONC`
e `TestWriteFileAtomic_ReplacesWithoutTempLeftovers`;
`TestRoutes_NestJS_DynamicPathsAreOmitted`. Os cinco contraexemplos falham contra
o código anterior via overlay (`/tmp/codegraph-fixes-lot3-red.log`): arestas
genéricas ausentes, QN `Box[T].Get`, `loadPatterns=[./...]`, JSONC rejeitado e
rotas `GET /`, `GET /literal`, `GET /users` inventadas.

Limites: R01 continua aberto como *runtime* Windows — apenas a publicação foi
bloqueada; não foi executado binário em Windows. R08 não cobre módulos aninhados
sem `go.work`/`go.mod` na raiz (contrato documentado: usar `go.work`); não há
resolução multi-módulo fora do workspace. R07 não promete cobertura de métodos
genéricos nunca instanciados. Sem claims de performance.

Validação local: macOS/arm64 (M1), Go 1.27.0, Node 26.0.0, SCIP TypeScript 0.4.0,
CGO habilitado, golangci-lint 2.13.1 (CI: Go 1.26 e lint 2.12.2). `gofmt`,
`go mod verify`, `go mod tidy -diff`, build, vet, suíte completa, race, integração
SCIP real com race e lint passaram. Cobertura: `internal/index` 84,3%,
`internal/gocalls` 83,4%, `internal/install` 70,1% (cobertura não substitui os
oráculos positivos). Logs:
`/tmp/codegraph-fixes-lot3-{red,tests,race,integration-race,lint,coverage}.log`.
Self-review
de código/testes: sem achados bloqueantes. Windows/Linux nativos e o CI remoto
Linux/Go 1.26 não foram verificados. Sem commit solicitado/realizado.

## Contrato — lote 4, consulta/avaliação/transporte (R10/R11/R13/R16/R18/R19)

- [x] R10: o cursor de snippet carrega o sha256 do arquivo inteiro que a página
  leu; edição same-size, mudança de quebras e rename-replacement falham a página
  seguinte em vez de deslocar linhas. Tamanho/inode/mtime não são prova.
- [x] R11: `status` e a cobertura de similaridade vêm do manifest capturado com o
  engine no reopen, não de leitura de disco posterior; um writer entre reopen e
  publish aparece como `lag`, e status nunca discorda do trailer da página.
- [x] R13: `eval/graph-answers.js` percorre o trailer até `has_more=false`, cobra
  chamadas/bytes reais e usa `CODEGRAPH_EXE` (sem `codegraph.exe` fixo); os hints
  do workflow ensinam a continuação por cursor.
- [x] R16: `Architecture` clampa o top-N (`MaxArchitectureTopN`) e o snippet tem
  teto absoluto de 1 MiB por linha com erro orientado a range/leitura direta,
  aplicado também ao skip e ao lookahead.
- [x] R18: `detect_changes` adiciona linhas `config<TAB>path` para inputs laterais
  registrados (tsconfig/go.mod/go.work/ignore), distinguindo ausência de mudança de
  fonte de índice fresco; identidade de ambiente fica explicitamente fora.
- [x] R19: erro de parse vira -32700 (id null) e o stream continua; notificação não
  recebe resposta; campos required por tool viram -32602; falha de escrita encerra
  `Serve`; `initialize` negocia a versão do protocolo.
- [x] Gates default/race/SCIP real, build/vet/mod/lint/gofmt.

Implementação: `internal/graph/snippet_page.go` (digest do arquivo em passe único
com tee, teto de linha, leitura limitada inclusive em skip/lookahead);
`internal/query/{budget,page,query,architecture}.go` (cursor v2 com digest, clamp,
config no detect_changes); `cmd/codegraph/session.go` +
`internal/query/query.go` (`Engine.Manifest`, captura no reopen, publish da
identidade servida); `internal/mcp/server.go` (transporte estrito, validação por
tool, negociação); `eval/{graph-answers,quality-workflow}.js` e `eval/README.md`.
Sem mudança de identidade de análise: nenhum rebuild forçado.

Regressões: `TestSnippetPaged_DetectsSameSizeEdit`,
`TestSnippetPaged_DetectsRenameReplacement`,
`TestSnippetPaged_RejectsOversizeLine`, `TestEngine_ArchitectureClampsTopN`,
`TestSession_PublishReportsServedGeneration`,
`TestEngine_DetectChangesReportsConfigOnlyEdit`,
`TestServer_ParseErrorIsAnswered`, `TestServer_NotificationGetsNoReply`,
`TestServer_RequiresToolArguments`, `TestServer_WriteFailureStopsTheLoop`,
`TestServer_NegotiatesProtocolVersion`. Contra o código anterior via overlay, os
contraexemplos falham: cursor só por tamanho aceita edição same-size; sem teto de
linha não há erro; `hotspots=250` sem clamp; página e status divergem de geração;
config-only não aparece; parse/notificação/required/escrita/negociação não são
tratados (`/tmp/codegraph-fixes-lot4-red.log`).

R13 foi verificado ponta-a-ponta: hub com 720 callers, 4 páginas (500 + cortes por
bytes + cauda), 720 refs únicas, calls=4 — `/tmp/codegraph-fixes-lot4-eval.log`.
Limites: o scorer da avaliação segue baseado em nome (homônimos de arquivos
diferentes colapsam); um scorer estrito por QN fica aberto. Apenas a superfície
tools/stdio do MCP é exercitada. Sem claims de performance.

Validação local: macOS/arm64 (M1), Go 1.27.0, Node 26.0.0, SCIP TypeScript 0.4.0,
CGO habilitado, golangci-lint 2.13.1 (CI: Go 1.26 e lint 2.12.2). `gofmt`,
`go mod verify`, `go mod tidy -diff`, build, vet, suíte completa, race, integração
SCIP real com race e lint passaram. Logs:
`/tmp/codegraph-fixes-lot4-{red,eval,tests,race,integration-race,lint}.log`.
Self-review de código/testes: sem achados bloqueantes. Windows/Linux nativos e o
CI remoto Linux/Go 1.26 não foram verificados. Sem commit solicitado/realizado.

## Contrato — lote 5, performance medida (P1/P4/P6/P7)

- [x] P1: remover o `runtime.GC` redundante do `memory.Gate` (FreeOSMemory já
  coleta) e gatear a passada de assinaturas de similaridade por lote (64 arquivos),
  não por arquivo. Mediana de 5×20 iterações num microbenchmark com lixo:
  ~604 µs → ~453 µs por gate (~25% nesta execução, ~19% em outra; a fixture Ruby
  da revisão mediu 16%).
- [x] P4: reter durante o scan apenas os `MaxEdges` menores `(source,target)` num
  max-heap limitado — exatamente o prefixo ordenado que o collect+sort+truncate
  antigo produzia, então saída e cobertura não mudam. Clone bomb: 140,5 MB → 76,5 MB
  alocados (−45%), 146,7 ms → 124,5 ms.
- [x] P6: `Architecture` cacheada por geração servida + top-N clampado, invalidada
  no reopen (epoch monotônico). Store de 3000 funções: 5,82 ms → 17 ns por chamada
  repetida.
- [x] P7 (limites): memória efetiva usa o limite do cgroup leaf quando menor que a
  RAM do host; `GOMEMLIMIT` explícito do operador não é sobrescrito; stdout/stderr do
  SCIP vira tail buffer limitado (64 KiB); peak RSS soma a árvore de processos, não
  só o shim do npx.
- [x] Gates default/race/SCIP real, build/vet/mod/lint/gofmt.
- [ ] Em aberto: P2 (keyset pagination / offsets de byte no snippet), P3 (projeção
  compacta sem decodificar properties), P5 (reuso de canonicalização/staging) e a
  observabilidade restante do P7 (duração por fase, bytes de staging, motivo de
  invalidação, escopos reusados). Cada um exige benchmark e contrato próprios; sem
  claim de ganho para eles.

Implementação: `internal/memory/{memory,memory_linux}.go` (gate, cgroup,
GOMEMLIMIT), `internal/index/similar.go` (gate por lote),
`internal/similar/{similar,retention}.go` (retenção limitada),
`internal/query/{query,architecture}.go` (cache), `internal/scip/{run,rss_linux}.go`
(tail buffer, RSS da árvore). Sem mudança de identidade de análise (a saída da
similaridade é idêntica), portanto nenhum rebuild forçado.

Regressões/benchmarks: `TestTopEdges_MatchesSortAndTruncate`,
`TestTopEdges_ExactFitIsNotOverflow`, `TestOperatorPinnedMemoryLimit`,
`TestTailBuffer_KeepsOnlyTheTail`, `TestTailBuffer_KeepsLastPartialWrites`,
`TestEngine_ArchitectureCacheAndReopenInvalidation`,
`BenchmarkGateWithGarbage`, `BenchmarkBudgetedPass_CloneBomb`,
`BenchmarkEngine_ArchitectureCache|Compute`. Números antes/depois via overlay em
`/tmp/codegraph-fixes-lot5-bench.log`.

Limites: o benchmark de gate é um microbenchmark dominado por lixo (a primeira
leitura isolada deu 45%, medianas estáveis 19–25% — reportada a faixa);
P2/P3/P5 e a observabilidade restante do P7 continuam abertos; P4 não limita o mapa
`seen` nem os buckets (custos separados). Sem claims além do medido. Windows/Linux
nativos e o CI remoto Linux/Go 1.26 não foram verificados. Sem commit.

## Contrato — lote 6, performance de consulta (P2/P3)

- [x] P3: as consultas compactas (search, callers, callees, neighbors, similar)
  selecionam apenas as colunas de ref (`graph.RefNode`) e nunca leem nem decodificam
  a propriedades JSON. Mesmas refs, ordem, paginação e erros.
- [x] P2 (neighbors): as páginas de vizinhos continuam por **keyset** no último
  `qualified_name` servido (cursor V=2), não por `OFFSET`; página profunda custa o
  mesmo que a primeira. Cursor V=1 (offset) num tool de vizinhos é rejeitado com
  orientação de reiniciar.
- [x] P2 (snippet): a página profunda avança até a linha de retomada em blocos, sem
  materializar uma string por linha pulada, mantendo o hash (digest do arquivo) e o
  teto de linha nessa fase também.
- [x] Gates default/race/SCIP real, build/vet/mod/lint/gofmt.

Medições (`/tmp/codegraph-fixes-lot6-bench.log`):
- P3: hub de 500 callers 1,77 ms → 0,79 ms (−55%), 657 KB → 204 KB (−69%);
  search 200 hits 1,44 ms → 1,03 ms (−28%), 277 KB → 90 KB (−67%).
- P2b: hub de 5000, página 500 no offset 4500: 4,39 ms → 2,06 ms (−53%), agora
  igual à primeira página (2,33 ms).
- P2a: página a partir da linha 4001: 301 µs → 202 µs (−33%), 221 KB → 93 KB
  (−58%), allocs 8.506 → 506 (−94%).

`EXPLAIN QUERY PLAN` (offset e keyset) mostra o mesmo plano de acesso:
`SEARCH e USING INDEX idx_edges_target_type (target_id=? AND type=?)` +
`SEARCH n USING INTEGER PRIMARY KEY` + `USE TEMP B-TREE FOR ORDER BY`. Ou seja, o
índice existente já é usado e **nenhum índice composto foi acrescentado**; o ganho
vem de o filtro keyset reduzir o conjunto antes do sort. O `USE TEMP B-TREE` é
inerente ao `ORDER BY n.qualified_name` sobre o join e permanece nos dois casos.

Implementação: `internal/graph/refs.go` (`RefNode`, projeção compacta,
`searchSelect`/`neighborSelect` compartilhados, `SearchRefs`, `NeighborRefs`,
`NeighborRefsAfter`), `internal/graph/store.go` (usa os builders),
`internal/graph/snippet_page.go` (`skipSnippetLines`), `internal/query/{page,budget,query}.go`
(cursor V=2 keyset, `refBudget`, rejeição de V incompatível). Sem mudança de
identidade de análise (o grafo e as respostas não mudam), então nenhum rebuild.

Regressões: `TestRefs_MatchFullNodeQueries` (projeção == nós completos),
`TestNeighborRefsAfter_MatchesOffsetPaging` (enumerar por keyset == enumerar por
offset, nas três direções, com o dedup do UNION), `TestNeighborRefs_RejectsUnknownDirection`,
`TestSnippetPaged_RejectsOversizeLine` (caso `skipped-before-range`), além dos
testes de paginação/limite existentes que passam sem alteração.
Benchmarks: `BenchmarkNeighborRefs_{Compact,FullNode}`, `BenchmarkSearchRefs_{Compact,FullNode}`,
`BenchmarkNeighborRefs_{FirstPage,DeepPage}`, `BenchmarkNeighborRefsAfter_DeepPage`,
`BenchmarkSnippetPaged_{FirstPage,DeepPage}`.

Limites: o keyset foi aplicado aos vizinhos; **search** fica no cursor de offset
porque a ordem é por `fts.rank` (tupla (rank,id) é frágil) — documentado como
aberto. `dead_code` já usava cursor de candidato bruto. P5 e a observabilidade do
P7 continuam abertos. Benchmarks sintéticos, sem claim de ganho universal.
Windows/Linux nativos e o CI remoto Linux/Go 1.26 não foram verificados.

## Contrato e evidência — lote 7, root por execução e observabilidade (P5/P7)

Contrato definido antes do código em `docs/VALIDATION_P5_P7.md`.

- [x] P5 (root/imports): `repositoryRoot` transporta o spelling físico validado
  pela preparação, reobservações, expansão de configs e handoff. Leitura de
  configs e applicability deixam de recanonicalizar esse mesmo root. Raízes
  públicas/standalone ainda são validadas; o root privado do resolver permanece
  ancorado nos descriptors retidos, nunca recanonicalizado por path.
- [x] IMPORTS mantém todos os arquivos no lookup de targets, mas abre apenas
  fontes TS/JS/Ruby, não Go (sem modelo de IMPORTS suportado). A regressão de
  leitura insegura foi mantida para TS; definições Go continuam verificadas.
- [x] Nenhuma reobservação, leitura no-follow, hash de snapshot, verificação de
  links, validação exata ou política conservadora de invalidação foi removida.
- [x] P7: `Result.Metrics` registra duração monotônica por fase (sequencial,
  sem dupla contagem, nomes repetidos agregados), bytes/arquivos escritos no
  staging, decisão/reasons de rebuild/no-op e escopos reusados. Saída em CLI
  index/bench e MCP status; não entra no manifest/fingerprint/digest.
- [x] Métricas sobrevivem a erros/cancelamento e são separadas da identidade
  servida no MCP. Contagem de invocações e amostras de RSS do SCIP sobrevivem
  ao cancelamento; `ScipScopes` mantém a semântica anterior de escopos com
  sucesso. RSS é da árvore do resolver, não peak do indexador Go.
- [x] Report de escopos quoted e limitado a 20 linhas / 240 bytes por nome,
  com omissão/truncamento explícitos; não publica valores brutos de ambiente.

### Medições

macOS/arm64, Apple M1, Go local 1.27; duas repetições de 10 iterações, sem as
suítes concorrendo. Baseline: sources do commit `993bc04` via overlay, mesmos
benchmarks. Logs: `/tmp/cg-lot7/bench-{before,after}-isolated.log`.

| Fixture | Antes | Depois |
| --- | --- | --- |
| Scan, 40 jsconfigs referenciando base compartilhada | 622 ms / 139 MB / 1,15 M allocs | 26,5 ms / 3,58 MB / 30.689 allocs |
| RunAtomic no-op estrito, mesma config graph + fonte Go sem módulo | 1,36 s / 302 MB / 2,50 M allocs | 71 ms / 10,6 MB / 89.994 allocs |
| IMPORTS, 200 fontes Go | 14,9 ms / 250 KB / 3.203 allocs | 3,7 µs / 6,6 KB / 3 allocs |

O no-op estrito mantém digest e validador exato: o ganho não vem de relaxar sua
certificação. Microbenchmarks sintéticos; custo de spelling depende também do
número de entries dos ancestors desta máquina. Sem claim de ganho universal nem
medição nova de peak RSS do processo Go.

### Regressões e gates

`repository_root_test.go`: alias mantém identidade e root substituído por symlink
externo continua falhando com ErrUnsafePath, mesmo com o valor já validado.
`performance_test.go`: imports Go não lê path ausente; benchmarks scan/imports/no-op.
`metrics_test.go`: no-op, alteração com reuso Ruby, metadados Go contados no no-op,
staging exato TS/dependencies, falha de refresh preservando digest, cancelamento,
raiz inválida, retenção de RSS/invocações canceladas, sum de fases sem overlap,
escopos limitados/escaped, ambiente secreto ausente da saída.
`cmd/codegraph/metrics_test.go`: saída real de index/no-op, renderer de bench e
status MCP após cancelamento. Suítes existentes de freshness/integridade,
graph-digest, inputs, segurança e cancelamento continuam passando.

Gates locais: gofmt, mod verify/tidy-diff, build, vet, full tests, full race,
SCIP real com race e lint (0 issues). Logs em `/tmp/cg-lot7/*-final.log`.
Self-review de código/testes: PASS após corrigir argumento booleano ambíguo,
limitar o report e reter métricas em cancelamento. CI Linux remoto é gate de merge
verificado nos checks do PR; não é afirmado por estes resultados locais.

### Limites deliberados

P5 foi avançado no root/imports; **reuso de bytes/staging mais profundo permanece
aberto**. Plano observado continua sendo a única fonte de inputs do snapshot;
sem cache global, snapshot cross-run, bypass de hashes ou novo rebuild forçado.
Staged bytes são payloads de escritas bem-sucedidas, incluindo os views privados
de Go env (dois no no-op, três na construção estável antes do snapshot), não
ocupação de disco nem bytes SQLite/SCIP. Diretórios/links/escritas falhadas não
contam. Gates dentro de batches/resolvers fazem parte dessas fases; gates entre
fases têm timing agregado separado. `manifest-untrusted` agrupa causas de trust
miss (não finge provar uma edição de config); reasons de inputs não certificados
continuam explícitos. P2 de search e os gaps R01/R04/R05/R13 continuam abertos
neste lote (R13 é avançado no follow-up abaixo).

## Contrato/evidência — R13, identidade estrita e avaliação completa

Contrato pré-implementação: `docs/VALIDATION_R13.md`. O padrão agora é
`qualified-name-v1`: QNs repository-relative/project-stripped, comparação exata,
case-sensitive, sem colapsar arquivo/owner, Ruby instance/singleton ou namespaces.
Sufixos são opacos (TS permite métodos quoted/computed com espaços/slashes).
Definition usa caminho relativo completo e linha exata, com uma resposta única.
`name-v1` só por opção explícita, mantendo nomes normalizados e basename/±3 linhas
históricos. Nenhum número histórico foi convertido ou nova rodada LLM executada.

Admission valida o conjunto não vazio de questões, IDs/tipos, uma truth por ID e
uma resposta por questão×modo declarado (padrão graph+baseline; graph-only
explícito). Duplicatas/IDs ou modos desconhecidos, rows ausentes, items null,
rubrica/texto/judge ausentes ou fora do intervalo, custos negativos/overflow
falham antes de médias e publicação. `[]` explícito continua sendo conjunto de
calls conhecido vazio. Report marca scorer/modos, escapa metadados e não substitui
report anterior em falha; scaffolds são deliberadamente não preenchidos.

O produtor determinístico lê a coluna TSV 4, guarda homônimos, cobra invocações e
bytes UTF-8 e exige trailer terminal, cursor com progresso e geração estável.
Cap atingido/trailer inválido/erro do executável não publica resposta parcial.
Aceita somente experimento call-only explícito, sem subset implícito; substitui
answers por rename privado e não segue symlink de destino. Workflow independente
oracle/baseline deriva QNs do source, preserva limit/query na continuação e
seleciona definição por QN, não primeiro homônimo. Falhas de agentes/judge/pipeline
não viram []/zero nem são descartadas silenciosamente.

Regressões red: homônimos recebiam 100% e ausência de baseline ainda gerava
report (`/tmp/cg-r13/red.log`). Testes Go cobrem identidades, sets/empty, definições,
matriz/erro/contexto/custos/judges, determinismo, geração e CLI/report privado.
16 testes Node cobrem paginação, cap/ciclo/geração/trailers, bytes/custos, argv real,
preservação/publicação de artefato e falhas do workflow, no job Node fixado.
Fixture Go real independente do checkout servido: 720 tipos `OwnerN`, todos com
método `Run` chamando `Target`; truth foi gerada do source antes da indexação.
CLI→Node→CLI preservou **720 QNs em 2 páginas**, F1 estrito **100%**, 8.005 tokens
estimados de output. É evidência sintética de contrato, não ganho de qualidade
em repositório real (`/tmp/cg-r13/e2e-*.log`).

Gates locais: gofmt, módulos verify/tidy-diff, build, vet, full/default/coverage,
full race, race final dos pacotes alterados, SCIP real com race e lint (0 issues).
`internal/quality` teve 96,9% de statement coverage. Logs `/tmp/cg-r13/`.
Self-review código/testes: PASS após preservar CODEGRAPH_EXE, checar argv tool-first,
proteger artefatos/row matrix e preservar símbolos TS opacos. CI remoto é gate de
merge, não inferido a partir dos testes locais.

Limites: validação não prova veracidade do oracle/modelo, source freshness,
escrita do agente ou custo autodeclarado; CLI checa arquivos reais, não ack do
workflow. Não muda resolvers, manifest/fingerprint, graph schema ou analysis
identity. R01/R04/R05, benchmarks representativos, search keyset e reuso mais
profundo de staging seguem separados.

## Acompanhamento — matriz de benchmark reproduzível

Contrato prévio: VALIDATION_BENCH_MATRIX.md. `bench-sample` mede o caminho real
`RunAtomicContext` em processo novo; Node admite clones descartáveis exclusivos,
SHA/HEAD fixados e edições explícitas, prepara cinco cenários independentes,
restaura fontes/configs e publica um relatório privado apenas após a matriz
completa. Sem alteração de resolver, regra de reuso ou identidade de análise.

Piloto final Cobra v1.9.1 + Zustand v5.0.1, n=3/cenário: 30 rebuilds saudáveis,
30 equivalências com rebuilds novos e CALLS do probe independente em cada edição
de fonte. Rebuild sem edição é resultado observado, não um no-op forçado:
inputs Go externos/cgo e runtime TS ainda não certificados. Travessias nativas:
Cobra 132 callers/19 páginas, 18 callees/3, 310 search/45; Zustand 4/4, 4/2, 2/2.
Minima explícitos impedem QN errado de fabricar uma resposta vazia barata.

BENCH_MATRIX.md contém reprodução, tabela p50/p95 e raw artifact versionado.
Zustand grava ~142 MB/14.576 payloads por execução; observação/handoff/staging
são custos visíveis. Métricas são separadas: heap Go, RSS self real do worker e
RSS da árvore SCIP amostrado (indisponível em macOS, zero não significa zero).
Variância e n=3 não autorizam significância, ganho universal ou comparação com
outro binário. Medium/large/monorepo e coleta Linux continuam abertos.

Gates locais: módulos/gofmt/build/vet/default/full race/coverage, SCIP real,
lint e Node; após ajustes finais, default/race dos pacotes alterados, lint,
Node e cobertura foram repetidos. Lint inicialmente identificou permissão 0644
num fixture; corrigida para 0600 antes do gate final. Self-review corrigiu
restauração armada após ack, perda de backup em falha de cleanup, UTF-8 dividido
em chunks e queries nativas vazias. Código/testes: PASS; limites de concorrência
(shared tree não é admitida) e SIGKILL ficam explícitos. CI remoto continua gate
de merge; merge exige autorização. R04/R05, P2 search e P5 profundo permanecem
abertos, agora com evidência de produção para priorização.

## Acompanhamento — admissão de inputs externos Go (R04/R05)

Contrato prévio: VALIDATION_GO_INPUT_ADMISSION.md. O recorte corrige admissão e
certificação indevida, não habilita reuso de dependências externas.

Regressões vermelhas: `strings.Fields` ignorava quotes envolvendo a flag e nomes
com `--`; overlay quoted chegou ao Go resolver e gerou erro de leitura de input
não admitido. Um wrapper `-toolexec` no mesmo caminho, com bytes alterados, ainda
podia terminar em no-op saudável. Também não havia cobertura para GOCACHEPROG,
flags desconhecidas, pkgdir/PGO e argumentos internos do compilador/linker.

Implementação: gramática de fields igual à do Go (não shell), controles process
checados antes da observação e probe nomeado GOFLAGS/GOCACHEPROG para defaults
persistidos antes de `go env -json` custoso. Falha no probe é erro de admissão
sem valores sensíveis; tool ausente mantém fallback unavailable. O probe não inicializa cache
externo; a observação completa e o resolver mantêm execução configurada pelo
operador, sem sandbox. Settings do probe sobrevivem à falha da observação completa.

Overlay/modfile/`-C` falham em todas as grafias. Allowlist finita e validada de
flags sem novos inputs, e `go-build-flags-inputs-unobserved` para flags restantes;
`go-external-cache-inputs-unobserved` para cache externo; gccgo mantém motivo
específico. O plano `resolver-inputs-v4` força rebuild de certificados anteriores,
sem mudar schema ou algoritmo de CALLS. Digests são opacos; diagnósticos de
admissão não incluem valores, comandos, stderr ou credentials.

Fixture real Go + wrapper delegante: edição só do wrapper, refresh intacto e
edição só Ruby exigem novas resoluções Go; CALLS esperadas e equivalência com
rebuild novo. Preservação de grafo/manifest em rejeição, defaults persistidos,
valores/quotes/fronteiras, JSON inválido/null, cancelamento/tool ausente,
redação de segredos e migração v3→v4 são testados. Tags suportadas mantêm bindings
e no-op validado. Gates locais finais PASS: módulos, formatting/build/vet,
full default/race/coverage, integração SCIP real, lint e 26 testes Node. Funções
novas classifier/probe com 100% statement coverage, sem inferir closure completa.
CLI isolado confirma CALLS e quatro rebuilds equivalentes (seed, wrapper alterado,
refresh intacto, referência nova); rejeição conserva sidecar sem valor sensível
no erro. Evidência local em `/tmp/cg-go-admission/`.

Self-review: corrigidos falha de `go env` que escondia defaults de cache, descarte
de controles efetivos, JSON null e diagnósticos; lint exige justificativas locais
para launchers próprios executáveis (0700) e comandos com argv fixo. Código/testes
PASS após os ajustes. O probe acrescenta um subprocesso Go por observação — custo
de segurança explícito, sem claim de performance. R04/R05 continuam abertos para
program-input closure e transporte verificado externo; P2 search/P5 profundo,
Windows e a matriz Linux/grande continuam separados. CI remoto é gate de merge.

## Priorização vigente — uso pessoal no macOS (2026-10-09)

Decisão confirmada pelo usuário: `PERSONAL_MACOS_SCOPE.md` governa os próximos
milestones. Windows e benchmarks Linux/Windows ficam fora do escopo atual; manter
CI Linux e invariantes existentes não implica ampliar cobertura de produto.
R01 continua mitigado pela ausência de asset/promessa de suporte, não implementado.
R04/R05 mantêm limitações honestas e rebuild/no-reuse conservador; closure completa
não é requisito para uso pessoal nem autorização para remover gates.

Validação do fluxo no Mac foi executada com fixtures isoladas: Go real, refresh e
coordenação de sessão, cursores/snippets, recuperação e quatro oráculos SCIP reais.
Default/race focado/build/vet/modules/format/lint e 26 testes Node PASS; detalhes em
`VALIDATION_MACOS_WORKFLOW.md`. Não é benchmark dos repositórios do usuário.
Escolher o workload com o usuário antes de medir; search keyset, staging profundo,
matrizes grandes e RSS dos filhos no Mac dependem de necessidade demonstrada.
Prioridade alterada não fecha findings nem muda algoritmo/runtime/schema/CI.

## Acompanhamento — inputs de dependência Go (R04/R05)

Recorte bounded motivado por medição (2026-10-09; workload escolhido pelo dono:
este repositório, clone descartável). O refresh sem mudanças era rebuild integral
(4,6–5,1 s; 1,4–1,7 GiB) porque `go.mod` tem `require` e os bytes das dependências
não eram observados (`go-dependency-inputs-unobserved`); dois dos três motivos
eram falsos positivos do heurístico `bytes.Contains` (`"C"` literal em
`go_flags.go:86`, `//go:embed` em fixtures de teste).

Implementação: `go list -deps -compiled -test` (offline, `GOPROXY=off`; `-mod`
preservado, `-mod=readonly` quando `-mod=mod` efetivo) enumera os pacotes do
build; os arquivos de dependência consumidos (`CompiledGoFiles` mais cgo
`CgoFiles/CFiles/CXXFiles/HFiles/SFiles`), fora do repositório e do GOROOT, entram
num digest canônico (`go-dependency-inputs-v1`, plano `resolver-inputs-v5`).
Falha de enumeração/leitura mantém o motivo (fail-closed); repositórios com
vendor pulam o probe de módulos (`go list -m all` não computa `all` contra
vendor) e certificam pelos bytes do vendor já observados. Diretivas reais
(`import "C"`, `//go:embed`) passam a ser detectadas por parser; literais não
bloqueiam mais.

Regressões vermelhas: no-op em repositório inalterado com dependência via
`replace`; edição/adição/remoção de arquivo de dependência e diretório ausente →
rebuild/fail-closed; arquivo ilegível → fail-closed; vendor editado → rebuild;
literais vs. diretivas reais; parsing do output e derivação de `-mod`. Gates
locais: formatting/build/vet, default completo, race dos pacotes afetados, lint;
CI remoto é o gate de merge.

Medição antes/depois (macOS/M1, n=3): no-op certificado 1,09–1,12 s e ~48 MB RSS
(antes: rebuild 4,6–5,1 s e 1,4–1,7 GiB); rebuild com edição 5,97 s vs 4,80 s
(+24%: enumeração 2× — scan inicial + handoff re-scan — e certificação exata do
`freshManifestFor` agora alcançada, antes curto-circuitada pelo motivo). Limites:
embed real, workspace/GOPATH, GOCACHEPROG, identidade do compilador C e closure
externa completa seguem conservadores. Ver VALIDATION_GO_DEPENDENCY_INPUTS.md.

## Acompanhamento — certificação de embed local (R04/R05)

Recorte bounded a partir da medição no workload do dono (`AutoTradersOMQS-GO`,
1029 arquivos Go, 2 arquivos com `//go:embed` real): o refresh sem mudanças era
sempre rebuild (19,3–19,8 s; 2,6–2,9 GiB) com `go-embed-inputs-unobserved`,
embora as deps já estivessem certificadas (v5). O transporte local do 2c já
observava os assets selecionados (patterns do parser, oráculo contra
`go list -json`, leituras no-follow, rejeição de symlink/.env, mutação de
bytes/membership, falha de staging tardio); o motivo era mantido por política
("não prova fechamento completo do programa").

Implementação: o motivo passa a ser registrado apenas quando uma diretiva
detectada não é tratada pelo transporte (erro de parse, falta do import
`embed`, pattern inválido); diretivas tratadas têm seus assets hasheados no
plano e re-derivados a cada scan, então qualquer mudança de bytes/membership/
pattern invalida o fingerprint. Nenhum outro motivo muda (workspace/cgo/TS
runtime bloqueiam por conta própria). Descoberta durante a validação: com o
motivo de embed destravado, a observação de deps rodou pela primeira vez neste
repo e o probe `go list -m all` (grafo completo, offline) falhou; o probe virou
oportunista — falha cai na enumeração autoritativa por pacote, que continua
fail-closed.

Regressões: no-op certificado em fixture com diretiva real; edição/adição/
remoção de asset → rebuild; diretiva sem import, pattern inválido e fonte com
erro de parse mantêm o motivo; oráculos de seleção seguem verdes; fixture com
`require` não importado certifica via fallback. Medição antes/depois no clone:
no-op certificado 7,04–7,13 s e 67,8–69,1 MB (antes: rebuild 19,3–19,8 s e
2,6–2,9 GiB), grafo idêntico (files=1029 nodes=11188 edges=34896, 0 dropped).
Limites: certifica o transporte local de embed, não o fechamento do programa;
união de diretivas inativas segue conservadora; sem claim de performance além
do no-op. Ver VALIDATION_GO_EMBED_CERTIFICATION.md.
