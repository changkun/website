# changkun.de

One Go binary serves changkun.de:

- `/` serves the static site embedded from `static/`.
- `/<folder>/` serves each folder of `WWW_DIR`, such as the books and the
  blog. See [Folders](#folders).
- `/ideas/` is an API that polishes, translates, and expands an idea with LLMs,
  then commits it as bilingual markdown to
  [changkun/blog](https://github.com/changkun/blog).
- `/library/` is Changkun's Zotero library, browsable by the owner and
  shareable by topic. See [Library](#library).

It also serves golang.design, from a folder. See [Sites](#sites).

## Usage

```
$ docker network create traefik_proxy
$ make build && make up
```

`make build` runs the tests, builds a Linux binary, and builds the image.

The site needs no configuration. Without the ideas credentials, the site still
serves and `/ideas/` answers 503; without the Zotero credentials, `/library/`
answers 503.

## Layout

| Path | Contents |
|---|---|
| `main.go` | Signal handling, the HTTP server, request logging |
| `site.go` | The embedded `static/` site and its file server |
| `www.go` | The folders of `WWW_DIR` and the sites of `WWW_SITES` |
| `internal/ideas/` | The ideas API |
| `internal/ideas/prompts/` | Model prompts, as Go text templates embedded in the binary |
| `internal/research/` | The research page, rendered from a checkout of changkun/research |
| `internal/blog/` | The blog, rendered from a checkout of changkun/blog |
| `internal/library/` | The library: Zotero sync, owner pages, shares, comments |

## Folders

A folder of `WWW_DIR` is served at `/<folder>/` from the moment it exists, so
publishing one takes an `rsync` or a `git clone` into `WWW_DIR`, and no
restart. In production `WWW_DIR` is `/www/changkun.de`, mounted read-only.

Folders are served like files on disk: a folder without an `index.html` is
listed, and symlinks are followed. Hidden files, such as `.git`, are not
served. A name the embedded site has, such as `books`, stays the site's.

File responses have no write timeout, since some are 80 MB.

`research` is a checkout of
[changkun/research](https://github.com/changkun/research), so a `git pull`
there publishes a paper or a change to the page. The website renders
`/research/` from the checkout's `README.md`, again whenever it changes, and
serves the page's stylesheet and portrait from the binary. A talk at
`/research/talks/<name>.pdf`, where `/talk/<name>.pdf` redirects, is found in
the dated folder that holds it. Without a `README.md` the folder is served like
any other.

`blog` is a checkout of [changkun/blog](https://github.com/changkun/blog),
which holds the posts and ideas as markdown in its `content` folder. The
website renders every page, list, tag, feed and the sitemap from it, and
builds them again within seconds of the checkout changing, so a `git pull`
there publishes a post. The addresses are those Hugo, which built the blog
before, gave the pages, and `internal/blog/testdata/live.txt` lists them;
`BLOG_CHECKOUT=<checkout> go test ./internal/blog` checks a checkout against
it. Math between `$` or `$$` reaches MathJax as written. Without a `content`
folder, as while the folder still holds Hugo's output, it is served like any
other.

## Sites

A site that `WWW_SITES` lists is served from a folder of its own, under the
rules of the folders above: every path of the host, and of its `www.` name,
comes from the folder, and none of changkun.de's pages or APIs answer for it.
In production golang.design is `/www/golang.design`, mounted read-only, which
its repositories publish to. A missing file is a plain 404, which traefik
replaces with the folder's `404.html`.

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

## Library

`/library/` serves the Zotero library. Zotero is the only source of entries
and topics (collections); the server keeps a cache it can rebuild at any time
and owns only shares and the comments on them. The design, with its reasoning,
is in [the design doc](https://claude.ai/artifact/4jUmk2S2wv4XXSe6fsWab2).

### Run it locally

With the 1Password CLI signed in:

```
$ make run
```

This serves the site at <http://localhost:8080>, syncs your library into `./data`, and serves it at
<http://localhost:8080/library/>. Without `op`, the site runs and `/library/`
answers 503. `LIBRARY_DEV_OWNER=1` makes requests from
this machine the owner, so no login is needed; it has no effect behind a
proxy. Share links work locally too: open one in a private window.

### Data

```
$LIBRARY_DIR/
  cache.json        Zotero items and collections; rebuilt by sync, safe to delete
  state/            the only data that exists nowhere else; back it up
    shares.yaml     every share: title, topics, members, tokens
    <id>.jsonl      one per share: comments, suggestions, hides; append-only
    read.txt        ids of inbox items marked read
    session.key     signs the owner's session cookie
```

Sync runs at start, every `SYNC_INTERVAL`, and on Sync now. It fetches only
what changed since the cached library version (Zotero Web API v3, read-only
key). On any error the site keeps serving the last good copy, the owner pages
show the error, and `/library/healthz` answers 503 once the last good sync is
an hour old.

### Routes

| Route | Who | Returns |
|---|---|---|
| `GET /library/` | owner | Topic tree and all entries |
| `GET /library/` | anyone else | Every public share's topics together, read-only, without comments; an empty library if none is public |
| `GET /library/t/<key>` | owner | One topic, with its subtopics' entries |
| `GET /library/e/<key>` | owner | One entry, with comments from every share |
| `GET /library/e/<key>` | anyone else | One entry of a public share, without comments; else the login page |
| `GET /library/inbox` | owner | Every comment and suggestion, unread in bold |
| `GET /library/shares` | owner | Every share, its links, revoke |
| `POST /library/sync` | owner | Sync now |
| `GET`, `POST /library/login` | owner | latere login, then a session cookie |
| `GET /library/s/<token>` | member | The share: entries, suggestions, comments |
| `GET /library/s/<token>/e/<key>` | member | One entry with the share's comments |
| `GET /library/p/<id>` | anyone | A public share, read-only, without comments |
| `GET /library/healthz` | anyone | 200, or 503 when sync is stale |

Every list accepts `?q=` (every word must match title, authors, venue, or
year), `?sort=` (`year`, `title`, `authors`, `venue`, `added`; `-` in front
for descending; default `-year`) and `?page=`. Share pages also accept `?t=`,
a topic inside the share. On a wide screen a list page has three panes:
topics, entries, and the selected entry with its comments; `?e=<key>` selects
an entry, and an entry's page with `?pane=1` returns only that pane. Adding
`.bib` to a list's path (`/library/all.bib` for the whole library) returns
the same entries as BibTeX.

### Login

The login page runs the same browser PKCE flow as the compose box, with
`/login-sdk.js`, so `https://changkun.de/library/login` must be on the
`changkun-blog` client's redirect URIs. The token is checked against
`AUTH_ALLOWED_PRINCIPALS` and `AUTH_ALLOWED_CLIENTS` once, and the server then
sets its own cookie for 30 days.

## Configuration

Copy `.env.template` to `.env`. The ideas API needs `LLM_API_KEY` and
`GIT_TOKEN`, the library `ZOTERO_USER_ID` and `ZOTERO_API_KEY`; everything
else has a default.

| Variable | Default | Description |
|---|---|---|
| `WEBSITE_ADDR` | `0.0.0.0:80` | Listen address |
| `WWW_DIR` | | Folder whose folders are served. Unset serves none |
| `WWW_CORS` | | Comma-separated path prefixes any origin may fetch from, such as `/demos/` |
| `WWW_SITES` | | Comma-separated `host=folder` pairs, such as `golang.design=/www/golang.design`. Each host, and its `www.` name, is served from its folder |
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
| `ZOTERO_USER_ID` | | Zotero user id, from zotero.org/settings/keys |
| `ZOTERO_API_KEY` | | Read-only Zotero key: library access only, notes and write off |
| `LIBRARY_DIR` | `/data` | Holds `cache.json` and `state/` |
| `SYNC_INTERVAL` | `15m` | Time between syncs, at least `1m` |
| `ZOTERO_API_URL` | `https://api.zotero.org` | Zotero Web API root |
| `LIBRARY_DEV_OWNER` | | `1` makes requests from this machine the owner, for local runs |

## License

MIT | Copyright &copy; 2016 - 2026 [Changkun Ou](https://changkun.de)
