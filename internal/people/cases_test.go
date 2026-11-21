package people_test

import (
	"slices"
	"testing"

	p "github.com/pwngh/gtm-connector/internal/people"
)

// The invented deal: Acme (acc-acme) renews with the seller. Acme Group (acc-group) is configured as its
// parent; Holdco (acc-holdco) is a parent that is not configured.

func contact(key, name, account, address string, more ...p.Identifier) p.Person {
	ids := []p.Identifier{{Kind: p.SalesforceContact, Value: "003" + key, Scope: account, From: "crm/contacts/" + key},
		{Kind: p.Name, Value: name, From: "crm/contacts/" + key}}
	if address != "" {
		ids = append(ids, p.Identifier{Kind: p.Email, Value: address, From: "crm/contacts/" + key})
	}
	return p.Person{Key: key, Identifiers: append(ids, more...)}
}

func user(key, name, address string, more ...p.Identifier) p.Person {
	ids := []p.Identifier{{Kind: p.SalesforceUser, Value: "005" + key, From: "crm/users/" + key},
		{Kind: p.Name, Value: name, From: "crm/users/" + key}}
	if address != "" {
		ids = append(ids, p.Identifier{Kind: p.Email, Value: address, From: "crm/users/" + key})
	}
	return p.Person{Key: key, Identifiers: append(ids, more...)}
}

func world() p.Known {
	morgan := contact("morgan", "Morgan Ellis", "acc-acme", "morgan@seller.example")
	morgan.Identifiers = append(morgan.Identifiers, p.Identifier{Kind: p.SalesforceUser, Value: "005morgan",
		From: "crm/users/morgan"})
	return p.Known{
		Account: "acc-acme",
		Related: []string{"acc-group"},
		People: []p.Person{
			contact("dana", "Dana Crest", "acc-acme", "dana@acme.example",
				p.Identifier{Kind: p.SlackUser, Value: "U0DANA", Scope: "T0ACME", From: "slack/users/U0DANA"}),
			contact("lee", "Lee Park", "acc-acme", "lee@acme.example"),
			contact("pat", "Pat Moss", "acc-group", "pat@acmegroup.example"),
			contact("quinn", "Quinn Hale", "acc-holdco", "quinn@holdco.example"),
			contact("robin", "Robin Vale", "acc-acme", "robin@globex.example"),
			contact("jordan-contact", "Jordan Lake", "acc-acme", "jordan.lake@acme.example"),
			contact("casey-1", "Casey Fox", "acc-acme", "casey@acme.example"),
			contact("casey-2", "Casey Fox", "acc-acme", "cfox@acme.example"),
			contact("taylor", "Taylor Quill", "acc-acme", "taylor@seller.example"),
			contact("drew", "Drew Hart", "acc-acme", "drew@freemail.example"),
			morgan,
			user("sam", "Sam Reed", "sam@seller.example"),
			user("jordan-seller", "Jordan Lake", "jordan@seller.example"),
			{Key: "avery", Identifiers: []p.Identifier{{Kind: p.ListedSeller, Value: "Avery Stone", From: "sellers"}}},
		},
		CompanyDomains: []string{"seller.example"},
		AccountDomains: []string{"acme.example", "acmegroup.example", "reseller.example"},
		SharedDomains:  []string{"freemail.example", "reseller.example"},
		HomeTeam:       "T0HOME",
		NameVariants:   true,
	}
}

type want struct {
	person     string
	role       p.Role
	rung       p.Rung
	basis      p.Basis
	reason     p.Reason
	domain     string
	candidates []string
}

type speakerCase struct {
	name    string
	ref     p.Ref
	aliases []p.AliasEntry
	want    want
}

func call(label string, attendees ...p.Attendee) p.Ref {
	return p.Ref{Source: p.Call, CallID: "call-7", Label: label, Attendees: attendees, From: "calls/call-7"}
}

func withEmail(r p.Ref, address string) p.Ref { r.Email = address; return r }

func slack(label, team, user, address string) p.Ref {
	return p.Ref{Source: p.Slack, CallID: "thread-1", Label: label,
		Slack: &p.SlackAuthor{Team: team, User: user, Email: address}, From: "slack/thread-1"}
}

var danaAttends = p.Attendee{Label: "Dana Crest", Email: "dana@acme.example"}

var speakerCases = []speakerCase{
	// Labels.
	{name: "label with a trailing organisation, Dana attends",
		ref:  call("Dana Crest (Acme)", danaAttends),
		want: want{person: "dana", role: p.Customer, rung: p.RungVariant, basis: p.ByNameVariant}},
	{name: "label with a trailing organisation, no attendee list",
		ref:  call("Dana Crest (Acme)"),
		want: want{role: p.Unknown, basis: p.NoBasis, reason: p.NoMatch}},
	{name: "label with a trailing organisation, the attendee row uses it",
		ref:  call("Dana Crest (Acme)", p.Attendee{Label: "Dana Crest (Acme)", Email: "dana@acme.example"}),
		want: want{person: "dana", role: p.Customer, rung: p.RungAttendees, basis: p.ByAttendeeList}},
	{name: "initial, the attendee row uses it",
		ref:  call("Dana C.", p.Attendee{Label: "Dana C.", Email: "Dana@Acme.example"}),
		want: want{person: "dana", role: p.Customer, rung: p.RungAttendees, basis: p.ByAttendeeList}},
	{name: "initial, Dana attends under her full name",
		ref:  call("Dana C.", danaAttends),
		want: want{role: p.Unknown, basis: p.NoBasis, reason: p.NoMatch}},
	{name: "lower case, Dana attends",
		ref:  call("dana crest", danaAttends),
		want: want{person: "dana", role: p.Customer, rung: p.RungVariant, basis: p.ByNameVariant}},
	{name: "lower case, no attendee list",
		ref:  call("dana crest"),
		want: want{role: p.Unknown, basis: p.NoBasis, reason: p.NoMatch}},
	{name: "Last, First, Dana attends",
		ref:  call("Crest, Dana", danaAttends),
		want: want{person: "dana", role: p.Customer, rung: p.RungVariant, basis: p.ByNameVariant}},
	{name: "device with a first name names nobody",
		ref:  call("Dana's iPhone", danaAttends),
		want: want{role: p.Unknown, basis: p.NoBasis, reason: p.NoMatch}},
	{name: "device with a full name, Dana attends",
		ref:  call("Dana Crest\u2019s iPhone", danaAttends),
		want: want{person: "dana", role: p.Customer, rung: p.RungVariant, basis: p.ByNameVariant}},
	{name: "device, the attendee row uses it",
		ref:  call("Dana's iPhone", p.Attendee{Label: "Dana's iPhone", Email: "dana@acme.example"}),
		want: want{person: "dana", role: p.Customer, rung: p.RungAttendees, basis: p.ByAttendeeList}},
	{name: "anonymous label without an alias",
		ref:  call("Speaker 1"),
		want: want{role: p.Unknown, basis: p.NoBasis, reason: p.AnonymousLabel}},
	{name: "anonymous label with an alias for this call",
		ref:     call("Speaker 1"),
		aliases: []p.AliasEntry{{Label: "Speaker 1", Call: "call-7", Person: "dana", From: "gtm.yaml@3"}},
		want:    want{person: "dana", role: p.Customer, rung: p.RungAlias, basis: p.ByAlias}},
	{name: "anonymous label with aliases for another call and for every call",
		ref: call("Speaker 1"),
		aliases: []p.AliasEntry{{Label: "Speaker 1", Call: "call-8", Person: "dana", From: "gtm.yaml@3"},
			{Label: "Speaker 1", Person: "sam", From: "gtm.yaml@3"}},
		want: want{role: p.Unknown, basis: p.NoBasis, reason: p.AnonymousLabel}},
	{name: "anonymous label is not read from the attendee list",
		ref:  call("Speaker 1", p.Attendee{Label: "Speaker 1", Email: "sam@seller.example"}),
		want: want{role: p.Unknown, basis: p.NoBasis, reason: p.AnonymousLabel}},
	{name: "empty label",
		ref:  call("  "),
		want: want{role: p.Unknown, basis: p.NoBasis, reason: p.NoLabel}},
	{name: "a named label with an alias for every call",
		ref:     call("Front desk"),
		aliases: []p.AliasEntry{{Label: "Front desk", Person: "lee", From: "coverage@12"}},
		want:    want{person: "lee", role: p.Customer, rung: p.RungAlias, basis: p.ByAlias}},

	// Slack.
	{name: "home workspace author with a company-domain email",
		ref:  slack("U0OPS", "T0HOME", "U0OPS", "ops@seller.example"),
		want: want{role: p.Seller, rung: p.RungPlatform, basis: p.ByDomain, domain: "seller.example"}},
	{name: "home workspace author whose email is a user's",
		ref:  slack("U0SAM", "T0HOME", "U0SAM", "sam@seller.example"),
		want: want{person: "sam", role: p.Seller, rung: p.RungPlatform, basis: p.BySlackProfile}},
	{name: "Slack Connect author whose email is a contact's",
		ref:  slack("Dana", "T0ACME", "U0D", "DANA@acme.example"),
		want: want{person: "dana", role: p.Customer, rung: p.RungPlatform, basis: p.BySlackProfile}},
	{name: "Slack Connect author whose user ID is on a contact, no email",
		ref:  slack("U0DANA", "T0ACME", "U0DANA", ""),
		want: want{person: "dana", role: p.Customer, rung: p.RungPlatform, basis: p.BySlackProfile}},
	{name: "Slack Connect author on the account's domain, not a contact",
		ref:  slack("New CFO", "T0ACME", "U0CFO", "cfo@acme.example"),
		want: want{role: p.CustomerNotInCRM, rung: p.RungPlatform, basis: p.ByDomain, domain: "acme.example"}},
	{name: "Slack Connect author on a free mail domain",
		ref:  slack("Dana Crest", "T0ACME", "U0X", "dana.crest@freemail.example"),
		want: want{role: p.Unknown, basis: p.NoBasis, reason: p.SharedDomain}},
	{name: "Slack author with no email whose name is a contact's",
		ref:  slack("Dana Crest", "T0ACME", "U0Y", ""),
		want: want{role: p.Unknown, basis: p.NoBasis, reason: p.SlackAuthorUnidentified}},
	{name: "Slack author with no email who is a listed seller",
		ref:  slack("Avery Stone", "T0HOME", "U0AV", ""),
		want: want{person: "avery", role: p.Seller, rung: p.RungName, basis: p.ByName}},
	{name: "company-domain email outside the home workspace shows no side",
		ref:  slack("U0Z", "T0ACME", "U0Z", "someone@seller.example"),
		want: want{role: p.Unknown, basis: p.NoBasis, reason: p.SlackAuthorUnidentified}},

	// People.
	{name: "new stakeholder not in the CRM",
		ref:  withEmail(call("Chief Financial Officer"), "cfo@acme.example"),
		want: want{role: p.CustomerNotInCRM, rung: p.RungPlatform, basis: p.ByDomain, domain: "acme.example"}},
	{name: "contact on a configured parent",
		ref:  call("Pat Moss"),
		want: want{person: "pat", role: p.Customer, rung: p.RungName, basis: p.ByName}},
	{name: "contact on a parent that is not configured, by name",
		ref:  call("Quinn Hale"),
		want: want{person: "quinn", role: p.Unknown, rung: p.RungName, basis: p.ByName, reason: p.AccountNotRelated}},
	{name: "contact on a parent that is not configured, by email",
		ref:  withEmail(call(""), "quinn@holdco.example"),
		want: want{person: "quinn", role: p.Unknown, rung: p.RungPlatform, basis: p.ByEmail, reason: p.AccountNotRelated}},
	{name: "contact whose email is on another account's domain",
		ref:  withEmail(call("Robin"), "robin@globex.example"),
		want: want{person: "robin", role: p.Customer, rung: p.RungPlatform, basis: p.ByEmail}},
	{name: "someone else on that domain is another party",
		ref:  withEmail(call("Riley"), "riley@globex.example"),
		want: want{role: p.OtherParty, rung: p.RungPlatform, basis: p.ByDomain, domain: "globex.example"}},
	{name: "seller not on the list who is a Salesforce User",
		ref:  call("Sam Reed"),
		want: want{person: "sam", role: p.Seller, rung: p.RungName, basis: p.ByName}},
	{name: "seller and contact with one name, no email",
		ref: call("Jordan Lake"),
		want: want{role: p.Unknown, rung: p.RungName, basis: p.ByName, reason: p.CandidatesOnBothSides,
			candidates: []string{"jordan-contact", "jordan-seller"}}},
	{name: "seller and contact with one name, the seller's email",
		ref:  withEmail(call("Jordan Lake"), "jordan@seller.example"),
		want: want{person: "jordan-seller", role: p.Seller, rung: p.RungPlatform, basis: p.ByEmail}},
	{name: "seller and contact with one name, the contact's email",
		ref:  withEmail(call("Jordan Lake"), "jordan.lake@acme.example"),
		want: want{person: "jordan-contact", role: p.Customer, rung: p.RungPlatform, basis: p.ByEmail}},
	{name: "two contacts with one name",
		ref: call("Casey Fox"),
		want: want{role: p.Unknown, rung: p.RungName, basis: p.ByName, reason: p.SeveralCandidates,
			candidates: []string{"casey-1", "casey-2"}}},
	{name: "two contacts with one name, one's email",
		ref:  withEmail(call("Casey Fox"), "cfox@acme.example"),
		want: want{person: "casey-2", role: p.Customer, rung: p.RungPlatform, basis: p.ByEmail}},
	{name: "person who is both a contact and a user",
		ref:  call("Morgan Ellis"),
		want: want{person: "morgan", role: p.Unknown, rung: p.RungName, basis: p.ByName, reason: p.PersonOnBothSides}},
	{name: "outside counsel on a third domain",
		ref:  withEmail(call("Kim Ode"), "kim@counsel.example"),
		want: want{role: p.OtherParty, rung: p.RungPlatform, basis: p.ByDomain, domain: "counsel.example"}},
	{name: "reseller domain seen on several accounts",
		ref:  withEmail(call("Ann"), "ann@reseller.example"),
		want: want{role: p.Unknown, basis: p.NoBasis, reason: p.SharedDomain}},
	{name: "a contact's own address on a free domain names the contact",
		ref:  withEmail(call(""), "drew@freemail.example"),
		want: want{person: "drew", role: p.Customer, rung: p.RungPlatform, basis: p.ByEmail}},

	// Rooms.
	{name: "one microphone, every attendee on the customer side",
		ref: call("Acme Boardroom", p.Attendee{Label: "Acme Boardroom", Email: "dana@acme.example"},
			p.Attendee{Label: "Acme Boardroom", Email: "cfo@acme.example"},
			p.Attendee{Label: "Lee Park", Email: "lee@acme.example"}),
		want: want{role: p.Customer, rung: p.RungAttendees, basis: p.ByRoom}},
	{name: "one microphone, attendees on both sides",
		ref: call("Boardroom", p.Attendee{Label: "Boardroom", Email: "dana@acme.example"},
			p.Attendee{Label: "Boardroom", Email: "sam@seller.example"}),
		want: want{role: p.Unknown, rung: p.RungAttendees, basis: p.ByRoom, reason: p.RoomMixed}},

	// Conflicts.
	{name: "an email that names a seller, a name that names a customer",
		ref:  withEmail(call("Dana Crest"), "sam@seller.example"),
		want: want{person: "sam", role: p.Seller, rung: p.RungPlatform, basis: p.ByEmail}},
	{name: "an email on the seller's domain, a name that names a customer",
		ref: withEmail(call("Dana Crest"), "ops@seller.example"),
		want: want{role: p.Unknown, rung: p.RungName, basis: p.ByName, reason: p.RungsDisagree,
			candidates: []string{"dana"}}},
	{name: "an alias that contradicts an email that names a contact",
		ref:     withEmail(call("Speaker 3"), "dana@acme.example"),
		aliases: []p.AliasEntry{{Label: "Speaker 3", Call: "call-7", Person: "sam", From: "gtm.yaml@4"}},
		want:    want{person: "dana", role: p.Customer, rung: p.RungPlatform, basis: p.ByEmail}},
	{name: "an alias that contradicts an email on the account's domain",
		ref:     withEmail(call("Speaker 3"), "cfo@acme.example"),
		aliases: []p.AliasEntry{{Label: "Speaker 3", Call: "call-7", Person: "sam", From: "gtm.yaml@4"}},
		want: want{role: p.Unknown, rung: p.RungAlias, basis: p.ByAlias, reason: p.RungsDisagree,
			candidates: []string{"sam"}}},
	{name: "an email on the account's domain and a name on the same side",
		ref:  withEmail(call("Lee Park"), "lee.park@acme.example"),
		want: want{person: "lee", role: p.Customer, rung: p.RungName, basis: p.ByName}},
	{name: "an email that names a contact, on the seller's domain",
		ref: withEmail(call("Taylor Quill"), "taylor@seller.example"),
		want: want{role: p.Unknown, rung: p.RungPlatform, basis: p.ByEmail, reason: p.EmailDomainDisagrees,
			candidates: []string{"taylor"}}},
	{name: "a Slack user ID and a profile email that name two people",
		ref: slack("U0DANA", "T0ACME", "U0DANA", "lee@acme.example"),
		want: want{role: p.Unknown, rung: p.RungPlatform, basis: p.BySlackProfile, reason: p.SeveralCandidates,
			candidates: []string{"dana", "lee"}}},
}

func TestCases(t *testing.T) {
	for _, c := range speakerCases {
		t.Run(c.name, func(t *testing.T) {
			k := world()
			k.Aliases = c.aliases
			r := p.Resolve(k, c.ref)
			got := want{role: r.Role, rung: r.Rung, basis: r.Basis, reason: r.Reason, domain: r.Domain,
				candidates: r.Candidates}
			if r.Person != nil {
				got.person = r.Person.Key
			}
			if got.person != c.want.person || got.role != c.want.role || got.rung != c.want.rung ||
				got.basis != c.want.basis || got.reason != c.want.reason ||
				c.want.domain != "" && got.domain != c.want.domain ||
				!slices.Equal(got.candidates, c.want.candidates) {
				t.Errorf("got %+v\nwant %+v", got, c.want)
			}
			if r.Rules != p.RulesVersion {
				t.Errorf("rules %d, want %d", r.Rules, p.RulesVersion)
			}
			if (r.Role == p.Unknown) != (r.Reason != p.NoReason) {
				t.Errorf("role %s with reason %q", r.Role, r.Reason)
			}
		})
	}
}

// TestMatchedCarriesItsRead checks that a result names the identifier that decided and the read that
// supplied it.
func TestMatchedCarriesItsRead(t *testing.T) {
	k := world()
	r := p.Resolve(k, withEmail(call(""), "DANA@acme.example"))
	if r.Matched != (p.Identifier{Kind: p.Email, Value: "dana@acme.example", From: "crm/contacts/dana"}) {
		t.Errorf("matched %+v", r.Matched)
	}
	k.Aliases = []p.AliasEntry{{Label: "Speaker 1", Call: "call-7", Person: "dana", From: "gtm.yaml@3"}}
	r = p.Resolve(k, call("Speaker 1"))
	if r.Matched != (p.Identifier{Kind: p.Alias, Value: "Speaker 1", Scope: "call-7", From: "gtm.yaml@3"}) {
		t.Errorf("matched %+v", r.Matched)
	}
	r = p.Resolve(k, withEmail(call("x"), "cfo@acme.example"))
	if r.Matched != (p.Identifier{Kind: p.Email, Value: "cfo@acme.example", From: "calls/call-7"}) {
		t.Errorf("matched %+v", r.Matched)
	}
}
