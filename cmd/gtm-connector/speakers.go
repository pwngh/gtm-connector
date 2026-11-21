package main

import (
	"strings"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// A workspace's speaker settings, saved by connect beside its seller list and sealed in every card's
// evidence: the company's own email domains (--company-domain), and aliases that map a speaker label to a
// person (--alias). Like the seller list, each list replaces the saved one when it is given, and is kept
// when it is not.

// speakerSettings are a workspace's company domains and aliases.
type speakerSettings struct {
	domains []string
	aliases []evidence.Alias
}

// errSpeakers is saved settings that are not the object connect saves.
var errSpeakers = local("This workspace's speaker settings are not the object connect saves. Run connect " +
	"with --company-domain and --alias again.")

// readSpeakers reads saved settings, as speakerSettings.encode wrote them.
func readSpeakers(saved string) (speakerSettings, error) {
	var s speakerSettings
	value, err := strictjson.Loads([]byte(saved))
	o, ok := value.(*strictjson.Object)
	if err != nil || !ok || o == nil {
		return s, errSpeakers
	}
	for _, key := range o.Keys {
		if key != "company_domains" && key != "aliases" {
			return s, errSpeakers
		}
	}
	domains, _ := get(o, "company_domains").([]any)
	for _, d := range domains {
		text, isText := d.(string)
		if !isText || !evidence.ValidDomain(text) {
			return s, errSpeakers
		}
		s.domains = append(s.domains, text)
	}
	aliases, _ := get(o, "aliases").([]any)
	for _, value := range aliases {
		entry, isObject := value.(*strictjson.Object)
		l, okLabel := get(entry, "label").(string)
		target, okTarget := get(entry, "target").(string)
		call, _ := get(entry, "call").(string)
		if !isObject || !okLabel || !okTarget || !validAlias(evidence.Alias{Label: l, Call: call, Target: target}) {
			return s, errSpeakers
		}
		s.aliases = append(s.aliases, evidence.Alias{Label: l, Call: call, Target: target})
	}
	if len(s.domains) > evidence.MaxCompanyDomains || len(s.aliases) > evidence.MaxAliases {
		return s, errSpeakers
	}
	return s, nil
}

// encode is the settings as the workspace saves them. An empty list is left out.
func (s speakerSettings) encode() string {
	o := strictjson.NewObject()
	if len(s.domains) > 0 {
		o.Set("company_domains", s.domainValues())
	}
	if len(s.aliases) > 0 {
		o.Set("aliases", s.aliasValues())
	}
	encoded, _ := strictjson.Dumps(o, strictjson.Prepared)
	return encoded
}

func (s speakerSettings) domainValues() []any {
	out := make([]any, len(s.domains))
	for i, d := range s.domains {
		out[i] = d
	}
	return out
}

func (s speakerSettings) aliasValues() []any {
	out := make([]any, len(s.aliases))
	for i, a := range s.aliases {
		var call any
		if a.Call != "" {
			call = a.Call
		}
		out[i] = strictjson.NewObject("label", a.Label, "call", call, "target", a.Target)
	}
	return out
}

// validAlias is whether an alias is one a workspace may hold.
func validAlias(a evidence.Alias) bool {
	return evidence.ValidAliasLabel(a.Label) && evidence.ValidAliasTarget(a.Target) &&
		(a.Call == "" || isLowerHex(a.Call, 64))
}

func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// parseAlias reads --alias [CONTENT_SHA256:]LABEL=TARGET. The label and the target are split at the last
// "="; a call's transcript is named by the content_sha256 debug calls import printed for it.
func parseAlias(text string) (evidence.Alias, bool) {
	at := strings.LastIndex(text, "=")
	if at < 0 {
		return evidence.Alias{}, false
	}
	a := evidence.Alias{Label: text[:at], Target: text[at+1:]}
	if call, rest, scoped := strings.Cut(a.Label, ":"); scoped && isLowerHex(call, 64) {
		a.Call, a.Label = call, rest
	}
	return a, validAlias(a)
}

// aliasText is an alias as connect status prints it, each part a quoted literal.
func aliasText(a evidence.Alias) string {
	text := strictjson.Quote(a.Label) + " = " + strictjson.Quote(a.Target)
	if a.Call != "" {
		text += " in the call " + a.Call
	}
	return oneLine(text)
}
