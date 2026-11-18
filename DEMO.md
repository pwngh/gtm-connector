# Walk through the renewal demo

The quick command and this longer walkthrough follow the same invented rep,
Morgan Reed, preparing Juniper Crest Learning's renewal. Both use the same CRM,
call, and Slack fixtures. The longer walkthrough explains the saved evidence
and checks; it does not switch to a different story or more authentic data.

For the short recording, see [the recording and rerun script](docs/recording.md).

From a clean checkout, run:

```sh
./scripts/five-minute-demo.sh
```

Use macOS or Linux with Python 3.11+, SQLite, and venv support. The first
installation needs internet. Download time varies and sits outside the
walkthrough. Runtime needs no external services, account, model, or API key.
The script runs the tests and demo, saves JSON results, checks the same brief,
and reads its usage. Errors and recovery commands go to stderr.

The script prints its private output directory. Set `demo_state` to that
folder's `state` subdirectory:

```sh
demo_state='/replace/with/printed/output/directory/state'
```

## Find the account

Morgan looks up Juniper Crest Learning. Its renewal is January 29, fourteen days
after the fixed demo date of January 15, 2027. These dates are scenario data,
not the date the program runs. The contact has no recorded role, so the brief
says the role is not provided.

The opening source line identifies CRM, call notes, and Slack as invented demo
data. This command does not connect to Salesforce or Slack.

## Keep permission visible

The lookup uses a CRM-only grant. Attempting to read the account's call note
with that grant returns HTTP 403. The demo then explicitly grants CRM, calls,
and Slack read access through its local owner emulator.

A source read does not grant access to another source. The owner of this local
state can grant all scopes; this is not an organization-wide identity system.

## Read the brief and proposed follow-up

The saved brief cites the CRM account and opportunity, the call note, and the
Slack thread. The note and message include injected instructions. They remain
quoted data in a workflow whose reads were chosen in advance. No model selects
a tool, no activity is created, and nothing is sent.

The proposed follow-up is part of the checked brief. It remains a proposal.
Copy the printed brief ID for the following commands:

```sh
.venv/bin/python -m gtm_connector audit --brief brief-REPLACE_WITH_ID --state-dir "$demo_state"
.venv/bin/python -m gtm_connector usage --brief brief-REPLACE_WITH_ID --state-dir "$demo_state"
```

The brief has four successful read receipts. The earlier lookup and refusal
bring the demo total to six receipts: five successes and one denial. Fresh state
contains one saved brief and zero activities. The fixture drafter uses zero
model tokens. Retrying the demo creates another brief and six more receipts;
it does not create an activity.

## Check what can be published

The script checks the saved text, then appends `The renewal was approved.` and
checks again. The original is accepted. The uncited addition fails with exit
`4` and empty stdout. The candidate file is then restored to the original text.

The checker compares wording and references with the saved evidence. It does
not verify that the source is true or detect arbitrary business contradictions.
The separate [evaluation](docs/evaluation.md) exercises the whole fixture book.

## Keep the other workflows separate

The guarantee checks exercise scope isolation, rate limits, and safe write
retries against the same invented fixture book. They intentionally create one
local activity to test idempotency, so use separate state:

```sh
.venv/bin/python -m gtm_connector verify --with-slack --state-dir var/verification
```

For an external MCP client, follow the [Inspector guide](docs/inspector.md).
The recording ends with a separate Inspector CLI tools listing. That optional
client check requires Node and an npm download; it is not another renewal story.

The [Salesforce demo kit](docs/salesforce-demo.md) runs the workflow against
a smaller, separate book of invented records that you import into an existing
org, with matching local call notes and Slack fixtures. It follows the same
renewal workflow; its record identifiers and dates belong to that imported kit. It is explicitly a mixture of connected CRM and
invented local sources. Ordinary [Salesforce reads](docs/salesforce.md) require
an eligible Contract date. The connector remains read-only.

See [SECURITY_REVIEW.md](SECURITY_REVIEW.md) for local ownership, manual retention,
and the limits of saved receipts and evidence. Keep connected records and state
private. No live org has been verified.
