# Serve selected accounts

A selection limits what a connected client can read to the accounts you name. It
also adds two tools that let the client prepare a cited renewal brief with its
own grant and read that brief again later. Use it before you connect a client
to real CRM, transcript, or Slack data.

## Write the selection

The selection is a small JSON file that you write and keep private:

```json
{"schema_version": 1, "accounts": [
  {"account_id": "acc-007", "slack": true},
  {"account_id": "acc-012", "slack": false}
]}
```

- `accounts` lists 1 to 100 distinct accounts. Use `acc-007` style IDs with the
  fixture CRM and `acc-sf-` followed by the 18-character ID with Salesforce.
- `slack: true` selects that account's Slack thread. In live Slack mode the thread
  must already be mapped with `slack map`, or the server refuses to start.
- The file must be a regular file you own that others cannot write, at most
  64 KiB. Unknown keys, repeated keys or accounts, and non-finite numbers are
  refused.

The selection holds no credentials, names, transcript text, or Slack text.

## Start the server with it

```sh
chmod 600 selection.json
gtm-connector serve --state-dir var/selected --selection selection.json
```

`serve` checks the file before it opens any credential or source. The running
server keeps the selection it started with. To change it, stop the server and
start it again. A restart also replaces every grant, so clients need new grants.
Without `--selection`, `serve` behaves as before and offers neither brief tool.

The startup notice states the number of selected accounts and Slack threads.
`serve --json` adds a `selection` object to the ready line with `accounts`,
`slack_accounts`, and `tools` (the seven tool names).

## What a client can do

A selected server offers seven tools: the five read tools, `prepare_renewal_brief`,
and `read_renewal_brief`. `log_activity` is not offered, and a write attempt is
refused as `crm_write_disabled`.

| Tool | Requires | Behavior |
| --- | --- | --- |
| `search_accounts`, `list_opportunities` | `crm:read` | The selected IDs are part of the query itself, before ordering and the result limit. |
| `get_account`, `list_opportunities` with an account | `crm:read` | An unselected account is refused with `account_not_selected`. It is not looked up. |
| `read_call_notes` | `calls:read` | Selected accounts only. |
| `read_slack_messages` | `slack:read` | Selected accounts whose `slack` is `true`. Otherwise `slack_not_selected`. |
| `prepare_renewal_brief(account_id, as_of)` | `crm:read` | Reads the account, its opportunities, its call notes, and its Slack thread when selected. Each read checks its own scope. |
| `read_renewal_brief(brief_id)` | `crm:read`, plus the scopes the brief's content needs | Returns a saved brief without calling any source system. |

`as_of` is optional and uses `YYYY-MM-DD`. The fixture CRM defaults to
2027-01-15 and Salesforce defaults to today.

## How a brief is prepared

`prepare_renewal_brief` uses only the grant the client presented. It never reads
the owner file, uses the owner credential, or calls the local server over HTTP.
Each read inside it is audited on its own:

1. It commits a pending receipt with the caller's credential fingerprint and a
   link to the outer call: `{"kind": "brief_child", "parent_audit_id": ..., "ordinal": N}`.
2. It verifies the caller's token again, counts one attempt against that
   caller's rate limit, and checks the read's scope and the selection.
3. It runs the same source code as the direct tool.
4. It finishes the receipt before the data reaches the brief.

A successful brief has one outer receipt and three read receipts, or four with
Slack. If the token lacks `calls:read` or `slack:read`, that read's receipt
records the refusal and the brief states that the source was unavailable, as the
`brief` command does. Any other failure, such as an expired token, a rate limit,
a missing transcript, or an audit write failure, stops the brief with a fixed
error code and leaves no readable brief. If the outer receipt cannot be
finished, a saved file can remain in `selected-briefs/`, but it is never
readable because its receipt did not finish successfully.

The brief uses the fixed extractive drafter and the same citation checker as the
`brief` command, so its claims and citations follow the same rules. The whole
call has 45 seconds, checked before each read, again just before its source
work, and after saving. A read that has already started keeps its own time
limit, 20 seconds for Salesforce and live Slack, and its result is discarded if
the deadline passed.

## How a saved brief is read again

The brief is saved as `selected-briefs/BRIEF_ID.json` in the state folder, with
mode 0600. It counts only if its outer call finished successfully and that
call's receipt names the SHA-256 digest of the saved file. A failed call, a
pending receipt, or a changed file makes it unreadable.

`read_renewal_brief` releases the brief only when all of these hold now:

- the account is still selected, and so is its Slack thread if the brief
  contains Slack messages;
- the caller's grant holds `crm:read`, plus `calls:read` if the brief contains
  call notes and `slack:read` if it contains Slack messages;
- the CRM, calls, and Slack bindings equal the running server's; and
- for imported calls and live Slack, the current transcript revision and Slack
  mapping are the ones the brief used, and the local Slack connection still
  holds a ready credential for the same identity with at least the scopes of
  each credential that returned part of the brief's Slack thread. Disconnecting
  Slack, or reconnecting with fewer scopes, withholds saved Slack text. The Slack checks
  and the release decision happen while the connection is locked, so a
  disconnect cannot land between them. A disconnect that finishes after that
  decision, while the receipt is written and the response is sent, does not
  recall the response. If another Slack operation holds the lock, the reread
  returns `brief_source_busy` and can be retried. A prepare that meets a busy
  connection returns the Slack reader's own `slack_connection_busy`.

Knowing a brief ID is not permission. Any caller that meets these checks may read
the brief again. The result includes `provider_rechecked: false` and a notice,
because the check cannot see what the source systems show now.

## Limits

- The selection chooses accounts and Slack threads, not fields. Records keep the
  fields the adapters return.
- Call notes are always part of a selected account. Withhold them by giving a
  client a grant without `calls:read`.
- A connected server can still run without `--selection`. It then behaves as
  before, with the whole source visible to any `crm:read` grant.
- Caller-prepared briefs live apart from the owner's `briefs/` folder, so
  `check-brief` and the usage report do not read them.
- The final grant check happens when the tool finishes, just before the receipt
  is written. A grant that expires in that gap still receives the response.
- Salesforce has no local sign-out signal. Logging out of the sf CLI does not
  withhold saved Salesforce content; the pinned connection identity is the
  check, until the server restarts.
- The reads inside a brief write receipts but no `serve` event lines. The event
  stream shows the outer call; the audit log shows every read.
