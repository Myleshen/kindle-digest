# kindle-digest

A single-binary Go CLI that turns a [Miniflux](https://miniflux.app) newsreader's last 24 hours of entries into one or more EPUB volumes and emails them to your Kindle. It fetches entries from the Miniflux API, sanitizes the article HTML, downloads and normalizes the images for e-ink, packs the articles into size-capped parts, and sends each part as its own attachment.

## Features

- Fetches every entry published in the last 24 hours (read or unread), following pagination — or any explicit window with `-since`/`-until`.
- Strips scripts, styles, iframes, forms, navs, buttons, SVG and embedded media — `video`, `audio`, `object`, `embed`, `canvas`, `frame`, `frameset`, `applet` — from article HTML, subtrees included. Images, `picture` and `figure` are kept.
- Downloads article images, flattens transparency onto white, converts WebP to JPEG, resizes to at most 800px wide at JPEG quality 75, with a per-article image budget.
- Builds EPUB3 volumes with a generated cover, e-ink-friendly CSS and one section per article.
- Groups the daily digest by feed: sections are alphabetical by feed title (case-insensitive), articles are newest first, and each feed opens on a separator page with its title, article count and publication dates.
- Adds a hand-built Contents page per part linking to those feed sections, plus an A–Z Index of every article in the part (`#` for titles starting with a digit, symbol or non-ASCII letter). Collection exports get the Index only.
- Greedy-packs articles into parts, backing off to the last feed boundary instead of splitting a feed when the shortened part stays at least half full.
- Emails each part to your `@kindle.com` address, one message per part, or runs generate-only when mail settings are absent.
- Optionally folds newsletter emails into the same digest, read read-only over IMAP: one entry per email, grouped under the source's name, with images stripped.
- Serves an optional single-page web UI (`-serve`) to trigger today's digest or a custom feed/window run and watch its status.
- Logs to a file and exits non-zero on failure.

## Requirements

- **A Miniflux instance** with the v2 API reachable over HTTP(S), and an API key from **Settings → API Keys**.
- **An SMTP account** that allows authenticated submission with TLS, ideally using an app password rather than your main account password.
- **A send-to-Kindle address**: your device's `@kindle.com` address, found in Amazon's *Manage Your Content and Devices → Devices*.
- **Amazon's "Approved Personal Document Email list"**: add the sender address (`smtp_from`) to that list in your Amazon account. Documents from unapproved senders are silently dropped by Amazon — no bounce, no error, they just never arrive.

## Quickstart

Install with Go:

```sh
go install github.com/myleshen/kindle-digest@latest
```

Or build from source (Go 1.27+):

```sh
git clone https://github.com/myleshen/kindle-digest
cd kindle-digest
go build -o kindle-digest .
```

Cross-compile for a small server or Raspberry Pi:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o kindle-digest .
```

Copy the example config and fill it in:

```sh
cp config.example.json config.json
chmod 600 config.json
```

A complete config looks like this:

```json
{
  "miniflux_url": "https://miniflux.example.com",
  "miniflux_api_key": "your-miniflux-api-key",
  "kindle_email": "you@kindle.com",
  "smtp_host": "smtp.example.com",
  "smtp_port": 587,
  "smtp_username": "you@example.com",
  "smtp_password": "your-app-password",
  "smtp_from": "you@example.com",
  "output_dir": "/opt/kindle-digest/output",
  "log_file": "/opt/kindle-digest/kindle-digest.log",
  "max_part_bytes": 4500000,
  "web_token": "long-random-string-for-the-web-ui",
  "ntfy_url": "https://ntfy.example.org",
  "ntfy_topic": "kindle-digest",
  "ntfy_token": "ntfy-access-token",
  "ntfy_click": "",
  "imap_host": "imap.zoho.in",
  "imap_port": 993,
  "imap_username": "you@example.com",
  "imap_password": "your-app-password",
  "max_emails_total": 5,
  "email_sources": [
    {
      "name": "Example Newsletter",
      "folder": "Newsletters",
      "from": "newsletter@example.com",
      "from_name": "",
      "subject_match": "",
      "max_per_day": 1
    }
  ]
}
```

Run it:

```sh
./kindle-digest -config /path/to/config.json
```

Schedule it with cron:

```cron
45 6 * * * /path/to/kindle-digest -config /path/to/config.json
```

## Usage

### One feed (collection mode)

`-feed` exports a single feed instead of the daily digest. It accepts a numeric feed id, an exact title (case-insensitive) or a unique title substring; `-limit` defaults to 50 and is clamped to 1–500. Use `-list-feeds` to see the ids and titles Miniflux reports, one `<id>\t<title>` per line:

```sh
./kindle-digest -config config.json -list-feeds
./kindle-digest -config config.json -feed "Ars Technica" -limit 20
./kindle-digest -config config.json -feed 42 -limit 20 -send
```

On-demand runs are **generate-only** unless you pass `-send`; `-send` needs a complete SMTP block in the config and fails with an error otherwise. The plain daily run needs no `-send` (it sends from the config), so `-send` on its own is an error. The output is named `<Feed title> - N articles - <date>.epub`, with `, Part N` appended when the article count needs more than one volume.

### Time windows

`-since` and `-until` replace the default last-24-hours window. Both accept RFC3339 (`2026-11-26T06:00:00+05:30`), `2006-01-02T15:04` and `2006-01-02`; the two short forms are read in `Asia/Kolkata`, and a bare date means local midnight. The window is half-open, `[since, until)`, and `-until` defaults to now. `-until` without `-since` is an error.

```sh
./kindle-digest -config config.json -since 2026-11-26 -until 2026-11-27 -send
```

Windowed runs are named `Daily Digest - 26 Nov 2026 to 27 Nov 2026.epub` (a single date when the window sits inside one day). **A windowed run only sends with an explicit `-send`** — the plain daily cron run keeps auto-sending from the config. Combine with `-feed` for one feed's window.

### Email sources

Newsletter emails can join the same daily digest as a second content source. Each configured source is a folder plus a sender filter, and every matching email becomes one entry: the subject is the title and the email body is the article. Email sections merge into the same alphabetical grouping, Contents page and A–Z Index as the RSS feeds, under the source's `name` (for example `TLDR AI`).

```json
"email_sources": [
  {"name": "Example AI", "folder": "Newsletters", "from": "news@example.com", "from_name": "Example AI Daily", "max_per_day": 1},
  {"name": "Example Habits", "folder": "Weekly", "from": "james@example.com", "max_per_day": 1}
]
```

Selection, per run:

- The mailbox is opened read-only: `EXAMINE` instead of `SELECT`, headers and bodies via `BODY.PEEK[]` only. Messages are never marked `\Seen`, moved, copied or deleted.
- Candidates are searched with IMAP `SINCE` (date granularity) and then filtered on the message's internal date to the same `[since, until)` window as the RSS digest.
- `from` must equal the sender address (case-insensitive); `from_name` and `subject_match`, when set, must also match (case-insensitive, substring for the subject). `from_name` exists because senders such as TLDR run several editions from one address.
- At most `max_per_day` newest emails are kept per source, then at most `max_emails_total` newest across all sources. Every drop is logged with its reason, and duplicate Message-IDs are dropped once per run.
- Bodies prefer the `text/html` part, falling back to paragraphs from `text/plain`, and are decoded with the declared charset. The existing sanitizer removes scripts, styles, iframes, forms and inline CSS; **all images are stripped** (tracking pixels and logos included) while links are kept.
- Accepted emails are logged with source, subject, received time, word count and estimated reading minutes at 220 wpm.

Failures never block the digest: if a folder cannot be read, the error is logged with the source and folder, the remaining sources and all RSS entries still build and send, and the process exits non-zero at the end to flag the partial failure. If every email source fails, the RSS-only digest still goes out.

Email bodies are used as-is: links in them are never fetched or followed, and no remote content is downloaded from them. `email_sources` empty (or absent) turns the feature off entirely and all IMAP fields are ignored.

### Web UI

`-serve <addr>` starts a small dashboard. The config must set `web_token`; the process refuses to start without it and never logs the token. Sign in once with that token and the browser keeps an HttpOnly cookie for 30 days.

```sh
./kindle-digest -config config.json -serve 0.0.0.0:8090
```

Two actions: **Send today's digest now** (the same daily run as cron, following the config's send mode) and a **custom run** — any feed or all feeds, optional since/until dates, a limit of 1–500 and an explicit *Send to Kindle* checkbox. The page polls `/api/status`, so the current run and the last five runs (kept in memory, lost on restart) show their outcome, parts and sizes without a reload. Only one run happens at a time: the UI single-flights and takes the same `flock` in `output_dir` as a CLI or cron run, so overlapping runs are rejected instead of racing.

The UI deliberately has no EPUB download or file browser — it triggers runs and reports status, and files stay on disk under `output_dir`. It has no TLS or user management of its own, so put it behind your reverse proxy or VPN and treat `web_token` like a password.

### ntfy notifications

Set `ntfy_url` (for example `https://ntfy.example.org`), `ntfy_topic` and a token with publish permission on that topic as `ntfy_token` to get a push when a run finishes. `ntfy_click` is optional: it is the URL opened when the notification is tapped. All four fields are optional, but the first three must be set together — a partial block is a fatal config error.

- **Send mode, success:** one notification titled `Daily Digest ready` (the run's own title for windowed and collection runs) with the article count, part count and total size. Priority 3, tag `books`.
- **Any failed run:** one notification titled `Digest failed` with the error text (truncated to 200 characters). Priority 4, tag `warning`.
- **Generate-only runs:** no notification, because nothing reached the Kindle.
- **Best-effort:** if the notification itself fails, the error is logged and the run's exit code is unchanged — a broken ntfy server never fails the digest.

The ntfy server must allow the token to publish to the topic (ntfy's write-only access token covers this).

Docker Compose (Alpine plus the cross-built binary; the project directory is bind-mounted at the same absolute path so the paths in `config.json` keep working):

```yaml
services:
  kindle-digest:
    image: alpine:3.20
    command: sh -c "apk add --no-cache ca-certificates && exec /opt/kindle-digest/kindle-digest -config /opt/kindle-digest/config.json -serve 0.0.0.0:8090"
    volumes:
      - /opt/kindle-digest:/opt/kindle-digest
    ports:
      - "8090:8090"
    restart: unless-stopped
```

## Configuration

| Field | Meaning | Default / validation |
|---|---|---|
| `miniflux_url` | Base URL of your Miniflux instance | Required; must be `http` or `https` with a host |
| `miniflux_api_key` | Miniflux API key (sent as `X-Auth-Token`) | Required |
| `kindle_email` | Send-to-Kindle address for your device | Optional; empty means generate-only |
| `smtp_host` | SMTP server hostname | Required when `kindle_email` is set |
| `smtp_port` | SMTP submission port | Required when `kindle_email` is set; 1–65535 (587 typical) |
| `smtp_username` | SMTP username | Required when `kindle_email` is set |
| `smtp_password` | SMTP password or app password | Required when `kindle_email` is set |
| `smtp_from` | From address; must be on Amazon's approved sender list | Required when `kindle_email` is set |
| `output_dir` | Directory for generated EPUBs; each run writes to `<output_dir>/YYYY-MM-DD/` | Required |
| `log_file` | File to append log lines to; stdout/stderr always get them too | Optional; empty disables file logging |
| `max_part_bytes` | Target size of each EPUB part | Default `4500000`; must be 100000–50000000 (Amazon's per-attachment limit) |
| `web_token` | Shared secret for the web UI login | Required only with `-serve`; the server refuses to start if empty |
| `ntfy_url` | Base URL of your ntfy server, e.g. `https://ntfy.example.org` | Optional; required together with `ntfy_topic` and `ntfy_token` |
| `ntfy_topic` | ntfy topic to publish on | Optional; required together with `ntfy_url` and `ntfy_token` |
| `ntfy_token` | ntfy access token with publish permission on the topic | Optional; required together with `ntfy_url` and `ntfy_topic` |
| `ntfy_click` | URL opened when the notification is tapped | Optional; valid alone or with the other ntfy fields |
| `imap_host` | IMAP server for email sources | Default `imap.zoho.in`; only read when `email_sources` is non-empty |
| `imap_port` | IMAP implicit-TLS port | Default `993`; 1–65535 |
| `imap_username` | IMAP username | Required when `email_sources` is non-empty |
| `imap_password` | IMAP password or app password | Required when `email_sources` is non-empty |
| `max_emails_total` | Most emails in one digest, newest first across sources | Default `5`; at least 1 |
| `email_sources` | Array of `{name, folder, from, from_name, subject_match, max_per_day}` | Empty or absent disables email sources |
| `email_sources[].name` | Section name in the digest | Required |
| `email_sources[].folder` | IMAP folder to read read-only | Required |
| `email_sources[].from` | Sender address, matched exactly and case-insensitively | Required |
| `email_sources[].from_name` | Sender display name, matched exactly | Optional |
| `email_sources[].subject_match` | Case-insensitive subject substring | Optional |
| `email_sources[].max_per_day` | Newest emails kept per source per run | Default `1` |

Mode rules:

- `kindle_email` empty → **generate-only**. EPUBs are written to `output_dir`; nothing is sent. Other SMTP fields are ignored.
- `kindle_email` set and all SMTP fields present → **send mode**. One email per part, with a pause between sends.
- `kindle_email` set but any SMTP field missing → **fatal config error**; the run exits non-zero before doing anything.

## Typography

The generated CSS is tuned for e-ink readers and for the Kindle's own accessibility settings:

- Only relative font sizes (`1em`, `1.6em`, `1.3em`, `1.1em`, `0.85em`, `0.9em`), never `px` or `pt`, so the Kindle's size slider and per-book font settings keep working.
- No embedded font files. The stacks name fonts already on the device — Bookerly/Caecilia for body text, Amazon Ember for headings, Courier for code — and fall back to Georgia/Arial/monospace elsewhere. Embedding fonts would bloat every part, override the reader's chosen typeface and defeat those same controls.
- Left-aligned text and 1.5 line-height rather than justified text, which avoids the wide word gaps e-ink is prone to, and EPUB `dc:language` `en` so hyphenation and the dictionary work.

## Kindle gotchas

- **The attachment filename becomes the book title in your Kindle library.** This tool names parts `Daily Digest - <date>, Part N.epub` in pure ASCII for that reason. Non-ASCII filenames came out malformed as titles.
- **Zoho Mail on the India or EU datacenter needs the regional SMTP host.** Accounts hosted on Zoho's Indian datacenter must use `smtp.zoho.in`, and EU accounts `smtp.zoho.eu`; the generic `smtp.zoho.com` endpoint fails authentication with SMTP 535 errors for those accounts.
- **Amazon caps email attachments at 25 files per message and 50 MB per file.** Large or complex EPUBs are also rejected by some send-to-Kindle paths regardless, so parts default to a 4.5 MB target; `max_part_bytes` can be raised to 50 MB, Amazon's per-attachment limit. More than one part per day is normal.
- **Digest quality depends on what Miniflux stores.** Feeds that only publish summaries (common for paywalled or aggregated sites) produce thin articles. Enable the per-feed **Fetch original content** option in Miniflux so the digest gets the full article HTML.
- **One embedded video can turn the whole digest into a PDF.** Amazon's EPUB converter silently renders a book as PDF when it finds embedded media, and a single `<video>` in one article is enough. E-ink EPUBs do not need media embeds, so the sanitizer removes them and their subtrees; a `<source>` inside a kept `<picture>` is untouched.
- **Send-to-Kindle converts EPUBs to AZW3 a few minutes after delivery.** The book may appear briefly as a document and then re-render; that is normal.
- Dates and the 24-hour window are computed in `Asia/Kolkata` (IST), matching the intended daily reading schedule.

## How it works

1. **Fetch** — lists feeds and pages through `/v1/entries` for entries inside the window (last 24 hours by default, or `-since`/`-until`), sorted by feed title and newest first.
2. **Email sources** — when configured, reads each folder read-only over IMAP, filters by sender, window and caps, and decodes the selected messages into entries alongside the feed entries.
3. **Sanitize** — parses each entry's HTML, drops non-content elements, and rewrites image references. Email bodies skip image handling entirely; their images were already stripped.
4. **Images** — downloads each image concurrently (bounded), flattens it onto white, downscales to ≤800px wide, re-encodes as JPEG q75, and drops the largest images if an article exceeds its 2.5 MB image budget.
5. **Build** — groups the digest by feed or email source with a separator, Contents and Index page per part (collection mode gets the Index only), and writes each EPUB3.
6. **Pack** — greedily fills parts by estimated size, pulls each cut back to the last feed boundary if the shortened part is still at least half of `max_part_bytes`, then verifies the real file size against the 50 MB hard cap, splitting or shrinking only when it overflows.
7. **Send** — emails each part to the Kindle address over authenticated TLS SMTP, then exits non-zero if anything failed.

## License

MIT — see [LICENSE](LICENSE).
