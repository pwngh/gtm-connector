# Serve selected accounts

A selection limits a connected client to the accounts you name. It also adds two tools, so the
client can prepare a [Deal card](briefs.md) with its own grant and read it again later.

```json
{"schema_version": 1, "accounts": [
  {"account_id": "acc-007", "slack": true}, {"account_id": "acc-012", "slack": false}]}
```

```sh
chmod 600 selection.json
gtm-connector serve --state-dir var/selected --selection selection.json
```

- `accounts` lists 1 to 100 distinct accounts. Use `acc-007` style IDs with the fixture CRM and
  `acc-sf-` plus the 18-character ID with Salesforce.
- `slack: true` selects the account's Slack thread. In live Slack mode it must already be mapped
  with `slack map`, or the server refuses to start.
- The file must be a regular file you own that others cannot write, at most 64 KiB. Unknown keys,
  repeated keys or accounts, and non-finite numbers refuse.
- To change the selection, restart `serve`. A restart also replaces every grant.
- `--selection` cannot be combined with `--salesforce-demo-binding`.

## Tools

| Tool | Requires | Behavior |
| --- | --- | --- |
| `search_accounts`, `list_opportunities` | `crm:read` | The selected IDs are part of the query, before ordering and the result limit, so a search never reports more matches than the selection holds. |
| `get_account`, `list_opportunities` with an account | `crm:read` | An unselected account is refused and not looked up. |
| `read_call_notes`, `read_slack_messages` | `calls:read`, `slack:read` | They read selected accounts only, and Slack only where `slack` is `true`. To withhold call notes, grant no `calls:read`. |
| `prepare_renewal_brief(account_id, as_of)` | `crm:read` | Reads the account, its opportunities, its call notes, and its Slack thread when selected. Each read checks its own scope. A grant without `calls:read` or `slack:read` gets a card that says the source was not read. `as_of` is optional and uses `YYYY-MM-DD`. The fixture CRM defaults to 2027-01-15 and Salesforce defaults to today. |
| `read_renewal_brief(brief_id)` | `crm:read`, plus the scopes the brief's content needs | Returns a saved brief without calling any source system. |

## Prepared briefs

`prepare_renewal_brief` returns the same card as the `brief` command, as `text`, with its `gaps`
and its `units`: each number on the card with its record reference and, for a sentence, its
neutralized text, speaker, and role. The units hold more than the card prints: every sentence that
was read, shown or not, and for a transcript the cues it lies in and their times. A caller with
`calls:read` receives those. Each read inside it writes its own receipt, linked to the outer call by
`{"kind": "brief_child", "parent_audit_id": ..., "ordinal": N}`. A successful brief has one outer
receipt and three read receipts, or four with Slack. The reads write no `serve` event lines.

- Preparing a brief has 45 seconds. Writing the final receipt and sending the response come after
  that check. A read already in progress keeps its own limit, 20 seconds for Salesforce and live
  Slack, and a late result is discarded.
- The brief is saved as `selected-briefs/BRIEF_ID.json` in the state folder, mode 0600, at most
  4 MiB. `trace` reads the cards the `brief` command saves, not these, so a caller resolves a
  number from `units`.
- `read_renewal_brief` releases a saved brief, with the same `text`, `units`, and `gaps`, to any
  caller that passes the rule in the [security review](../SECURITY_REVIEW.md). A brief saved before
  gaps existed releases `gaps: null`. Its result includes `provider_rechecked: false` and a notice.
  Logging out of the `sf` CLI does not withhold saved Salesforce content.

## Errors

A bad selection file stops `serve` with exit `2`, and an unmapped Slack thread with exit `3`. Tools
return these codes as tool errors, or a failed source's own code, such as `calls_not_imported`.

| Code | Cause | Fix |
| --- | --- | --- |
| `account_not_selected`, `slack_not_selected` | The account or its Slack thread is not selected. | Edit the file and restart `serve`. |
| `crm_write_disabled` | A client attempted a write. | Do not retry. A selected server is read-only. |
| `invalid_as_of` | `as_of` is not a `YYYY-MM-DD` date. | Correct the date. |
| `rate_limited`, `brief_deadline_exceeded`, `brief_source_busy` | The rate limit was reached, the 45 seconds passed, or another Slack operation holds the connection. | Retry. |
| `invalid_token`, `brief_scope_not_covered` | The grant expired, or it lacks a scope the brief needs. | Obtain a grant with those scopes. |
| `brief_source_failed`, `brief_evidence_invalid`, `brief_rejected` | A read failed, or the evidence did not pass its checks, so no card was produced. | Read the receipts with `audit`, then retry. |
| `brief_too_large`, `brief_storage_failed` | The brief would exceed 4 MiB, or it could not be written. | Check the state folder's permissions. |
| `brief_not_found`, `brief_invalid`, `brief_not_accepted`, `brief_source_changed` | No valid, successfully prepared brief has this ID, its file changed, or a binding, the transcript revision, the Slack mapping, or the Slack connection changed. | Prepare a new brief. |
