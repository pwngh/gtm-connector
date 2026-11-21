package people

// RulesVersion is the version of the rules in this package. A card seals it, so an old card still
// verifies under the rules that made it. Any change to what Resolve returns for some input raises it.
const RulesVersion = 1

// Kind is the type of an identifier.
type Kind string

const (
	// Email is an email address. Value is the address.
	Email Kind = "email"
	// SlackUser is a Slack member. Value is the user ID and Scope the team ID.
	SlackUser Kind = "slack_user"
	// SalesforceContact is a Salesforce Contact. Value is the Contact ID and Scope its account ID.
	SalesforceContact Kind = "salesforce_contact"
	// SalesforceUser is a Salesforce User of the org. Value is the User ID.
	SalesforceUser Kind = "salesforce_user"
	// Name is a name the person goes by. Value is the name as the source wrote it.
	Name Kind = "name"
	// ListedSeller is an entry of the operator's seller list. Value is the entry, a name or a Slack ID.
	ListedSeller Kind = "listed_seller"
	// Alias is an alias an engineer mapped to the person. It appears only in a Result.
	Alias Kind = "alias"
)

// Identifier is one typed identifier and the read that supplied it.
type Identifier struct {
	Kind  Kind
	Value string
	Scope string // the Slack team ID of a SlackUser, the account ID of a SalesforceContact
	From  string // the read that supplied it: a receipt ID or a record reference
}

// Person is one person of the workspace with every identifier known for them. A caller merges the records
// of one human (a Contact, a User, a listed seller) into one Person; Resolve never merges two.
type Person struct {
	Key         string // unique within the workspace
	Identifiers []Identifier
}

// AliasEntry maps a speaker label to a person. With Call set it applies to that call only; an anonymous
// label such as "Speaker 1" takes only an alias with Call set.
type AliasEntry struct {
	Label  string
	Call   string
	Person string // a Person's Key
	From   string // where the alias was set: gtm.yaml or Coverage, with its revision
}

// Known is what is known for one deal.
type Known struct {
	Account string   // the deal's account ID
	Related []string // account IDs configured as the account's parent or subsidiary
	// People are the contacts of the account and of its related accounts, the org's users and the listed
	// sellers. A Slack member or meeting participant who is none of these is not a Person: their
	// reference resolves by its email's domain.
	People         []Person
	Aliases        []AliasEntry
	CompanyDomains []string // the seller's own email domains
	AccountDomains []string // the email domains of the account and its related accounts
	SharedDomains  []string // free and shared mail domains, and any domain seen on several accounts
	HomeTeam       string   // the Slack team ID of the seller's own workspace
	NameVariants   bool     // whether rung 5 is on
}

// Source is the kind of source a reference comes from.
type Source string

const (
	Call  Source = "call"
	Slack Source = "slack"
)

// Ref is a speaker reference as a source hands it over.
type Ref struct {
	Source    Source
	CallID    string       // the call or thread the label belongs to; it scopes anonymous labels and aliases
	Label     string       // the speaker label as the source wrote it, or ""
	Slack     *SlackAuthor // the author of a Slack message, when the source read it
	Email     string       // the participant email a meeting tool gave for this speaker
	Attendees []Attendee   // the call's attendee list from an imported sidecar
	From      string       // the read that supplied the reference
}

// SlackAuthor is a Slack message's author: user and team IDs and the profile email, when visible.
type SlackAuthor struct {
	Team, User, Email string
}

// Attendee is one row of a call's attendee list: the label the transcript uses and an email. Two or more
// rows with one label and different emails are people behind one microphone: a room or a shared device.
type Attendee struct {
	Label, Email string
}

// Role is the side a speaker is on.
type Role string

const (
	Seller           Role = "seller"
	Customer         Role = "customer"
	CustomerNotInCRM Role = "customer_side_not_in_crm"
	OtherParty       Role = "other_party"
	Unknown          Role = "unknown"
)

// Rung is the rung of the ladder that decided, strongest first. NoRung is none.
type Rung int

const (
	NoRung        Rung = 0
	RungPlatform  Rung = 1 // a Slack author's user ID and profile email, or a meeting tool's participant email
	RungAttendees Rung = 2 // the call's attendee list
	RungAlias     Rung = 3 // an alias
	RungName      Rung = 4 // the label equals a name
	RungVariant   Rung = 5 // a name variant among the meeting's known participants
)

// Basis is how a result was reached, as a fixed code.
type Basis string

const (
	BySlackProfile Basis = "slack_profile"     // the Slack user ID or profile email names the person
	ByEmail        Basis = "participant_email" // the meeting tool's participant email names the person
	ByAttendeeList Basis = "attendee_list"     // the attendee row with the label names the person
	ByAlias        Basis = "alias"             // an alias names the person
	ByName         Basis = "name"              // the label equals the person's name or seller entry
	ByNameVariant  Basis = "name_variant"      // the label's variant equals a participant's name variant
	ByDomain       Basis = "domain"            // the email's domain puts the speaker on a side
	ByRoom         Basis = "room"              // every attendee behind the label is on one side
	NoBasis        Basis = "none"              // no rung found anything
)

// Reason is why a result is unknown, as a fixed code.
type Reason string

const (
	NoReason Reason = ""
	// NoLabel: the reference has no label and no other identifier.
	NoLabel Reason = "no_label"
	// NoMatch: no rung found a person or a side.
	NoMatch Reason = "no_match"
	// AnonymousLabel: the label names no one ("Speaker 2") and no alias for this call maps it.
	AnonymousLabel Reason = "anonymous_label"
	// SlackAuthorUnidentified: a Slack author that no identity, alias or seller entry names. A Slack
	// author's display name never makes a customer.
	SlackAuthorUnidentified Reason = "slack_author_unidentified"
	// SharedDomain: the only email is on a free or shared domain, which never shows a side.
	SharedDomain Reason = "shared_domain"
	// SeveralCandidates: the deciding rung found more than one person, all on one side.
	SeveralCandidates Reason = "several_candidates"
	// CandidatesOnBothSides: the deciding rung found people, or sides, on more than one side.
	CandidatesOnBothSides Reason = "candidates_on_both_sides"
	// RungsDisagree: a stronger rung put the speaker on a side without naming a person, and the person a
	// weaker rung found is on another side.
	RungsDisagree Reason = "rungs_disagree"
	// EmailDomainDisagrees: the email names a person whose side its own domain contradicts: a contact
	// with an address on the seller's domain, or a seller with one on the account's.
	EmailDomainDisagrees Reason = "email_domain_disagrees"
	// PersonOnBothSides: the person is a contact of the account and also a user or a listed seller.
	PersonOnBothSides Reason = "person_on_both_sides"
	// AccountNotRelated: the person is a contact only of an account not configured as related.
	AccountNotRelated Reason = "account_not_related"
	// PersonWithoutSide: the person is no contact, no user and no listed seller.
	PersonWithoutSide Reason = "person_without_side"
	// RoomMixed: the attendees behind a room or shared device are not all on one side.
	RoomMixed Reason = "room_mixed"
)

// Result is what Resolve decides for one reference.
type Result struct {
	Person *Person // the person, or nil; it points into Known.People
	Role   Role
	Rung   Rung
	Basis  Basis
	Reason Reason // set only when Role is Unknown
	// Matched is the identifier that decided: the person's identifier that matched, the alias, or the
	// reference's own email for a result by domain. It is empty for a room and when nothing decided.
	Matched Identifier
	// Domain is the email domain a result by domain rests on; for OtherParty it names the organisation.
	Domain string
	// Candidates are the keys of the people the deciding rung found, sorted, when it found more than one
	// or when their sides disagreed with a stronger rung.
	Candidates []string
	Rules      int // RulesVersion
}
