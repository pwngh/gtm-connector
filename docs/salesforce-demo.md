# Run the invented Salesforce demo book

This kit puts three invented accounts into an existing Developer Edition org.
The connector reads those live CRM records and combines them with invented local
call notes and Slack messages. AccountNumber supplies the explicit link. This
does not connect to a call provider or a Slack workspace.

The steps below are commands for you to run from the checkout root. They need
the repository's Python environment and an existing Salesforce CLI login named
`gtm-de`. Use the alias for your demo org if it differs. Follow the
[Salesforce setup](salesforce.md) first if the CLI is not signed in. The import
and removal use your Salesforce CLI permissions. The connector stays GET-only.

## Import and read a brief

Choose an empty Developer Edition demo org. Do not add customer data, automation,
or unrelated records to this book. Keep the receipt and binding until removal
finishes. AccountNumber is not unique across an org, so it cannot recover a lost
receipt safely.

Prepare private output and export the three packaged kit files:

```sh
umask 077
.venv/bin/python scripts/salesforce-demo.py prepare --output var/salesforce-demo/kit
```

Import all twelve records with one command. Salesforce computes Contract EndDate
from StartDate and ContractTerm; the file supplies neither EndDate nor an activation
update. All three contracts remain Draft.

```sh
sf data import tree --target-org gtm-de --plan var/salesforce-demo/kit/plan.json --json > var/salesforce-demo/import.json
```

Bind the complete successful receipt, then read the first account:

```sh
.venv/bin/python scripts/salesforce-demo.py bind --receipt var/salesforce-demo/import.json --output var/salesforce-demo/binding.json
.venv/bin/python scripts/salesforce-demo.py brief --sf-org gtm-de --binding var/salesforce-demo/binding.json
```

The brief uses the kit's fixed planning date, January 15, 2027. Expect a Draft
contract ending January 31, a Closed Won opportunity, a call quote saying
procurement is unapproved, and a Slack quote saying security review is delayed.
The quotes carry separate CRM, calls, and Slack citations. The follow-up remains
a proposal. No message or activity is sent.

The same command accepts `--account-number GTM-DEMO-002` or `GTM-DEMO-003`:

| AccountNumber | Invented account | What to inspect |
| --- | --- | --- |
| GTM-DEMO-001 | Alder Vale Packaging | CRM, procurement, and security signals disagree. |
| GTM-DEMO-002 | Harborlight Field Services | An October note predates the January staffing discussion. |
| GTM-DEMO-003 | Juniper Crest Learning | Industry and contact title are missing; an injected note stays quoted. |

There are three Accounts, three Contacts, three Opportunities, and three Draft
Contracts. The local fixture stores hold three notes and three Slack messages.
All names, addresses, and source text in the packaged kit are invented.

## Use the ordinary CLI and MCP server

The helper resolves AccountNumber through the private import binding and calls
the ordinary `brief` command with `--with-slack`. To use the same bound source
stores in an MCP server, choose a separate fresh state folder:

```sh
gtm-connector serve --crm-backend salesforce --sf-org gtm-de --salesforce-demo-binding var/salesforce-demo/binding.json --state-dir var/salesforce-demo/server
```

Use the [client setup commands](clients.md) for that state folder. The source
permissions stay independent: `crm:read`, `calls:read`, and `slack:read`.
The startup screen names the selected Salesforce backend and demo kit, and labels
calls and Slack as demo fixtures. It reports the selection without claiming that
Salesforce connectivity was verified before the first CRM read.
Reading local notes or Slack fixtures does not issue a Salesforce request.
The binding is an explicit owner mapping. The CRM read checks AccountNumber
against it without an extra GET.

The private state folder records its source profile. Restarting with the same
binding preserves the local sources. Switching bindings, opening bound state
without its binding, or overlaying a binding on existing default fixtures fails
before source changes. Choose another fresh `--state-dir` for a new import.

## Remove the imported book

Stop the demo server and finish editing the book before removal. Run one command:

```sh
.venv/bin/python scripts/salesforce-demo.py remove --sf-org gtm-de --binding var/salesforce-demo/binding.json
```

The helper checks the selected edition, exact imported IDs, original kit field
values, account associations, and Draft status. The third account's empty Industry,
contact Title, and opportunity Amount must still be null. Every checked field
must be present in the response. Fields outside the kit are not compared.
It checks cascading and restricted child
relationships on Accounts, Contacts, Opportunities, and Contracts before the
first delete. It refuses changed kit fields, unrelated children, manual shares, or
relationships it cannot inspect. A small fixed list permits system-created
history and Owner sharing rows derived from the imported records.
An ordinary related record must also have passed the direct inventory and field
checks and be scheduled for deletion; a missing inventory row is not sufficient.

Contacts, Opportunities, and Contracts are removed before Accounts. The helper
then verifies that all imported IDs are absent. Missing records are skipped on
a retry. It never chooses records by a name prefix or searches AccountNumber
to reconstruct a deletion list. If a command fails, keep the binding and receipt;
the error reports attempted and confirmed deletes without record details.

The preflight sees records accessible to the selected user and is separate from
the deletes. It cannot prevent another person adding a child during cleanup or
custom automation changing other records. Use the isolated demo org and stop
editing while cleanup runs. It is not a transactional cascade guarantee.

Removal does not erase private local briefs, receipts, databases, or recordings.
Review and remove those files yourself when finished. The repository ignores
`var/`; none of its contents belong in this PR.

## Record your live demo

Install the optional `asciinema` recording tool described in
[the recording guide](recording.md). With the imported book still present, run:

```sh
.venv/bin/python scripts/salesforce-demo.py record --sf-org gtm-de --binding var/salesforce-demo/binding.json --output var/salesforce-demo/live.cast
asciinema play var/salesforce-demo/live.cast
```

The script records the actual brief command and identifies the source modes on
screen. It captures no keyboard input, shell prompt, token, or credential command
output. It writes a private cast and provenance file under ignored `var/`, and
does not stage, commit, upload, or publish them. The recording contains live
record names and IDs in the requested brief, so review it privately. The decision
to retain or share it is yours. Do not commit org data.

The source-data binding, refusal paths, and cleanup behavior are tested with
invented responses and a fake CLI. A fake response test alone does not establish
a live Salesforce, Slack, or assistant-app connection.

An authorized Developer Edition run on November 17, 2026 completed the four
prepare/import/bind/brief steps in 7.565 seconds of command time on an M1 Max
development machine. Dependencies and CLI login were already available; that
number excludes installation, login, and typing. The accepted brief had thirteen
cited lines, four audit receipts, and evidence from live CRM plus the two local
fixture sources. It retained the Draft disclosure and Salesforce's calculated
January 31, 2027 end date.

The private live recording also completed. Removal used 74 captured Salesforce
CLI commands in 99.557 seconds, confirmed twelve deletes, and verified zero
remaining imported records. The relationship preflight accounts for most of
those commands. These are observations of one isolated demo run, not a timing
guarantee. No live Slack workspace, call provider, or hosted assistant was used.
All org-bearing receipts, snapshots, metadata, and recording output stayed
private and outside committed source.

## Implementation references

The kit uses Salesforce's [tree import command](https://developer.salesforce.com/docs/platform/salesforce-cli-reference/guide/cli_reference_data_import_tree.html).
Cleanup uses [object metadata](https://developer.salesforce.com/docs/platform/api-rest/guide/resources-sobject-describe.html)
and [exact-record deletion](https://developer.salesforce.com/docs/platform/salesforce-cli-reference/guide/cli_reference_data_delete_record.html).
The [Contract reference](https://developer.salesforce.com/docs/platform/object-reference/references/standard-objects/object-contract.html)
defines the calculated end date and separate activation step.
