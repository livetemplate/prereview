# Security

prereview is a local web server that you and your coding agent share while you review a change. It trusts your user account. It guards the review against other web pages in your browser, other people on your machine or network, and the files under review. This page lists who can reach a review, what prereview reads, writes, connects to and runs, and what it does not protect.

Line references are to v0.29.1.

## Short answers

- **Works offline.** Every script, stylesheet and font is built into the binary (`internal/assets/assets.go:24`). prereview makes no network call unless you turn updates on.
- **Downloads nothing at startup.** Automatic updates are off unless `PREREVIEW_AUTO_UPDATE=1` is set (`internal/update/update.go:224`). `prereview --update` checks once, on demand.
- **Reports nothing.** There is no telemetry. The only outbound connections are the update check you opt into, the site you name with `--external`, and `--replace` asking the old server for its pid. See [Network](#network).
- **Changes your files only when you restore a version.** Restore version writes the file you restored. Everything else prereview writes goes to its own folders: the review store, the library, the agent skill folders, the cache, and the binary during an update. See [What prereview writes](#what-prereview-writes).
- **28 Go modules are linked.** See [Dependencies](#dependencies).

## Who can reach a review

| Who | Trusted | What stands in the way |
|---|---|---|
| The browser that opened the printed link | Yes | Nothing |
| Your coding agent, and any process running as your user | Yes | Nothing. It can read the review store, `server.pid`, and wherever the agent wrote the server's output. |
| Other web pages in your browser | No | The origin check on WebSocket and HTTP actions (livetemplate v0.25.8 and later, set up at `server.go:138`), and a `SameSite=Strict` cookie (`token.go:93`). |
| Other users on the machine, and tailnet peers | No | The launch token (`token.go:81`). prereview binds to `127.0.0.1` unless you're on an SSH session with a tailnet (`internal/netaddr/netaddr.go:165`). |
| The files under review | No | Repo files are served with `Content-Security-Policy: sandbox` and `nosniff` (`server.go:666`). Dot files and file types off the allowlist are refused, by both the requested name and the file it resolves to (`server.go:676`, `server.go:683`). Reads stay inside the review root (`gitdiff/pathguard.go:20`). git treats every path as a literal name (`gitdiff/gitdiff.go:402`). The HTML preview runs in a sandboxed iframe with no access to prereview's origin. |
| The site behind `--external` | No | The proxy drops prereview's cookies before forwarding (`internal/proxy/proxy.go:70`), and the proxy port needs the token (`server.go:512`). |
| GitHub, for updates | Partly | The download is checked against the release's `checksums.txt` (`internal/update/update.go:468`). It is not signed. |

## The printed link

The link prereview prints carries a token: 32 random bytes, new for each launch, in `?token=` (`token.go:41`).

- **Opening the link sets a cookie.** The server answers with a 303 that sets a cookie and drops the token from the address bar (`token.go:81`). The cookie is `HttpOnly` and `SameSite=Strict`. Its name is `prereview-` plus a hash of the token, so two reviews on one host keep separate cookies.
- **Everything else needs the cookie.** A request without it gets 403. Only the built-in assets and `/_server-pid` are open (`server.go:362`). Tokens are compared in constant time.
- **The token stays out of logs and headers.** Every response sets `Referrer-Policy: no-referrer` (`token.go:128`). When the server's output goes to a file, prereview makes that file readable by you only (`token.go:140`).
- **The HTML preview uses a separate key.** The sandboxed preview iframe gets no cookie, so it loads repo files through `/_files/<key>/`, a second per-launch key that only reads repo files (`token.go:110`). A root-absolute CSS `url(/…)` inside a previewed file isn't rewritten to use the key, so it doesn't load.
- **The library review has its own link.** "Review in prereview" starts a child server with its own token and sends you to it (`library_http.go:154`).
- **A restart makes a new token.** Tabs from the old server get 403 until you open the new link.

## Where it listens

- **Bind address.** `127.0.0.1` by default (`main.go:259`). On an SSH session with a Tailscale address, prereview binds to that address instead (`internal/netaddr/netaddr.go:165`). An explicit `--host` is used as given, including `0.0.0.0`.
- **Port.** A random free port unless you pass `--port`, or `--public-url` carries one (`main.go:257`).
- **Allowed origins.** Without `--public-url`, a request must come from the page's own origin. `--public-url` replaces that check with a list: the public URL, the bind address, the tailnet addresses, and on a loopback bind `localhost` and `127.0.0.1` (`internal/netaddr/netaddr.go:216`).
- **Extra listeners.** `--external` opens a second listener for the proxy (`server.go:504`). The library review is a separate prereview process on its own random port.

## Network

| Connection | When | What is sent | Where |
|---|---|---|---|
| `https://api.github.com/repos/livetemplate/prereview/releases/latest` | At launch only with `PREREVIEW_AUTO_UPDATE=1`, at most once an hour; or on `--update`. Never for Homebrew or Scoop installs. | A GET with no credentials | `internal/update/update.go:46` |
| The release archive and `checksums.txt`, at the URLs GitHub returns | Only when a newer release exists | GETs. The archive's sha256 must match its line in `checksums.txt`. | `internal/update/update.go:468` |
| The `--external` target | Every request to the proxy port, after the token check | The whole request, including `Authorization` and every cookie except prereview's own | `internal/proxy/proxy.go:70` |
| `http://<recorded address>/_server-pid` | `--replace`, when the pid in `server.pid` is alive | A GET with a 2-second timeout | `server_pid.go:118` |

No other code opens a connection. git, Vale, the skill sync and the library make no network calls.

## What prereview runs

| Program | Purpose | When |
|---|---|---|
| `git` | Lists and diffs files: `ls-files`, `diff`, `rev-parse`, `for-each-ref`, `cat-file -e`, `status --porcelain`. Always with `--literal-pathspecs` (`gitdiff/gitdiff.go:402`). A base ref picked in the browser can't start with `-` (`gitdiff/gitdiff.go:224`). | Repo mode |
| `tailscale status --json` | Finds this machine's MagicDNS name for the printed links (`internal/netaddr/netaddr.go:97`) | Any launch on a machine with a 100.64.0.0/10 address |
| `vale` | Runs the repo's own Vale config (`internal/checks/vale.go:69`) | Only after you click Trust for that repo (`internal/review/controller_checks.go:757`) |
| Command checks | Runs the `command:` of a check rule on each checked file (`internal/checks/command.go:44`) | Only rules from the built-ins or your own checks folder may run a command (`internal/checks/rule.go:283`). Rules in a reviewed repo are never loaded. |
| `prereview` (library review) | `prereview --agent --no-update` on the library folder (`library_http.go:154`) | When you click "Review in prereview" in the Library |
| `prereview` (after an update) | Re-runs the updated binary with the same arguments | Only after an automatic update at launch |

prereview never opens a browser or runs a shell.

## What prereview writes

| Location | What | When |
|---|---|---|
| `<review folder>/.prereview/`, or `--out` | The review store: `comments.csv`, `server.pid` and `versions/` (snapshots of the files under review). Beside them: the agent's reply, suggestion, question, walkthrough and done files; your decisions and answers; the import inbox and its ledger; your viewed marks and last looks. A single-file review uses `.prereview/files/<name>-<hash>/` beside the file (`store.go:273`). | At launch, and as you and the agent work |
| The reviewed files | Restore version writes the restored contents, or deletes the file if it didn't exist in that version (`internal/review/controller_versions.go:380`). The path must be a file from the review's own list, its folder must be inside the review root, and it must not be a link (`gitdiff/pathguard.go:48`). | Only when you click Restore |
| The library: `$PREREVIEW_LIBRARY_DIR`, else your config folder's `prereview/` (`internal/library/library.go:37`) | Learnings, sessions, shortcuts, benchmarks, generated skills, your copies of check rules, and the library's own review store. Learning jobs (`learn-jobs.jsonl`), the background learner's heartbeat (`learner.json`), and when you last saw the learnings added for you (`learn-seen.json`). | As you use the Library, and at launch to record the session. When a review queues a learning job, while a learner waits for one, and when you close the notice. |
| `ui-prefs.json` in the same config folder, or `$PREREVIEW_UI_PREFS_PATH` (`store.go:194`) | View settings, and the repos whose Vale config you trusted | When you change a view setting |
| Agent folders under `$PREREVIEW_HOME`, else your home folder (`internal/skills/owned.go:22`) | prereview's own skill or command for each agent you installed: `.claude/skills/prereview/`, `.codex/skills/prereview/`, `.agents/skills/prereview/`, `.gemini/commands/`, `.config/opencode/commands/`, `.cursor/rules/`, `.config/prereview/aider/`. Generated `learned-*` skills go into the same agents' skill folders. | `--install-skill`. At every launch, files already installed are refreshed to match the binary (`main.go:408`), unless you've edited them. |
| Agent memory files under the same home: `.claude/CLAUDE.md`, `.codex/AGENTS.md`, `.gemini/GEMINI.md`, and `.config/opencode/AGENTS.md` when it exists | A block between `<!-- prereview:learned-skills -->` markers telling the agent to use the `learned-*` skills. Text outside the markers is never changed, and a block you edited is left alone. Only where the agent's folder exists. | While you have a learned skill and "Tell agents to use these skills" is on; removed otherwise |
| `.claude/settings.json` under the same home | One `PostToolUse` hook that runs `prereview check --hook`. The rest of the file keeps its values and order; the previous bytes go to `settings.json.prereview-bak`. Only when `.claude/` exists. | Same as the memory files |
| Your cache folder's `prereview/` | `update-check.json`, and a marker for the one-time "updates are off" notice | At launch and on `--update` |
| The prereview binary | Replaced by a verified download, through temporary files in the binary's folder; deleted by `--uninstall`. Neither happens for Homebrew or Scoop installs. | `--update`, an automatic update, or `--uninstall` |
| The file the server's output goes to | Made readable by you only, because the output carries the link (`token.go:140`). A pipe or terminal is left alone. | At launch |
| The system temp folder | The page templates, in a folder only you can read, removed at exit | At launch |

Two side effects come from other programs. `git status --porcelain` may refresh `.git/index`, which is git's normal behaviour. Vale and command checks can write whatever their own configuration tells them to.

## What prereview reads

- **The review root.** The files under review, through git in a repo. Every path the browser sends must resolve inside the root (`gitdiff/pathguard.go:20`). A single-file review is confined to that file's folder, not to the file.
- **Past reviews, the first time you launch.** prereview walks your home folder, six levels deep, for `.prereview` stores to offer as learnings (`internal/library/extract.go:16`, `internal/library/scan.go:25`). It skips `node_modules`, `.git`, caches, `vendor` and similar folders, and reads each store's comments, replies, suggestions and decisions. It runs again only when you open that offer in the Library. `$PREREVIEW_SCAN_HOME` changes where it looks.
- **Agent folders, at every launch.** It checks for prereview's own files in the agent folders listed above, so it can refresh them. The Library's Skills tab also reads other skills' `SKILL.md` files in those folders.
- **Its own folders.** The library, `ui-prefs.json`, and the cache.
- **Your git configuration**, through git, as git always does.

## Dependencies

prereview links these 28 modules, from `go list -deps -f '{{with .Module}}{{.Path}} {{.Version}}{{end}}' . | sort -u`, leaving out prereview itself:

```
aead.dev/minisign v0.2.0
github.com/alecthomas/chroma/v2 v2.24.1
github.com/cespare/xxhash/v2 v2.3.0
github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f
github.com/dlclark/regexp2 v1.12.0
github.com/gabriel-vasile/mimetype v1.4.12
github.com/google/uuid v1.6.0
github.com/go-playground/locales v0.14.1
github.com/go-playground/universal-translator v0.18.1
github.com/go-playground/validator/v10 v10.30.1
github.com/gorilla/websocket v1.5.3
github.com/hexops/gotextdiff v1.0.3
github.com/json-iterator/go v1.1.12
github.com/leodido/go-urn v1.4.0
github.com/livetemplate/livetemplate v0.25.8
github.com/minio/selfupdate v0.6.0
github.com/modern-go/concurrent v0.0.0-20180228061459-e0a39a4cb421
github.com/modern-go/reflect2 v1.0.2
github.com/redis/go-redis/v9 v9.17.2
github.com/yuin/goldmark-emoji v1.0.6
github.com/yuin/goldmark-highlighting/v2 v2.0.0-20230729083705-37449abec8cc
github.com/yuin/goldmark v1.8.2
golang.org/x/crypto v0.47.0
golang.org/x/net v0.49.0
golang.org/x/sys v0.40.0
golang.org/x/text v0.38.0
golang.org/x/time v0.14.0
gopkg.in/yaml.v3 v3.0.1
```

Two of them are linked but never used at runtime. `minisign` comes in with `minio/selfupdate`; prereview passes no signature verifier. `go-redis` comes in with livetemplate, which only connects to Redis when an app hands it a client, and prereview never does.

## What is not protected

- **Anyone with the link can drive the review.** The token is the access check. Treat the link like a password: it works until the server stops.
- **Processes running as you are trusted.** They can read the link from the terminal or the log file it went to, and the review store.
- **A previewed HTML file can read its files key.** The preview's own scripts can read the key from the page's `<base>` and send it elsewhere. With it, anyone who can reach the server's port can read the repo files prereview would serve (allowlisted types, no dot files) until the server stops. The key doesn't reach the review page, its actions or its socket.
- **Updates are checked, not signed.** The checksum guards against a corrupt download, not against a compromised release or GitHub account. Updates are off by default.
- **`--external` forwards everything to its target.** That includes `Authorization` headers and the target's cookies, to any http or https host you name. It also strips the target's framing and CSP headers so the page can render inside the review. Point it only at a site you would open yourself.
- **Learnings can reach your skills without a click.** With "Accept automatically" on (the default), what the background learner proposes for a learning job is accepted at once and installed in every agent's `learned-*` skills. The learner reads review stores, which also hold text agents wrote, so a misleading record can become an instruction. Only a job's learnings skip the click, and only from the stores the job names; imported learnings always wait. The notice that announces them has Undo, and turning the option off makes each one wait for you.
- **The hook runs whatever `prereview` is first on your `PATH`.** Claude Code runs it after every Write or Edit, in every project. Something that can put a `prereview` ahead of yours on `PATH` already runs as you.
- **Command checks run with your environment** on every file they check. Only rules you or prereview wrote can run one.
- **There is no TLS.** Over a tailnet the connection is encrypted by WireGuard; elsewhere it is plain HTTP, and the cookie has no `Secure` flag. `--host 0.0.0.0` puts the review on every network the machine is on.
- **Older versions are exposed.** Before v0.29.0, any web page could run a review action over HTTP, because only the WebSocket checked the origin. Before v0.29.1, a link inside the repo with an allowed name could serve a file that would otherwise be refused. Upgrade to v0.29.1 or later.
