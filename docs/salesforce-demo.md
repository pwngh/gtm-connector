# Run the invented Salesforce demo book

The kit imports three invented accounts into an existing Developer Edition org.
The connector reads those CRM records and joins invented local call notes and
Slack messages to them by AccountNumber. It connects to no call provider or
Slack workspace. The connector stays GET-only. The import and removal use your
Salesforce CLI permissions.

The binding, refusals, and cleanup are tested with invented responses and a fake
CLI. No live org has been verified.

## Import and read a brief

Run these from the checkout root with the repository's Python environment and an
existing `sf` login. Replace `gtm-de` with your alias, or follow the
[Salesforce setup](salesforce.md) first.

```sh
umask 077
.venv/bin/python scripts/salesforce-demo.py prepare --output var/salesforce-demo/kit
sf data import tree --target-org gtm-de --plan var/salesforce-demo/kit/plan.json --json > var/salesforce-demo/import.json
.venv/bin/python scripts/salesforce-demo.py bind --receipt var/salesforce-demo/import.json --output var/salesforce-demo/binding.json
.venv/bin/python scripts/salesforce-demo.py brief --sf-org gtm-de --binding var/salesforce-demo/binding.json
```

- Use an empty Developer Edition demo org with no customer data or automation.
- Keep the receipt and binding until removal finishes. AccountNumber is not
  unique across an org, so it cannot recover a lost receipt.
- The import creates twelve records: three Accounts, each with one Contact, one
  Opportunity, and one Draft Contract. The file supplies no EndDate. Salesforce
  computes it from StartDate and ContractTerm, and no contract is activated.
- The local stores hold three call notes and three Slack messages.

| Helper action | Options | Result |
| --- | --- | --- |
| `prepare` | `--output NEW_FOLDER` | Writes `plan.json`, `accounts.json`, and `manifest.json`. |
| `bind` | `--receipt FILE --output FILE` | Checks the complete receipt against the packaged manifest and writes a private binding of the twelve IDs. |
| `brief` | `--sf-org`, `--binding`, optional `--account-number` (default `GTM-DEMO-001`), `--state-dir` (default `var/salesforce-demo/run`), `--json` | Runs the ordinary `brief` with `--with-slack` and the kit's planning date, `2027-01-15`. |
| `remove` | `--sf-org`, `--binding` | Deletes the imported records. |
| `record` | `--sf-org`, `--binding`, `--output NEW_FILE` | Records the brief with `asciinema`. |

| AccountNumber | Invented account | What to inspect |
| --- | --- | --- |
| GTM-DEMO-001 | Alder Vale Packaging | A Draft contract ending 2027-01-31, a Closed Won opportunity, a call note about unapproved procurement, and a Slack message about a delayed security review. |
| GTM-DEMO-002 | Harborlight Field Services | An October note predates the January staffing discussion. |
| GTM-DEMO-003 | Juniper Crest Learning | Industry, contact title, and opportunity amount are missing. An instruction inside a note stays quoted data. |

Each quote carries its own CRM, calls, or Slack citation. The follow-up is a
proposal. No message or activity is sent.

## Use the MCP server

```sh
gtm-connector serve --crm-backend salesforce --sf-org gtm-de --salesforce-demo-binding var/salesforce-demo/binding.json --state-dir var/salesforce-demo/server
```

Use the [client setup commands](clients.md) for that state folder. The
`crm:read`, `calls:read`, and `slack:read` grants stay independent.

- The first bound run needs an empty state folder. The folder then records its
  binding and connection, and a restart must use the same ones.
- A different binding, bound state opened without its binding, or a binding on
  existing fixture state fails before any source changes. Use a fresh
  `--state-dir` for a new import.
- `--salesforce-demo-binding` requires `--crm-backend salesforce`. It cannot be
  combined with `--selection`, `--calls-backend imported`, or
  `--slack-backend slack`.
- `get_account` checks the returned AccountNumber against the binding without an
  extra GET. Reading the local notes or Slack fixtures makes no Salesforce
  request. An unbound account has no demo notes or messages.
- The binding adds no scope and does not prove which org produced the receipt.

## Remove the imported book

Stop the demo server and stop editing the book, then run:

```sh
.venv/bin/python scripts/salesforce-demo.py remove --sf-org gtm-de --binding var/salesforce-demo/binding.json
```

Before the first delete, the helper checks all of the following. A failed check
stops it before any delete.

- The org is Developer Edition.
- Only the exact imported IDs are selected, never a name prefix or AccountNumber.
- Every kit field still has its original value, including the three null
  fields, and the contracts are still Draft. Other fields are not compared.
- Cascading and restricted child relationships of all four objects hold no
  unexpected records. System history rows and Owner sharing rows are allowed.
  Other children, manual shares, and relationships it cannot inspect refuse.

Contacts, Opportunities, and Contracts are deleted before Accounts. The helper
then verifies that all imported IDs are absent. A retry skips records that are
already gone. If a command fails, keep the binding and receipt. A helper error
exits `3` and reports attempted and confirmed deletes without record details.
One removal stops after 80 `sf` commands or 240 seconds.

The checks and deletes are separate operations, not a transaction. They cannot
stop another person or custom automation changing records during cleanup, and a
late failure can leave some records removed. Removal does not erase local
briefs, receipts, databases, or recordings under `var/`. Remove those yourself.

## Record the brief

With `asciinema` installed and the imported book still present, run:

```sh
.venv/bin/python scripts/salesforce-demo.py record --sf-org gtm-de --binding var/salesforce-demo/binding.json --output var/salesforce-demo/live.cast
asciinema play var/salesforce-demo/live.cast
```

The output must be a new file under `var/`. The helper writes a private cast and
a `.provenance.json` file beside it and captures no keyboard input. The cast
contains live record names and IDs, and receipts and bindings contain org
identifiers. Keep all of them private and out of Git.
