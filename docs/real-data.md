# Real data

`make verify-real` runs real public text through the Python package and the Go port and compares what
they do. It downloads datasets, so it runs only when you ask for it. The tests and CI never run it.

```sh
make verify-real                                  # every pinned dataset that is not opt-in
make verify-real DATASETS='earnings21 irc-disentanglement'
make verify-real DATASETS=aishell-4-test          # an opt-in dataset runs only when named
make verify-real LOCAL=/path/to/messages.jsonl    # a private file, see below
```

It needs Go, git, and the pinned CPython 3.14.6 with Unicode 16.0.0 (`PYTHON=`, as for the oracle fixtures;
the default is `.venv/bin/python`). The oracle fixtures are the reference outputs in `testdata/oracle/`,
which `tools/oracle/` writes from the Python package.

## What it does

1. **Fetch.** Each dataset's files are listed in [`internal/realdata/pins`](../internal/realdata/pins) with
   their size and SHA-256, under the dataset's `base_url` in
   [`internal/realdata/manifest.json`](../internal/realdata/manifest.json). Each is downloaded over https into
   the cache and checked against its pin. A file already in the cache is checked, not downloaded again. A
   mismatch stops the run.
2. **Convert.** A converter in `internal/realdata` reads each dataset and writes its items: one per utterance,
   turn, message or field, and one per JSON file or JSON Lines line. Downloads are parsed by code in this
   repository and nothing in them is run.
3. **Oracle.** `tools/oracle/real.py` runs the Python package over the items, under `-I`, and records each
   output and its SHA-256. It refuses a package that differs from the one `testdata/oracle/` was made from.
4. **Compare.** The Go port runs over the same items, and each output's SHA-256 is compared with Python's.
   The records are checked first: they must come from CPython 3.14.6 with Unicode 16.0.0 and the package
   `testdata/oracle/` was made from, name exactly each item's functions, hold only SHA-256 digests, and,
   when Python's outputs are there, be the digests of those outputs. Records that fail any check are refused, never compared.

| Input | Functions |
| --- | --- |
| Each text item | `safe_text`; `sentences`, without and with `lines`, of the text and of its safe text; each scanner the oracle fixtures record (`units_target`, `units_raw_link`, `units_sentence_end`, `units_opener`, `units_named`, `slack_mapping_percent`, `slack_callback_percent`) |
| Each JSON document | `strict_json`, without and with `parse_float=Decimal`: the value or the refusal |

## Where the data goes

The cache is `$GTM_REAL_CACHE`, or `~/.cache/gtm-connector/real`. Each dataset has its own folder:

| File | Holds |
| --- | --- |
| `files/` | the downloads, as the pins give them |
| `items.jsonl.gz`, `items.json` | the converted items, and what the converter counted |
| `oracle.jsonl.gz` | Python's outputs |
| `oracle-digests.jsonl.gz`, `oracle.json` | the SHA-256 of each input and each output, the interpreter and the items' digest |
| `report.json` | the comparison |

The cache must be outside the repository: a path that reaches the repository through a link or in another
case is refused too. Every write stays in its dataset's folder: a link there that leads out of the cache is
refused, and each file is written fresh under an unpredictable name and renamed into place. A conversion
reads each download through the file it checked, and checks it again before keeping the items.

Nothing real is committed. The repository holds the manifest, the pins, the fetch, the converters, the
comparison and the oracle runner.

## The report

The report holds counts, positions and digests, never text. For each dataset and each function it gives the
items, how many of them gave a non-empty output, the matches and the differences, and the same split by
kind: `natural`, `role-played`, `elicited` or `invented`, as the manifest gives it for a dataset or the
converter for each meeting. Each difference names the dataset file, the item ID, its kind, the function,
where the outputs first differ, and the SHA-256 of the input and of both outputs:

```text
segments/XX0001a.A.segments.xml XX0001a.sync.4 (role-played) sentences at output[2]: code point 17 (Python's string has 40 code points, Go's 41); input 3f2a..., Python 9c1b..., Go 0d7e...
```

`output[2]` is the third element of the output, here the third sentence. The terminal shows the first 20
differences and `report.json` lists the first 1,000; past those, differences are counted but not listed. The exit status is 0 with no differences, 1 with
differences, and 2 when the run fails. A regression fixture made from a difference is invented text that
reproduces it.

A pattern the real text shows is kept the same way, as invented items in
[`internal/realdata/testdata/patterns.jsonl`](../internal/realdata/testdata/patterns.jsonl): sentences over
300 characters, character references such as `&amp;`, format characters other than ZWJ, control characters, a
sentence split after a Spanish abbreviation, and line breaks inside a message. The oracle fixtures record every
function of each item, and the Go tests compare against them, so the tests and CI hold both runtimes to it.

The first row, `item (decoding the line)`, counts the items file's lines: a line Python and Go decode
differently is a difference there, with its line, and its functions are not compared. Records of another
items file are stale and refused. A run that fails reports why and leaves no `report.json`; the other runs go
on, and the command exits 2.

A run never passes without comparing: a dataset whose converter finds nothing, or a run with no items, fails.
A dataset named in `DATASETS` that cannot be compared (one with no converter, one waiting for the owner, or
one not pinned) fails before anything is fetched; `realdata fetch ID` still fetches its files. Without names,
such datasets are skipped, each with what it waits for.

## A private file

`LOCAL=FILE` runs the same comparison on a JSON Lines file outside the repository, one object a line with a
string `id` and a string `text`. Other keys are ignored and blank lines skipped. The results go to the folder
`FILE.r0` beside it, never to the cache or the repository. It is made with mode 0700 when absent; one that
exists must be a folder, not a link, that you own with mode 0700, or the run stops. Each result is written as
a fresh file with mode 0600 and renamed into place, so a link or a file left in `FILE.r0` is replaced, never
written through.

The report names each item by its line, never by its ID. A line that is not an item stops the run with its
number and a fixed reason. A failure names its exception type only, and a crash in the Go port is counted as
a difference without its message. `FILE.r0/oracle.jsonl.gz` holds Python's outputs, which contain the text;
delete the folder when you are done.

## After the Python package is gone

`PYTHON=` skips the oracle and compares against `oracle-digests.jsonl.gz` from an earlier run. The
differences are still found, without positions. The digests belong to one items file: if a converter
changes what it writes, record the digests again while Python is still here.

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

Add an entry to `internal/realdata/manifest.json`: an `id`, the dataset's `url`, `version`, `licence` and
`licence_source` (the https page that states the licence), its `access` and `kind`, a `converter`, the
`base_url` its files are under, and in `redirect_hosts` any host besides the base URL's that a download is
redirected to (an archive host or a CDN); a redirect anywhere else is refused. Then list the files:

- a few files: write `pins/ID.tsv` with one line per file, `-<tab>-<tab>PATH`;
- many files in a git repository or a Hugging Face dataset: give a `listing` with the repository, a full
  revision and `include` patterns. It is listed from a clone of its trees alone, or one folder level at a
  time on the Hub, so audio beside the text is never fetched.

`go run ./cmd/realdata pin ID` then fetches each file the first time, checks it against the git blob ID or
LFS digest its source lists where there is one, and writes its size and SHA-256 into `pins/ID.tsv`, which you
commit. If no converter reads the format, add one to the `converters` map in `internal/realdata/convert.go`,
with a test on an invented sample.

A dataset that needs an account, a registration or accepted terms has `"access": "owner"` and says in
`owner_steps` what the owner must do. Agents never fetch such a dataset. A dataset with `opt_in` runs only
when it is named.
