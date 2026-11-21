// Package people decides who said a sentence and for which side. Resolve takes what is known for one deal
// and one speaker reference, and returns the person or none, a role, the rung that decided, a basis code,
// and for an unknown role a reason code. Codes are fixed strings; this package never writes text for a
// card. Resolve reads nothing but its arguments: no I/O, no clock, no globals, and the order of any list
// in its input never changes its result. internal/evidence calls it for every sentence of a card.
//
// # People
//
// A person is a contact of the deal's account or of an account configured as its parent or subsidiary,
// a Salesforce User of the org, or a listed seller. A caller merges one human's records into one Person.
// Each identifier carries the read that supplied it, and a result names the identifier that decided.
//
// A person's role comes from their records, never from a label's spelling or what a sentence says:
//
//   - seller: a Salesforce User or a listed seller;
//   - customer: a contact of the account or of a related account;
//   - unknown, person_on_both_sides: a contact who is also a user or a listed seller;
//   - unknown, account_not_related: only a contact of an account not configured as related.
//
// A speaker who is no person can still be placed by an email's domain: the seller's own domain makes a
// seller (for Slack, only a member of the seller's own workspace), the account's domain makes
// customer_side_not_in_crm, and any other domain makes other_party, shown with the domain. A free or
// shared domain never shows a side, and a domain on both the seller's list and the account's counts as
// shared. A member of the seller's own Slack workspace is never put on the customer side by a domain.
//
// A ParticipantName, such as a Salesforce user's, is compared only by rung 5, for a person the attendee
// list names by email, and never with a label on its own.
//
// # The ladder
//
// The rungs run from the strongest identifier to the weakest:
//
//  1. Platform: a Slack author's user ID and team ID, the Slack profile email, or a meeting tool's
//     participant email. An address names the person who holds it, on any domain.
//  2. Attendees: the rows of the call's attendee list whose label is the speaker's. One row names the
//     person who holds its email. Several rows under one label are people behind one microphone, a room
//     or a shared device: it takes a side only when every one of them is on that side.
//  3. Alias: a label an engineer mapped to a person, for one call or for every call.
//  4. Exact name: the label equals a person's name or seller entry, compared on one line (Label). This
//     is the rule cards use today, and it gives the same answer on every recorded card.
//  5. Name variant, when switched on: the label's Variant equals the Variant of a name of exactly one of
//     the meeting's known participants, the people the attendee list's emails name.
//
// # The rules
//
// The first rung that finds exactly one person decides, and no weaker rung is read after it.
//
// A rung that finds two or more people decides unknown, with several_candidates, or with
// candidates_on_both_sides when sellers and customers are among them. Nothing is picked.
//
// A rung that finds a side but no person (a domain, or a room) does not decide: a weaker rung may still
// name a person on that side, and that person is the result. A weaker rung that names a person on
// another side makes the result unknown with rungs_disagree. A weaker rung that finds several people,
// or a person whose own side is unknown, leaves the side as it is. When no weaker rung names a person,
// the side is the result.
//
// An email that names a person whose side the email's own domain contradicts (a contact with an
// address on the seller's domain, a seller with one on the account's) gives unknown with
// email_domain_disagrees.
//
// A Slack author's name never makes a customer: for Slack, rungs 4 and 5 compare only sellers, so an
// author no identity, alias or seller entry names is unknown with slack_author_unidentified.
//
// An anonymous label ("Speaker 2", see Anonymous) is one unnamed speaker within its call. Only an alias
// set for that call gives it a person; the attendee list and the name rungs never read it.
//
// When nothing decides, the reason is shared_domain if an email on a free or shared domain led nowhere,
// then anonymous_label, no_label, slack_author_unidentified for Slack, and no_match otherwise.
//
// RulesVersion numbers these rules. A card seals it, so an old card verifies under the rules that made
// it.
package people
