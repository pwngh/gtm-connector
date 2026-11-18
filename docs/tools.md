# Tools and source data

Six tools read from three local SQLite source stores or log an activity, a note
about an account interaction. Run these commands from the repository root.

| Tool | Required scope | Records returned |
| --- | --- | --- |
| `search_accounts` | `crm:read` | Accounts matching a literal name substring |
| `get_account` | `crm:read` | One account and its contacts |
| `list_opportunities` | `crm:read` | Opportunities, optionally filtered by account |
| `read_call_notes` | `calls:read` | Notes from the separate calls database |
| `read_slack_messages` | `slack:read` | Messages from the separate Slack database |
| `log_activity` | `crm:write` | An activity created or reused by idempotency key |

CRM, calls, and Slack grants do not imply one another. Each tool declares its
scope in `gtm.demo/requiredScope` metadata. The gateway and handler enforce it;
a denial returns HTTP 403.

Each returned record has a reference that names its source, such as
`crm/accounts/acc-001`, `calls/call_notes/note-007`, or `slack/messages/msg-007`.
The content is marked as untrusted data.

## Fixtures

Fixtures use labels such as `Fixture account acc-001` and reserved `.example`
email domains, with no plausible company, person, or product names.
`crm.sqlite3` holds 30 accounts with their contacts, opportunities, and activities.
Call notes live separately in `calls.sqlite3`, which holds 29 notes. Account
`acc-004` has none.

`slack.sqlite3` holds 29 messages in account-specific channels. Account
`acc-005` has none. `msg-003` reports delayed security review and pending
procurement, while `msg-007` contains an instruction to invoke a write.
Both remain quoted source data.

On the fixed demo date `2027-01-15`, `acc-001` renews in 17 days and `acc-002`
is at risk. `opp-003` reports `closed_won`, while `note-003` says procurement
has not approved the renewal.

`note-007` asks for a write. The workflow keeps it as quoted data and creates
zero activities.

A reseed preserves record IDs, edits, and activities. Incomplete or unexpected
fixtures are rejected. The legacy combined CRM/calls database cannot be used
with separate stores; choose `var/v2` or a fresh path.
There is no automatic migration or deletion.

## Run the connector demo

The demo requires macOS or Linux with Python 3.11+, SQLite, and venv support. The first
dependency install needs internet. No external database service, account,
model, or API key is needed.

```sh
./scripts/demo.sh
```

The script creates `.venv`, installs `requirements.lock`, and runs the tests.
It then starts two temporary HTTPS services on the loopback interface. The CLI
obtains scoped tokens with PKCE, exercises the five CRM/calls tools, and builds
renewal briefs. Both services stop afterward, leaving state in the gitignored
`var/v2` directory. To use another directory, run:

```sh
./scripts/demo.sh --state-dir /tmp/gtm-fixture
```

The demo prints a readable brief and check summary by default. To inspect the
complete result as JSON, run:

```sh
./scripts/demo.sh --json
```

That JSON has these fields and results:

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

The run produces 23 receipts: 14 connector attempts and nine briefing reads.
None of the briefs creates an activity, even when a note requests a write.
The `conflicting_sources` result displays both records; its flag follows
the specific rule described in [Briefs and citations](briefs.md).

The full walkthrough runs with `./scripts/five-minute-demo.sh` and includes
evaluation, usage, and a live checker rejection. It prints the private directory
where it saves its output. See [DEMO.md](../DEMO.md). First-install download
time varies.

The `--with-slack` option includes all three sources, so each of the three briefs
uses four reads. The full brief includes a three-source conflict and a cited
follow-up proposal. The restricted brief reports both denied grants. This mode
also demonstrates CRM-to-Slack, Slack-to-CRM, and Slack-to-calls denials.
[Slack context](slack.md) describes those cases.

## Inspect the local CRM

```sh
python3 -m gtm_connector seed
python3 -m gtm_connector accounts --query acc-001
python3 -m gtm_connector account acc-007
```

These commands inspect the local CRM file directly, outside MCP authorization.
Each accepts `--db PATH` to select a file; read commands do not create one if it
is missing. The account view excludes opportunities and call notes. Output is
readable text by default. Add `--json` for counts, an account array, or the full
account object, respectively. See [the CLI contract](cli.md).

## Run the tests

```sh
.venv/bin/python -m unittest discover -s tests -v
```

Tests run the real HTTPS CLI flow with temporary data. They cover stable fixtures
and edge cases, independent scopes, invalid tokens, and PKCE. They also check
resources, issuers, and protocol headers.

Gateway tests cover one receipt per tool attempt, audit failures, rate limits,
and idempotency. Brief tests distinguish denied and empty notes, preserve
conflicts, and check evidence-copy changes. They reject unsupported citations
and keep instructions as data. CI runs the full five-minute walkthrough on
Python 3.11 and 3.14.

## Optional external CRM reads

The command `serve --crm-backend salesforce` replaces the three `crm:read`
handlers. External mode omits `log_activity` and rejects direct write requests,
while calls and Slack remain local fixtures. The direct `seed`, `accounts`, and
`account` commands still inspect SQLite only. See
[the Salesforce mapping and limits](salesforce.md).
