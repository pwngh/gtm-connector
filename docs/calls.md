# Import a call transcript

Import an existing UTF-8 TXT or WebVTT export and associate it with one account.
The renewal brief then quotes a selected excerpt. The connector does not record,
transcribe, connect to a call provider, or summarize. The original bytes stay in
private local state.

## Commands

```sh
gtm-connector calls import meeting.vtt --account acc-007
gtm-connector calls import meeting.txt --account acc-007 --lines 12:20
gtm-connector calls import meeting.vtt --account acc-007 --cues 8:14 --called-at 2027-01-14T15:00:00Z
gtm-connector calls list
gtm-connector calls inspect IMPORT_ID
gtm-connector calls inspect IMPORT_ID --full --json
gtm-connector brief acc-007 --calls-backend imported --state-dir var/imported-calls
```

| Option | Meaning |
| --- | --- |
| `--account ID`, `--account-number NUMBER` | `import` requires exactly one. `--account-number` needs Salesforce. `list` accepts `--account` as a filter. |
| `--lines FIRST:LAST`, `--cues FIRST:LAST` | The excerpt, in TXT lines or VTT cues. Ranges start at one and include the end. |
| `--called-at TIMESTAMP` | The meeting time with a UTC offset. Without it the call time stays unknown. |
| `--reassign` | Moves known content, or a known AccountNumber, to another account. |
| `--state-dir PATH` | The import state folder. `calls` commands default to `var/imported-calls`. |
| `--after IMPORT_ID` | For `list`: continues after the returned `next_after`. A page holds 100 rows. |
| `--full` | For `inspect`: prints every unit and the original text. This is not available as an MCP tool. |

`brief`, `serve`, and Salesforce `accounts` or `account` use imports only with
`--calls-backend imported` and an explicit `--state-dir` naming the same folder.
Fixture and imported calls never share a folder. Stop `serve` before running a
`calls` command on its folder.

## Rules

- `import` resolves the account through a local `crm:read` grant and an audited
  MCP request. It writes no CRM record. Later reads need only `calls:read`.
- Each account has one active excerpt. An account with none refuses with
  `calls_not_imported`. Fixture notes are never substituted.
- The default excerpt is the first 20 units, or fewer if they exceed a limit
  below. Every excerpt adds one adjacent unit on each side where available. An
  explicit range that exceeds a limit refuses and is never truncated.
- The brief labels the quote as an excerpt, not a complete call summary, and
  states the requested and quoted ranges. Source text stays quoted data.
- Repeating the same bytes, account, call time, and range reuses the revision.
  Any change creates a new active revision. Older revisions stay inspectable.
- A brief with an imported transcript records `adapter: "extractive"`, no model,
  and zero tokens. Its evidence uses `workflow_version` 4, or 5 with live Slack.

| Limit | Value |
| --- | ---: |
| Original UTF-8 file | 2 MiB |
| Parsed lines or cues | 10,000 |
| Quoted text, including context | 16 KiB of UTF-8 |
| Returned units, including context | 256 |
| Serialized selected record | 128 KiB of ASCII-escaped JSON |

## File formats

The extension, `.txt` or `.vtt`, selects the parser. TXT units are physical
lines. [WebVTT](https://www.w3.org/TR/2026/CRD-webvtt1-20260520/) units are
timed cues. The VTT parser accepts cue IDs, multiline payloads, and valid cue
timings, keeps markup literally, and refuses malformed timing or ambiguous
blocks. Control characters other than tabs and line endings refuse.

Export the transcript yourself from
[Zoom](https://support.zoom.com/hc/en/article?id=zm_kb&sysparm_article=KB0064927) (VTT),
[Gong](https://help.gong.io/docs/view-a-call-transcript), or
[Google Meet](https://support.google.com/meet/answer/12849897) (plain text). An
AI notes document is not a transcript. No provider download has been tested.

## Salesforce accounts

Use the same connection and environment for import and for later use:

```sh
gtm-connector calls import meeting.vtt --account-number ACCOUNT_NUMBER --crm-backend salesforce --sf-org EXISTING_ALIAS --sf-environment sandbox --state-dir var/connected-calls
gtm-connector brief ACCOUNT_ID --crm-backend salesforce --sf-org EXISTING_ALIAS --sf-environment sandbox --calls-backend imported --state-dir var/connected-calls
```

`ACCOUNT_ID` is the canonical ID that the import returns. See
[Salesforce connection checks](salesforce.md).

- The AccountNumber lookup is exact, reads at most two matches, and compares the
  returned number verbatim. The number has at most 40 characters.
- Salesforce equality can ignore letter case, so a non-literal or second match
  refuses. Pass `--account acc-sf-NATIVE_ID` with an ID from `accounts` instead.
- The AccountNumber is not copied into the manifest returned under `calls:read`.
- The state is tied to the validated CRM identity. An alias repointed to another
  org cannot reuse it. `calls list` and `calls inspect` do not contact Salesforce.
- A demo-kit binding cannot be combined with imported calls.

To add selected Slack, run `slack map` with `--calls-backend imported` and
`calls import` with `--slack-backend slack --slack-connection-dir var/slack-connection`
on one fresh folder, in either order. Briefs then pass the same folder and
flags with `--with-slack`. See the [live Slack guide](slack-live.md).

## Errors

| Code | Exit | Cause | Fix |
| --- | ---: | --- | --- |
| `calls_import_invalid` | 2 | The export or range is invalid. | Use a valid UTF-8 TXT or VTT file and range. |
| `calls_import_too_large`, `calls_excerpt_too_large` | 2 | The export or excerpt exceeds a limit above. | Use a smaller export or fewer units. |
| `calls_import_not_found` | 2 | The import is not in this state folder. | Run `calls list`. |
| `calls_account_not_found` | 2 | No CRM account matched. | Check the ID or exact AccountNumber. |
| `calls_not_imported` | 3 | The account has no selected transcript. | Import an export first. |
| `calls_reassign_required`, `calls_association_changed` | 3 | The content or AccountNumber now belongs to another account. | Review it. Add `--reassign` only to move it. |
| `calls_account_ambiguous` | 3 | The AccountNumber is not one literal match. | Use `--account` with the canonical ID. |
| `calls_source_mismatch`, `source_identity_mismatch` | 3 | The calls mode, CRM connection, or folder differs from this state. | Use the original folder and connection, or a fresh folder. |
| `calls_import_corrupt` | 4 | The stored bytes or excerpt no longer match the saved manifest. Every read checks this. | Keep this state and reimport into a fresh folder. |
| `calls_association_invalid`, `calls_association_failed` | 4 | The CRM read failed or did not match its receipt. Nothing was imported. | Check the CRM connection and retry. |
| `calls_source_unavailable` | 5 | The local CRM connection could not be reached. | Check the setup and retry. |
| `calls_import_io` | 1 | The export or private store could not be read or written. | Check permissions and retry. |
