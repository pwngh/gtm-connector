# Serve selected accounts

A selection limits a connected client to the accounts you name. It also adds two
tools, so the client can prepare a cited renewal brief with its own grant and
read it again later. Without `--selection`, any `crm:read` grant can read the
whole source.

## Start a selected server

```json
{"schema_version": 1, "accounts": [
  {"account_id": "acc-007", "slack": true},
  {"account_id": "acc-012", "slack": false}
]}
```

```sh
chmod 600 selection.json
gtm-connector serve --state-dir var/selected --selection selection.json
```

- `accounts` lists 1 to 100 distinct accounts. Use `acc-007` style IDs with the
  fixture CRM and `acc-sf-` plus the 18-character ID with Salesforce.
- `slack: true` selects the account's Slack thread. In live Slack mode it must
  already be mapped with `slack map`, or the server refuses to start.
- The file must be a regular file you own that others cannot write, at most
  64 KiB. Unknown keys, repeated keys or accounts, and non-finite numbers refuse.
- `serve` reads the file once, before any credential or source. To change the
  selection, restart. A restart also replaces every grant.
- `--selection` cannot be combined with `--salesforce-demo-binding`.

## Tools

A selected server offers these seven tools and no `log_activity`.

| Tool | Requires | Behavior |
| --- | --- | --- |
| `search_accounts`, `list_opportunities` | `crm:read` | The selected IDs are part of the query, before ordering and the result limit. |
| `get_account`, `list_opportunities` with an account | `crm:read` | An unselected account is refused and not looked up. |
| `read_call_notes` | `calls:read` | It reads selected accounts only. To withhold call notes, grant no `calls:read`. |
| `read_slack_messages` | `slack:read` | It reads selected accounts whose `slack` is `true`. |
| `prepare_renewal_brief(account_id, as_of)` | `crm:read` | Reads the account, its opportunities, its call notes, and its Slack thread when selected. Each read checks its own scope. |
| `read_renewal_brief(brief_id)` | `crm:read`, plus the scopes the brief's content needs | Returns a saved brief without calling any source system. |

`as_of` is optional and uses `YYYY-MM-DD`. The fixture CRM defaults to
2027-01-15 and Salesforce defaults to today. The selection chooses accounts and
Slack threads, not fields.

## Prepared briefs

`prepare_renewal_brief` uses only the caller's grant, never the owner
credential, and the same drafter and checker as the `brief` command. Each read
inside it checks the token, rate limit, scope, and selection again and writes
its own receipt, linked to the outer call by
`{"kind": "brief_child", "parent_audit_id": ..., "ordinal": N}`. A successful
brief has one outer receipt and three read receipts, or four with Slack. The
reads write no `serve` event lines.

- A grant without `calls:read` or `slack:read` gets a brief that states the
  source was unavailable. Any other failure leaves no readable brief.
- The whole call has 45 seconds. A read already in progress keeps its own limit,
  20 seconds for Salesforce and live Slack, and a late result is discarded.
- The brief is saved as `selected-briefs/BRIEF_ID.json` in the state folder,
  mode 0600, at most 4 MiB. `check-brief` and the usage report do not read it.

`read_renewal_brief` releases a saved brief to any caller, but only when all of
these hold now:

- Its preparing call finished successfully, and that receipt names the SHA-256
  digest of the saved file.
- The account is still selected, and its Slack thread too if the brief contains
  Slack messages.
- The grant holds `crm:read`, plus `calls:read` or `slack:read` if the brief
  contains call notes or Slack messages.
- The CRM, calls, and Slack bindings equal the running server's. For imported
  calls the active transcript revision is unchanged. For live Slack the mapping
  is unchanged, and the connection holds a ready credential for the same
  identity with at least the scopes that read the thread.

The result includes `provider_rechecked: false` and a notice, because the check
cannot see what the source systems show now. Logging out of the `sf` CLI does
not withhold saved Salesforce content. A grant that expires, or a Slack
disconnect that finishes, after the last check does not recall the response.
`serve --json` reports the selection's counts and tool names on its ready line.

## Errors

A bad selection file stops `serve` with exit `2`, and an unmapped Slack thread
with exit `3`. Tools return these codes as tool errors. A failed source read
returns that source's own code, such as `calls_not_imported`.

| Code | Cause | Fix |
| --- | --- | --- |
| `account_not_selected`, `slack_not_selected` | The account or its Slack thread is not selected. | Edit the file and restart `serve`. |
| `crm_write_disabled` | A client attempted a write. | Do not retry. A selected server is read-only. |
| `invalid_as_of` | `as_of` is not a `YYYY-MM-DD` date. | Correct the date. |
| `rate_limited`, `brief_deadline_exceeded`, `brief_source_busy` | The rate limit was reached, the 45 seconds passed, or another Slack operation holds the connection. | Retry. |
| `invalid_token`, `brief_scope_not_covered` | The grant expired, or it lacks a scope the brief needs. | Obtain a grant with those scopes. |
| `renewal_fields_unmapped` | The Salesforce account has no eligible Contract. | Run `salesforce check`. |
| `brief_source_failed`, `brief_evidence_invalid`, `brief_rejected` | A read failed, or the evidence or draft did not pass its checks. | Read the receipts with `audit`, then retry. |
| `brief_too_large`, `brief_storage_failed` | The brief would exceed 4 MiB, or it could not be written. | Check the state folder's permissions. |
| `brief_not_found`, `brief_invalid`, `brief_not_accepted` | No valid, successfully prepared brief has this ID, or its file changed. | Prepare a new brief. |
| `brief_source_changed` | A binding, the transcript revision, the Slack mapping, or the Slack connection changed. | Prepare a new brief. |
