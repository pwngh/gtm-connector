# Working a deal

**Open a renewal and know in 30 seconds where it stands, what changed, and what is blocking it.**

Deals is the home page for sales reps and CSMs. It shows quotes, sources and times. The plumbing
behind them is in Ops.

```sh
gtm deal thornfield-2027            # the card, in a terminal
gtm deal thornfield-2027 --refresh  # read the sources again first
```

## The page

| Part | What it shows |
| --- | --- |
| Deal list, on the left | Active deals sorted by urgency: red for a risk or blocker, amber for new changes, green for on track. A deal with an unread source says so and is never green. |
| Deal card, in the main pane | What changed since you last looked, then the risks and blockers. |
| Proof drawer | The record, or the whole sentence with its speaker, time and source, behind the line you clicked. |
| Communications | The deal's calls and Slack threads in time order. |
| Work queue | Your open blockers and playbook work, across all your deals. |

## The five steps

1. **Your calls and Slack threads find the deal.** The connector matches them to your account by email, domain and channel. You do nothing.
2. **Get a Slack alert when a renewal turns risky.** The alert names the rule that fired and links to the card.
3. **Refresh before your call,** if you want the last few minutes. Scheduled reads already keep the card current.
4. **Read the Deal card,** your 30-second renewal snapshot. Click any line for its source proof, track blockers on the checklist and playbook, or ask the council what is blocking the renewal.
5. **The deal closes.** On Closed Won or Closed Lost the reads stop and the final card is sealed and archived.

If your engineer has turned the matches queue on, new calls and threads wait for your OK first.

## What flags a deal

| Flag | When it is raised |
| --- | --- |
| Customer silence | No customer reply near the renewal, in the sources that were read |
| Close-date slippage | The renewal date was pushed again |
| Single-threaded deal | Only one person on the customer side is talking |
| Pricing or seat conflict | A figure in Slack or on a call differs from the CRM. A person resolves it. |
| Legal or security blocker | Redlines or riders are holding up signature |

If a source was not read, a rule answers unknown, not silence.

## The card

- **Renewal countdown and ARR,** from typed CRM fields.
- **What changed since last time:** the sentences, record values and unread sources that differ from the card you last saw. Your position is your own. A teammate reading the card does not clear your changes.
- **Exact buyer quotes and titles.** A quotation is one whole sentence, never a cut one.
- **Promises you made:** seller sentences marked as commitments.
- **Missing or unread sources.**

If the account has two open deals, a call or thread about the account is on both cards, marked as
shared.

## The checklist and playbooks

The checklist lists the deal's open blockers, filled in from the flags. You can also mark any whole
sentence as a blocker or as a commitment. The label comes from a fixed list and is checked against
the speaker's role.

Resolving a blocker takes later proof, your decision, and a reason. The resolved blocker stays in
the deal's history.

A playbook turns a blocker into a plan. There are three: obtain the current rider, review a
renewal condition, and reconcile an approval conflict. A reviewer approves one plan revision and
the evidence it cites. If either changes, then the review reopens.

## Ask the council

Ask "what is blocking this renewal?" and Claude, Codex and your local models each answer from the
same deal, card and proof, over MCP. Where they disagree, you see it. Each answer cites stored
records. A model can also propose marks, and you accept or reject each one.

> **No model writes the card.** A model's answer is never card text, and a proposal becomes a mark
> only when you accept it.

## When something fails

| What happened | What you see |
| --- | --- |
| A read failed during refresh | The last sealed card, its read time, and the reason. No new card. |
| The evidence failed a check | No card. The last sealed card stays. |
| You lost access to a source | That source shows as not read on your card |
| The deal has no card yet | The failure alone |
