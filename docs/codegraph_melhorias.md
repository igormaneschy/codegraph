# codegraph — Documento de Melhorias (RFC para planeamento)

> **Veredito de avaliação (2026-10-09, sessão de agente).** Este RFC antecede os
> lotes R/P (R01–R19, P1–P7), a matriz de benchmark reproduzível e a decisão de
> escopo `PERSONAL_MACOS_SCOPE.md` (uso pessoal, macOS). **Nada aqui foi
> implementado e este documento não é autorização para implementar em bloco.**
>
> | # | Veredito |
> |---|----------|
> | 1 Ferramentas compostas | Candidato condicional mais forte (`explore`/`understand`; `prepare_change` depende do 7) |
> | 2 Presets | Condicional/baixo valor — o `tools/list` atual tem 9 tools compactas; medir antes |
> | 3 `max_tokens` | Já coberto em essência (budgets em bytes, cursores e clamp do R16) — não refazer |
> | 4 PageRank | Alto risco (schema/identidade + incremental); só com problema de ranking demonstrado |
> | 5 Watch/frescura | Parcial; o framing "commits atrás do HEAD" é inadequado (frescura é por hash de conteúdo); subset útil é o aviso de stale |
> | 6 IDs estáveis | Defer/rejeitar agora — dor não demonstrada, migração de schema + detecção de rename |
> | 7 Impacto/risco | Candidato (raio de impacto determinístico); score de risco só com heurística documentada |
> | 8 Testes afetados | Condicional, após o 7; chamadas indiretas (interfaces/func values) têm limite honesto |
> | 9 Hotspots | Parcial (complexidade já entregue no M4/arquitetura); churn/CODEOWNERS defer |
> | 10 Novas linguagens | Fora do escopo atual (condicional a uso real do dono) |
> | 11 Review sem LLM | Fora do escopo atual (depende de 7/8/9; fluxo pessoal não tem CI de review) |
> | 12 Fallback tree-sitter | Rejeitar agora — conflita com o invariante de descartar arestas sem endpoint real |
> | 13 Histórico git | Defer (barato apenas como subproduto de 7/9) |
>
> Próximo passo pelo escopo vigente: escolher o workload real do dono e medir
> antes de implementar qualquer item; o recorte mais defensável é 1+7. Benchmarks
> devem usar o harness do repo (`codegraph bench`); "goclaw/openclaude" citados
> abaixo não existem neste repositório. O esforço real aqui inclui contrato
> pré-implementação, evidência local/race/SCIP/lint e possíveis mudanças de
> identidade/versão — ~2–3× as estimativas do RFC para 4/5/6/10.

Objetivo: servir de entrada para pedir um plano de implementação detalhado a um agente de código, diretamente no repositório `igormaneschy/codegraph`.

Contexto: codegraph é um servidor MCP em Go que indexa repositórios Go e TS/JS num grafo (SQLite + FTS5), com arestas CALLS resolvidas por type checkers (go/packages + VTA; scip-typescript). Ferramentas atuais: get_architecture, search, callers, callees, neighbors, similar, dead_code, detect_changes, snippet. Roadmap M0–M5 concluído; M6 (HTTP_CALLS e artefato graph.db.zst) pendente.

Referências de inspiração (design apenas, NÃO copiar código):
- CKB (SimplyLiz/ckb) — licença com restrições comerciais; usar só como ideia de design.
- Aider repo map — tree-sitter + PageRank + orçamento de tokens.
- Sourcegraph MCP / SCIP — navegação entre repositórios, histórico de commits e diffs.

Princípios a manter: arestas incertas são descartadas (precisão > recall); respostas compactas (sem código-fonte, exceto via `snippet`); licença MIT; instalação sem sudo para agentes.

---

## Visão geral e prioridades

| # | Melhoria | Valor | Esforço | Depende de |
|---|----------|-------|---------|------------|
| 1 | Ferramentas compostas (explore, understand, prepare_change, batch) | Alto | Baixo | — |
| 2 | Presets de ferramentas (core/review) + expansão em sessão | Alto | Baixo | 1 |
| 3 | Orçamento de tokens (`max_tokens`) em todas as respostas | Alto | Baixo–Médio | — |
| 4 | Ranking PageRank no get_architecture / search | Alto | Médio | 3 |
| 5 | Modo watch + aviso de índice desatualizado | Alto | Médio | — |
| 6 | IDs estáveis de símbolos (sobrevivem a renomeações) | Médio | Médio | 5 |
| 7 | Impacto e risco (estender detect_changes) | Alto | Médio | 1 |
| 8 | Testes afetados | Médio | Médio | 7 |
| 9 | Hotspots (churn git, CODEOWNERS) e complexidade | Médio | Médio | — |
| 10 | Novas linguagens via SCIP | Médio | Médio–Alto | — |
| 11 | Revisão de PR sem LLM (SARIF, modo CI) | Médio | Alto | 7, 8, 9 |
| 12 | Fallback tree-sitter com confiança baixa | Baixo–Médio | Médio | — |
| 13 | Histórico de commits/diffs como consulta | Médio | Médio | 9 |

---

## 1. Ferramentas compostas

Problema: agentes fazem muitas chamadas sequenciais (search → callers → callees → snippet).
Proposta: ferramentas que agregam consultas existentes numa só chamada.
- `explore(query, max_tokens)`: search + vizinhança 1 salto dos melhores resultados.
- `understand(symbol, depth)`: definição (ref compacta), callers, callees, símbolos similares.
- `prepare_change(symbol|files)`: callers transitivos, ficheiros/pacotes afetados, risco (ver 7).
- `batch_get(ids[≤50])` e `batch_search(queries[≤10])`.
Critérios de aceitação:
- Cada composta reutiliza as funções internas existentes (sem duplicar SQL).
- Formato de saída consistente com o atual (linhas separadas por tab).
- Benchmark: nº de chamadas e tokens vs. sequência manual, no mesmo harness já usado nos testes (goclaw, openclaude).

## 2. Presets de ferramentas

Problema: cada ferramenta MCP consome contexto na descrição.
Proposta: flag/env `--preset core|review|full`; ferramenta `expand_toolset` para ativar mais ferramentas na sessão (e notificação `tools/list_changed`).
Critérios: preset `core` com poucas ferramentas (get_architecture, search, callers, callees, snippet, explore); medir tokens de `tools/list` por preset; `codegraph install` aceita `--preset`.

## 3. Orçamento de tokens

Proposta: parâmetro `max_tokens` (e default configurável) em search/neighbors/get_architecture/compostas. Truncar de forma determinística (por ranking), indicar `truncated: N omitidos` e sugerir a consulta seguinte.
Critérios: contador de tokens aproximado (ex.: bytes/4, com opção de tokenizer real); testes com saídas acima/abaixo do limite; nunca devolver resposta parcial sem indicador.

## 4. Ranking PageRank

Proposta: calcular PageRank sobre o grafo CALLS (opcionalmente personalizado por ficheiros "em foco" passados pelo agente) e guardar `rank` em `nodes` na indexação. Usar para ordenar get_architecture, desempate em search (BM25 × rank) e truncamento do item 3.
Vantagem: o grafo de chamadas é type-checked, mais preciso do que referências por tree-sitter usadas pelo Aider.
Critérios: recalcular de forma incremental ou em lote após reindexação; avaliação com perguntas reais (top-k contém símbolos relevantes); custo de indexação medido.

## 5. Modo watch e frescura

Proposta: `codegraph mcp --watch` com reindexação por debounce (fsnotify) ou verificação periódica; comando `status` com commits atrás do HEAD; todas as respostas incluem um campo curto de frescor quando o índice está desatualizado.
Critérios: não reindexar se nada mudou (hash de ficheiros/HEAD); reindexação nunca bloqueia consultas (leitura em snapshot/WAL); testes de concorrência.

## 6. IDs estáveis de símbolos

Proposta: ID baseado em fingerprint (pacote + nome qualificado + assinatura) com tabela de aliases para renomeações/movimentos detetados entre indexações.
Critérios: consulta por ID antigo resolve para o novo; migração de esquema com versão; testes com rename/move.

## 7. Impacto e risco

Proposta: estender `detect_changes` com: raio de impacto (callers transitivos até N saltos), pacotes/rotas afetados (incl. rotas NestJS), pontuação de risco (fan-in, rank, complexidade, churn), e lista de testes afetados (item 8). Saída compacta e ordenada.
Critérios: dado um diff (git range), devolve conjunto afetado determinístico; documentação das heurísticas de risco.

## 8. Testes afetados

Proposta: marcar nós de teste (Go `_test.go`, TS `*.test.*`/`*.spec.*`); ferramenta `affected_tests(diff|symbols)` e saída pronta para comando (ex.: `go test -run ...`).
Critérios: precisão medida em repositórios de teste; sem falsos negativos conhecidos para chamadas diretas.

## 9. Hotspots e complexidade

Proposta: complexidade ciclomática/cognitiva via tree-sitter por função; churn por ficheiro via `git log`; propriedade via CODEOWNERS; ferramenta `hotspots` (churn × complexidade × fan-in) e integração com `dead_code` e risco.
Critérios: cálculo incremental; limites de histórico configuráveis (ex.: últimos 6 meses).

## 10. Novas linguagens via SCIP

Proposta: abstrair "indexador SCIP" (comando, pré-requisitos, mapeamento de símbolos → nodes/edges) para adicionar Python, Rust, Java etc. com pouco código por linguagem, reutilizando a integração do scip-typescript.
Critérios: interface `Indexer` documentada; uma linguagem-piloto (sugestão: Python ou Rust) com testes e benchmark.

## 11. Revisão de PR sem LLM

Proposta: `codegraph review --base X --head Y` com verificações determinísticas (impacto, testes não cobertos, novo código morto, aumento de complexidade, hotspots tocados), saída texto/JSON/SARIF e código de saída para CI.
Critérios: <10 s em repos médios; regras ativáveis por configuração; exemplo de workflow GitHub Actions.

## 12. Fallback tree-sitter com confiança

Proposta: quando o type checker falha (módulo não compila, falta node_modules), usar tree-sitter para arestas heurísticas marcadas `confidence=low`, excluídas por padrão e incluíveis via flag.
Atenção: respeita o princípio "não adivinhar" porque fica opt-in e rotulado.

## 13. Histórico de commits/diffs

Proposta: ferramenta `history(symbol|file)` com commits recentes e autores, a partir de git; base para hotspots.

---

## Fases sugeridas

- Fase A (rápida): itens 1, 2, 3.
- Fase B (qualidade/ frescura): itens 4, 5, 6.
- Fase C (valor de engenharia): itens 7, 8, 9, 13.
- Fase D (escopo maior): itens 10, 11, 12 e M6 existente.

## Riscos transversais
- Aumento do tamanho/tempo de indexação (medir em cada fase).
- Compatibilidade de esquema SQLite (versionar e migrar).
- Inflação do contexto MCP (controlar com presets).
- Licenciamento: não copiar código de projetos com licença restritiva.

## Prompt sugerido para o agente no repositório

"Leia este documento e o código atual. Para a Fase A, produza um plano de implementação com: ficheiros a alterar, mudanças de esquema, assinaturas das novas ferramentas MCP, plano de testes e benchmark, ordem de commits/PRs e riscos. Não implemente ainda."
