package evidence

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/pwngh/gtm-connector/internal/people"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// Who said a sentence, and for which side. The card hands every speaker to internal/people, which decides
// by the strongest identifier the evidence holds: a Slack author's profile, the call's attendee list, an
// alias, then the label as a name. This file builds what people reads from the evidence, and turns its
// result into the role, the basis and the identifier a unit records.
//
// People are the account's contacts, the org's Salesforce Users (Standard users only, read by list_users),
// and the listed sellers. Records of one human are merged into one person: a contact and a user with the
// same email address, and a listed seller whose entry equals the name of exactly one user. The account's
// domains are the email domains of its contacts. The company's domains and the aliases are the workspace's
// own settings, sealed in the evidence's people object. A free or shared mail domain never shows a side.

// SpeakerRules is the version of the rules a sentence's role is decided under: people.RulesVersion, which
// also covers how this file reads the evidence. A unit seals it.
const SpeakerRules = people.RulesVersion

// Limits of the workspace's speaker settings.
const (
	MaxCompanyDomains = 50
	MaxAliases        = 200
)

// SharedMailDomains are free and shared mail domains. An address on one names its holder but never shows a
// side.
var SharedMailDomains = []string{"163.com", "126.com", "aol.com", "att.net", "comcast.net", "fastmail.com",
	"free.fr", "gmail.com", "gmx.com", "gmx.de", "gmx.net", "googlemail.com", "hey.com", "hotmail.com",
	"icloud.com", "live.com", "mac.com", "mail.com", "me.com", "msn.com", "naver.com", "orange.fr",
	"outlook.com", "pm.me", "proton.me", "protonmail.com", "qq.com", "t-online.de", "verizon.net", "web.de",
	"yahoo.com", "yandex.com", "ymail.com", "zoho.com"}

// Alias maps a speaker label to a person: an email address that a contact, a user or an attendee holds, or
// a listed seller's entry. Call, when set, is the content_sha256 of the one transcript it applies to.
type Alias struct {
	Label, Call, Target string
}

// Matched is the identifier that decided a speaker: its kind (people.Kind), its value as the card shows
// it, and the record it came from, or nil for an alias, which comes from the workspace's settings.
type Matched struct {
	Kind, Value string
	RecordRef   *string
}

func (m *Matched) value() any {
	if m == nil {
		return nil
	}
	return strictjson.NewObject("kind", m.Kind, "value", m.Value, "record_ref", optional(m.RecordRef))
}

// ValidDomain is whether a text is a mail domain as the settings hold it: lower-case ASCII labels of
// letters, digits and hyphens, with at least one dot.
func ValidDomain(domain string) bool { return ValidEmail("x@" + domain) }

var slackUserID = regexp.MustCompile(`^[UW][A-Z0-9]{2,}$`)

// speakers is what the card knows of the people of one deal, and how it resolves a speaker.
type speakers struct {
	known    people.Known
	contacts map[string]int  // a contact record's reference to its unit number
	names    map[string]bool // the account's contacts' names, on one line
}

// draft is a person being merged from records.
type draft struct {
	key     string
	ids     []people.Identifier
	user    bool
	nameKey string
}

// newSpeakers reads the people of the evidence: its contacts (with their unit numbers), its users, the
// listed sellers of one backend, the members of a live Slack thread, and the people object.
func newSpeakers(c *card, contacts []*strictjson.Object, numbers map[string]int, backend string) *speakers {
	s := &speakers{contacts: map[string]int{}, names: map[string]bool{}}
	s.known.Account = str(field(c.evidence, "account_id"))
	s.known.NameVariants = true
	s.known.SharedDomains = SharedMailDomains
	for _, member := range c.groups["members"] {
		if field(member, "home") == true {
			s.known.HomeTeam = str(field(member, "team_id"))
		}
	}
	var drafts []*draft
	byEmail := map[string]*draft{}
	add := func(d *draft, address any, from string) *draft {
		text, _ := address.(string)
		a, _, ok := people.Address(text)
		if !ok {
			drafts = append(drafts, d)
			return d
		}
		if held := byEmail[a]; held != nil {
			held.ids = append(held.ids, d.ids...)
			held.ids = append(held.ids, people.Identifier{Kind: people.Email, Value: a, From: from})
			held.user = held.user || d.user
			if d.user && held.nameKey == "" {
				held.nameKey = d.nameKey
			}
			return held
		}
		d.ids = append(d.ids, people.Identifier{Kind: people.Email, Value: a, From: from})
		byEmail[a] = d
		drafts = append(drafts, d)
		return d
	}
	domains := map[string]bool{}
	for _, row := range contacts {
		ref, id := str(field(row, "record_ref")), rowID(row)
		s.contacts[ref] = numbers[id]
		d := &draft{key: "contact:" + id, ids: []people.Identifier{
			{Kind: people.SalesforceContact, Value: id, Scope: str(field(row, "account_id")), From: ref}}}
		if name, ok := field(row, "name").(string); ok && name != "" {
			d.ids = append(d.ids, people.Identifier{Kind: people.Name, Value: name, From: ref})
			s.names[label(name)] = true
		}
		email, _ := field(row, "email").(string)
		if _, at, ok := people.Address(email); ok {
			domains[at] = true
		}
		add(d, field(row, "email"), ref)
	}
	for _, row := range byID(c.groups["users"]) {
		ref, id := str(field(row, "record_ref")), rowID(row)
		name := str(field(row, "name"))
		d := &draft{key: "user:" + id, user: true, nameKey: label(name), ids: []people.Identifier{
			{Kind: people.SalesforceUser, Value: id, From: ref}, {Kind: people.Name, Value: name, From: ref}}}
		add(d, field(row, "email"), ref)
	}
	// A listed seller whose entry is the name of exactly one user is that user.
	seen := map[string]bool{}
	for _, entry := range c.sellers(backend) {
		l := label(entry)
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		id := people.Identifier{Kind: people.ListedSeller, Value: entry, From: "sellers"}
		var same []*draft
		for _, d := range drafts {
			if d.user && d.nameKey == l {
				same = append(same, d)
			}
		}
		d := &draft{key: "seller:" + l}
		if len(same) == 1 {
			d = same[0]
		} else {
			drafts = append(drafts, d)
		}
		d.ids = append(d.ids, id)
		if s.known.HomeTeam != "" && slackUserID.MatchString(entry) {
			d.ids = append(d.ids, people.Identifier{Kind: people.SlackUser, Value: entry, Scope: s.known.HomeTeam,
				From: "sellers"})
		}
	}
	for _, d := range drafts {
		s.known.People = append(s.known.People, people.Person{Key: d.key, Identifiers: d.ids})
	}
	for domain := range domains {
		s.known.AccountDomains = append(s.known.AccountDomains, domain)
	}
	sort.Strings(s.known.AccountDomains)
	settings, _ := c.evidence.Get("people")
	if settings, ok := settings.(*strictjson.Object); ok {
		for _, domain := range list(field(settings, "company_domains")) {
			s.known.CompanyDomains = append(s.known.CompanyDomains, str(domain))
		}
		for _, value := range list(field(settings, "aliases")) {
			alias := object(value)
			l, target := str(field(alias, "label")), str(field(alias, "target"))
			call, _ := field(alias, "call").(string)
			for _, p := range s.known.People {
				if names(p, target) {
					s.known.Aliases = append(s.known.Aliases, people.AliasEntry{Label: l, Call: call, Person: p.Key,
						From: "alias"})
				}
			}
		}
	}
	return s
}

// names is whether an alias's target names a person: an address the person holds, or the entry of a
// listed seller.
func names(p people.Person, target string) bool {
	address, _, isAddress := people.Address(target)
	for _, id := range p.Identifiers {
		switch {
		case isAddress && id.Kind == people.Email && id.Value == address:
			return true
		case !isAddress && id.Kind == people.ListedSeller && label(id.Value) == label(target):
			return true
		}
	}
	return false
}

// callRef is a speaker of a call record: in an imported transcript, the call is its content digest and
// the attendee list is the manifest's.
func callRef(note *strictjson.Object, l string) people.Ref {
	r := people.Ref{Source: people.Call, CallID: rowID(note), Label: l, From: str(field(note, "record_ref"))}
	if manifest, ok := note.Get("manifest"); ok {
		m := object(manifest)
		r.CallID = str(field(m, "content_sha256"))
		if rows, listed := m.Get("attendees"); listed {
			for _, value := range list(rows) {
				row := object(value)
				r.Attendees = append(r.Attendees, people.Attendee{Label: str(field(row, "label")),
					Email: str(field(row, "email"))})
			}
		}
	}
	return r
}

// slackRef is the author of a Slack message. A live message's author is a member, whose profile the read
// may have read.
func (c *card) slackRef(message *strictjson.Object, l string) people.Ref {
	r := people.Ref{Source: people.Slack, Label: l, From: str(field(message, "record_ref"))}
	if l == "" {
		return r
	}
	for _, member := range c.groups["members"] {
		if field(member, "user_id") != l {
			continue
		}
		author := &people.SlackAuthor{User: l}
		if field(member, "profile") == "read" {
			author.Team, _ = field(member, "team_id").(string)
			author.Email, _ = field(member, "email").(string)
		}
		r.Slack, r.From = author, str(field(member, "record_ref"))
	}
	return r
}

// The basis of each rung, as a sentence prints it.
var basisWords = map[people.Basis]string{
	people.BySlackProfile: "by Slack profile",
	people.ByEmail:        "matched by email",
	people.ByAttendeeList: "by attendee list",
	people.ByAlias:        "by alias",
	people.ByName:         "by name",
	people.ByNameVariant:  "by name variant on the attendee list",
	people.ByRoom:         "by attendee list, everyone behind the label",
}

// Why a role is not established, as a sentence prints it.
var reasonWords = map[people.Reason]string{
	people.NoLabel:                 "no speaker label",
	people.NoMatch:                 "names no listed seller, contact or user",
	people.AnonymousLabel:          "anonymous label with no alias for this call",
	people.SlackAuthorUnidentified: "Slack author not identified; a Slack name never shows a customer",
	people.SharedDomain:            "email on a free or shared domain shows no side",
	people.RungsDisagree:           "a stronger identifier puts the speaker on another side",
	people.EmailDomainDisagrees:    "the email's domain contradicts the person's side",
	people.PersonOnBothSides:       "the person is both a contact and a seller",
	people.AccountNotRelated:       "the person is a contact of another account",
	people.PersonWithoutSide:       "the person is no contact, user or listed seller",
	people.RoomMixed:               "the attendees behind this label are on different sides",
}

// resolve is the role of one speaker.
func (s *speakers) resolve(c *card, r people.Ref) role {
	result := people.Resolve(s.known, r)
	words := basisWords[result.Basis]
	who := role{role: string(result.Role), rung: int(result.Rung)}
	if id := result.Matched; id.Kind != "" {
		m := &Matched{Kind: string(id.Kind), Value: c.line(id.Value)}
		if strings.Contains(id.From, "/") {
			from := id.From
			m.RecordRef = &from
		}
		who.matched = m
	}
	switch {
	case result.Role == people.Unknown && (result.Reason == people.SeveralCandidates ||
		result.Reason == people.CandidatesOnBothSides):
		sides := ""
		if result.Reason == people.CandidatesOnBothSides {
			sides = " on both sides"
		}
		who.basis = fmt.Sprintf("%d people%s match %s", len(result.Candidates), sides, words)
	case result.Role == people.Unknown && result.Rung != people.NoRung && words != "" &&
		result.Reason != people.RoomMixed:
		who.basis = reasonWords[result.Reason] + ", " + words
	case result.Role == people.Unknown:
		who.basis = reasonWords[result.Reason]
	case result.Basis == people.ByDomain:
		switch result.Role {
		case people.CustomerNotInCRM:
			who.basis = "customer side by domain " + result.Domain + ", not a contact"
		case people.Seller:
			who.basis = "seller side by domain " + result.Domain
		default:
			who.basis = "other party by domain " + result.Domain
		}
	default:
		who.basis = words
	}
	if result.Person != nil && result.Role == people.Customer {
		who.contact = s.contactOf(*result.Person, result.Matched)
	}
	// Whether the speaker was tied to a contact: by the person, a candidate, or a label equal to a name. The
	// records of a person with a contact are merged into that contact's, whose key it keeps.
	who.contactSeen = s.names[r.Label] && r.Source == people.Call
	keys := result.Candidates
	if result.Person != nil {
		keys = append(keys, result.Person.Key)
	}
	for _, key := range keys {
		who.contactSeen = who.contactSeen || strings.HasPrefix(key, "contact:")
	}
	return who
}

// contactOf is the unit number of the contact a customer was matched to: the one contact record that
// holds the matched identifier, or for an alias the person's one contact record. A person merged from
// several contact records that all hold it, such as two records with one address, cites none, so no
// title is printed that may be another record's.
func (s *speakers) contactOf(p people.Person, matched people.Identifier) *int {
	found := map[int]bool{}
	for _, id := range p.Identifiers {
		n, isContact := s.contacts[id.From]
		if isContact && (matched.Kind == people.Alias || id.Kind == matched.Kind && id.Value == matched.Value) {
			found[n] = true
		}
	}
	if len(found) != 1 {
		return nil
	}
	for n := range found {
		return &n
	}
	return nil
}

// profilesNotRead is what a live Slack read left out of its authors' profiles, as a sentence that follows
// the thread's line under NOT READ: the scopes the sign-in lacks, and the authors past the read's limit.
// It is "" when every profile was read whole, or when the evidence holds no members.
func (c *card) profilesNotRead() string {
	missing, over := map[string]bool{}, 0
	for _, member := range c.groups["members"] {
		if field(member, "profile") == "over_limit" {
			over++
		}
		for _, scope := range list(field(member, "missing_scopes")) {
			missing[str(scope)] = true
		}
	}
	text := ""
	switch {
	case missing["users:read"]:
		text = " Authors' profiles were not read: the Slack sign-in lacks users:read and users:read.email."
	case missing["users:read.email"]:
		text = " Authors' profile emails were not read: the Slack sign-in lacks users:read.email."
	}
	switch {
	case over == 1:
		text += " 1 author's profile past the read's limit was not read."
	case over > 1:
		text += fmt.Sprintf(" %d authors' profiles past the read's limit were not read.", over)
	}
	return text
}

// side is the section of the card a role is dealt in: the customer side, the seller side, another party,
// or no established role.
func side(role string) string {
	switch role {
	case "customer", string(people.CustomerNotInCRM):
		return "customer"
	case "seller", string(people.OtherParty):
		return role
	}
	return "unknown"
}

// ---- validation of what the speakers read ----

// flagInt is 1 for true and 0 for false.
func flagInt(set bool) int {
	if set {
		return 1
	}
	return 0
}

var (
	peopleKeys = []string{"rules_version", "company_domains", "aliases"}
	aliasKeys  = []string{"label", "call", "target"}
	userKeys   = []string{"id", "source_id", "backend", "name", "email", "is_active", "record_ref"}
	memberKeys = []string{"id", "account_id", "user_id", "team_id", "home", "email", "profile",
		"missing_scopes", "record_ref"}
	profileScopes = []string{"users:read", "users:read.email"}
)

// ValidAliasLabel is whether a text is a label an alias may map: source text, not empty on one line, at
// most MaxLabel characters.
func ValidAliasLabel(text string) bool {
	safe, ok := SourceText(text, 4*MaxLabel, true)
	return ok && oneLine(safe) != "" && length(safe) <= MaxLabel
}

// ValidAliasTarget is whether a text is what an alias may name: an email address, or a seller entry of 1
// to 200 characters.
func ValidAliasTarget(text string) bool {
	if strings.Contains(text, "@") {
		return ValidEmail(text)
	}
	return ValidAliasLabel(text)
}

// validPeople checks the evidence's people object: this version of the rules, the company's domains and the
// aliases.
func validPeople(value any) bool {
	o, ok := value.(*strictjson.Object)
	if !ok || !hasKeys(o, peopleKeys...) {
		return false
	}
	_, current := intIn(field(o, "rules_version"), SpeakerRules, SpeakerRules)
	domains, okDomains := field(o, "company_domains").([]any)
	aliases, okAliases := field(o, "aliases").([]any)
	if !current || !okDomains || !okAliases || len(domains) > MaxCompanyDomains || len(aliases) > MaxAliases {
		return false
	}
	for _, d := range domains {
		text, isText := d.(string)
		if !isText || !ValidDomain(text) {
			return false
		}
	}
	for _, value := range aliases {
		alias, isObject := value.(*strictjson.Object)
		if !isObject || !hasKeys(alias, aliasKeys...) {
			return false
		}
		l, okLabel := field(alias, "label").(string)
		target, okTarget := field(alias, "target").(string)
		call := field(alias, "call")
		digest, okCall := call.(string)
		if !okLabel || !ValidAliasLabel(l) || !okTarget || !ValidAliasTarget(target) ||
			call != nil && (!okCall || !lowerHex64(digest)) {
			return false
		}
	}
	return true
}

// validUser checks a Salesforce User record as list_users returns it.
func validUser(record *strictjson.Object) bool {
	if !hasKeys(record, userKeys...) {
		return false
	}
	_, named := field(record, "name").(string)
	email := field(record, "email")
	_, isText := email.(string)
	_, active := field(record, "is_active").(bool)
	return named && (email == nil || isText) && active
}

// validMember checks a Slack member record as a live read returns it.
func validMember(record *strictjson.Object) bool {
	if !hasKeys(record, memberKeys...) {
		return false
	}
	user, okUser := field(record, "user_id").(string)
	team, email := field(record, "team_id"), field(record, "email")
	_, teamText := team.(string)
	_, emailText := email.(string)
	_, home := field(record, "home").(bool)
	missing, okMissing := field(record, "missing_scopes").([]any)
	if !okUser || !slackUserID.MatchString(user) || field(record, "id") != "mem-"+user ||
		team != nil && !teamText || email != nil && !emailText || !home || !okMissing ||
		!oneOf(field(record, "profile"), "read", "missing_scope", "over_limit") {
		return false
	}
	for _, scope := range missing {
		if !oneOf(scope, profileScopes...) {
			return false
		}
	}
	return true
}
