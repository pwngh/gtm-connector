# Import a call transcript

Import a UTF-8 TXT, WebVTT, or SRT export and associate it with one account. The
[Deal card](briefs.md) then shows a selected excerpt as whole sentences. The connector does not
record, transcribe, connect to a call provider, or summarize. The original bytes stay in private
local state.

## Commands

```sh
gtm-connector connect calls --seller 'Morgan Reed'    # once, in a folder with a connected CRM; name every seller
gtm-connector calls import meeting.vtt --account acc-007
gtm-connector calls import meeting.vtt --account acc-007 --cues 8:14 --called-at 2027-01-14T15:00:00Z
gtm-connector calls import meeting.txt --account acc-007 --speaker-names
gtm-connector calls list
gtm-connector calls inspect IMPORT_ID --full --json
gtm-connector brief acc-007
```

| Option | Meaning |
| --- | --- |
| `--account ID`, `--account-number NUMBER` | `import` requires exactly one. `--account-number` needs Salesforce. `list` accepts `--account` as a filter. |
| `--lines FIRST:LAST`, `--cues FIRST:LAST` | The excerpt, in TXT lines or in VTT or SRT cues. Ranges start at one and include the end. |
| `--called-at TIMESTAMP` | The meeting time with a UTC offset. Without it the call time stays unknown. |
| `--speaker-names` | States that every line or cue of the file is one line that opens with its speaker's name and a colon ("Dana Crest: We will sign."). Each name is then the speaker's label. A file with one line or cue that is not laid out that way refuses. |
| `--reassign` | Moves known content, or a known AccountNumber, to another account. |
| `--state-dir PATH` | The state folder, `var/v2` by default. Use the folder `connect calls` was given. |
| `--after IMPORT_ID` | For `list`: continues after the returned `next_after`. A page holds 100 rows. |
| `--full` | For `inspect`: prints every unit and the original text. This is not available as an MCP tool. |

`connect calls` connects transcripts beside Salesforce or beside the demo CRM, and
`connect calls --seller NAME` lists who the sellers are. `calls import` refuses until transcripts
are connected. `brief` and `serve` then read the imports with no further option.
`connect calls --remove` detaches them and deletes nothing. Demo call notes and imported
transcripts never share a folder: a folder that has read demo call notes refuses an import and
names a fresh `--state-dir` to connect, and beside the demo CRM a folder that holds imports refuses
`connect calls --remove`, which would make its calls demo data again. Stop `serve` before running a `calls` command on its
folder.

## Rules

- `import` resolves the account through a local `crm:read` grant and an audited MCP request. It
  writes no CRM record. Later reads need only `calls:read`.
- Each account has one active excerpt. Fixture notes are never substituted for a missing one: the
  card of an account with nothing imported says "Calls: no transcript is imported for this
  account."
- The default excerpt is the first 20 units, or fewer if they exceed a limit below. Every excerpt
  adds one adjacent unit on each side where available. An explicit range that exceeds a limit
  refuses and is never truncated.
- Repeating the same bytes, account, call time, and range reuses the revision. Any change creates
  a new active revision. Older revisions stay inspectable.
- `.txt`, `.vtt`, or `.srt` selects the parser. TXT units are lines. VTT and SRT units are timed
  cues, in file order. Cues may repeat or reorder start times, end where they start, and be
  separated by lines of spaces where a cue follows. A cue that ends before it starts refuses, and so
  does anything after an end time other than WebVTT cue settings.
- A WebVTT identifier may repeat. Inline tags such as `<v Name>` are stored as the file has them. An
  SRT cue may be numbered and carries no other identifier.
- On a card, each whole sentence is one numbered unit. A WebVTT voice tag names the speaker of the
  text after it and is not part of the sentence, and a cue with two tags has two speakers. A cue
  with no tag takes its identifier as the label when that is not a number. TXT lines and SRT cues
  have no label, so their role is `unknown`, unless the import states speaker names (below).
  WebVTT styling tags (`<b>`, `<i>`, `<u>`, `<c>`,
  `<lang>`, `<ruby>`), the reading inside `<rt>`, and cue timestamps are not part of a sentence.
- The connector never guesses that a file names its speakers at the start of each line. With
  `--speaker-names` you state it, the whole file is checked, and the statement is saved with the
  import, so a name is read the same whichever lines are cited. The name is the label and is not
  part of the sentence, and a cue identifier is not a label in such an import. The file refuses
  unless all of this holds for every line or cue with text:
  - It is one line. On a second line of a cue nothing says whose words they are.
  - It opens with a name, a colon, a space, and words.
  - The name starts with a letter that is not lower case, is at most 100 characters, and holds no
    colon, question or exclamation mark, angle bracket, or sentence end. "Dr. J. Vance" is a name.
    "We have signed. Dana Crest" and "final word on the matter" are not.
- The checks on a name are made when the file is imported. An import that was accepted is read
  afterwards by the layout alone, so a later change to how sentences are split never unreads it.
- A stated layout says how the file is read. It does not show who spoke: anyone who can start a
  line of the file can write a name there, and a wrapped line that happens to open like a name
  reads as one. State it only of an export whose lines the recorder wrote. `calls list` and
  `calls inspect` show which imports state it.
- Neighbouring cues under one speaker label are read together, so a sentence that runs across them
  stays whole: when they are at most two seconds apart, or at most ten when the earlier cue stops
  mid-sentence and the later one goes on in lower case. Cues or lines with no label are never
  joined, because two people may be speaking. Lines of a TXT file have no times and are never
  joined either.
- A sentence is shown only whole. Text that stops without closing punctuation, or on a short form
  that expects more ("28 U.S.C."), is counted in `NOT READ` and not shown. So is what the same
  speaker says next, what anyone says next in lower case, and each speaker's first words when the
  excerpt starts inside the call. In a TXT file,
  keep each sentence on one line.
- `NOT READ` says which lines or cues of how many were read.
- An import saved by an earlier version of the parser reads as `calls_import_corrupt`. Import the
  file again.
- Well-formed `Kind:`, `Language:`, and `X-TIMESTAMP-MAP=` lines under the `WEBVTT` line are
  skipped, and any other text there refuses. Control characters other than tabs and line endings
  refuse. No provider download has been tested.
- With Salesforce connected, `--account-number` is exact, at most 40 characters, and refuses a
  non-literal or second match. On a refusal, pass `--account acc-sf-NATIVE_ID`. See
  [Salesforce](salesforce.md) and [live Slack](slack.md).

| Limit | Value |
| --- | ---: |
| Original UTF-8 file | 2 MiB |
| Parsed lines or cues | 10,000 |
| Quoted text, including context | 16 KiB of UTF-8 |
| Returned units, including context | 256 |
| Quoted text with its units | 120 KiB of ASCII-escaped JSON. The saved record stays under 128 KiB. |

## Errors

| Code | Exit | Cause | Fix |
| --- | ---: | --- | --- |
| `calls_import_invalid` | 2 | The export or range is invalid. | Use a valid UTF-8 TXT, VTT, or SRT file and range. |
| `calls_import_too_large`, `calls_excerpt_too_large` | 2 | The export or excerpt exceeds a limit above. | Use a smaller export or fewer units. |
| `calls_speaker_names_missing` | 2 | `--speaker-names` was given and a line or cue does not open with a name and a colon. | Import the file without `--speaker-names`. |
| `calls_import_not_found` | 2 | The import is not in this state folder. | Run `calls list`. |
| `calls_account_not_found` | 2 | No CRM account matched. | Check the ID or exact AccountNumber. |
| `calls_reassign_required`, `calls_association_changed` | 3 | The content or AccountNumber now belongs to another account. | Review it. Add `--reassign` only to move it. |
| `calls_account_ambiguous` | 3 | The AccountNumber is not one literal match. | Use `--account` with the canonical ID. |
| `calls_source_mismatch`, `source_identity_mismatch` | 3 | The connected calls source, the CRM connection, or the folder differs from this state. | Connect the original sources in the original folder, or connect a fresh folder. |
| `calls_import_corrupt` | 4 | The stored bytes or excerpt no longer match the saved manifest. Every read checks this. | Keep this state and reimport into a fresh folder. |
| `calls_association_invalid`, `calls_association_failed` | 4 | The CRM read failed or did not match its receipt. Nothing was imported. | Check the CRM connection and retry. |
| `calls_source_unavailable` | 5 | The local CRM connection could not be reached. | Check the setup and retry. |
| `calls_import_io` | 1 | The export or private store could not be read or written. | Check permissions and retry. |
