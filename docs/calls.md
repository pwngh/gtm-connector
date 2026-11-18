# Import a call transcript

Import an existing UTF-8 TXT or WebVTT export and associate it with one account.
The connector quotes a selected excerpt in the renewal brief. It does not start
a recording, transcribe audio, connect to a call provider, or infer a summary.
The original bytes stay in private local state. The source is labeled as a
user-supplied transcript export, including when the CRM uses invented fixtures.

## Try the local flow

Use an existing transcript file that you are allowed to read. These commands
associate it with the invented Juniper Crest Learning account:

```sh
gtm-connector calls import meeting.vtt --account acc-007
gtm-connector calls list
gtm-connector calls inspect IMPORT_ID
gtm-connector brief acc-007 --calls-backend imported --state-dir var/imported-calls
```

Replace `meeting.vtt` with your file and `IMPORT_ID` with the returned import ID.
`calls import` uses `var/imported-calls` by default. The daily commands retain
their existing defaults, so give `brief` or `serve` that same state folder and
`--calls-backend imported`. This separates importing data from using it.

The import command resolves the account through a local `crm:read` grant and
audited MCP request, then commits the selected local revision. It writes no CRM
record. Later transcript reads require only `calls:read`; they do not perform a
CRM lookup under that permission. If no transcript is selected for the account,
the read refuses with `calls_not_imported`. It never substitutes fixture notes.

## Associate an existing Salesforce account

Use the same explicitly selected connection and environment for import and use:

```sh
gtm-connector calls import meeting.vtt --account-number ACCOUNT_NUMBER --crm-backend salesforce --sf-org EXISTING_ALIAS --sf-environment sandbox --state-dir var/connected-calls
gtm-connector brief ACCOUNT_ID --crm-backend salesforce --sf-org EXISTING_ALIAS --sf-environment sandbox --calls-backend imported --state-dir var/connected-calls
```

Replace the uppercase values with your account number, existing signed-in CLI
alias, and the canonical account ID returned by import. Select `developer` or
`production` instead when appropriate. See [Salesforce connection checks](salesforce.md).

AccountNumber is a standard Salesforce field, but it can be blank or duplicated.
The exact lookup reads at most two matches and compares the returned number
verbatim. Zero matches, ambiguity, and a changed association refuse clearly.
No company-name similarity or email-domain join is used. If AccountNumber is
unavailable, pass `--account acc-sf-NATIVE_ID` using an ID from `accounts` instead.
That fallback uses the existing account-detail read, including its Contact and
Contract bounds; it costs three record GETs rather than the exact lookup's one.

The imported state is tied to the validated CRM identity. Repointing an alias
to another org cannot reuse it. Fixture and imported calls have separate modes;
choose fresh state rather than converting a directory containing fixture notes.
A Salesforce demo-kit binding cannot be combined with imported calls in this
slice. Calls list and inspect use the saved private association without
contacting Salesforce; they do not claim to verify the current CLI login.

## Choose an excerpt

TXT ranges use physical lines. WebVTT ranges use timed cues. Both start at one,
include the end, and include one adjacent unit on either side where available:

```sh
gtm-connector calls import meeting.txt --account acc-007 --lines 12:20
gtm-connector calls import meeting.vtt --account acc-007 --cues 8:14 --called-at 2027-01-14T15:00:00Z
```

The default selects the first 20 units plus available context. It uses fewer
complete units if that prefix exceeds the quotation bound. Explicit oversized
ranges refuse instead of truncating silently. The result identifies both the
requested range and the actual cited range. A brief labels it as an excerpt,
not a complete call summary. Unknown speakers remain literal source text.

`--called-at` supplies an offset-aware meeting timestamp, normalized to UTC.
Without it, the call time stays unknown. Import time is recorded separately;
file modification time is never used as a meeting date. Source text, including
voice tags or apparent instructions, remains quoted data.

| Bound | Limit |
| --- | ---: |
| Original UTF-8 file | 2 MiB. |
| Parsed lines or cues | 10,000. |
| Quoted text, including context | 16 KiB of UTF-8. |
| Returned units, including context | 256. |
| Serialized selected record | 128 KiB of ASCII-escaped JSON, including its reference. |

MCP adds its envelope and may repeat the selected record in text and structured
content. The record bound is not an exact HTTP response-size promise. Ordinary
MCP results, briefs, and inspection contain only the selected excerpt and its
provenance. To inspect the complete private original explicitly, run:

```sh
gtm-connector calls inspect IMPORT_ID --full --json
```

This output can contain the entire transcript. It is a local owner operation,
not an MCP tool. The source file's existing permissions are unchanged; the
stored copy and imported database use owner-only permissions.

## Revisions and checks

Repeating identical bytes, account, date, and range reuses the same revision.
Changed bytes or selection create a new immutable revision and make it active
for that account. Older revisions remain inspectable. There is one active
excerpt per account, which bounds a normal read independently of import history.
Use the list's continuation cursor with `--after` to inspect more than 100 rows.

A known transcript cannot move to another account accidentally. Repeat the
import with the new account and explicit `--reassign` to create a new association
revision. This does not rewrite previous revisions or accepted brief snapshots.
The same rule applies when an AccountNumber now resolves to a different account.

Before returning a source record, the reader hashes the stored bytes, reparses
them, and checks the selected units against the saved manifest. Saved brief
checks verify the excerpt, manifest ID, and receipt reference without rereading
the original or the current selection. They do not independently prove that an
excerpt came from unseen original bytes. Neither check authenticates a provider,
a speaker, or the truth of the source. An owner who rewrites all evidence and
receipts remains outside this integrity boundary.

The default formatter records `adapter: "extractive"` for imported workflows,
with no model and zero model tokens. Evidence uses workflow version 4 and names
the CRM backend, imported calls mode, and saved source binding. Fixture workflows
keep their existing versions and output. Optional local-model limits still apply
when the owner explicitly selects Ollama; the default requires no model.

## Obtain an existing export

| Existing service | Owner action |
| --- | --- |
| Zoom | Download the existing recording's transcript VTT. [Zoom transcript guide](https://support.zoom.com/hc/en/article?id=zm_kb&sysparm_article=KB0064927). |
| Gong | Open the call and download its transcript from the available call actions. [Gong transcript guide](https://help.gong.io/docs/view-a-call-transcript). |
| Google Meet | Open the existing transcript document and download it as plain text. An AI meeting-notes document is not a verbatim transcript. [Meet transcript locations](https://support.google.com/meet/answer/12849897), [Docs downloads](https://support.google.com/docs/answer/49114). |

These are commands and downloads for the owner to perform with existing access.
No provider connection or live transcript download has been tested here. Parser
and integration tests use invented TXT and VTT files. The importer supports the
documented transcript subset of [WebVTT](https://www.w3.org/TR/2026/CRD-webvtt1-20260520/):
UTF-8, common line endings, cue IDs, multiline payloads, and valid cue timings.
It preserves display markup literally and refuses malformed timing or ambiguous
blocks instead of silently discarding spoken content.
