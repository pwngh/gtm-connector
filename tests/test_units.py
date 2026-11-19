"""Units, neutralized text, speaker roles, and the evidence checks behind a card."""

from copy import deepcopy
import unittest

from gtm_connector.units import BriefRejected, build, safe_text, sentences, speaker_role, trace


def evidence(*, notes_status="available", slack_status="not_requested", notes=None, messages=()):
    """One invented account as checked evidence, with one finished receipt per read."""
    records = [
        {"id": "acc-001", "record_ref": "crm/accounts/acc-001", "name": "Fixture account", "industry": "Legal",
         "arr_cents": 2050000, "currency": "USD", "renewal_date": "2027-02-01", "risk_status": "healthy",
         "risk_reason": ""},
        {"id": "con-001", "record_ref": "crm/contacts/con-001", "account_id": "acc-001", "name": "Dana Ito",
         "title": "Counsel", "email": "dana@fixture.example"},
        {"id": "opp-001", "record_ref": "crm/opportunities/opp-001", "account_id": "acc-001", "name": "Renewal",
         "stage": "proposal", "amount_cents": 2050000, "currency": "USD", "close_date": "2027-01-25"},
    ]
    if notes_status == "available":
        for index, (speaker, body) in enumerate(notes if notes is not None else [("Dana Ito", "We will see.")]):
            records.append({"id": f"note-{index}", "record_ref": f"calls/call_notes/note-{index}",
                            "account_id": "acc-001", "called_at": "2027-01-12T15:00:00Z", "speaker": speaker,
                            "body": body})
    for index, (author, body) in enumerate(messages):
        records.append({"id": f"msg-{index}", "record_ref": f"slack/messages/msg-{index}", "account_id": "acc-001",
                        "channel": "acct-fixture", "author": author, "sent_at": "2027-01-13T15:00:00Z",
                        "body": body, "files": 0})
    tools = [("get_account", ("crm/accounts/", "crm/contacts/")), ("list_opportunities", ("crm/opportunities/",)),
             ("read_call_notes", ("calls/",))]
    if slack_status != "not_requested":
        tools.append(("read_slack_messages", ("slack/",)))
    receipts = []
    for tool, prefixes in tools:
        rows = [record["record_ref"] for record in records if record["record_ref"].startswith(prefixes)]
        denied = ((tool == "read_call_notes" and notes_status == "denied")
                  or (tool == "read_slack_messages" and slack_status == "denied"))
        receipts.append({"audit_id": "receipt-" + tool, "brief_id": "brief-fixture", "tool": tool,
                         "arguments": {"account_id": "acc-001"}, "rows": rows, "row_count": len(rows),
                         "status": "error" if denied else "ok", "http_status": 403 if denied else 200})
    return {"brief_id": "brief-fixture", "account_id": "acc-001", "as_of": "2027-01-15", "records": records,
            "receipts": receipts, "notes_status": notes_status, "slack_status": slack_status,
            "crm_backend": "sqlite", "crm_binding": None, "calls_backend": "fixture", "calls_binding": None,
            "slack_backend": "fixture", "slack_binding": None, "thread_snapshot": None, "sellers": ["Morgan Reed"]}


class NeutralizedText(unittest.TestCase):
    def test_slack_markup_links_and_controls_become_inert_words(self):
        text, counts = safe_text("<!here> <!subteam^S1|@team> <@U1AB> <#C9|general> <https://a.example/x|label> "
                                 "<mailto:a@b.example> [doc](https://b.example/y) http://c.example/z?q=1 \x07\u202e")
        self.assertEqual(text, "[mention here] [mention group] [user U1AB] [channel C9] label [link a.example] "
                               "[link mail] doc [link b.example] [link c.example] [U+0007][U+202E]")
        self.assertEqual(counts, {"mentions": 4, "links": 4, "controls": 2})

    def test_ordinary_text_in_any_script_is_unchanged(self):
        for text in ("Zoë O'Néill-Çelik said 3 < 4 and a > b.", "株式会社サンプル", "شركة العينة",
                     "a <v Dana>voice tag"):
            self.assertEqual(safe_text(text), (text, {"mentions": 0, "links": 0, "controls": 0}))

    def test_no_output_keeps_a_token_a_client_would_render(self):
        hostile = ("<!channel>", "<!everyone|x>", "<@U1>", "<@|>", "<!>", "<https://x.example|<!channel>>",
                   "[<!here>](https://x.example)", "<<!channel>>", "http://x.example/<@U1>")
        for raw in hostile:
            text, _ = safe_text(raw)
            self.assertNotRegex(text, r"<[!@]|https?://", raw)

    def test_sentences_are_whole_and_lose_nothing(self):
        text = 'Dr. No will not sign. "We agreed." Then what? e.g. nothing here!\nA new line (v. 3.5) ends'
        found = sentences(text)
        self.assertEqual(found, ["Dr. No will not sign.", '"We agreed."', "Then what? e.g. nothing here!",
                                 "A new line (v. 3.5) ends"])
        self.assertEqual(" ".join(found), " ".join(text.split()))


class SpeakerRoles(unittest.TestCase):
    CONTACTS = [{"name": "Dana Ito", "n": 2}, {"name": "Pat Kim", "n": 3}, {"name": "Pat Kim", "n": 4},
                {"name": "Sam Lee", "n": 5}]

    def test_a_role_needs_one_exact_identity(self):
        role = lambda label, **options: speaker_role(label, ["Morgan Reed", "Sam Lee"], self.CONTACTS, **options)[0]  # noqa: E731
        self.assertEqual(role("Morgan Reed"), "seller")
        self.assertEqual(role("Dana  Ito"), "customer")
        for label in (None, "", "dana ito", "Dana", "Pat Kim", "Sam Lee", "Dana Ito (customer)", "customer"):
            self.assertEqual(role(label), "unknown", label)
        self.assertEqual(role("Dana Ito", slack=True), "unknown")
        self.assertEqual(role("Morgan Reed", slack=True), "seller")


class Evidence(unittest.TestCase):
    def test_a_card_cites_every_value_and_sentence_by_number(self):
        units, text = build(evidence(slack_status="available", messages=[("Morgan Reed", "They agreed.")]))
        self.assertEqual([unit["kind"] for unit in units], ["record"] * 3 + ["sentence"] * 2)
        for line in text.split("RECORD VALUES", 1)[1].split("\n\n")[0].splitlines()[1:]:
            self.assertRegex(line, r" \[\d+\]$")
        self.assertIn('  [4] customer (label matches contact [2]); call 2027-01-12T15:00:00Z; speaker "Dana Ito"\n'
                      '      "We will see."', text)
        self.assertEqual(trace(evidence(), 4)["receipt"]["audit_id"], "receipt-read_call_notes")

    def test_a_denied_source_is_stated_and_never_filled_in(self):
        _, text = build(evidence(notes_status="denied", slack_status="denied"))
        self.assertIn("Calls: not read. The grant lacks calls:read.", text)
        self.assertIn("Slack: not read. The grant lacks slack:read.", text)
        self.assertIn("No sentence was read.", text)

    def test_tampered_evidence_gives_no_card(self):
        def changed(edit, **options):
            source = evidence(**options)
            edit(source)
            return source

        def record(index, key, value):
            return lambda source: source["records"][index].__setitem__(key, value)

        def receipt(index, key, value):
            return lambda source: source["receipts"][index].__setitem__(key, value)

        tampered = [
            changed(record(1, "account_id", "acc-002")),
            changed(record(2, "record_ref", "crm/opportunities/opp-002")),
            changed(record(0, "currency", "usd")),
            changed(record(0, "currency", "US dollars")),
            changed(record(2, "amount_cents", -1)),
            changed(record(2, "amount_cents", 1.5)),
            changed(record(0, "renewal_date", "2027-2-1")),
            changed(record(0, "risk_status", "fine")),
            changed(record(3, "body", 7)),
            changed(lambda source: source["records"].append(deepcopy(source["records"][2]))),
            changed(lambda source: source["records"].append(
                {"id": "msg-9", "record_ref": "slack/messages/msg-9", "account_id": "acc-001", "channel": "c",
                 "author": "a", "sent_at": "2027-01-13T15:00:00Z", "body": "Unrequested.", "files": 0})),
            changed(lambda source: source["records"].pop(0)),
            changed(lambda source: source["receipts"].pop()),
            changed(receipt(0, "status", "pending")),
            changed(receipt(1, "rows", [])),
            changed(receipt(2, "brief_id", "brief-other")),
            changed(receipt(2, "arguments", {"account_id": "acc-002"})),
            changed(receipt(1, "audit_id", "receipt-get_account")),
            changed(lambda source: source.__setitem__("account_id", "acc-002")),
            changed(lambda source: source.__setitem__("as_of", "today")),
            changed(lambda source: source.__setitem__("sellers", ["x" * 201])),
            changed(lambda source: source.__setitem__("thread_snapshot", {})),
            changed(lambda source: source.__setitem__("extra", True)),
            changed(lambda source: source.pop("sellers")),
            changed(receipt(2, "status", "ok"), notes_status="denied"),
        ]
        for index, source in enumerate(tampered):
            with self.assertRaises(BriefRejected, msg=index):
                build(source)
        build(evidence())


if __name__ == "__main__":
    unittest.main()
