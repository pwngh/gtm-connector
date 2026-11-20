# Oracle fixtures

`tools/oracle/` runs the Python package and writes what it does to `testdata/oracle/`. The Go port is checked
against these files: card text byte for byte, card JSON as values, and every table and scanner exactly.

## What it records

| Folder | What it holds |
| --- | --- |
| `unicode/` | Every code point under each class an evidence rule uses: `unicodedata.category`, `isspace`, `isdigit`, `isdecimal`, `isprintable`, `islower`, `isupper`, the titlecase set, `isalpha`, `re`'s `\s`, `\d` and `\w`, and each letter and `[a-z]` under `IGNORECASE`. `str.lower` per code point, the two sets that decide a final sigma, and whole strings with `lower`, `islower` and `isupper`. Combining classes, NFD and NFC per code point, the canonical pairs that compose, and NFC of `a` followed by every ordered pair of combining marks where one of them is among the 34 code points whose class changes in Unicode 17. |
| `scanners/` | The matches of each pattern with a lookaround (`units.py` lines 118, 122, 222, 253 and 342, `slack_mapping.py` line 108, `slack_callback.py` line 163) over the fuzz tests' strings, every ordered pair of their source-text pieces, and 20,000 seeded random strings. `windowed.json.gz` holds the `search(line, pos, endpos)` calls the sentence rules make, and every window of a few short texts, where `^` never matches at `pos > 0`. |
| `corpora/` | `safe_text` of the fuzz strings and 100,000 seeded random strings, and `sentences` (with and without `lines`) of 100,000 more. |
| `library/` | `urlsplit` and the host a card shows, the `json.dumps` options the package uses, `Decimal` amounts, `datetime.fromisoformat` and the card's date rules, `strict_json` refusals, and the cases where Go will differ on purpose (backend plan section 4). |
| `cards/` | Every card the demo book, `tests/books` (through the demo stores, Salesforce, imported transcripts and live Slack), the varied accounts, `tests/slack_threads` and `tests/test_cases.py` build. Each line holds the inputs, the sealed file's exact text, its SHA-256 and its seal receipt. `demo_book.json` holds the demo stores. |
| `commands/` | `demo`, `connect`, `accounts`, `brief`, `inspect`, `audit` and `mcp` run in order against one demo folder: argv, exit code, and output. IDs, ports, token fingerprints, receipt times and folder paths are numbered placeholders, by where each first appears. Help text is kept for review and is not compared byte for byte. |
| `folders/` | A card folder saved by `brief` and a brief prepared with `prepare_renewal_brief`, as a verifier admits them: the card files, the audit log as gzipped SQLite, and its rows. |

`manifest.json` names the interpreter, the Unicode version, a digest of `gtm_connector/`, and each file with the
SHA-256 of what it holds (of the rows, for an SQLite file).

Inside the generator, receipts start at 2027-01-15T15:00:00Z and are one second apart, elapsed times are 0, and
IDs and tokens come from counters named by each card. Only calls from `gtm_connector` get these values, so the
HTTPS services and TLS keep the real clock.

## Regenerate

The fixtures are made on CPython 3.14.6 with Unicode 16.0.0. The generator refuses any other interpreter.

```sh
.venv/bin/python -I tools/oracle/generate.py
```

It takes about 40 seconds and writes the same bytes on every run. `tests/test_oracle_fixtures.py` regenerates
them into a temporary folder and fails when any file differs. It is skipped on another interpreter.

A Python fix regenerates its fixtures in the same pull request. The diff of `testdata/oracle/` is part of the
review: it shows what the fix changed in what the package does.
