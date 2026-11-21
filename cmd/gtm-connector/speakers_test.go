package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pwngh/gtm-connector/internal/evidence"
	"github.com/pwngh/gtm-connector/internal/strictjson"
)

// A call of the demo account acc-007, whose one contact is Dana Crest (dana@juniper-crest.example): an
// unnamed speaker, two people the attendee list names by address only, and the listed seller. One contact
// alone does not make juniper-crest.example the account's domain, so its holder is another party.
const anonymousMeeting = "WEBVTT\n\n" +
	"1\n00:00:01.000 --> 00:00:04.000\n<v Speaker 1>We will sign the renewal this quarter.\n\n" +
	"2\n00:00:05.000 --> 00:00:08.000\n<v Lee Park>I will send the order form today.\n\n" +
	"3\n00:00:09.000 --> 00:00:12.000\n<v Pat Quill>Our budget for the renewal is approved.\n\n" +
	"4\n00:00:13.000 --> 00:00:16.000\n<v Morgan Reed>Great, the countersigned copy follows.\n"

// connect saves the company's domains and the aliases beside the seller list, and every card seals them: a
// card then names each speaker by the strongest identifier it has, and says which.
func TestSpeakerSettingsDecideACardsRoles(t *testing.T) {
	d := setUpDemo(t)
	folder := exports(t)
	file := filepath.Join(folder, "meeting.vtt")
	sidecar := filepath.Join(folder, "attendees.json")
	if err := os.WriteFile(file, []byte(anonymousMeeting), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecar, []byte(`{"schema_version": 1, "attendees": [`+
		`{"label": "Lee Park", "email": "lee@northwind.example"}, `+
		`{"label": "Pat Quill", "email": "pat@juniper-crest.example"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	where := []string{"--state-dir", "speakers"}
	with := func(argv ...string) []string { return append(argv, where...) }
	d.succeeded(t, with("connect", "demo")...)
	d.succeeded(t, with("connect", "calls", "--seller", "Morgan Reed", "--company-domain", "northwind.example")...)
	stdout, _ := d.succeeded(t, with("--json", "debug", "calls", "import", file, "--account", "acc-007",
		"--attendees", sidecar)...)
	call := get(object(t, stdout), "content_sha256").(string)

	// The alias names the unnamed speaker of that one call. The domains given before are kept.
	d.succeeded(t, with("connect", "calls", "--alias", call+":Speaker 1=dana@juniper-crest.example")...)
	stdout, _ = d.succeeded(t, with("connect", "status")...)
	if !strings.HasSuffix(stdout, "\nSellers: \"Morgan Reed\"\nCompany domains: northwind.example\nAliases:\n"+
		"  \"Speaker 1\" = \"dana@juniper-crest.example\" in the call "+call+"\n") {
		t.Errorf("connect status:\n%s", stdout)
	}
	stdout, _ = d.succeeded(t, with("--json", "connect", "status")...)
	status := object(t, stdout)
	if !evidence.Equal(get(status, "company_domains"), []any{"northwind.example"}) ||
		!evidence.Equal(get(status, "aliases"), []any{strictjson.NewObject("label", "Speaker 1", "call", call,
			"target", "dana@juniper-crest.example")}) {
		t.Errorf("connect status --json: %s", stdout)
	}

	stdout, _ = d.succeeded(t, with("brief", "acc-007")...)
	for _, line := range []string{
		"  Customer side\n",
		`; "Speaker 1", role Missing [2], by alias` + "\n        \"We will sign the renewal this quarter.\"\n",
		"  Other party\n",
		`; "Pat Quill", other party by domain juniper-crest.example` + "\n",
		"  Seller side\n",
		`; "Lee Park", seller side by domain northwind.example` + "\n",
		`; "Morgan Reed", by name` + "\n",
	} {
		if !strings.Contains(stdout, line) {
			t.Errorf("brief does not print %q:\n%s", line, stdout)
		}
	}
	if strings.Contains(stdout, "No established role") {
		t.Errorf("brief left a speaker with no role:\n%s", stdout)
	}

	// inspect names the alias that decided, and for an address the record and the receipt of its read.
	stdout, _ = d.succeeded(t, with("--json", "brief", "acc-007")...)
	brief := cellText(get(object(t, stdout), "brief_id"))
	stdout, _ = d.succeeded(t, with("--json", "inspect", brief)...)
	numbers := map[string]string{}
	for _, value := range get(object(t, stdout), "units").([]any) {
		unit := value.(*strictjson.Object)
		if speaker, ok := get(unit, "speaker").(string); ok {
			numbers[speaker] = cellText(get(unit, "n"))
		}
	}
	stdout, _ = d.succeeded(t, with("--json", "inspect", brief, numbers["Speaker 1"], numbers["Pat Quill"])...)
	var traced []any
	if value, err := strictjson.Loads([]byte(stdout)); err == nil {
		traced, _ = value.([]any)
	}
	if len(traced) != 2 {
		t.Fatalf("inspect: %s", stdout)
	}
	alias, domain := traced[0].(*strictjson.Object), traced[1].(*strictjson.Object)
	if !evidence.Equal(get(alias, "rung"), 3) || !evidence.Equal(get(alias, "speaker_rules"), evidence.SpeakerRules) ||
		!evidence.Equal(get(alias, "matched"), strictjson.NewObject("kind", "alias", "value", "Speaker 1",
			"record_ref", nil)) {
		t.Errorf("the alias's sentence: %s", stdout)
	}
	matched, _ := get(domain, "matched").(*strictjson.Object)
	receipt, _ := get(matched, "receipt").(*strictjson.Object)
	if get(domain, "role") != "other_party" || get(matched, "value") != "juniper-crest.example" ||
		!strings.HasPrefix(cellText(get(matched, "record_ref")), "calls/call_notes/") ||
		get(receipt, "tool") != "read_call_notes" || get(receipt, "status") != "ok" {
		t.Errorf("the domain's sentence: %s", stdout)
	}

	// Each list given replaces the saved one; a bad entry is refused and saves nothing.
	for _, c := range []struct {
		argv    []string
		message string
	}{
		{[]string{"--company-domain", "Northwind.example"},
			"Error: A --company-domain is a mail domain in lower case, such as example.com.\n"},
		{[]string{"--alias", "Speaker 1"}, "Error: An --alias is LABEL=EMAIL or LABEL=SELLER, with CONTENT_SHA256: " +
			"before the label for one call's transcript. The label and a seller have 1 to 200 characters.\n"},
	} {
		if message := d.failed(t, 2, with(append([]string{"connect", "calls"}, c.argv...)...)...); !strings.HasPrefix(
			message, c.message) {
			t.Errorf("%v: %s", c.argv, message)
		}
	}
	d.succeeded(t, with("connect", "calls", "--company-domain", "seller.example")...)
	stdout, _ = d.succeeded(t, with("--json", "connect", "status")...)
	if status = object(t, stdout); !evidence.Equal(get(status, "company_domains"), []any{"seller.example"}) ||
		get(status, "aliases") == nil {
		t.Errorf("connect status after one list was given: %s", stdout)
	}
	if message := d.failed(t, 2, with("connect", "demo", "--alias", "A=a@b.example")...); !strings.HasPrefix(message,
		"Error: connect demo does not take --alias.") {
		t.Errorf("connect demo --alias: %s", message)
	}
}

// Settings that the workspace's column cannot hold are refused by name, and nothing is saved.
func TestSpeakerSettingsOverTheirSizeAreRefused(t *testing.T) {
	d := setUpDemo(t)
	where := []string{"--state-dir", "speakers-size"}
	d.succeeded(t, append([]string{"connect", "demo"}, where...)...)
	argv := []string{"connect", "calls"}
	for i := range 200 {
		argv = append(argv, "--alias", fmt.Sprintf("%s %03d=%s", strings.Repeat("話", 195), i,
			"someone@example.com"))
	}
	if message := d.failed(t, 2, append(argv, where...)...); !strings.HasPrefix(message,
		"Error: The speaker settings would take ") {
		t.Errorf("settings over their size: %s", message)
	}
	stdout, _ := d.succeeded(t, append([]string{"--json", "connect", "status"}, where...)...)
	if get(object(t, stdout), "aliases") != nil {
		t.Errorf("settings over their size were saved: %s", stdout)
	}
}
