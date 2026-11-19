# Tools and source data

The connector serves six tools over CRM, calls, and Slack. By default each
source is its own local SQLite store of invented records.

| Tool | Scope | Returns |
| --- | --- | --- |
| `search_accounts` | `crm:read` | Accounts whose name contains the literal query, or at most two exact Salesforce AccountNumber matches. |
| `get_account` | `crm:read` | One account, its contacts, and with Salesforce its standard Contracts. |
| `list_opportunities` | `crm:read` | Opportunities, for one account when given. |
| `read_call_notes` | `calls:read` | Fixture notes, or the selected excerpt of an imported transcript. |
| `read_slack_messages` | `slack:read` | Fixture messages, or the mapped live thread. |
| `log_activity` | `crm:write` | The activity created, or the existing one for that idempotency key. |

- Scopes do not imply one another. Each tool declares its scope in
  `gtm.demo/requiredScope` metadata, and a call without it gets HTTP 403.
- Every record carries a reference such as `crm/accounts/acc-001`,
  `calls/call_notes/note-007`, or `slack/messages/msg-007`. Results are marked
  as untrusted data.
- An imported transcript returns only its selected cue or line ranges. See
  [transcripts](calls.md).
- `serve --selection FILE` limits reads to the selected accounts, replaces
  `log_activity` with `prepare_renewal_brief` and `read_renewal_brief`, and
  turns writes off. See [selection](selection.md).
- `serve --crm-backend salesforce` reads CRM from Salesforce, omits
  `log_activity`, and refuses writes. See [Salesforce](salesforce.md).
  [Live Slack](slack-live.md) covers `--slack-backend slack`.

## Fixtures

Every company, person, and message is invented, with `.example` email domains.
`crm.sqlite3` holds 30 accounts with contacts, opportunities, and activities.
`calls.sqlite3` holds 29 notes, and `slack.sqlite3` holds 29 messages. The
fixed demo date is `2027-01-15`.

| Record | Case |
| --- | --- |
| `acc-001` | Renews in 17 days. |
| `acc-002` | At risk. |
| `opp-003`, `note-003`, `msg-003` | `closed_won` against unapproved procurement and a delayed security review. |
| `acc-004`, `acc-005` | `acc-004` has no call notes. `acc-005` has no Slack messages. |
| `acc-006` | An October call note and a January Slack message disagree on seats. Nothing is flagged. |
| `acc-007` | Juniper Crest Learning, the demo account. It renews on `2027-01-29`. Its contact has no role. |
| `note-007`, `msg-007` | Each asks for a write. Both stay quoted data, and no activity is created. |

Reseeding keeps record IDs, edits, and activities. An incomplete or unknown
fixture set is rejected, and so is a legacy combined CRM and calls database.
Nothing is migrated, so use a fresh `--state-dir` for the current book.

## Run the demo

The demo needs macOS or Linux, Python 3.11 or later, SQLite, and venv support.
The first install needs internet. No account, model, or API key is needed.

```sh
./scripts/demo.sh
./scripts/demo.sh --state-dir /tmp/gtm-fixture
./scripts/demo.sh --json
./scripts/five-minute-demo.sh
```

`demo.sh` creates `.venv`, installs `requirements.lock`, runs the tests, and
runs `demo` with state in `var/renewal-demo`. The demo looks up Juniper Crest
Learning with `crm:read`, is refused a call-note read, then makes four brief
reads with all three read scopes. Each run adds one accepted brief and six
receipts, five successful and one error, and creates no activity.
[The CLI contract](cli.md) describes the JSON result.

`five-minute-demo.sh` runs the same demo in a new private folder, then shows
the checker accepting the saved brief and rejecting one appended sentence.

## Run the guarantee checks

`verify` creates one fixture activity to check idempotent retries, so keep its
state apart from the demo's:

```sh
.venv/bin/python -m gtm_connector verify --state-dir var/verification --json
```

| Field | Result |
| --- | --- |
| `accounts` | `30` |
| `notes_without_scope.status` | HTTP `403` |
| `empty_notes` | `[]` for `acc-004` |
| `conflicting_sources` | `opp-003` says `closed_won`; `note-003` reports unapproved procurement |
| `injection_note.activities_created` | `0` |
| `idempotent_write` | `requests: 2`, `activities: 1` |
| `rate_limit.status` | HTTP `429` |
| `audit_rows` | `14` connector receipts |
| `briefs` | Two briefs: `acc-003` with calls access, `acc-001` without it |
| `injection_brief.activities_created` | `0` across all three briefs, including `acc-007` |
| `brief_audit_rows` | `9` reads across the three briefs |
| `uncited_candidate_rejected` | `true` |
| `contradiction_flags` | One flag citing `opp-003` and `note-003` |

`verify --with-slack` adds a Slack read to each brief and checks the
CRM-to-Slack, Slack-to-CRM, and Slack-to-calls denials. `audit_rows` becomes
`18` and `brief_audit_rows` becomes `12`. See [Slack context](slack.md) and
[Briefs and citations](briefs.md).

## Inspect the local CRM

```sh
python3 -m gtm_connector seed
python3 -m gtm_connector accounts --query 'Alder Vale'
python3 -m gtm_connector account acc-007
```

These read the SQLite file directly, outside MCP authorization. `--db PATH`
selects the file, and only `seed` creates it. The account view leaves out
opportunities and call notes. With `--crm-backend salesforce`, `accounts` and
`account` read through the scoped, audited gateway instead.

Run the tests with `.venv/bin/python -m unittest discover -s tests -v`. See
[development checks](development.md).
