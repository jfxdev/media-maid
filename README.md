<p align="center">
  <img src="docs/logo.png" alt="Media Maid" width="200">
</p>

<h1 align="center">media-maid</h1>

Sidecar que organiza mídias baixadas (filmes e séries) no formato de nomes
que **Jellyfin**, **Emby** ou **Plex** reconhecem, e depois pede para o media
server sincronizar a biblioteca.

O Media Maid roda ao lado do container do media server, compartilhando volume:
observa a pasta de downloads, identifica título/ano/temporada/episódio,
cria **hardlinks** na biblioteca (o torrent continua semeando, sem ocupar
espaço extra) e dispara o sync. Também detecta órfãos (itens do server sem
arquivo e pastas sem vídeo) e permite removê-los de forma explícita.

```
downloads/Inception.2010.1080p.BluRay.x264-GRP/Inception.2010.1080p.BluRay.x264-GRP.mkv
downloads/Inception.2010.1080p.BluRay.x264-GRP/Subs/Brazilian.srt
downloads/Dark.S01.1080p/Dark.S01E01E02.1080p.mkv
                         ↓
movies/Inception (2010) [tmdbid-27205]/Inception (2010) [tmdbid-27205].mkv
movies/Inception (2010) [tmdbid-27205]/Inception (2010) [tmdbid-27205].pt-BR.srt
tv/Dark (2017) [tmdbid-70523]/Season 01/Dark S01E01-E02.mkv
```

A tag de ID só aparece quando `TMDB_API_KEY` está configurada, e o formato
segue a plataforma: `[tmdbid-1]` (Jellyfin), `[tmdbid=1]` (Emby), `{tmdb-1}` (Plex).

## Single tenant

Cada instância fica vinculada a **exatamente um** media server, definido por
`INTEGRATION_MODE`. Combinações (Jellyfin + Plex, dois Jellyfin…) são
recusadas no start: valores múltiplos em `INTEGRATION_MODE`,
`MEDIA_SERVER_URL` ou `MEDIA_SERVER_TOKEN`, ou variáveis `JELLYFIN_*`,
`EMBY_*` e `PLEX_*` presentes, encerram o processo. Para vários servers, rode
um sidecar por server.

Todo o código específico de plataforma está numa única tabela
(`internal/integration/platforms.go`). Sync, listagem e remoção de itens são
genéricos e guiados pelo modo selecionado.

| Modo | Sync | Listagem (órfãos) | Remoção de item | Header |
|---|---|---|---|---|
| `jellyfin` | `POST /Library/Refresh` | `GET /Items` | `DELETE /Items/{id}` | `X-Emby-Token` |
| `emby` | `POST /emby/Library/Refresh` | `GET /emby/Items` | `DELETE /emby/Items/{id}` | `X-Emby-Token` |
| `plex` | `GET /library/sections/all/refresh` | `GET /library/sections/{id}/all` | `DELETE /library/metadata/{id}` | `X-Plex-Token` |

## Configuração

| Variável | Obrigatória | Padrão | Descrição |
|---|---|---|---|
| `INTEGRATION_MODE` | sim | — | `jellyfin`, `emby` ou `plex` (um único valor) |
| `MEDIA_SERVER_URL` | não¹ | — | ex.: `http://jellyfin:8096` |
| `MEDIA_SERVER_TOKEN` | não¹ | — | API key (Jellyfin/Emby) ou X-Plex-Token |
| `API_TOKEN` | sim | — | Bearer token da API (≥ 32 caracteres) |
| `DOWNLOADS_DIR` | sim | — | pasta de downloads (origem) |
| `MOVIES_DIR` | sim | — | biblioteca de filmes |
| `TV_DIR` | sim | — | biblioteca de séries |
| `VIDEO_EXTENSIONS` | não | `mkv,mp4,avi,m4v,mov,wmv,ts,webm` | allowlist de vídeos |
| `SUBTITLE_EXTENSIONS` | não | `srt,ass,ssa,sub,idx,vtt` | allowlist de legendas |
| `TMDB_API_KEY` | não | — | chave v3 ou token v4 do TMDB (título/ano/ID oficiais) |
| `SERVER_PATH_MAP` | não | — | `prefixo_server=prefixo_local` (um único par) |
| `ORPHAN_SCAN_INTERVAL` | não | — | scan periódico de órfãos (ex.: `24h`), só relatório |
| `SYNC_DEBOUNCE` | não | `30s` | agrupa vários links em um único sync |
| `STABLE_SECONDS` | não | `30` | tempo sem mudanças para considerar o download concluído |
| `LISTEN_ADDR` | não | `:8080` | endereço HTTP |
| `DRY_RUN` | não | `false` | não cria links nem remove nada |

¹ `MEDIA_SERVER_URL` e `MEDIA_SERVER_TOKEN` vêm em par. Com as duas vazias,
o sync e a checagem de itens do server ficam desativados.

`DOWNLOADS_DIR`, `MOVIES_DIR` e `TV_DIR` precisam ser distintos, não
aninhados e estar dentro de volumes montados no container. Para que o
hardlink funcione, downloads e biblioteca devem estar no mesmo filesystem; se
não estiverem, o Media Maid copia o arquivo.

Exemplos completos: [`examples/`](examples/) (um `docker-compose` por modo).

## API

Todas as rotas `/api/v1/*` exigem `Authorization: Bearer $API_TOKEN`.

| Método | Rota | Descrição |
|---|---|---|
| `GET` | `/healthz` | liveness (sem autenticação) |
| `POST` | `/api/v1/scan` | organiza todo o `DOWNLOADS_DIR` |
| `POST` | `/api/v1/organize` | `{"path": "..."}`: organiza um arquivo ou pasta (relativo a `DOWNLOADS_DIR` ou absoluto dentro dele) |
| `POST` | `/api/v1/preview` | igual a `organize`, mas só mostra o destino planejado |
| `GET` | `/api/v1/history` | ações recentes e resultado do último sync |
| `POST` | `/api/v1/sync` | força o sync na plataforma configurada |
| `POST` | `/api/v1/orphans/scan` | gera o relatório de órfãos |
| `GET` | `/api/v1/orphans` | último relatório |
| `POST` | `/api/v1/orphans/remove` | `{"ids": [...]}`: remove órfãos do relatório atual |

Para disparar a organização ao concluir um download no qBittorrent, use
*Executar programa externo*:

```sh
curl -s -X POST -H "Authorization: Bearer $API_TOKEN" \
  -d "{\"path\":\"%N\"}" http://media-maid:8080/api/v1/organize
```

Status por arquivo: `linked`, `copied`, `exists`, `conflict` (o destino já
existe com outro conteúdo e nunca é sobrescrito), `planned` (preview),
`skipped` e `error`.

Códigos de erro: `400 invalid_path`, `401 unauthorized`,
`403 unauthorized_volume`, `404 not_found` e `409 sync_disabled`.

## Órfãos

- `server_item_missing_file`: o item existe no media server, mas o arquivo
  não está mais no disco. Só é reportado quando **todos** os paths do item
  estão dentro das bibliotecas gerenciadas e nenhum existe. Remover chama o
  `DELETE` da plataforma. Jellyfin, Emby e Plex também apagam a mídia do
  item, mas como os arquivos já não existem (isso é revalidado antes da
  chamada), o efeito fica restrito à entrada e à pasta do item dentro da
  biblioteca gerenciada.
- `orphan_directory`: pasta da biblioteca sem nenhum vídeo (vazia, ou só com
  legendas/nfo/imagens). Só a pasta mais alta é reportada. Remover apaga a
  pasta.

A remoção nunca é automática: só acontece via `/api/v1/orphans/remove`, com
IDs do relatório atual. Cada item é revalidado na hora; se não for mais órfão
(arquivo restaurado, vídeo adicionado, pasta trocada por symlink…), retorna
`stale` e nada é feito.

## Segurança

- `API_TOKEN` é obrigatório e comparado em tempo constante.
- **Volumes autorizados** são os volumes montados no container, lidos de
  `/proc/self/mountinfo`. O rootfs do container, `/proc`, `/sys`, `/dev` e
  arquivos injetados (`/etc/hosts`…) ficam de fora.
- Paths da API não podem conter `..`, bytes nulos nem apontar para fora de
  `DOWNLOADS_DIR`.
- Symlinks na origem podem apontar para **qualquer** volume montado. Se
  apontarem para fora deles, o resultado é `unauthorized_volume` (403 na API,
  erro no histórico) e nada é linkado. O arquivo é aberto pelo path resolvido
  com `O_NOFOLLOW` e checado com `fstat` (arquivo regular em um dispositivo
  autorizado) antes do link.
- A escrita na biblioteca passa por `os.Root`. Pastas da biblioteca que sejam
  symlinks para fora geram `unauthorized_volume`.
- Nomes vindos do parser e do TMDB são sanitizados (sem separadores, `..` ou
  caracteres de controle).
- IDs de itens do server são validados antes de entrar em URLs.
- Os tokens nunca aparecem em logs ou respostas.
- A imagem roda como non-root (distroless). Os exemplos usam `read_only`,
  `cap_drop: [ALL]` e `no-new-privileges`.

## Desenvolvimento

```sh
go test ./...
go build ./cmd/media-maid
docker build -t media-maid .
```

Requer Go 1.25+ (`os.Root`).
