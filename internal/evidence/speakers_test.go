package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// The speaker rules on invented evidence: each rung of the ladder, the sides an email's domain shows, and
// the refusals of what the speakers read. The evidence is an oracle card's, with its records changed.

const (
	contactEmail  = "c0@gemini-exports.example" // the one contact of the oracle cards used here
	accountDomain = "gemini-exports.example"
	sellerDomain  = "northwind.example"
)

// speakerSettings is a people object: the company's domains and the aliases.
func speakerSettings(domains []string, aliases ...*strictjson.Object) *strictjson.Object {
	d, a := make([]any, len(domains)), make([]any, len(aliases))
	for i, domain := range domains {
		d[i] = domain
	}
	for i, alias := range aliases {
		a[i] = alias
	}
	return strictjson.NewObject("rules_version", SpeakerRules, "company_domains", d, "aliases", a, "users", UsersNone)
}

// namedTranscript is the evidence of a card that read one imported text transcript whose lines each open
// with their speaker's name, with an attendee list when rows are given, a listed seller, and these speaker
// settings. It returns the evidence and the transcript's content digest, which names the call to an alias.
func namedTranscript(t *testing.T, lines []string, attendees [][2]string, settings func(call string) *strictjson.Object) *strictjson.Object {
	t.Helper()
	evidence := sealedEvidence(t, "books_calls", "exports-gemini/0")
	account := member(evidence, "account_id").(string)
	var records []any
	for _, value := range member(evidence, "records").([]any) {
		if ref, _ := member(value, "record_ref").(string); !strings.HasPrefix(ref, "calls/call_notes/") {
			records = append(records, value)
		}
	}
	var units []any
	for i, line := range lines {
		units = append(units, strictjson.NewObject("index", i+1, "text", line, "cue_id", nil, "start_ms", nil,
			"end_ms", nil))
	}
	raw := strings.Join(lines, "\n") + "\n"
	sum := sha256.Sum256([]byte(raw))
	content := hex.EncodeToString(sum[:])
	manifest := strictjson.NewObject("schema_version", 1, "source", "user_supplied_export", "format", "txt",
		"parser_version", ParserVersion, "content_sha256", content, "raw_byte_count", len(raw),
		"association", strictjson.NewObject("crm_binding", strictjson.Clone(member(evidence, "calls_binding")),
			"account_id", account, "method", "account_id",
			"resolution_receipt_id", "00000000-0000-4000-8000-000000000001"),
		"association_generation", 1, "called_at", nil, "imported_at", "2027-01-15T09:00:00Z",
		"selection_mode", "default", "unit_kind", "lines",
		"requested_range", strictjson.NewObject("first", 1, "last", len(lines)),
		"cited_range", strictjson.NewObject("first", 1, "last", len(lines)), "total_units", len(lines),
		"units", units, "speaker_names", true)
	if len(attendees) > 0 {
		rows := make([]any, len(attendees))
		for i, row := range attendees {
			rows[i] = strictjson.NewObject("label", row[0], "email", row[1])
		}
		manifest.Set("attendees", rows)
	}
	cues := make([]*strictjson.Object, len(units))
	for i, unit := range units {
		cues[i] = unit.(*strictjson.Object)
	}
	record := strictjson.NewObject("id", "", "account_id", account, "record_kind", "imported_transcript",
		"record_version", RecordVersion, "called_at", nil, "body", QuotedUnits(cues), "manifest", manifest,
		"record_ref", "calls/call_notes/")
	evidence.Set("records", append(records, record))
	// The account's one contact is Dana Crest.
	recordOf(evidence, "crm/contacts").Set("name", "Dana Crest")
	receiptOf(evidence, "read_call_notes").Set("row_count", 1)
	remake(t, evidence)
	evidence.Set("sellers", []any{"Morgan Reed"})
	evidence.Set("people", settings(content))
	return evidence
}

// spoken is the sentence units of a built card by their speaker label.
func spoken(t *testing.T, evidence *strictjson.Object) (map[string]Unit, string) {
	t.Helper()
	units, text, _, _, err := Build(evidence)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Unit{}
	for _, u := range units {
		if u.Kind == "sentence" && u.Speaker != nil {
			out[*u.Speaker] = u
		}
	}
	return out, text
}

func TestSpeakerLadderOnACall(t *testing.T) {
	lines := []string{
		"Dana: We need the rider approved.",
		"Pat Quill: The budget is approved.",
		"Lee Park: We can send the order form.",
		"Ari Stone: Counsel will review it.",
		"Jo Ames: I will forward it.",
		"Speaker 1: Please hold the date.",
		"Morgan Reed: We will countersign.",
		"Room: We agree.",
		"Dana Crest: Thanks for the call.",
		"Dr Dana Crest: One more thing.",
		"Quinn Moss: That works.",
	}
	attendees := [][2]string{{"Dana", contactEmail}, {"Pat Quill", "pat@" + accountDomain},
		{"Lee Park", "lee@" + sellerDomain}, {"Ari Stone", "ari@counsel.example"}, {"Jo Ames", "jo@gmail.com"},
		{"Morgan Reed", contactEmail}, {"Room", contactEmail}, {"Room", "lee@" + sellerDomain}}
	evidence := namedTranscript(t, lines, attendees, func(call string) *strictjson.Object {
		return speakerSettings([]string{sellerDomain},
			strictjson.NewObject("label", "Speaker 1", "call", call, "target", contactEmail))
	})
	// A second contact on the account's domain makes it the account's.
	addContact(t, evidence, "ladder-2", "Kit Vale", "kit@"+accountDomain)
	units, text := spoken(t, evidence)
	for _, c := range []struct {
		label, role, basis string
		rung               int
		contact            bool
	}{
		{"Dana", "customer", "by attendee list", 2, true},
		{"Pat Quill", "customer_side_not_in_crm", "customer side by domain " + accountDomain + ", not a contact", 2, false},
		{"Lee Park", "seller", "seller side by domain " + sellerDomain, 2, false},
		{"Ari Stone", "other_party", "other party by domain counsel.example", 2, false},
		{"Jo Ames", "unknown", "email on a free or shared domain shows no side", 0, false},
		{"Speaker 1", "customer", "by alias", 3, true},
		// The attendee list is stronger than the label: the listed seller's name is a contact here.
		{"Morgan Reed", "customer", "by attendee list", 2, true},
		{"Room", "unknown", "the attendees behind this label are on different sides", 2, false},
		{"Dana Crest", "customer", "by name", 4, true},
		{"Dr Dana Crest", "customer", "by name variant on the attendee list", 5, true},
		{"Quinn Moss", "unknown", "names no listed seller, contact or user", 0, false},
	} {
		u, found := units[c.label]
		if !found {
			t.Errorf("%s: no sentence", c.label)
			continue
		}
		if u.Role != c.role || u.Basis != c.basis || u.Rung != c.rung || (u.Contact != nil) != c.contact ||
			u.SpeakerRules != SpeakerRules {
			t.Errorf("%s: role %s, basis %q, rung %d, contact %v; want %s, %q, %d, %v", c.label, u.Role, u.Basis,
				u.Rung, u.Contact != nil, c.role, c.basis, c.rung, c.contact)
		}
	}
	if m := units["Dana"].Matched; m == nil || m.Kind != "email" || m.Value != contactEmail || m.RecordRef == nil ||
		!strings.HasPrefix(*m.RecordRef, "crm/contacts/") {
		t.Errorf("Dana was matched by %+v", m)
	}
	if m := units["Speaker 1"].Matched; m == nil || m.Kind != "alias" || m.RecordRef != nil {
		t.Errorf("Speaker 1 was matched by %+v", m)
	}
	for _, want := range []string{"  Customer side\n", "  Seller side\n", "  Other party\n", "  No established role\n",
		`; "Pat Quill", customer side by domain ` + accountDomain + `, not a contact` + "\n",
		`; "Ari Stone", other party by domain counsel.example` + "\n",
		`; "Lee Park", seller side by domain ` + sellerDomain + "\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("the card does not print %q:\n%s", want, text)
		}
	}
}

// TestAStrongerRungIsNeverOverruledOnACard checks that no sentence of a call moves to the other side when
// a weaker rung would say otherwise: the label names a listed seller, the attendee list a contact.
func TestASellerNameIsNoSellerWhenTheAttendeeListNamesAContact(t *testing.T) {
	evidence := namedTranscript(t, []string{"Morgan Reed: We will countersign."},
		[][2]string{{"Morgan Reed", contactEmail}}, func(string) *strictjson.Object { return speakerSettings(nil) })
	units, _ := spoken(t, evidence)
	if u := units["Morgan Reed"]; u.Role != "customer" || u.Rung != 2 {
		t.Errorf("role %s by rung %d", u.Role, u.Rung)
	}
	// Without the attendee list the same label is the listed seller, by name.
	evidence = namedTranscript(t, []string{"Morgan Reed: We will countersign."}, nil,
		func(string) *strictjson.Object { return speakerSettings(nil) })
	units, _ = spoken(t, evidence)
	if u := units["Morgan Reed"]; u.Role != "seller" || u.Rung != 4 || u.Basis != "by name" {
		t.Errorf("role %s by rung %d, %q", u.Role, u.Rung, u.Basis)
	}
}

// slackMember is a member record as a live read returns it.
func slackMember(account, user, team string, home bool, email any, profile string, missing ...any) *strictjson.Object {
	if missing == nil {
		missing = []any{}
	}
	var teamID any
	if team != "" {
		teamID = team
	}
	return strictjson.NewObject("id", "mem-"+user, "account_id", account, "user_id", user, "team_id", teamID,
		"home", home, "email", email, "profile", profile, "missing_scopes", missing,
		"record_ref", "slack/members/mem-"+user)
}

// withMember is a live Slack card's evidence whose one author has this member record, and these sellers and
// speaker settings.
func withMember(t *testing.T, change func(account string) *strictjson.Object, sellers []any,
	settings *strictjson.Object) *strictjson.Object {
	t.Helper()
	evidence := sealedEvidence(t, "books_slack", "exports-gemini/0")
	record := change(member(evidence, "account_id").(string))
	evidence.Set("records", append(member(evidence, "records").([]any), record))
	receipt := receiptOf(evidence, "read_slack_messages")
	rows := append(member(receipt, "rows").([]any), member(record, "record_ref"))
	receipt.Set("rows", rows)
	receipt.Set("row_count", len(rows))
	evidence.Set("sellers", sellers)
	evidence.Set("people", settings)
	return evidence
}

func TestSlackAuthorsByProfile(t *testing.T) {
	const author = "UF26F5BFF47"
	for _, c := range []struct {
		name          string
		member        func(account string) *strictjson.Object
		sellers       []any
		domains       []string
		role, basis   string
		rung          int
		matchedKind   string
		matchedRecord string
	}{
		{"a profile email of a contact", func(a string) *strictjson.Object {
			return slackMember(a, author, "T0OTHER01", false, contactEmail, "read")
		}, nil, nil, "customer", "by Slack profile", 1, "email", "crm/contacts/"},
		{"a home member on the company's domain", func(a string) *strictjson.Object {
			return slackMember(a, author, "T0HOME001", true, "kim@"+sellerDomain, "read")
		}, nil, []string{sellerDomain}, "seller", "seller side by domain " + sellerDomain, 1, "email", "slack/members/"},
		{"a guest on the company's domain shows no side", func(a string) *strictjson.Object {
			return slackMember(a, author, "T0OTHER01", false, "kim@"+sellerDomain, "read")
		}, nil, []string{sellerDomain}, "unknown", "Slack author not identified; a Slack name never shows a customer", 0, "", ""},
		{"a listed seller's Slack ID in the home team", func(a string) *strictjson.Object {
			return slackMember(a, author, "T0HOME001", true, nil, "read", "users:read.email")
		}, []any{author}, nil, "seller", "by Slack profile", 1, "slack_user", ""},
		{"a profile not read falls to the name", func(a string) *strictjson.Object {
			return slackMember(a, author, "", false, nil, "missing_scope", "users:read", "users:read.email")
		}, []any{author}, nil, "seller", "by name", 4, "listed_seller", ""},
		{"a profile not read names no customer", func(a string) *strictjson.Object {
			return slackMember(a, author, "", false, nil, "missing_scope", "users:read", "users:read.email")
		}, nil, nil, "unknown", "Slack author not identified; a Slack name never shows a customer", 0, "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			evidence := withMember(t, c.member, append([]any{}, c.sellers...), speakerSettings(c.domains))
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
				if u.Role != c.role || u.Basis != c.basis || u.Rung != c.rung {
					t.Errorf("role %s, %q, rung %d", u.Role, u.Basis, u.Rung)
				}
				switch {
				case c.matchedKind == "" && u.Matched != nil:
					t.Errorf("matched %+v", u.Matched)
				case c.matchedKind != "" && (u.Matched == nil || u.Matched.Kind != c.matchedKind):
					t.Errorf("matched %+v", u.Matched)
				case c.matchedRecord != "" && (u.Matched.RecordRef == nil ||
					!strings.HasPrefix(*u.Matched.RecordRef, c.matchedRecord)):
					t.Errorf("matched from %v", u.Matched.RecordRef)
				}
			}
			if checked == 0 {
				t.Fatal("no sentence")
			}
		})
	}
}

func TestSpeakerSettingsAndRecordsAreChecked(t *testing.T) {
	call := func(change func(*strictjson.Object)) error {
		evidence := namedTranscript(t, []string{"Dana: We agree."}, nil,
			func(string) *strictjson.Object { return speakerSettings(nil) })
		change(evidence)
		_, _, _, _, err := Build(evidence)
		return err
	}
	const settings = "Evidence has unsupported speaker settings."
	for name, c := range map[string]struct {
		change func(*strictjson.Object)
		want   string
	}{
		"a domain in upper case": {func(e *strictjson.Object) {
			e.Set("people", speakerSettings([]string{"Northwind.example"}))
		}, settings},
		"another rules version": {func(e *strictjson.Object) {
			memberObject(e, "people").Set("rules_version", SpeakerRules+1)
		}, settings},
		"an alias with a call that is no digest": {func(e *strictjson.Object) {
			e.Set("people", speakerSettings(nil, strictjson.NewObject("label", "Speaker 1", "call", "call-7",
				"target", contactEmail)))
		}, settings},
		"an alias to no address or seller": {func(e *strictjson.Object) {
			e.Set("people", speakerSettings(nil, strictjson.NewObject("label", "Speaker 1", "call", nil,
				"target", "not@an@address")))
		}, settings},
		"another member of the object": {func(e *strictjson.Object) {
			memberObject(e, "people").Set("home_team", "T0HOME001")
		}, settings},
	} {
		if err := call(c.change); err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	// A member record of another shape is refused.
	evidence := withMember(t, func(a string) *strictjson.Object {
		return slackMember(a, "UF26F5BFF47", "", false, nil, "unread")
	}, nil, speakerSettings(nil))
	if _, _, _, _, err := Build(evidence); err == nil || err.Error() != "A Slack member record has an unsupported shape." {
		t.Errorf("a member with an unknown profile state: %v", err)
	}
}

// TestAProfileNotReadIsStated checks that the thread's line under NOT READ names the scopes the sign-in
// lacks, and the authors past the read's limit.
func TestAProfileNotReadIsStated(t *testing.T) {
	for _, c := range []struct {
		member func(account string) *strictjson.Object
		want   string
	}{
		{func(a string) *strictjson.Object {
			return slackMember(a, "UF26F5BFF47", "", false, nil, "missing_scope", "users:read", "users:read.email")
		}, " Authors' profiles were not read: the Slack sign-in lacks users:read and users:read.email."},
		{func(a string) *strictjson.Object {
			return slackMember(a, "UF26F5BFF47", "T0OTHER01", false, nil, "read", "users:read.email")
		}, " Authors' profile emails were not read: the Slack sign-in lacks users:read.email."},
		{func(a string) *strictjson.Object {
			return slackMember(a, "UF26F5BFF47", "", false, nil, "over_limit")
		}, " 1 author's profile past the read's limit was not read."},
		{func(a string) *strictjson.Object {
			return slackMember(a, "UF26F5BFF47", "T0OTHER01", false, contactEmail, "read")
		}, "every other thread.\n"},
	} {
		_, text, _, _, err := Build(withMember(t, c.member, []any{}, speakerSettings(nil)))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(text, c.want) {
			t.Errorf("the card does not say %q:\n%s", c.want, text)
		}
	}
}
