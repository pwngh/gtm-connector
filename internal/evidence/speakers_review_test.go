package evidence

import (
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/people"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The ways a speaker could be put on the wrong side, each closed: a Salesforce user's name, a domain one
// contact holds, a free-mail domain not on the list, a seller's own workspace member, and source text in a
// domain. The evidence is an oracle card's, with its records changed.

// addContact adds a contact of the card's account, returned by the account's read.
func addContact(t *testing.T, evidence *strictjson.Object, native, name, email string) {
	t.Helper()
	account := member(evidence, "account_id").(string)
	salesforce := member(evidence, "crm_backend") == "salesforce"
	id := "con-" + native
	contact := strictjson.NewObject("id", id, "account_id", account, "name", name, "title", "Buyer", "email", email,
		"record_ref", "crm/contacts/"+id)
	if salesforce {
		id = "con-sf-" + native
		contact = strictjson.NewObject("id", id, "source_id", native, "backend", "salesforce", "account_id", account,
			"name", name, "title", "Buyer", "email", email, "record_ref", "crm/contacts/"+id)
	}
	evidence.Set("records", append(member(evidence, "records").([]any), contact))
	receipt := receiptOf(evidence, "get_account")
	rows := append(member(receipt, "rows").([]any), "crm/contacts/"+id)
	receipt.Set("rows", rows)
	receipt.Set("row_count", len(rows))
}

// salesforceCard is the evidence of a Salesforce card whose org has these users and whose fixture call notes
// hold one note by each speaker.
func salesforceCard(t *testing.T, users []*strictjson.Object, speakers ...string) *strictjson.Object {
	t.Helper()
	evidence := sealedEvidence(t, "books_salesforce", "exports-gemini/0")
	account := member(evidence, "account_id").(string)
	records := member(evidence, "records").([]any)
	var userRows, noteRows []any
	for _, user := range users {
		records = append(records, user)
		userRows = append(userRows, member(user, "record_ref"))
	}
	for i, speaker := range speakers {
		id := "note-review-" + string(rune('a'+i))
		records = append(records, strictjson.NewObject("id", id, "account_id", account,
			"called_at", "2027-01-10T15:00:00Z", "speaker", speaker, "body", "We will sign the order form.",
			"record_ref", "calls/call_notes/"+id))
		noteRows = append(noteRows, "calls/call_notes/"+id)
	}
	evidence.Set("records", records)
	for tool, rows := range map[string][]any{"list_users": userRows, "read_call_notes": noteRows} {
		if rows == nil {
			rows = []any{}
		}
		receiptOf(evidence, tool).Set("rows", rows)
		receiptOf(evidence, tool).Set("row_count", len(rows))
	}
	evidence.Set("people", strictjson.NewObject("rules_version", SpeakerRules, "company_domains", []any{},
		"aliases", []any{}, "users", "read"))
	return evidence
}

func salesforceUser(native, name, email string, active bool) *strictjson.Object {
	return strictjson.NewObject("id", "usr-sf-"+native, "source_id", native, "backend", "salesforce", "name", name,
		"email", email, "is_active", active, "record_ref", "crm/users/usr-sf-"+native)
}

// A customer who is not in the CRM is never a seller because a Salesforce user of the org has the same
// name. A user is a person by email and user ID; a label is never compared with a user's name.
func TestAUserNameNeverNamesALabel(t *testing.T) {
	evidence := salesforceCard(t, []*strictjson.Object{
		salesforceUser("005000000000000AAA", "David Lee", "dlee@"+sellerDomain, true),
		salesforceUser("005000000000000AAB", "Invented author", "author@"+sellerDomain, false)}, "David Lee")
	units, _, _, _, err := Build(evidence)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, u := range units {
		if u.Kind != "sentence" || u.Quoted {
			continue
		}
		checked++
		if u.Role == "seller" {
			t.Errorf("%q is a seller by %q", *u.Speaker, u.Basis)
		}
	}
	if checked < 3 {
		t.Fatalf("%d sentences", checked)
	}
	// A user record in evidence made before the speaker rules is no table it can hold.
	evidence.Delete("people")
	if _, _, _, _, err := Build(evidence); err == nil || err.Error() != "Unknown or duplicate source reference." {
		t.Errorf("evidence with no people object and a user: %v", err)
	}
}

// A user's name is compared within the meeting's known participants only, and only an active user's.
func TestAUserNameNamesOnlyAKnownParticipant(t *testing.T) {
	user := func(active bool) people.Person {
		ids := []people.Identifier{{Kind: people.SalesforceUser, Value: "u1"}, {Kind: people.Email, Value: "dlee@seller.example"}}
		if active {
			ids = append(ids, people.Identifier{Kind: people.ParticipantName, Value: "David Lee"})
		}
		return people.Person{Key: "user:u1", Identifiers: ids}
	}
	for _, c := range []struct {
		active    bool
		attendees []people.Attendee
		want      people.Role
	}{
		{true, nil, people.Unknown},
		{true, []people.Attendee{{Label: "Host", Email: "dlee@seller.example"}}, people.Seller},
		{false, []people.Attendee{{Label: "Host", Email: "dlee@seller.example"}}, people.Unknown},
	} {
		k := people.Known{Account: "acc-1", NameVariants: true, People: []people.Person{user(c.active)}}
		r := people.Resolve(k, people.Ref{Source: people.Call, CallID: "c", Label: "David Lee", Attendees: c.attendees})
		if r.Role != c.want {
			t.Errorf("active %v, attendees %v: %s by rung %d", c.active, c.attendees, r.Role, r.Rung)
		}
	}
}

// A contact whose address is on the seller's own domain does not make that domain the account's: a seller
// on it is not put on the customer side.
func TestAContactOnTheSellersDomainGivesItNoSide(t *testing.T) {
	evidence := namedTranscript(t, []string{"Sam Hale: We will send the order form."},
		[][2]string{{"Sam Hale", "sam@" + sellerDomain}}, func(string) *strictjson.Object { return speakerSettings(nil) })
	recordOf(evidence, "crm/contacts").Set("email", "old.rep@"+sellerDomain)
	units, _ := spoken(t, evidence)
	if u := units["Sam Hale"]; side(u.Role) == "customer" {
		t.Errorf("Sam Hale is %s by %q", u.Role, u.Basis)
	}
	// Two contacts on it make it the account's, unless it is the company's own domain.
	evidence = namedTranscript(t, []string{"Sam Hale: We will send the order form."},
		[][2]string{{"Sam Hale", "sam@" + sellerDomain}},
		func(string) *strictjson.Object { return speakerSettings([]string{sellerDomain}) })
	recordOf(evidence, "crm/contacts").Set("email", "old.rep@"+sellerDomain)
	addContact(t, evidence, "review-2", "Pat Rowe", "pat@"+sellerDomain)
	units, _ = spoken(t, evidence)
	if u := units["Sam Hale"]; u.Role != "seller" {
		t.Errorf("Sam Hale on the company's domain is %s by %q", u.Role, u.Basis)
	}
}

// One contact on a domain is not enough to show the customer side; two distinct contacts are.
func TestADomainShowsTheCustomerSideOnlyWhenTwoContactsHoldIt(t *testing.T) {
	for _, c := range []struct {
		name  string
		extra bool
		want  string
	}{
		{"one contact", false, "other_party"},
		{"two contacts", true, "customer_side_not_in_crm"},
	} {
		evidence := namedTranscript(t, []string{"Pat Quill: The budget is approved."},
			[][2]string{{"Pat Quill", "pat@" + accountDomain}}, func(string) *strictjson.Object { return speakerSettings(nil) })
		if c.extra {
			addContact(t, evidence, "review-2", "Kit Vale", "kit@"+accountDomain)
		}
		units, _ := spoken(t, evidence)
		if u := units["Pat Quill"]; u.Role != c.want {
			t.Errorf("%s: %s by %q", c.name, u.Role, u.Basis)
		}
	}
}

// A free-mail domain not on the list shows a side only as any other domain does: never from one contact.
func TestAnUnlistedFreeMailDomainOfOneContactShowsNoSide(t *testing.T) {
	evidence := namedTranscript(t, []string{"Lee Park: We will send the order form."},
		[][2]string{{"Lee Park", "lee.park@yahoo.co.uk"}},
		func(string) *strictjson.Object { return speakerSettings([]string{sellerDomain}) })
	recordOf(evidence, "crm/contacts").Set("email", "dana@yahoo.co.uk")
	units, _ := spoken(t, evidence)
	if u := units["Lee Park"]; side(u.Role) == "customer" {
		t.Errorf("Lee Park is %s by %q", u.Role, u.Basis)
	}
}

// A member of the seller's own Slack workspace is never put on the customer side by a domain.
func TestAHomeMemberIsNeverCustomerSideByDomain(t *testing.T) {
	evidence := withMember(t, func(a string) *strictjson.Object {
		return slackMember(a, "UF26F5BFF47", "T0HOME001", true, "kim@yahoo.co.uk", "read")
	}, []any{}, speakerSettings([]string{sellerDomain}))
	recordOf(evidence, "crm/contacts").Set("email", "dana@yahoo.co.uk")
	addContact(t, evidence, "review-2", "Kit Vale", "kit@yahoo.co.uk")
	units, _, _, _, err := Build(evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range units {
		if u.Kind == "sentence" && !u.Quoted && side(u.Role) == "customer" {
			t.Errorf("a home member is %s by %q", u.Role, u.Basis)
		}
	}
}

// A profile email that is no address is refused with the evidence, and a domain reaches the card only
// neutralized, as a sentence's basis and as the identifier that decided, which for a person outside the CRM
// is the domain alone.
func TestADomainReachesTheCardNeutralized(t *testing.T) {
	for _, mail := range []string{"kim@[click](https://evil.example)", "kim@<!channel>", "kim@<https://evil.example|verify>"} {
		evidence := withMember(t, func(a string) *strictjson.Object {
			return slackMember(a, "UF26F5BFF47", "T0OTHER01", false, mail, "read")
		}, []any{}, speakerSettings(nil))
		if _, _, _, _, err := Build(evidence); err == nil {
			t.Errorf("%q: a member with that email was built", mail)
		}
	}
	evidence := withMember(t, func(a string) *strictjson.Object {
		return slackMember(a, "UF26F5BFF47", "T0HOME001", true, "kim@www.evil.example", "read")
	}, []any{}, speakerSettings([]string{"www.evil.example"}))
	units, text, _, _, err := Build(evidence)
	if err != nil {
		t.Fatal(err)
	}
	safe, _, err := SafeText("www.evil.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range units {
		if u.Kind != "sentence" || u.Quoted {
			continue
		}
		if u.Basis != "seller side by domain "+oneLine(safe) || u.Matched == nil || u.Matched.Value != oneLine(safe) {
			t.Errorf("basis %q, matched %+v", u.Basis, u.Matched)
		}
	}
	if safe != "www.evil.example" && strings.Contains(text, "www.evil.example") {
		t.Errorf("the card prints the domain as written:\n%s", text)
	}
}

// The account's Website names its domain, unless it is a page on a shared host.
func TestTheWebsiteNamesTheAccountsDomain(t *testing.T) {
	for _, c := range []struct {
		website any
		want    string
	}{
		{"https://www.gemini-exports.example/about", "customer_side_not_in_crm"},
		{"gemini-exports.example", "customer_side_not_in_crm"},
		{"https://sites.google.com/view/gemini-exports", "other_party"},
		{"https://gemini-exports.github.io", "other_party"},
		{nil, "other_party"},
	} {
		evidence := salesforceCard(t, nil)
		recordOf(evidence, "crm/accounts").Set("website", c.website)
		k := knownOf(t, evidence)
		r := people.Resolve(k, people.Ref{Source: people.Call, CallID: "c", Label: "Pat Quill",
			Attendees: []people.Attendee{{Label: "Pat Quill", Email: "pat@gemini-exports.example"}}})
		if string(r.Role) != c.want && !(c.want == "other_party" && side(string(r.Role)) != "customer") {
			t.Errorf("website %v: %s", c.website, r.Role)
		}
	}
}

// knownOf is what a card knows of the people of a Salesforce card's evidence, for its calls.
func knownOf(t *testing.T, evidence *strictjson.Object) people.Known {
	t.Helper()
	v := validate(evidence)
	c := &card{evidence: evidence, groups: v.groups, titles: map[int]string{}, unshownTitles: map[int]any{},
		receipts: v.receipts}
	return newSpeakers(c, byID(c.groups["contacts"]), map[string]int{}, "calls_backend").known
}

// An org with more users than a card reads gives a card without them, which says so.
func TestUsersNotReadAreStated(t *testing.T) {
	evidence := salesforceCard(t, nil, "Dana")
	memberObject(evidence, "people").Set("users", "not_read")
	receipt := receiptOf(evidence, "list_users")
	receipt.Set("status", "error")
	_, text, gaps, _, err := Build(evidence)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range gaps {
		found = found || g.Code == "crm_users_not_read" && GapCodes[g.Code] == "not_read"
	}
	if !found || !strings.Contains(text, "CRM: the org's users were not read") {
		t.Errorf("no line says the users were not read:\n%s", text)
	}
	// A users read that returned rows is no read that was not done.
	memberObject(evidence, "people").Set("users", "read")
	if _, _, _, _, err := Build(evidence); err == nil {
		t.Error("a users read that failed was built as read")
	}
}
