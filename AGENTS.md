# Instruções do repositório

## Escopo atual
- Este fork é de uso pessoal do Igor, atualmente exclusivo no macOS. A decisão em `docs/PERSONAL_MACOS_SCOPE.md` governa a priorização; backlog histórico não autoriza expansão de plataforma.
- Windows e benchmarks Linux/Windows ficam fora dos próximos milestones. Mantenha o CI Linux e os gates existentes, sem remover código/assets de plataformas por antecipação.
- Valide o fluxo diário no Mac e escolha com o usuário um repositório real antes de medir/otimizar. Search keyset, staging profundo e certificação externa completa são condicionais a necessidade demonstrada; inputs não certificados continuam sem reuso.

## Contexto
- Módulo único Go `github.com/Lordymine/codegraph`, Go 1.26. Tree-sitter usa cgo: build requer `CGO_ENABLED=1` e gcc/clang (MinGW no Windows).
- `cmd/codegraph` hospeda CLI/MCP; `internal/index` faz descoberta e indexação; `internal/graph` mantém SQLite/FTS5; `internal/query` define consultas/paginação; `internal/mcp` implementa JSON-RPC sobre stdio.
- Resolução `CALLS` é delegada ao SCIP (`internal/scip`, TS/JS) e `go/packages`/VTA (`internal/gocalls`, Go). Arestas sem endpoints reais são descartadas; não substitua isso por heurísticas que inventem chamadas.
- Use `index.RunAtomic` para indexar/reindexar: o banco e o manifest são preparados e validados antes da troca atômica. Em mudanças de sessão MCP, serialize a consulta inteira — não apenas o gate — contra `Engine.Close/Reopen`.
- Snapshot de resolvers deve consumir o plano observado de inputs, nunca redescobrir dependências no staging. Ambiente Go efetivo é injetado em `packages.Config.Env`; inputs não certificados não autorizam no-op/reuso de CALLS. Não leia `.env` sem admissão explícita.
- Consultas expõem refs compactas; conteúdo-fonte só deve sair por `snippet`. Não execute `codegraph index <repo>` enquanto o MCP estiver servindo o mesmo repositório.
- Node.js é necessário ao indexar TS/JS e nas integrações TS opt-in (`-tags integration`): SCIP roda via `npx` e usa o `node_modules` e os `tsconfig.json` do repositório-alvo. Não é requisito para compilar ou executar a suíte Go padrão. O CI tem um job separado com Node 26.0.0 e SCIP TypeScript 0.4.0 fixados.
- Para invariantes de design, leia `docs/ARCHITECTURE.md`; consulte `CLAUDE.md` e `CONTRIBUTING.md` para convenções e regras de autoria. O caminho Windows no início de `CLAUDE.md` é específico da estação de trabalho; use o checkout ativo.

## Verificação
O CI usa Go do `go.mod`, `CGO_ENABLED=1` e executa:

```bash
gofmt -l .
go mod verify
go mod tidy -diff
go vet ./...
go build ./...
go test ./...
go test -race ./...
go test ./... -coverprofile=coverage.out -covermode=set
golangci-lint run ./...  # CI fixa golangci-lint v2.12.2
```

O job TypeScript executa `go test -race -tags integration ./internal/index -run '^TestTSInvalidation_RealResolver' -count=1 -timeout 10m`, verificando CALLS esperadas e equivalência incremental/rebuild com o resolver real.

Para um recorte, use `go test ./internal/query` ou `go test ./internal/query -run '^TestNome$'`. `go test -race ./...` é caro; use um timeout longo ou execute em segundo plano. `make test` equivale a `go test ./...`; `make build` grava o binário `./codegraph`.

## Mudanças
- Ao alterar desenho ou invariantes, atualize `docs/ARCHITECTURE.md`; ao concluir um milestone, atualize `docs/ROADMAP.md`.
- Commits seguem Conventional Commits em inglês. Antes de commitar, siga a identidade `Igor Maneschy <igor@maneschy.com>` e nunca adicione trailer `Co-Authored-By` (incluindo de assistentes).
