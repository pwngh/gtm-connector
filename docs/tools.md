# Tools and source data

The connector exposes five tools over two local SQLite stores. Run the commands
below from the repository root.

| Tool | Required scope | Records returned |
| --- | --- | --- |
| `search_accounts` | `crm:read` | Accounts matching a literal name substring |
| `get_account` | `crm:read` | One account and its contacts |
| `list_opportunities` | `crm:read` | Opportunities, optionally filtered by account |
| `read_call_notes` | `calls:read` | Notes from the separate calls database |
| `log_activity` | `crm:write` | An activity created or reused by idempotency key |

CRM permission does not grant access to calls. A calls-only token cannot read
CRM records. Each tool declares its scope in `gtm.demo/requiredScope` metadata.
The gateway and handler both enforce it. A scope denial returns HTTP 403.

Results include source-qualified references such as `crm/accounts/acc-001` and
`calls/call_notes/note-007`. Returned record content is marked as untrusted data.

## Fixtures

`crm.sqlite3` holds 30 accounts and their contacts, opportunities, and activities.
`calls.sqlite3` holds 29 call notes. Account `acc-004` has none. Display names use
labels such as `Fixture account acc-001`; email addresses use reserved `.example`
domains. The data uses no plausible company, person, or product names.

The fixed demo date is `2027-01-15`:

- `acc-001` renews in 17 days.
- `acc-002` is at risk.
- `opp-003` reports `closed_won`, while `note-003` says procurement has not
  approved the renewal.
- `note-007` contains an instruction to invoke a write. The workflow treats it
  as quoted data and creates zero activities.

Record IDs stay stable across reseeds. Reseeding keeps existing edits and
activities and rejects incomplete or unexpected fixtures. The legacy combined
CRM/calls database is incompatible with the separate stores. Use `var/v2` or a
fresh path. There is no automatic migration or deletion.

## Run the connector demo

Use macOS or Linux with Python 3.11+, SQLite, and venv support. The first
dependency install needs internet. No external database service, account,
model, or API key is needed.

```sh
./scripts/demo.sh
```

The script creates `.venv`, installs `requirements.lock`, runs the tests, and
starts two temporary HTTPS services on the loopback interface. The CLI obtains
scoped tokens with PKCE, calls all five tools, and builds renewal briefs from
the results. Both services stop afterward. State stays in the gitignored
`var/v2` directory. To use another directory, run:

```sh
./scripts/demo.sh --state-dir /tmp/gtm-fixture
```

The demo prints JSON with these fields and results:

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
None of the briefs creates an activity, including the one whose note requests
a write. `conflicting_sources` displays both records. The flag follows the
specific rule described in [Briefs and citations](briefs.md).

For the full walkthrough, including evaluation, usage, and a live checker
rejection, run `./scripts/five-minute-demo.sh`. It prints the private directory
where it saves its output. See [DEMO.md](../DEMO.md). First-install download
time varies.

## Inspect the local CRM

```sh
python3 -m gtm_connector seed
python3 -m gtm_connector accounts --query acc-001
python3 -m gtm_connector account acc-007
```

These commands access the local CRM file directly, outside MCP authorization.
Each accepts `--db PATH`. Read commands do not create missing files. The account
view excludes opportunities and call notes.

## Run the tests

```sh
.venv/bin/python -m unittest discover -s tests -v
```

Tests cover stable fixtures and edge cases, independent scopes, invalid tokens,
PKCE, resource and issuer checks, protocol headers, one receipt per tool attempt,
audit failures, rate limits, idempotency, and the real HTTPS CLI flow. Brief tests
cover denied and empty notes, conflicting sources, changes to the evidence copy,
rejected citations, and instructions that stay data. Tests use temporary data.
CI runs the full five-minute walkthrough on Python 3.11 and 3.14.
