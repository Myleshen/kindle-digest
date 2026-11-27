# kindle-digest

A single-binary Go CLI that turns a [Miniflux](https://miniflux.app) newsreader's last 24 hours of entries into one or more EPUB volumes and emails them to your Kindle. It fetches entries from the Miniflux API, sanitizes the article HTML, downloads and normalizes the images for e-ink, packs the articles into size-capped parts, and sends each part as its own attachment.

- Fetches every entry published in the last 24 hours (read or unread), following pagination.
- Strips scripts, styles, iframes, forms, navs, buttons and SVG from article HTML.
- Downloads article images, flattens transparency onto white, converts WebP to JPEG, resizes to at most 800px wide at JPEG quality 75, with a per-article image budget.
- Builds EPUB3 volumes with a generated cover, e-ink-friendly CSS, one section per article and an automatic table of contents.
- Greedy-packs articles into parts with a hard ≤5 MB cap, splitting and shrinking when needed.
- Emails each part to your `@kindle.com` address, one message per part.
- Runs generate-only (writes EPUBs, sends nothing) when mail settings are absent.
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
  "max_part_bytes": 4500000
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
| `max_part_bytes` | Target size of each EPUB part | Default `4500000`; must be 100000–5000000 |

Mode rules:

- `kindle_email` empty → **generate-only**. EPUBs are written to `output_dir`; nothing is sent. Other SMTP fields are ignored.
- `kindle_email` set and all SMTP fields present → **send mode**. One email per part, with a pause between sends.
- `kindle_email` set but any SMTP field missing → **fatal config error**; the run exits non-zero before doing anything.

## Kindle gotchas

- **The attachment filename becomes the book title in your Kindle library.** This tool names parts `Daily Digest - <date>, Part N.epub` in pure ASCII for that reason. Non-ASCII filenames came out malformed as titles.
- **Zoho Mail on the India or EU datacenter needs the regional SMTP host.** Accounts hosted on Zoho's Indian datacenter must use `smtp.zoho.in`, and EU accounts `smtp.zoho.eu`; the generic `smtp.zoho.com` endpoint fails authentication with SMTP 535 errors for those accounts.
- **Amazon caps email attachments at 25 files per message and 50 MB per file.** Large or complex EPUBs are also rejected by some send-to-Kindle paths regardless, so parts default to a 4.5 MB target with a hard 5 MB ceiling. More than one part per day is normal.
- **Digest quality depends on what Miniflux stores.** Feeds that only publish summaries (common for paywalled or aggregated sites) produce thin articles. Enable the per-feed **Fetch original content** option in Miniflux so the digest gets the full article HTML.
- **Send-to-Kindle converts EPUBs to AZW3 a few minutes after delivery.** The book may appear briefly as a document and then re-render; that is normal.
- Dates and the 24-hour window are computed in `Asia/Kolkata` (IST), matching the intended daily reading schedule.

## How it works

1. **Fetch** — lists feeds and pages through `/v1/entries?published_after=<unix>` for entries published in the last 24 hours, sorted oldest first.
2. **Sanitize** — parses each entry's HTML, drops non-content elements, and rewrites image references.
3. **Images** — downloads each image concurrently (bounded), flattens it onto white, downscales to ≤800px wide, re-encodes as JPEG q75, and drops the largest images if an article exceeds its 2.5 MB image budget.
4. **Pack** — greedily groups articles into parts by estimated size, builds each EPUB, and verifies the real file size against the 5 MB hard cap, splitting the part or shrinking its images/text if it overflows.
5. **Send** — emails each part to the Kindle address over authenticated TLS SMTP, then exits non-zero if anything failed.

## License

MIT — see [LICENSE](LICENSE).
