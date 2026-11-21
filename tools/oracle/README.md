# Oracle fixtures

`tools/oracle/` runs the Python package and writes what it does to `testdata/oracle/`. The Go port is checked
against these files: card text byte for byte, card JSON as values, and every table and scanner exactly.

## What it records

| Folder | What it holds |
| --- | --- |
| `unicode/` | Every code point under each class an evidence rule uses: `unicodedata.category`, `isspace`, `isdigit`, `isdecimal`, `isprintable`, `islower`, `isupper`, the titlecase set, `isalpha`, `re`'s `\s`, `\d` and `\w`, and each letter and `[a-z]` under `IGNORECASE`. `str.lower` per code point, the two sets that decide a final sigma, and whole strings with `lower`, `islower` and `isupper`. Combining classes, NFD and NFC per code point, the canonical pairs that compose, the code points whose NFKC form holds one of `/ ? # @ :` (which `urlsplit` refuses in a netloc), and NFC of `a` followed by every ordered pair of combining marks where one of them is among the 34 code points whose class changes in Unicode 17, row by row. `nfc_all_marks.json` covers every ordered pair of the 968 marks after `a` (937,024 rows) and every canonical two-part decomposition with each mark between its parts (1,398,760 rows) as the inputs and a SHA-256 of the rows, which Go recomputes. |
| `scanners/` | The matches of each pattern with a lookaround (`units.py` lines 117, 122, 622, 652 and 742, and the two percent patterns that `slack_mapping.py` line 108 and `slack_callback.py` line 163 search with, each taken from its parser path as it runs) over the fuzz tests' strings, every ordered pair of their source-text pieces, and 20,000 seeded random strings. `windowed.json.gz` holds the `search(line, pos, endpos)` calls the sentence rules make, every window of a few short texts, where `^` never matches at `pos > 0`, and windows that reach outside the text or end before they start. |
| `corpora/` | `safe_text` of the fuzz strings and 100,000 seeded random strings, and `sentences` (with and without `lines`) of 100,000 more. `patterns.json` holds every R0 function of each invented item in `internal/realdata/testdata/patterns.jsonl`, which stand for text patterns real data showed. |
| `library/` | The host a card shows for a link, the HTML5 named character references and how a Markdown target reads its escapes and references, the `json.dumps` options the package uses, `Decimal` amounts, `datetime.fromisoformat` and the card's date rules, `strict_json` values and refusals (also with `parse_float=Decimal`), including floats whose digits and exponent cancel, and the cases where Go will differ on purpose (backend plan section 4). |
| `cards/` | Every card the demo book, `tests/books` (through the demo stores, Salesforce, imported transcripts and live Slack), the varied accounts, `tests/slack_threads` and `tests/test_cases.py` build. Each line holds the inputs, the sealed file's exact text, its SHA-256 and its seal receipt. `demo_book.json` holds the demo stores. A case's input marks the card its test checks as the target, with the rows its test inserts into the seeded demo stores and the `sellers.json` it writes. Every book in `tests/books` is read, whatever `GTM_BOOKS` says. `evidence.jsonl.gz` holds every `units.build` call of the tests that refuse evidence (`tests/test_units.py`, `test_salesforce_briefing.py` and `test_slack_briefing.py`): the evidence, and the units, text and gaps or the `BriefRejected` message, plus one synthetic call whose Slack file counts sum past 2**63. |
| `commands/` | `demo`, `connect`, `accounts`, `brief`, `inspect`, `audit` and `mcp` run in order against one demo folder: argv, exit code, and output. IDs, ports, token fingerprints, receipt times and folder paths are numbered placeholders, by where each first appears. Help text is kept for review and is not compared byte for byte. |
| `folders/` | A card folder saved by `brief`, a brief prepared with `prepare_renewal_brief`, one prepared with an explicit `as_of` (`prepared_as_of`), and one prepared for an account selected without Slack under a grant that includes `slack:read` (`prepared_no_slack`), as a verifier admits them: the card files, the audit log as gzipped SQLite, and its rows. `prepared/reads.jsonl` holds `read_renewal_brief` calls on the prepared brief, each with its response and receipts: a reread of the saved bytes, then refusals for a missing scope, changed bytes under the seal, a changed selection, and a token at the second it expires (and one second before). |

`manifest.json` names the interpreter, the Unicode version, a digest of `gtm_connector/`, and each file with the
SHA-256 of what it holds (of the schema, column names and rows, for an SQLite file).

Inside the generator, receipts start at 2027-01-15T15:00:00Z and are one second apart, elapsed times are 0, and
IDs and tokens come from counters named by each card. Authorization code and token expiry, the gateway's wall time
and rate window, brief deadlines and Salesforce's operation budget read one clock that stands at that instant
unless a fixture advances it, as the token expiry reads do. Only calls from `gtm_connector`, and the MCP SDK's
own expiry checks, get these values, so the HTTPS services and TLS keep the real clock.

The generator starts itself again under a fixed host: UTF-8 mode, the C locale, UTC, no color, a fixed `PATH` and
terminal width, and no other variables but `HOME` and `TMPDIR`. It names any of `GTM_BOOKS`, `FORCE_COLOR`,
`CLICOLOR_FORCE` and `PYTHON_COLORS` it ignores. A temporary folder whose path the package shell-quotes is
normalized as the whole quoted argument.

## Regenerate

The fixtures are made on CPython 3.14.6 with Unicode 16.0.0. The generator refuses any other interpreter.

```sh
.venv/bin/python -I tools/oracle/generate.py
```

It takes about 40 seconds and writes the same bytes on every run. `tests/test_oracle_fixtures.py` regenerates
them into a temporary folder and fails when any file differs. It is skipped on another interpreter.

A Python fix regenerates its fixtures in the same pull request. The diff of `testdata/oracle/` is part of the
review: it shows what the fix changed in what the package does.

## Real text

`real.py` runs the package over an items file for `make verify-real` and records each output and its
digest beside it. It reads no fixture and writes none. [Real data](../../docs/real-data.md)
