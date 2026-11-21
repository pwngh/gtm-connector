# Real data

`make verify-real` runs real public text through the Go binary's text functions and compares each output
with what the Python package gave for the same text, as digests recorded in the repository. It downloads
datasets, so it runs only when you ask for it. The tests and CI never run it.

```sh
make verify-real                                  # every pinned dataset that is not opt-in
make verify-real DATASETS='earnings21 irc-disentanglement'
make verify-real LOCAL=/path/to/messages.jsonl    # a private file, see below
```

It needs Go and git. No Python runs.

## What it does

1. **Fetch.** Each dataset's files are listed in [`internal/realdata/pins`](../internal/realdata/pins) with
   their size and SHA-256, under the dataset's `base_url` in
   [`internal/realdata/manifest.json`](../internal/realdata/manifest.json). Each is downloaded over https into
   the cache and checked against its pin. A file already in the cache is checked, not downloaded again. A
   mismatch stops the run.
2. **Convert.** A converter in `internal/realdata` reads each dataset and writes its items: one per utterance,
   turn, message or field, and one per JSON file or JSON Lines line. Downloads are parsed by code in this
   repository and nothing in them is run.
3. **Compare.** The Go port runs over the items, and its block digests are compared with the ones recorded
   for the dataset in [`internal/realdata/recorded`](../internal/realdata/recorded) (see
   [Recorded digests](#recorded-digests)). The recorded digests are checked first. They must:
   - come from CPython 3.14.6 with Unicode 16.0.0 and the package `testdata/oracle/` was made from
   - be for this very items file, by its SHA-256 and its count of items
   - name exactly the functions below, with one SHA-256 digest for each block

   Digests that fail any check are refused, never compared.

| Input | Functions |
| --- | --- |
| Each text item | `safe_text` |
| | `sentences`, without and with `lines`, of the text and of its safe text |
| | each scanner the oracle fixtures record (`units_target`, `units_raw_link`, `units_sentence_end`, `units_opener`, `units_named`, `slack_mapping_percent`, `slack_callback_percent`) |
| Each JSON document | `strict_json`, without and with `parse_float=Decimal`: the value or the refusal |

## Where the data goes

The cache is `$GTM_REAL_CACHE`, or `~/.cache/gtm-connector/real`. Each dataset has its own folder:

| File | Holds |
| --- | --- |
| `files/` | the downloads, as the pins give them |
| `items.jsonl.gz`, `items.json` | the converted items, and what the converter counted |
| `report.json` | the comparison |

A cache filled before the Python package was removed may also hold `oracle.jsonl.gz`,
`oracle-digests.jsonl.gz` and `oracle.json`, the oracle's own records. Nothing reads them now.

The cache must be outside the repository. A path that reaches the repository through a link or in another
case is refused too.

Every write stays in its dataset's folder. A link there that leads out of the cache is refused, and each file
is written fresh under an unpredictable name and renamed into place. A conversion reads each download through
the file it checked, and checks it again before keeping the items.

Nothing real is committed. The repository holds the manifest, the pins, the recorded digests (SHA-256
digests and counts, no text), the fetch, the converters and the comparison.

## The report

> **The report holds counts, positions and digests, never text.**

For each dataset and each function it gives the items, how many of them gave a non-empty output, the matches
and the differences. It gives the same split by kind: `natural`, `role-played`, `elicited` or `invented`, as
the manifest gives it for a dataset or the converter for each meeting.

Each difference names the function and the block it is in, by the first and last line of the items file
the block covers:

```text
sentences at lines 16385 to 24576 (items 16385 to 24576); only block digests were recorded
```

Every item of a block whose digest differs counts as a difference of that function, so the counts are an
upper bound and the block says where to look.

The first row, `item (decoding the line)`, counts the items file's lines. A line Go decodes differently from
Python, or refuses, makes its block a difference there and in each function. Digests of another items file
are stale and refused.

### Invented fixtures

A regression fixture made from a difference is invented text that reproduces it.

A pattern the real text shows is kept the same way, as invented items in
[`internal/realdata/testdata/patterns.jsonl`](../internal/realdata/testdata/patterns.jsonl): sentences over
300 characters, character references such as `&amp;`, format characters other than ZWJ, control characters, a
sentence split after a Spanish abbreviation, and line breaks inside a message. The oracle fixtures record every
function of each item, and the Go tests compare against them, so the tests and CI hold both runtimes to it.

### When a run fails

A run that fails reports why and leaves no `report.json`. The other runs go on, and the command exits 2.

> **A run never passes without comparing.** A dataset whose converter finds nothing, or a run with no items,
> fails.

A dataset named in `DATASETS` that cannot be compared fails before anything is fetched: one with no
converter, one waiting for the owner, one not pinned, or one with no recorded digests. `realdata fetch ID` still fetches its files. Without
names, such datasets are skipped, each with what it waits for.

| Limit or exit status | Value |
| --- | --- |
| Differences the terminal shows | the first 20 |
| Differences `report.json` lists | the first 1,000 |
| Past those | differences are counted but not listed |
| Exit status 0 | no differences |
| Exit status 1 | differences |
| Exit status 2 | the run fails |

## A private file

`LOCAL=FILE` runs the Go port on a JSON Lines file outside the repository, one object a line with a string
`id` and a string `text`, and compares it with the oracle's records of the same file, which a run made before
the Python package was removed left in the folder `FILE.r0` beside it. Other keys are ignored and blank lines
skipped. A file with no such records cannot be compared, and the run fails. The records are checked item by
item: each difference is named by its line and, when Python's outputs are there, by where the outputs first
differ.

The report goes to `FILE.r0`, never to the cache or the repository.

- It is made with mode 0700 when absent.
- One that exists must be a folder, not a link, that you own with mode 0700, or the run stops.
- Each result is written as a fresh file with mode 0600 and renamed into place, so a link or a file left in
  `FILE.r0` is replaced, never written through.

The report names each item by its line, never by its ID. A crash in the Go port is counted as a difference
without its message.

> **`FILE.r0/oracle.jsonl.gz` holds Python's outputs, which contain the text.** Delete the folder when you
> are done.

## Recorded digests

`internal/realdata/recorded/ID.json` holds, for each dataset below that runs by default, what the Python
package gave on it: recorded on CPython 3.14.6 from the package `testdata/oracle/` was made from, before the
package was removed. It holds:

- the SHA-256 and the count of the items file it was recorded for
- for each block of 8,192 items, in the items file's order, the SHA-256 of one row `LINE INPUT` per item,
  where `INPUT` is the SHA-256 of the item's text or document
- for each function and each block, the SHA-256 of one row `LINE OUTPUT` per item the function runs on,
  where `OUTPUT` is the SHA-256 of the function's output in its canonical encoding, and the function's count
  of items

They hold digests and counts, never text. The digests belong to one items file: if a converter changes what it
writes, the comparison refuses them as stale.

`aishell-4-test` (opt-in) and `fomc-press-conferences` (no converter) were never recorded, so they cannot be
compared. `realdata fetch` and `realdata convert` still run on them.

## Datasets

| Dataset | Licence | Kind | Items |
| --- | --- | --- | --- |
| `ami-manual-1.6.2`: [AMI Meeting Corpus](https://groups.inf.ed.ac.uk/ami/corpus/) manual annotations | CC BY 4.0 | natural (EN, IN meetings), role-played (ES, IS, TS, IB) | one per segment: its words one space apart, punctuation joined to the word before it |
| `notsofar-1-240825.1`: [NOTSOFAR-1](https://huggingface.co/datasets/microsoft/NOTSOFAR) transcript JSON | CC BY 4.0 | natural where the meeting's metadata says `#NaturalMeeting`, role-played otherwise | each utterance's `text`; each JSON file |
| `earnings21`, `earnings22`: [Earnings-21 and -22](https://github.com/revdotcom/speech-datasets) `.nlp` files | CC BY-SA 4.0 (the transcripts) | natural | one per speaker turn, rebuilt from the tokens, cased as Rev's README defines the case column, with their punctuation; a line that is not UTF-8 ends the turn; Earnings-21's `wer_tags` JSON |
| `apptek-callcenter`: [AppTek Call-Center Dialogues](https://huggingface.co/datasets/apptek-com/apptek_callcenter_dialogues) diarization metadata | CC BY-SA 4.0 | role-played | each segment's `text`; each JSON Lines line |
| `hyperledger-slack`: [Hyperledger Slack archive](https://github.com/hyperledger-archives/slack-archive) `raw/` | Apache-2.0 | natural | each message's `text`, markup kept; each JSON file |
| `archival-slack`: [Archival-worthy Slack conversations](https://zenodo.org/records/3468559) | CC BY 4.0 | natural | each row's `message`; rows with every cell empty are padding, counted and skipped |
| `irc-disentanglement`: [IRC Disentanglement](https://github.com/jkkummerfeld/irc-disentanglement) `.raw.txt` | CC BY 4.0 (`data/`) | natural | the words of each message (`[HH:MM] <nick> words`), action (`[HH:MM]  * nick words` or `=== nick words`) and notice (`[HH:MM] -nick- words`); joins, parts, quits and nick changes are counted, not read |
| `apex-recipes`, `lwc-recipes`: Trailhead [apex-recipes](https://github.com/trailheadapps/apex-recipes) and [lwc-recipes](https://github.com/trailheadapps/lwc-recipes) sample data | CC0-1.0 | invented | each record's name, title and description fields; each JSON file |
| `fivetran-salesforce-seeds`: [dbt_salesforce](https://github.com/fivetran/dbt_salesforce) integration test seeds | Apache-2.0 | invented | each non-empty CSV cell |
| `aishell-4-test`: [AISHELL-4](https://www.openslr.org/111/) test set, opt-in (a single 5.2 GB download) | CC BY-SA 4.0 | elicited | each non-blank TextGrid interval or point, read by Praat's token rules; a file whose declared counts do not account for every value is refused |
| `fomc-press-conferences`: [FOMC press conference transcripts](https://www.federalreserve.gov/monetarypolicy/fomccalendars.htm) | public domain | natural | none yet: the PDFs are pinned, and no PDF text extractor is written |

The manifest's notes say what each licence covers and what it does not. A public corpus tests parsing and
scale, not whether a card is useful.

Some datasets wait for the owner: the ICSI Meeting Corpus, the ELITR Minuting Corpus, SEC EDGAR earnings-call
exhibits, the Future of Coding Slack export, the MSR 2020 Slack chats, CRMArena, Maven's CRM sample and GLEIF's
LEI files. Each needs an account, accepted terms or a decision only the owner can make. They are listed with
`"access": "owner"` and what the owner must do, and every run skips them with those steps.

## Adding a dataset

1. Add an entry to `internal/realdata/manifest.json`: an `id`, the dataset's `url`, `version`, `licence` and
   `licence_source` (the https page that states the licence), its `access` and `kind`, a `converter`, and the
   `base_url` its files are under. In `redirect_hosts`, list any host besides the base URL's that a download
   is redirected to (an archive host or a CDN). A redirect anywhere else is refused.
2. List the files.
   - A few files: write `pins/ID.tsv` with one line per file, `-<tab>-<tab>PATH`.
   - Many files in a git repository or a Hugging Face dataset: give a `listing` with the repository, a full
     revision and `include` patterns. It is listed from a clone of its trees alone, or one folder level at a
     time on the Hub, so audio beside the text is never fetched.
3. Run `go run ./cmd/realdata pin ID`. It fetches each file the first time, checks it against the git blob ID
   or LFS digest its source lists where there is one, and writes its size and SHA-256 into `pins/ID.tsv`,
   which you commit.
4. If no converter reads the format, add one to the `converters` map in `internal/realdata/convert.go`, with
   a test on an invented sample.

A dataset added now has no recorded digests, since nothing records Python's outputs any more, so it is
fetched and converted but not compared.

A dataset that needs an account, a registration or accepted terms has `"access": "owner"` and says in
`owner_steps` what the owner must do. Agents never fetch such a dataset. A dataset with `opt_in` runs only
when it is named.
