# Plano de eficiencia para execucao por outro agente

Status: planejado; nenhuma mudanca de runtime implementada por este documento.
Data: 2026-09-07.
Origem: revisao geral de eficiencia de indexacao, consultas, storage e uso MCP.

## Escopo vigente (2026-10-09)

Este plano histórico deve ser priorizado sob `PERSONAL_MACOS_SCOPE.md`: uso pessoal
exclusivo no macOS. Exigências Windows/Unix e expansão genérica de corpus/plataforma
abaixo não são gates dos próximos milestones. Preserve o CI Linux e os invariantes;
valide no Mac e só escolha otimizações após medir necessidade no workload do usuário.
Resultados históricos e itens pendentes não são evidência de benefício atual.

## Objetivo e limites

Reduzir o custo de uma tarefa correta do agente, nao apenas o tempo de uma query:
preparacao/atualizacao do grafo + espera + tokens + verificacoes por fallback.
Priorizar sessoes que consultam, editam e consultam novamente, inclusive com dois
clientes no mesmo repositorio. Cobrir Go, TS/JS e a superficie Ruby/Rails existente.

Este plano nao substitui `RUBY_ROADMAP.md` nem autoriza implementar tudo de uma vez.
Executar uma unidade por vez, registrar resultados e preservar trabalho concorrente.
Nao fazer commit, push, instalar binarios ou alterar configuracao de agentes sem pedido.

## Invariantes obrigatorios

- Preservar precisao e recall existentes; nunca apresentar dados parciais como completos.
- Preservar isolamento dos snapshots, validacao exata de integridade, recuperacao e
  publicacao coerente do par banco/manifest. Nao retirar gates para acelerar testes.
- Nao trocar hashing por confianca exclusiva em mtime/size; ambos podem ser apenas hints.
- Nao usar hardlinks mutaveis como substitutos de snapshots privados.
- Nao trocar `UNION` por `UNION ALL` indiscriminadamente: vizinhos nas duas direcoes,
  self-edges e tipos diferentes podem representar o mesmo no.
- Nao reduzir silenciosamente limites de callers; oferecer continuacao verificavel.
- Nao podar VTA ou invalidacao TS por imports aproximados sem provar equivalencia.
- Preservar formato compacto das referencias, confinamento de arquivos e protecao
  contra symlinks. Novos metadados devem ter custo pequeno e contrato documentado.
- Evitar daemon, novo banco, dependencias e caches globais como primeira solucao.

## Evidencias e estado inicial

As linhas abaixo sao referencias da revisao; localizar simbolos na versao atual.

| ID | Evidencia | Natureza |
| --- | --- | --- |
| E1 | `cmd/codegraph/main.go:555`, `internal/index/lock.go:28` | Auto-index unico; reader lock mantido ate shutdown impede writer externo |
| E2 | `internal/graph/store.go:1186,1249,1319` | Limites sem teto; truncamento sem aviso; snippet le arquivo inteiro |
| E3 | `internal/similar/similar.go:59` | Buckets de clones podem gerar pares e arestas quadraticos |
| E4 | `internal/index/manifest.go:226,253,365` | Snapshot copia fontes e arvores de dependencias; escolha de isolamento com custo |
| E5 | `internal/bench/bench.go:145` | Benchmark limita relacionamentos a 200; produto usa 500 por padrao |
| E6 | `internal/graph/store.go:1032`, `internal/query/query.go:153` | dead_code materializa candidatos antes de aplicar limite final |
| E7 | `internal/graph/store.go:772` | InsertEdges recria mapa de todos os nos em cada chamada nao vazia |
| E8 | `internal/index/incremental.go:214` | Toda mudanca TS/JS invalida todos os escopos TS, intencionalmente |

Na revisao, `go test ./...`, `go vet ./...`, build e race de MCP/query/CLI passaram.
`go test -race ./...` excedeu o timeout externo de 120s; nao foi aprovado nem houve
diagnostico da causa. Nao foram medidos ganhos de escala. Havia `.DS_Store` e
`internal/.DS_Store` nao rastreados; nao remover arquivos preexistentes.

## Ordem e dependencias

| Unidade | Prioridade | Depende de | Entrega |
| --- | --- | --- | --- |
| P0 | Alta | nenhuma | Baseline e contrato de medicao |
| P1 | Alta | P0 | Atualizacao coordenada do MCP |
| P2 | Alta | P0 | Respostas limitadas, completas por paginacao |
| P3 | Alta | P0; integrar P2 depois | Benchmark sem vantagem por truncamento |
| P4 | Alta | P0; metadados de estado de P1/P2 | Similaridade com consumo limitado e estado explicito |
| P5 | Media | P0, contrato P2 | Streaming de dead_code e mapa de IDs reutilizavel |
| P6 | Condicional | P0, P1 estavel | Reducao de staging e releituras |
| P7 | Condicional | P0, P3, P6 avaliado | Invalidacao TS mais seletiva |
| P8 | Condicional | P0, P1, P2 | Concorrencia MCP, somente se houver ganho medido |

Ordem sugerida: P0, P1, P2, P3, P4, P5, depois os gates P6-P8.
P1 e P2 podem ter investigacoes paralelas, mas ambos alteram MCP/query: integrar
serialmente ou combinar previamente a propriedade dos arquivos. Nao delegar edicoes
concorrentes de `store.go` a P2 e P5.

## P0 - Baseline reproduzivel

1. Ler instrucoes locais, status/diff, manifest/freshness, testes e docs atuais.
2. Capturar commit, Go/Node/scip, SO, hardware, flags, tamanho do corpus e dependencias.
3. Criar fixtures deterministicas: Go, monorepo TS com projetos dependentes e
   independentes, Ruby, hub com mais de 500 callers, milhares de clones e arquivo
   grande incluindo uma linha longa. Usar temporarios privados, nunca segredos reais.
4. Medir index frio, no-op, edicao de um arquivo, mudanca de config/dependencia,
   dois clientes simultaneos, falha de resolver e cancelamento durante indexacao.
5. Registrar duracoes por fase, tempo ate primeira query utilizavel, p50/p95 de
   consultas, alocacoes, pico RSS, bytes/arquivos copiados, tamanho final do banco,
   respostas em bytes/tokens estimados e numero de round-trips por tarefa completa.
6. Usar benchmarks Go para algoritmos isolados; separar integracoes com toolchains.
   Usar warm-up e repeticoes documentadas; nao chamar tempo de parede deterministico.
7. Arquivar comandos e resultados em `docs/EFFICIENCY_RESULTS.md`. Nao executar
   `codegraph bench` sobre o banco vivo usado pelo MCP: ele pode reindexar.

Aceite: resultados repetiveis em corpus fixado, status do resolver registrado e
baseline que nao compara respostas com completudes diferentes. Sem limiar absoluto
de milissegundos em testes funcionais de CI.

## P1 - Atualizacao coordenada e estado do MCP

Arquivos principais: `cmd/codegraph/main.go`, `internal/mcp/server.go`,
`internal/query/query.go`, `internal/index/lock*.go`, `pipeline.go`, testes adjacentes.

1. Adicionar testes de segunda edicao apos ready, writer contra MCP vivo e dois
   processos reais. Reproduzir contencao por mais de dois segundos sem sleeps frageis.
2. Antes de implementar, registrar em `docs/ARCHITECTURE.md` o protocolo escolhido:
   dono da atualizacao, liberacao/reabertura de leitores, exclusao de queries durante
   troca, identificacao da geracao e comportamento com clientes antigos.
3. Preferir inicialmente refresh explicito e assincrono no MCP, com deduplicacao de
   pedidos no mesmo processo. Nao introduzir watcher automatico nesta unidade.
4. Definir estados observaveis: pronto, atualizando, degradado, falhou e indisponivel;
   informar geracao/atualizacao e motivo resumido. Nao prometer detecao continua de
   staleness se o repositorio nao foi observado desde a ultima verificacao.
5. Coordenar processos: leitores cooperantes precisam liberar handles/locks antes
   de um writer publicar; outro cliente nao pode bloquear refresh indefinidamente.
   Avaliar locks por operacao com revalidacao/reabertura ou protocolo cooperativo
   minimo. Escolher um desenho, nao implementar ambos. Manter fail-safe para leitores
   antigos que nao cooperam, com erro acionavel e espera cancelavel limitada.
6. Manter handshake/status responsivos durante espera. Se nao for possivel consultar
   o grafo antigo com seguranca, responder estado de atualizacao, nunca dados mistos.
7. Apos falha, reabrir o grafo anterior quando valido e expor falha; outro processo
   deve observar o commit vencedor sem precisar reiniciar. Preservar cleanup/recovery.

Aceite: editar e atualizar sem restart; dois MCPs cooperantes convergem para a mesma
geracao; nunca dois writers; query nao usa engine fechado; timeout/cancelamento nao
deixam bloqueio permanente. Validar recuperacao, stdin fechado e Windows/Unix.
Nao considerar P1 concluido apenas com refresh que funciona com um unico cliente.

## P2 - Orcamento de resposta e continuacao

Arquivos: `internal/graph/store.go`, `internal/query/*`, `internal/mcp/server.go`,
dispatch CLI e testes de limites/snippet.

1. Definir defaults e tetos compartilhados entre CLI/MCP, ajustados com P0. Ponto de
   partida a validar: 500 refs/pagina e 32 KiB de texto; snippet 200 linhas e 32 KiB.
   Valores sao propostas, nao numeros medidos. Limites de bytes prevalecem.
2. Validar argumentos antes de alocar: negativos, extremos, JSON invalido, limites e
   ranges incoerentes devem produzir erro acionavel. Definir limite individual de
   uma referencia longa; nao cortar qualified_name produzindo referencia invalida.
3. Introduzir resultado paginado com `has_more` e continuacao vinculada a consulta e
   geracao. Preferir ordenacao estavel/keyset. Rejeitar cursor de outra geracao com
   orientacao para reiniciar a consulta; nao misturar snapshots em paginas.
4. Preservar deduplicacao dos vizinhos. Consultar item extra ou mecanismo equivalente
   para detectar continuacao, sem COUNT completo obrigatorio em toda consulta.
5. Snippet deve ler incrementalmente, interromper no trecho/orcamento, suportar linha
   longa e oferecer continuacao sem perda. Preservar abertura segura/confinamento;
   documentar comportamento se o arquivo mudar entre partes.
6. Manter linhas TSV existentes e adicionar metadados compactos documentados fora
   das referencias. Revisar consumidores internos, benchmark e compatibilidade da
   saida CLI existente; nao alterar formato silenciosamente.

Aceite: hub >500 pode ser enumerado inteiro sem duplicatas/omissoes; defaults nao
despejam arquivo inteiro; pedidos gigantes nao causam alocacao proporcional ao pedido;
truncamento nunca parece resposta exaustiva. Testar bytes multibyte, arquivos vazios,
linhas gigantes, self-edges, aliases de QN, ranges e mudanca de geracao.

## P3 - Benchmark de respostas equivalentes

Arquivos: `internal/bench/bench.go`, testes, `docs/BENCHMARK.md`, `docs/QUALITY.md`.

1. Remover o cap fixo de 200 como atalho de custo. Antes de P2, obter conjunto completo
   por caminho interno testado; depois de P2, contabilizar todas as paginas reais.
2. Cobrar metadados, continuacao e round-trips, usando o mesmo formato do produto.
3. Medir completude contra fixture/oraculo independente. Nao usar apenas o proprio
   grafo para afirmar recall correto; registrar degraded, falha e staleness.
4. Separar custo de preparar/atualizar do custo de query; adicionar trajetoria
   consulta-edicao-refresh-consulta e custo amortizado para diferentes usos por sessao.
5. Documentar vies da selecao de hubs e estimativa bytes/4. Preservar resultados
   historicos identificados por versao/metodo, sem reatribuir numeros ao codigo atual.

Aceite: fixture com >500 callers cobra todas as referencias/paginas e nao ganha
artificialmente ao reduzir o limite. Mesma semantica de pergunta nos dois lados.

## P4 - Similaridade limitada por recursos

Arquivos: `internal/similar/*`, `internal/index/similar.go`, resultado/manifest do index.

1. Adicionar benchmark adversarial e testes de determinismo com assinaturas identicas.
2. Definir limites de candidatos examinados, memoria/arestas e checagens de contexto.
   Aplicar antes de popular `seen`/arestas sem limite; streaming sozinho nao limita
   o numero total de pares nem o tamanho final do banco.
3. Implementar primeiro interrupcao controlada da fase com status de similaridade
   parcial/omitida e politica explicita de manter ou descartar arestas parciais.
   Se mantidas, a selecao deve ser deterministica apesar da iteracao de maps.
4. Propagar cobertura da similaridade ate consultas, inclusive apos restart/no-op;
   nao representar interrupcao dessa fase como falha de CALLS. Definir invalidacao
   por mudanca de algoritmo/orcamento no manifest para evitar reuso inadequado.
5. Avaliar agrupamento de clones exatos como melhoria posterior se P0 justificar.
   Nao adicionar novo modelo de grafo nesta unidade sem necessidade demonstrada.

Aceite: corpus adversarial termina ou interrompe dentro do orcamento configurado,
cancelamento funciona e ferramentas mostram cobertura incompleta. Corpus normal
preserva resultados anteriores quando o orcamento nao e atingido.

## P5 - Otimizacoes locais de consultas e insercao

Entregar duas mudancas separadas e medidas; arquivos principais em graph/query/index.

1. P5a: substituir materializacao de candidatos dead_code por iteracao paginada ou
   streaming, mantendo filtros existentes e ordenacao. Parar quando a pagina e sua
   continuacao forem determinadas; muitos entry points nao podem causar falsa falta
   de candidatos. Testar cancelamento/fechamento de rows e conjunto majoritariamente
   exportado, decorado ou de testes. Memoria deve depender do lote, nao do total.
2. P5b: mapear quando todos os nos estao disponiveis, incluindo rotas/nos tardios.
   Reutilizar QN->ID apenas durante uma fase com conjunto estavel; atualizar ou
   reconstruir explicitamente quando houver novos nos. Nao criar cache global.
3. Preservar transacoes, endpoints existentes, contagem de dropped/duplicados e
   isolamento entre projetos. Comparar grafo logico antes/depois, nao IDs fisicos.

Aceite: resultados equivalentes; benchmark demonstra menor alocacao/trabalho em
corpus grande sem piora relevante do caso pequeno. Registrar numeros, nao promessas.

## P6 - Gate de staging e releituras

So implementar se P0 mostrar peso relevante. Ler antes `manifest.go`, `prepare.go`,
`securefile`, testes de mutacao/symlink/integridade e semantica dos snapshots.

1. Separar custos de leitura de fonte, hashing, verificacao de estabilidade, FTS e
   copia de dependencias. Nao afirmar numero fixo de passadas para todos os caminhos.
2. Avaliar primeiro compartilhar resultados entre consumidores dentro de uma
   observacao estavel, com memoria limitada e sem remover verificacoes entre etapas.
3. Para snapshots, avaliar clone copy-on-write com fallback seguro ou cache imutavel
   validado por identidade de conteudo. Lockfile sozinho nao prova que node_modules
   nao foi alterado. Cache exige invalidacao, limpeza, limite de disco e permissoes.
4. Nao omitir dependencias por suposicao de tsconfig, nem apontar snapshot para
   arvore viva mutavel. Preservar workspaces, symlinks seguros e replaces Go.

Aceite: mutacao durante staging e corrupcao ainda sao detectadas; resultados
equivalentes; reducao medida de bytes/tempo; fallback testado em SO sem CoW.
Se nao houver ganho suficiente, encerrar com evidencias e sem alteracao estrutural.

## P7 - Gate de incremental TS

1. Medir monorepo com escopos independentes e dependentes. A invalidacao total atual
   e correta por conservadorismo, nao bug a corrigir por heuristica.
2. Modelar ownership e dependencias de project references, packages, aliases,
   reexports, arquivos compartilhados, configuracoes e transicoes add/delete/rename.
3. Reutilizar somente onde dependencia estiver comprovada; casos nao resolvidos
   continuam invalidando todos os escopos pertinentes. Comparar CALLS incremental
   com rebuild completo em cada fixture, inclusive chamadores em outro projeto.
4. Versionar analise/manifest ao mudar a politica. Nao podar VTA Go nesta entrega;
   sua granularidade global exige investigacao separada e equivalencia propria.

Aceite: edicao comprovadamente isolada executa menos resolvers; alteracao transitiva
invalida dependentes; nenhum conjunto de CALLS difere do rebuild de referencia.

## P8 - Gate de concorrencia MCP

Se P0 demonstrar bloqueio relevante entre queries, implementar concorrencia limitada
com escrita JSON-RPC serializada, IDs corretos, contexto/cancelamento por request,
limite de requests em voo e coordenacao com refresh. Nao apenas criar goroutines:
verificar limites das conexoes SQLite e lifetime do engine. Proteger status e
handshake contra operacoes lentas. Testar backpressure, shutdown e clientes lentos.

Aceite: ganho de p95 sob carga mista, nenhuma corrida ou resposta intercalada,
memoria limitada e ausencia de starvation do refresh. Sem ganho, manter sequencial.

## Validacao e entrega por unidade

- Rodar testes focados novos e existentes, `go test ./...`, `go vet ./...` e
  `go build ./cmd/codegraph`; nao sobrescrever binario instalado do usuario.
- Rodar `go test -race ./... -timeout 20m` com timeout externo compativel. Se demorar
  ou falhar, registrar pacote/stack/comando e investigar; nao retirar checks para passar.
- Mudancas em locks/snapshots precisam de testes executados em Windows e Unix;
  compilacao cruzada sozinha nao valida comportamento de locks/replacement.
- Manter testes de cancelamento, corrupcao SQLite/FTS, mutacao, recovery, permissao
  e symlinks. Benchmarks nao devem alterar limites funcionais para melhorar numeros.
- Reexecutar fixtures de qualidade e comparar grafo/recall quando mudar indexacao.
- Atualizar `EFFICIENCY_RESULTS.md`, marcar somente unidades realmente concluidas,
  atualizar `ARCHITECTURE.md` ao mudar design e `ROADMAP.md` ao fechar entregas.
- Registrar diff, testes, ganhos medidos, limites conhecidos e proximo passo.
  Nenhuma unidade condicional e obrigatoria se a medicao nao justificar seu custo.

## Instrucao de retomada

O proximo agente deve comecar por P0, confirmar evidencias contra o checkout e
entregar baseline antes de modificar runtime. Usar este arquivo como backlog,
nao como prova de que as implementacoes propostas ja foram validadas. Ao concluir
uma unidade, deixar status e evidencias suficientes para outro agente continuar.
