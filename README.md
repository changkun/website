# changkun.de

One Go binary serves changkun.de:

- `/` serves the static site embedded from `static/`.
- `/<folder>/` serves each folder of `WWW_DIR`, such as the books and the
  blog. See [Folders](#folders).
- `/ideas/` is an API that polishes, translates, and expands an idea with LLMs,
  then commits it as bilingual markdown to
  [changkun/blog](https://github.com/changkun/blog).

## Usage

```
$ docker network create traefik_proxy
$ make build && make up
```

`make build` runs the tests, builds a Linux binary, and builds the image.

The site needs no configuration. Without the ideas credentials, the site still
serves and `/ideas/` answers 503.

## Layout

| Path | Contents |
|---|---|
| `main.go` | Signal handling, the HTTP server, request logging |
| `site.go` | The embedded `static/` site and its file server |
| `www.go` | The folders of `WWW_DIR`, and the downloads reported to urlstat |
| `internal/ideas/` | The ideas API |
| `internal/ideas/prompts/` | Model prompts, as Go text templates embedded in the binary |

## Folders

A folder of `WWW_DIR` is served at `/<folder>/` from the moment it exists, so
publishing one takes an `rsync` or a `git clone` into `WWW_DIR`, and no
restart. In production `WWW_DIR` is `/www/changkun.de`, mounted read-only.

Folders are served like files on disk: a folder without an `index.html` is
listed, and symlinks are followed. Hidden files, such as `.git`, are not
served. A name the embedded site has, such as `books`, stays the site's.

File responses have no write timeout, since some are 80 MB.

## Ideas API

In production the API is at `https://api.changkun.de/ideas/`, which traefik
routes to this binary. `changkun.de/ideas/` redirects to the blog's idea pages
instead.

| Route | Auth | Body | Reply |
|---|---|---|---|
| `GET /ideas/ping` | none | | `pong` |
| `POST /ideas/post` | Bearer | `{"title", "content", "augmented"}` | `{"ok", "message"}` |
| `POST /ideas/improve` | Bearer | `{"content"}` | `{"ok", "content"}` |

Only `content` is required. `/ideas/improve` returns the content polished, and
publishes nothing.

`/ideas/post` replies at once and publishes in the background. It fetches the
linked pages, generates a title if none is given, polishes the idea and
translates it, picks a slug, writes a sourced deep dive unless `augmented` is
given, translates that, and commits the post to `content/ideas/`. If the title
or the translation fails, nothing is published.

### Models

Model calls go through [Lux](https://github.com/latere-ai/lux), the model
gateway of the [latere platform](https://platform.latere.ai), in its native
dialect (`POST $LLM_BASE_URL/lux/v1/generate`). Hence:

- `LLM_BASE_URL` is the gateway root, not a provider-compatible path such as
  `.../anthropic`.
- Model names must match the gateway's catalog exactly. Lux does no aliasing.
- `LLM_API_KEY` is a Lux Key created on the platform. Scope it to the two
  models and give it a budget.

`LLM_MODEL` writes the deep dive, using web search and fetch so that citations
point at real pages. `LLM_TITLE_MODEL` does everything else: titles, slugs,
polishing, and translation.

### Auth

Routes other than `/ideas/ping` require an access token from
[latere auth](https://auth.latere.ai), which the compose box on changkun.de
gets through browser PKCE. The token must verify against the issuer's JWKS,
and be issued to a client in `AUTH_ALLOWED_CLIENTS` for a principal in
`AUTH_ALLOWED_PRINCIPALS`. Both checks matter: any latere account can get a
valid token, for any latere client.

## Configuration

Copy `.env.template` to `.env`. The API needs `LLM_API_KEY` and `GIT_TOKEN`;
everything else has a default.

| Variable | Default | Description |
|---|---|---|
| `WEBSITE_ADDR` | `0.0.0.0:80` | Listen address |
| `WWW_DIR` | | Folder whose folders are served. Unset serves none |
| `WWW_URLSTAT` | | Comma-separated URLs of folders whose file downloads urlstat counts, such as `https://changkun.de/research/` |
| `WWW_CORS` | | Comma-separated path prefixes any origin may fetch from, such as `/demos/` |
| `LLM_BASE_URL` | `https://api.latere.ai/v1/models` | Lux gateway root |
| `LLM_API_KEY` | | Lux Key |
| `LLM_MODEL` | `anthropic/claude-opus-5.5` | Writes the deep dive |
| `LLM_TITLE_MODEL` | `anthropic/claude-haiku-5.5` | Titles, slugs, polishing, translation |
| `GIT_TOKEN` | | GitHub token that can write to `GIT_REPO` |
| `GIT_REPO` | `changkun/blog` | Target repository, as `owner/repo` |
| `GIT_COMMITTER_NAME` | `Changkun Ideas API Server` | Name on the commits |
| `GIT_COMMITTER_EMAIL` | `hi+ideas@changkun.de` | Email on the commits |
| `AUTH_ALLOWED_PRINCIPALS` | | Comma-separated emails or principal ids allowed to post. Empty rejects every token |
| `AUTH_ALLOWED_CLIENTS` | `changkun-blog` | Comma-separated OAuth client ids whose tokens are accepted |
| `AUTH_URL` | `https://auth.latere.ai` | latere auth issuer |
| `AUTH_JWKS_URL` | `$AUTH_URL/.well-known/jwks.json` | JWKS that verifies tokens |

## License

MIT | Copyright &copy; 2016 - 2026 [Changkun Ou](https://changkun.de)
