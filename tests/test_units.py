"""Units, neutralized text, speaker roles, and the evidence checks behind a card."""

from copy import deepcopy
import random
import time
import unittest

from gtm_connector.units import BriefRejected, build, safe_text, sentences, speaker_role, trace, voices


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
        receipts.append({"audit_id": "receipt-" + tool, "started_at": "2027-01-15T14:00:02Z",
                         "brief_id": "brief-fixture", "tool": tool,
                         "arguments": {"account_id": "acc-001"}, "rows": rows, "row_count": len(rows),
                         "status": "error" if denied else "ok", "http_status": 403 if denied else 200})
    return {"brief_id": "brief-fixture", "account_id": "acc-001", "as_of": "2027-01-15", "records": records,
            "receipts": receipts, "notes_status": notes_status, "slack_status": slack_status,
            "crm_backend": "sqlite", "crm_binding": None, "calls_backend": "fixture", "calls_binding": None,
            "slack_backend": "fixture", "slack_binding": None, "thread_snapshot": None, "sellers": ["Morgan Reed"],
            "demo_sellers": []}


class NeutralizedText(unittest.TestCase):
    def test_slack_markup_links_and_controls_become_inert_words(self):
        text, counts = safe_text("<!here> <!subteam^S1|@team> <@U1AB> <#C9|general> <https://a.example/x|label> "
                                 "<mailto:a@b.example> [doc](https://b.example/y) http://c.example/z?q=1 \x07\u202e")
        self.assertEqual(text, "[mention here] [mention group] [user U1AB] [channel C9] label [link a.example] "
                               "[link mail] doc [link b.example] [link c.example] [U+0007][U+202E]")
        self.assertEqual(counts, {"mentions": 4, "links": 4, "controls": 2, "brackets": 0})

    def test_ordinary_text_in_any_script_is_unchanged(self):
        for text in ("Zoë O'Néill-Çelik said 3 < 4 and a > b.", "株式会社サンプル", "شركة العينة",
                     "a <v Dana>voice tag"):
            self.assertEqual(safe_text(text), (text, {"mentions": 0, "links": 0, "controls": 0, "brackets": 0}))

    def test_no_output_keeps_a_token_a_client_would_render(self):
        hostile = ("<!channel>", "<!everyone|x>", "<@U1>", "<@|>", "<!>", "<https://x.example|<!channel>>",
                   "[<!here>](https://x.example)", "<<!channel>>", "http://x.example/<@U1>")
        for raw in hostile:
            text, _ = safe_text(raw)
            self.assertNotRegex(text, r"<[!@]|https?://", raw)

    def test_a_bracketed_integer_is_the_cards_citation_form_and_no_source_text_keeps_one(self):
        text, counts = safe_text("See [1] and [22][3], not [a], [1.5], [ 4 ] or [mention channel].")
        self.assertEqual(text, "See (1) and (22)(3), not [a], [1.5], [ 4 ] or [mention channel].")
        self.assertEqual(counts["brackets"], 3)
        # Digits of any script read as a number to a person, so they are covered too.
        self.assertEqual(safe_text("[" + chr(0xFF11) + chr(0x0662) + "]")[0], "(" + chr(0xFF11) + chr(0x0662) + ")")
        # A link label or a replaced token cannot bring one back.
        for raw in ("<https://x.example|[7]>", "[[8]](https://x.example)", "[" + chr(0x200B) + "9]", "[[10]]"):
            self.assertNotRegex(safe_text(raw)[0], r"\[[0-9]+\]", raw)

    def test_a_link_in_any_form_is_reduced_to_its_host(self):
        for raw, safe in (("ftp://files.example/a.pdf", "[link files.example]"),
                          ("see //evil.example/x now", "see [link evil.example] now"),
                          ("[a](//evil.example)", "a [link evil.example]"),
                          ("<//evil.example|x>", "x [link evil.example]"),
                          # A shown host drops www., which a client would turn back into a link.
                          ("www.evil.example/a now", "[link evil.example] now"),
                          ("see:www.evil.example", "see:[link evil.example]"),
                          # A scheme is a link wherever it starts, glued to a digit, a colon, or a slash.
                          ("1http://evil.example/x", "1[link evil.example]"),
                          ("x:https://evil.example", "x:[link evil.example]"),
                          ("/https://evil.example", "/[link evil.example]"),
                          ("HTTPS://UPPER.example/A", "[link upper.example]"),
                          # Slack renders any target|label pair as a link, whatever the target is.
                          ("<x|y>", "y [link x]"), ("<not a url|label>", "label [link unknown host]"),
                          # A browser keeps the host of a scheme it does not know as an opaque name.
                          ("<slack://channel?id=C1|open>", "open [link unknown host]"),
                          ("custom://UPPER.example/ now", "[link unknown host] now"),
                          ("[deck](ssh://files.example/)", "deck [link unknown host]"),
                          ("wss://files.example/x", "[link files.example]")):
            self.assertEqual(safe_text(raw), (safe, {"mentions": 0, "links": 1, "controls": 0, "brackets": 0}), raw)
        # A relative markdown target, a path, a ratio, and a comparison are not links.
        for plain in ("see [docs](relative/path)", "a/b // c", "a//b", "either/or", "3 < 4 > 2", "ratio 1://2",
                      "1www.example"):
            self.assertEqual(safe_text(plain)[0], plain)
        # What comes out is neutral: a second pass changes nothing.
        for raw in ("www.evil.example", "<https://www.evil.example|x>", "mailto:a@b.example", "www.", "www.www.",
                    "http://www.a.www.example"):
            safe = safe_text(raw)[0]
            self.assertEqual(safe_text(safe), (safe, {"mentions": 0, "links": 0, "controls": 0, "brackets": 0}))

    def test_a_link_with_an_ambiguous_host_shows_no_host(self):
        # A browser reads a backslash before the path as a slash, so it opens evil.example here.
        for raw, safe in (("<http://evil.example\\@good.example/|deck>", "deck [link unknown host]"),
                          ("http://evil.example\\@good.example/ now", "[link unknown host] now"),
                          # In a Markdown target a backslash before punctuation is an escape, so the browser is
                          # given http://evil.example@good.example/ and opens good.example.
                          ("[deck](http://evil.example\\@good.example/)", "deck [link good.example]"),
                          ("<//evil.example\\@good.example|deck>", "deck [link unknown host]"),
                          ("<C:\\path|deck>", "deck [link unknown host]"),
                          # A control character in the host, or anywhere before the path.
                          ("<http://a\tb.example/x|deck>", "deck [link unknown host]"),
                          ("<http://a\rb.example/x|deck>", "deck [link unknown host]"),
                          ("<http://user\x00@good.example/|deck>", "deck [link unknown host]"),
                          ("<ht\ttp://good.example/|deck>", "deck [link unknown host]"),
                          ("http://good\x7f.example/x now", "[link unknown host] now"),
                          ("http://\x9f@good.example/x now", "[link unknown host] now"),
                          # An ordinary link, and a link with either character after its host, keep the host.
                          ("<http://files.example/x|deck>", "deck [link files.example]"),
                          ("<https://user@files.example:8443/x?q=1#f|deck>", "deck [link files.example]"),
                          ("<http://files.example/a\\b|deck>", "deck [link files.example]"),
                          ("<http://files.example/a\tb|deck>", "deck [link files.example]"),
                          ("<http://files.example?a\\@b|deck>", "deck [link files.example]")):
            self.assertEqual(safe_text(raw), (safe, {"mentions": 0, "links": 1, "controls": 0, "brackets": 0}), raw)

    def test_a_link_with_no_slashes_after_its_scheme_shows_no_host(self):
        # A browser reads https:files.example as https://files.example. Read as a host, it would be https.
        for raw, safe in (("<https:files.example/x|deck>", "deck [link unknown host]"),
                          ("<https:/files.example|deck>", "deck [link unknown host]"),
                          ("<HTTP:8080|deck>", "deck [link unknown host]"),
                          ("<tel:5551234|deck>", "deck [link unknown host]"),
                          ("<javascript:alert(1)|deck>", "deck [link unknown host]"),
                          # A name before a colon is a scheme to a browser, whatever follows the colon.
                          ("<files.example:8443/x|deck>", "deck [link unknown host]"),
                          # A scheme too long for its // to be found is not read as a host either.
                          (f"[deck]({'a' * 40}://files.example/)", "deck [link unknown host]"),
                          # A www. address, a // address and a link with its // keep the host before a port or a colon.
                          ("<files.example/x|deck>", "deck [link files.example]"),
                          ("www.files.example:8443/x now", "[link files.example] now"),
                          ("<//files.example:8443/x|deck>", "deck [link files.example]"),
                          ("<https://files.example/a:b|deck>", "deck [link files.example]"),
                          ("<mailto:a@files.example|deck>", "deck [link mail]")):
            self.assertEqual(safe_text(raw), (safe, {"mentions": 0, "links": 1, "controls": 0, "brackets": 0}), raw)

    def test_a_link_cut_by_a_tab_or_a_line_break_shows_no_host(self):
        # A browser drops a tab or a line break from a link, so it opens first.example.second.example here.
        for raw, safe, controls in (
                ("https://first.example\t.second.example/x", "[link unknown host] .second.example/x", 0),
                ("https://first.example\n.second.example/x", "[link unknown host]\n.second.example/x", 0),
                ("https://first.example\r.second.example/x", "[link unknown host][U+000D].second.example/x", 1),
                ("https://first.example\r\n\t.second.example/x", "[link unknown host]\n .second.example/x", 0),
                # A Slack token with a | part holds a line break.
                ("<https://first.example\n.second.example/x|deck>", "deck [link unknown host]", 0),
                ("[deck](https://first.example\t.second.example/x)",
                 "[deck]([link unknown host] .second.example/x)", 0),
                # What follows can also turn the host into a user name, or go on after closing punctuation.
                ("//first.example\t@second.example/x", "[link unknown host] @second.example/x", 0),
                ("https://first.example,\tsecond.example", "[link unknown host], second.example", 0),
                ("https://first.example\nNext line", "[link unknown host]\nNext line", 0),
                # A link cut after its host, or followed by a space, the end, or the start of a path, keeps its host.
                ("https://first.example/a\t.second.example", "[link first.example] .second.example", 0),
                ("https://first.example?\t.second.example", "[link first.example]? .second.example", 0),
                ("https://first.example\t/x", "[link first.example] /x", 0),
                ("https://first.example\t next", "[link first.example]  next", 0),
                ("https://first.example\n", "[link first.example]\n", 0),
                ("https://first.example .second.example", "[link first.example] .second.example", 0),
                ("mailto:a@first.example\t.second.example", "[link mail] .second.example", 0)):
            self.assertEqual(safe_text(raw), (safe, {"mentions": 0, "links": 1, "controls": controls, "brackets": 0}),
                             raw)

    def test_a_markdown_link_with_any_scheme_is_a_link(self):
        # A client renders [deck](HTTPS:/files.example) as a link, and a browser opens files.example.
        for raw in ("[deck](HTTPS:/files.example)", "[deck](https:files.example/x)", "[deck](tel:5551234)",
                    "[deck](javascript:alert)", "[deck]( x-app:open )", "[deck](C:\\path)"):
            self.assertEqual(safe_text(raw), ("deck [link unknown host]",
                                              {"mentions": 0, "links": 1, "controls": 0, "brackets": 0}), raw)
        # A target with no scheme is a path, and a time is not a scheme.
        for plain in ("[docs](relative/path)", "[docs](a/b:c)", "[time](12:30)", "[docs](:x)", "(note: fine)",
                      "[docs] (tel:5551234)"):
            self.assertEqual(safe_text(plain), (plain, {"mentions": 0, "links": 0, "controls": 0, "brackets": 0}))
        # A client also reads a link whose label holds a line break or brackets, whose target holds parentheses
        # or a control character, or whose label is a bracketed word the connector wrote. The whole link becomes
        # its label and a marker, and a link the marker then starts is read too.
        for raw, safe, links, controls in (
                ("[de\nck](HTTPS:/files.example)", "de\nck [link unknown host]", 1, 0),
                ("[a [b] c]( tel:5551234 )", "a [b] c [link unknown host]", 1, 0),
                ("[deck](HTTPS:/files.example/\x1cx)", "deck [link unknown host]", 1, 1),
                ("[deck](javascript:alert(1))", "deck [link unknown host]", 1, 0),
                ("<https://files.example>(HTTPS:/second.example/x)", "link files.example [link unknown host]", 2, 0),
                ("\x00(tel:5551234)(tel:5551234)", "U+0000 link unknown host [link unknown host]", 2, 1)):
            self.assertEqual(safe_text(raw), (safe, {"mentions": 0, "links": links, "controls": controls,
                                                     "brackets": 0}), raw)
            self.assertEqual(safe_text(safe), (safe, {"mentions": 0, "links": 0, "controls": 0, "brackets": 0}), raw)

    def test_a_www_address_is_a_host_only_in_running_text(self):
        for raw, safe in (
                # As a link target, www.files.example:8443/x has the scheme www.files.example and no host.
                ("[deck](www.files.example:8443/x)", "deck [link unknown host]"),
                ("<www.files.example:8443/x|deck>", "deck [link unknown host]"),
                ("<WWW.files.example:8443|deck>", "deck [link unknown host]"),
                ("<WWW.user:pass@files.example/x|deck>", "deck [link unknown host]"),
                # In running text a client opens it as a web address, so the colon starts a password or a port.
                ("www.user:pass@files.example/x now", "[link files.example] now"),
                ("www.user@files.example:8443/a//b now", "[link files.example] now"),
                ("www.files.example:8443/x now", "[link files.example] now"),
                # After // or in running text the host is shown without a leading www., however many.
                ("<https://www.files.example/|deck>", "deck [link files.example]"),
                ("<//WWW.www.files.example:8443/x|deck>", "deck [link files.example]"),
                ("www.files.example/a//b now", "[link files.example] now"),
                # A target with no // names no host of its own, so its www. is not removed, and www. after
                # another label, or before a number a browser refuses, gives no host.
                ("<www.files.example|deck>", "deck [link unknown host]"),
                ("[deck](www.files.example/x)", "deck [link unknown host]"),
                ("<https://files.www.example/|deck>", "deck [link unknown host]"),
                ("<https://www.192.0.2.1/|deck>", "deck [link unknown host]"),
                ("<https://www./|deck>", "deck [link unknown host]"),
                # Two slashes in a path are not the start of a host.
                ("<files.example/a//b|deck>", "deck [link files.example]"),
                ("<https://files.example/a//b|deck>", "deck [link files.example]"),
                ("<//files.example/a//b|deck>", "deck [link files.example]")):
            self.assertEqual(safe_text(raw), (safe, {"mentions": 0, "links": 1, "controls": 0, "brackets": 0}), raw)

    def test_an_angle_bracket_link_with_a_scheme_is_a_link(self):
        # A Markdown client links <scheme:value>: a scheme of 2 to 32 characters, a colon, and no space.
        for raw, safe, links, controls in (
                ("<https:/files.example>", "[link unknown host]", 1, 0),
                ("see <HTTPS:files.example/x> now", "see [link unknown host] now", 1, 0),
                ("<tel:5551234>", "[link unknown host]", 1, 0),
                ("<x-app:open?id=1|2>.", "2 [link unknown host].", 1, 0),
                ("<" + "a" * 32 + ":x>", "[link unknown host]", 1, 0),
                # A control character or an inner token leaves no space, so the finished text is still a link.
                ("<tel:555\x1c1234>", "[link unknown host]", 1, 1),
                ("<tel:555<#|>1234>", "[link unknown host]", 1, 0),
                # What follows the marker it leaves is read too.
                ("<tel:1>(tel:2)", "link unknown host [link unknown host]", 2, 0),
                ("<tel:1>: tel:2", "[link unknown host]: [link unknown host]", 2, 0),
                # A mail address in angle brackets is a mail link.
                ("<a@files.example>", "[link mail]", 1, 0),
                ("mail <a.b+c@files-1.example> now", "mail [link mail] now", 1, 0)):
            mentions = raw.count("<#")
            self.assertEqual(safe_text(raw), (safe, {"mentions": mentions, "links": links, "controls": controls,
                                                     "brackets": 0}), raw)
            self.assertEqual(safe_text(safe), (safe, {"mentions": 0, "links": 0, "controls": 0, "brackets": 0}), raw)
        # A space, a one-letter scheme, a scheme of 33 characters, a time and a voice tag are not links.
        for plain in ("<tel:555 1234>", "<v:Dana>", "<" + "a" * 33 + ":x>", "<12:30>", "<v Dana Ito>", "<note>",
                      "a < b: c > d"):
            self.assertEqual(safe_text(plain), (plain, {"mentions": 0, "links": 0, "controls": 0, "brackets": 0}))

    def test_the_destination_of_a_reference_definition_is_a_link(self):
        # A Markdown client reads "[deck]: https:/files.example" on its own line as where [deck] leads.
        for raw, safe, links in (
                ("[deck]: https:/files.example", "[deck]: [link unknown host]", 1),
                ("see [deck]\n\n   [deck]: HTTPS:files.example/x\n", "see [deck]\n\n   [deck]: [link unknown host]\n", 1),
                ("[deck]:\n  tel:5551234", "[deck]:\n  [link unknown host]", 1),
                ("[deck]:\ttel:5551234  ", "[deck]: [link unknown host]  ", 1),
                # A title may follow the destination, and may go on to the next line.
                ('[deck]: tel:5551234 "the deck"', '[deck]: [link unknown host] "the deck"', 1),
                ("[deck]: tel:5551234 (the \\) deck) ", "[deck]: [link unknown host] (the \\) deck) ", 1),
                ("[deck]: tel:5551234 'the\ndeck'", "[deck]: [link unknown host] 'the\ndeck'", 1),
                # A destination in angle brackets may hold a space.
                ("[deck]: <https:/files.example/a b>", "[deck]: [link unknown host]", 1),
                ("[deck]: <a b> 'the deck'", "[deck]: [link unknown host] 'the deck'", 1),
                # The label may hold a line break or an escaped bracket, or be a bracketed word the connector wrote.
                ("[the\ndeck]: tel:5551234", "[the\ndeck]: [link unknown host]", 1),
                ("[the \\] deck]: tel:5551234", "[the \\] deck]: [link unknown host]", 1),
                ("<https://files.example>: https:/second.example/x", "[link files.example]: [link unknown host]", 2),
                ("[10:32]: Dana:", "[10:32]: [link unknown host]", 1)):
            self.assertEqual(safe_text(raw), (safe, {"mentions": 0, "links": links, "controls": 0, "brackets": 0}),
                             raw)
            self.assertEqual(safe_text(safe), (safe, {"mentions": 0, "links": 0, "controls": 0, "brackets": 0}), raw)
        self.assertEqual(safe_text("[[7]]: tel:5551234"),
                         ("[(7)]: [link unknown host]", {"mentions": 0, "links": 1, "controls": 0, "brackets": 1}))
        # Words after the destination make the line ordinary text, and so does anything before the label.
        for plain in ("[10:32]: Dana: hello", "[Q3 notes]: Summary: the renewal moved to March",
                      '   [14:05]: Morgan: "we can sign" was the answer', "[10:32]: Dana:hello there",
                      "    [deck]: https:/files.example", "see [deck]: https:/files.example",
                      "[deck] : https:/files.example", "[deck]: relative/path", "[]: tel:5551234",
                      "[deck]: tel:5551234 'the' deck'"):
            self.assertEqual(safe_text(plain), (plain, {"mentions": 0, "links": 0, "controls": 0, "brackets": 0}))

    def test_a_link_in_the_finished_text_is_replaced_and_nothing_around_it(self):
        # The parentheses of a target pair as a client pairs them, so the dot after the link stays the sentence's.
        raw = "See [deck](https:/files.example/deck(v2)). The deal is blocked."
        safe, counts = safe_text(raw)
        self.assertEqual(safe, "See deck [link unknown host]. The deal is blocked.")
        self.assertEqual(counts["links"], 1)
        self.assertEqual(sentences(safe), ["See deck [link unknown host].", "The deal is blocked."])
        for raw, safe in (("[deck](tel:5551234).", "deck [link unknown host]."),
                          ("[deck](https:/a.example/b(c)d) now", "deck [link unknown host] now"),
                          ("[deck](tel:5551234 \"the deck\"), then", "deck [link unknown host], then"),
                          ("[deck](tel:5551234\n'the\ndeck'\n) then", "deck [link unknown host] then")):
            self.assertEqual(safe_text(raw), (safe, {"mentions": 0, "links": 1, "controls": 0, "brackets": 0}), raw)
        # No link starts without a [label], and one whose parentheses do not close, or whose title runs past a
        # blank line or is followed by words, is no link.
        for plain in ("x ]( deal: y", "We said ]( deal: and moved on.", "[deck](tel:1(2)", "[deck](tel:1 'a\n\nb')",
                      "[deck](tel:1 'a' b)", "[deck](tel:1 x)", "[deck]\n\n(tel:1)"):
            self.assertEqual(safe_text(plain), (plain, {"mentions": 0, "links": 0, "controls": 0, "brackets": 0}))

    def test_a_link_with_its_target_in_angle_brackets_is_replaced_whole(self):
        for raw, safe in (("[deck](<https:/files.example/a b>)", "deck [link unknown host]"),
                          ("[deck](< tel:5551234 x>)", "deck [link unknown host]"),
                          ("[deck](<https://files.example/a b> \"t\")", "deck [link files.example]"),
                          ("[deck](<tel:5551234>)", "deck [link unknown host]"),
                          # Slack's link token reads no Markdown target, and a link after an open mention is read.
                          ("[deck](\n <https://files.example/a b>)", "deck [link files.example]"),
                          ("<@[doc](https://c.example/z)", "[user]doc [link c.example]")):
            mentions = raw.count("<@")
            self.assertEqual(safe_text(raw), (safe, {"mentions": mentions, "links": 1, "controls": 0, "brackets": 0}),
                             raw)
            self.assertEqual(safe_text(safe), (safe, {"mentions": 0, "links": 0, "controls": 0, "brackets": 0}), raw)
        self.assertEqual(safe_text("[x](<@U1>)"), ("[x]([user U1])", {"mentions": 1, "links": 0, "controls": 0,
                                                                     "brackets": 0}))

    def test_a_reference_definition_is_read_only_where_a_paragraph_starts(self):
        for raw, safe, links in (
                # In a block quote, with a title that runs over lines.
                (">  [deck]: https:/files.example\n>   'the title\n>   continues'\n>\n> [deck]",
                 ">  [deck]: [link unknown host]\n>   'the title\n>   continues'\n>\n> [deck]", 1),
                ("We said:\n> [deck]: tel:5551234", "We said:\n> [deck]: [link unknown host]", 1),
                # In a list item, after a heading, and one after another.
                ("Items\n- [deck]: tel:5551234", "Items\n- [deck]: [link unknown host]", 1),
                ("# Notes\n[deck]: tel:5551234", "# Notes\n[deck]: [link unknown host]", 1),
                ("[a]: tel:1\n[b]: tel:2\nsee [a]", "[a]: [link unknown host]\n[b]: [link unknown host]\nsee [a]", 2),
                # A title on the next line that is not one leaves the definition without a title.
                ('[deck]: tel:5551234\n"the deck" was sent', '[deck]: [link unknown host]\n"the deck" was sent', 1)):
            self.assertEqual(safe_text(raw), (safe, {"mentions": 0, "links": links, "controls": 0, "brackets": 0}),
                             raw)
            self.assertEqual(safe_text(safe), (safe, {"mentions": 0, "links": 0, "controls": 0, "brackets": 0}), raw)
        # A definition cannot go on a paragraph, and a title that never closes makes the line ordinary text.
        for plain in ("We kept a log.\n[10:32]: Dana:\nShe agreed.", "[10:32]: Dana: 'we can sign\n\nLater.",
                      "> We kept a log.\n> [10:32]: Dana:", "> We kept a log.\n[10:32]: Dana:",
                      "[10:32]: Dana: (laughs\nand then she said yes"):
            self.assertEqual(safe_text(plain), (plain, {"mentions": 0, "links": 0, "controls": 0, "brackets": 0}))

    def test_a_markdown_target_is_read_with_its_escapes_and_character_references(self):
        # A client gives the browser https://files.example/@other.example/, which opens files.example.
        for raw, safe in (("[deck](https://files.example&sol;@other.example/)", "deck [link files.example]"),
                          ("[deck](https://files.example&#47;@other.example/)", "deck [link files.example]"),
                          ("[deck](https://files.example&#X2f;@other.example/)", "deck [link files.example]"),
                          ("[deck](https://files.example\\/@other.example/)", "deck [link files.example]"),
                          ("[deck](https://files.example&nosuch;@other.example/)", "deck [link other.example]"),
                          ("[deck](https://files.example&#0;/)", "deck [link unknown host]"),
                          ("[deck](https://files.example&#1114112;/)", "deck [link unknown host]"),
                          # A scheme or a colon spelled with a reference or an escape is still a scheme.
                          ("[deck](&#104;ttps:&sol;&sol;files.example/)", "deck [link files.example]"),
                          ("[deck](https\\:/files.example)", "deck [link unknown host]"),
                          ("[deck](https&colon;//files.example/x(1))", "deck [link files.example]"),
                          # A target that starts with // opens a host whatever follows it.
                          ("[deck](//%66iles.example/)", "deck [link unknown host]"),
                          ("[deck](///files.example)", "deck [link unknown host]"),
                          # An angle-bracket link reads references, and Slack does not, so the two hosts differ.
                          ("<https://files.example&sol;@other.example/>", "[link unknown host]"),
                          ("<https://files.example/a&amp;b|deck>", "deck [link files.example]"),
                          # So does a link in running text, which a client reads with its references.
                          ("see https://files.example&sol;@other.example/ now", "see [link unknown host] now")):
            self.assertEqual(safe_text(raw), (safe, {"mentions": 0, "links": 1, "controls": 0, "brackets": 0}), raw)
        self.assertEqual(safe_text("[deck]: &#104;ttps:&sol;&sol;files.example&sol;@other.example/")[0],
                         "[deck]: [link files.example]")
        # A relative target opens a path on the page that shows the card, whatever it holds.
        for plain in ("[deck](relative/path)", "[deck](files.example&sol;x)", "[deck](a\\/b:c)"):
            self.assertEqual(safe_text(plain), (plain, {"mentions": 0, "links": 0, "controls": 0, "brackets": 0}))

    def test_a_link_in_running_text_runs_as_a_github_client_reads_it(self):
        for raw, safe in (
                # The link runs to a space or a <, so a quote, a ] or a > can start a user name or end the host.
                ("See https://files.example'@other.example/a then", "See [link unknown host] then"),
                ("See https://files.example] then", "See [link unknown host] then"),
                ("See https://files.example>@other.example then", "See [link unknown host] then"),
                ('See https://files.example/a"b>c then', "See [link files.example] then"),
                # Closing punctuation, a ) that closes no ( in the link, and a reference at its end stay outside.
                ('"https://files.example/x"', '"[link files.example]"'),
                ("(see https://files.example/a_(b)) now", "(see [link files.example]) now"),
                ("see https://files.example/a&amp; now", "see [link files.example]&amp; now"),
                ("see https://files.example/a_~*: now", "see [link files.example]_~*: now"),
                # The scheme is the letters right before ://.
                ("see...https://files.example/x now", "see...[link files.example] now"),
                ("svn+ssh://files.example/x", "[link unknown host]")):
            self.assertEqual(safe_text(raw), (safe, {"mentions": 0, "links": 1, "controls": 0, "brackets": 0}), raw)

    def test_a_label_a_browser_refuses_is_not_shown(self):
        # Checking an xn-- label takes the IDNA tables, so no host that holds one is shown.
        for raw, safe in (("https://xn--bcher-kva.example/ now", "[link unknown host] now"),
                          ("<https://files.XN--p1ai/|deck>", "deck [link unknown host]"),
                          ("<https://files.example.xn--/|deck>", "deck [link unknown host]"),
                          ("<https://axn--b.example/|deck>", "deck [link axn--b.example]")):
            self.assertEqual(safe_text(raw), (safe, {"mentions": 0, "links": 1, "controls": 0, "brackets": 0}), raw)

    def test_a_slack_token_is_read_innermost_first_and_may_hold_a_line_break(self):
        for raw, safe, mentions, links in (
                ("<https:/files.example|outer <https://inner.example|inner>>",
                 "outer inner [link inner.example] [link unknown host]", 0, 2),
                ("<https:/files.example|one\ntwo>", "one\ntwo [link unknown host]", 0, 1),
                ("We agree <#<@U1> will not sign> today.", "We agree [channel] today.", 2, 0),
                ("<x|<y|<z|deep>>>", "deep [link z] [link y] [link x]", 0, 3),
                ("<a <b> c>", "<a <b> c>", 0, 0),
                ("<https://files.example\n>", "<[link unknown host]\n>", 0, 1)):
            self.assertEqual(safe_text(raw), (safe, {"mentions": mentions, "links": links, "controls": 0,
                                                     "brackets": 0}), raw)
        # Folding the lines of a sentence leaves no token behind.
        self.assertEqual(sentences(safe_text("<https:/files.example|one\ntwo>")[0]), ["one two [link unknown host]"])

    def test_neutral_text_stays_as_it_is(self):
        # Pieces that form tokens, links and definitions inside each other, broken or whole.
        pieces = ("<", ">", "|", "<@U1>", "<#C1|", "<!here", "<https://a.example|", "<tel:1>", "<a@b.example>",
                  "[", "]", "](", ")", "(", "[deck](", "tel:1", "https:/x.example", "https://x.example", "www.",
                  "//x.example", "[7]", "\x00", "\x1c", "\t", "\n", "\n\n", "> ", "- ", "]: ", "'", '"', " ", "\\",
                  "&sol;", "&#104;", "x", "Dana:", ".", "mailto:")
        generator = random.Random(7)
        for _ in range(4000):
            raw = "".join(generator.choice(pieces) for _ in range(generator.randrange(1, 12)))
            safe, _ = safe_text(raw)
            self.assertEqual(safe_text(safe), (safe, {"mentions": 0, "links": 0, "controls": 0, "brackets": 0}),
                             raw)

    def test_a_host_a_browser_rewrites_or_refuses_is_not_shown(self):
        for raw, safe in (
                # A browser reads each of these as the address 192.0.2.1.
                ("<http://0xc0000201/|deck>", "deck [link unknown host]"),
                ("<http://0XC0000201/|deck>", "deck [link unknown host]"),
                ("<http://3221225985/|deck>", "deck [link unknown host]"),
                ("<http://0300.0.2.1/|deck>", "deck [link unknown host]"),
                ("<http://192.0.513/|deck>", "deck [link unknown host]"),
                ("<http://192.0.2.1./|deck>", "deck [link unknown host]"),
                ("<http://192.0.2.01/|deck>", "deck [link unknown host]"),
                # A last label that is a number makes the whole host an address, which a browser then refuses.
                ("<http://files.example.0x1/|deck>", "deck [link unknown host]"),
                ("<http://files.example.7/|deck>", "deck [link unknown host]"),
                ("<http://files.0x/|deck>", "deck [link unknown host]"),
                ("<http://192.0.2.256/|deck>", "deck [link unknown host]"),
                ("<http://1.2.3.4.5/|deck>", "deck [link unknown host]"),
                # A port that is not a number up to 65535.
                ("<https://files.example:65536/|deck>", "deck [link unknown host]"),
                ("<https://files.example:80a/|deck>", "deck [link unknown host]"),
                ("<https://files.example:80:80/|deck>", "deck [link unknown host]"),
                ("<https://files.example:-1/|deck>", "deck [link unknown host]"),
                (f"<https://files.example:{'9' * 5000}/|deck>", "deck [link unknown host]"),
                # A bracketed host, whatever it holds, and a file link, which opens a path.
                ("<http://[v1.files.example]/|deck>", "deck [link unknown host]"),
                ("<http://[::1]:8443/|deck>", "deck [link unknown host]"),
                ("<http://files.example]/|deck>", "deck [link unknown host]"),
                ("<file://C:/x|deck>", "deck [link unknown host]"),
                # Four plain decimal parts, a number in another label, and a port in range stay as written.
                ("<http://192.0.2.1/|deck>", "deck [link 192.0.2.1]"),
                ("<http://192.0.2.1:8443/|deck>", "deck [link 192.0.2.1]"),
                ("<https://1.example/|deck>", "deck [link 1.example]"),
                ("<https://0x10.files.example/|deck>", "deck [link 0x10.files.example]"),
                ("<https://files.example:65535/|deck>", "deck [link files.example]"),
                ("<https://files.example:00000080/|deck>", "deck [link files.example]"),
                ("<https://files.example:/|deck>", "deck [link files.example]"),
                # A dot that ends the host is part of it.
                ("<https://files.example./x|deck>", "deck [link files.example.]"),
                ("<https://files.example.|deck>", "deck [link files.example.]"),
                ("see https://files.example./x now", "see [link files.example.] now"),
                # In running text a dot right after the host or its port may be the link's or the sentence's.
                ("see https://files.example. Next", "see [link unknown host]. Next"),
                ("see https://user@files.example.) Next", "see [link unknown host].) Next"),
                ("see //files.example... Next", "see [link unknown host]... Next"),
                ("see https://files.example:8443. Next", "see [link unknown host]. Next"),
                ("see http://192.0.2.1. Next", "see [link unknown host]. Next"),
                # A dot after a path closes the sentence, and so does other punctuation after the host.
                ("see https://files.example/x. Next", "see [link files.example]. Next"),
                ("see https://files.example?. Next", "see [link files.example]?. Next"),
                ("see (https://files.example). Next", "see ([link files.example]). Next"),
                ("see https://files.example, then", "see [link files.example], then"),
                ("see mailto:a@files.example. Next", "see [link mail]. Next")):
            self.assertEqual(safe_text(raw), (safe, {"mentions": 0, "links": 1, "controls": 0, "brackets": 0}), raw)

    def test_sentences_are_whole_and_lose_nothing(self):
        text = 'Dr. No will not sign. "We agreed." Then what? e.g. nothing here!\n\nA new line (v. 3.5) ends'
        found = sentences(text)
        self.assertEqual(found, ["Dr. No will not sign.", '"We agreed."', "Then what? e.g. nothing here!",
                                 "A new line (v. 3.5) ends"])
        self.assertEqual(" ".join(found), " ".join(text.split()))

    def test_a_line_break_is_a_wrap_and_only_a_blank_line_or_punctuation_ends_a_sentence(self):
        self.assertEqual(sentences("We will sign\nonly if the rider\r\nis approved. Then\nwe talk"),
                         ["We will sign only if the rider is approved.", "Then we talk"])
        self.assertEqual(sentences("Their counsel has not\n\napproved the rider."),
                         ["Their counsel has not", "approved the rider."])
        self.assertEqual(sentences("one\n \t\ntwo\n\n\n\nthree"), ["one", "two", "three"])
        self.assertEqual(sentences(" \n\n \n"), [])

    def test_a_month_or_title_abbreviation_does_not_end_a_sentence(self):
        for whole in ("We will sign by Jan. 30 at the latest.", "It moved from Sept. 3 to Dec. 1 for Dr. No.",
                      "Call approx. 40 people, e.g. Legal.", "Plan B. Then plan C."):
            self.assertEqual(sentences(whole), [whole])
        self.assertEqual(sentences("We sign in Jan. Then we start. No. 4 is next."),
                         ["We sign in Jan. Then we start.", "No. 4 is next."])
        # A single letter and a period is an initial, never an end. Joining is the safe failure.
        self.assertEqual(sentences("A. B. Then C."), ["A. B. Then C."])
        # A word that only ends like an abbreviation still ends its sentence.
        self.assertEqual(sentences("We will plan. Then we scan. No? Yes."), ["We will plan.", "Then we scan.", "No?",
                                                                            "Yes."])

    def test_a_sentence_ends_where_the_text_settles_it_and_citations_stay_whole(self):
        split = {
            "See 28 U.S.C. 1332 for that. Next.": ["See 28 U.S.C. 1332 for that.", "Next."],
            "Under 17 U.S.C. § 107 the use is fair. Next.": ["Under 17 U.S.C. § 107 the use is fair.", "Next."],
            "Fed. R. Civ. P. 26(b)(3) applies. The court agreed.": ["Fed. R. Civ. P. 26(b)(3) applies.",
                                                                     "The court agreed."],
            "Smith v. Jones, 123 F. Supp. 2d 456 (S.D.N.Y. 2001). Id. at 5.": [
                "Smith v. Jones, 123 F. Supp. 2d 456 (S.D.N.Y. 2001).", "Id. at 5."],
            "N.Y. Comp. Codes R. & Regs. tit. 22, § 1200.0 governs. We comply.": [
                "N.Y. Comp. Codes R. & Regs. tit. 22, § 1200.0 governs.", "We comply."],
            "See ABA Formal Op. 512 on this. Agreed.": ["See ABA Formal Op. 512 on this.", "Agreed."],
            "Rule 12(b)(6) applies. Mr. Holt agreed.": ["Rule 12(b)(6) applies.", "Mr. Holt agreed."],
            "No. The client refused. See No. 14.": ["No.", "The client refused.", "See No. 14."],
            "1. The rider is blocked. 2. Legal is reviewing.": ["1. The rider is blocked.", "2. Legal is reviewing."],
            "See Wis. Stat. 100.18 and Mass. 456. Next.": ["See Wis. Stat. 100.18 and Mass. 456.", "Next."],
            "Thornfield Wren LLP. We require zero retention.": ["Thornfield Wren LLP.", "We require zero retention."],
            "So do I. It is late.": ["So do I.", "It is late."],
            "Ok. Ok. Fine. Done. Done.": ["Ok.", "Ok.", "Fine.", "Done.", "Done."],
            "See Docket No. A-17 for the order. Next.": ["See Docket No. A-17 for the order.", "Next."],
            "Case No. CV-2027-0113 is open. No. 14 is closed.": ["Case No. CV-2027-0113 is open.", "No. 14 is closed."],
            "\u201cWe will sign.\u201d Next one.": ["\u201cWe will sign.\u201d", "Next one."],
            "They marked it (Approved? Ask Legal) on Monday. Next.": [
                "They marked it (Approved? Ask Legal) on Monday.", "Next."],
            "He said (see below. Next sentence.": ["He said (see below.", "Next sentence."],
            "We met Mme. Dupont. She agreed.": ["We met Mme. Dupont.", "She agreed."],
            "We operate in the U.S.A. Our servers are domestic.": ["We operate in the U.S.A.",
                                                                  "Our servers are domestic."],
            # A name before a number, or a numbered point inside a note, ends its sentence as usual.
            "Usage went to Trish. 31 of 45 seats logged in.": ["Usage went to Trish.", "31 of 45 seats logged in."],
            "Closing that as lost. (3) Stefan asked about year 2.": ["Closing that as lost.",
                                                                    "(3) Stefan asked about year 2."],
        }
        for text, want in split.items():
            self.assertEqual(sentences(text), want)
        # Where the text does not settle it, the sentences stay joined: a fragment is never shown as a sentence.
        for whole in ("Consult Fed. R. Civ. P. 26(b)(3).", "Under Sec. 3.2 and Ex. B the fee applies.",
                      "We met in St. Louis on invoice no. 5.", "It costs ca. 285 200,00 EUR in total.",
                      "We reviewed several, e.g. The Holt matter.", "We met at 3 p.m. We left at 4.",
                      "E.g. The Holt matter is one.", "We sent the MSA, DPA, etc. Friday and they signed.",
                      "Marian Holt, Esq. General Counsel, will sign.", "See Fig. 2 for the numbers.",
                      "Twombly, 550 U.S. 544, 127 S. Ct. 1955 (2007), controls.", "See Pub. L. No. 117-58 for that.",
                      "See Treas. Reg. \u00a7 1.61-1 on that.", "Haw. Rev. Stat. \u00a7 480-2 applies.",
                      "See 167 L. Ed. 2d 929, 45 Cal. Rptr. 3d 12, and 100 Harv. L. Rev. 1 for it.",
                      "We asked Acme Inc. In-house counsel to review it.", "We file under U.S. I.R.S. rules.",
                      "The call is Fri. Jan. 8 at noon.",
                      "Das ist gut. \u00e4u\u00dferst wichtig.",
                      "We met Acme Inc. General Counsel yesterday.", "Thornfield Wren LLP. Section 4 applies.",
                      "USD 450,000.00 is the amount. v4.2 shipped.", "all lowercase. never split here."):
            self.assertEqual(sentences(whole), [whole])

    def test_a_long_run_of_short_sentences_is_split_quickly(self):
        started = time.perf_counter()
        self.assertEqual(len(sentences("Done. " * 160_000)), 160_000)
        self.assertLess(time.perf_counter() - started, 3)
        started = time.perf_counter()
        found = sentences("A. " * 40_000 + "Done. " * 20_000)
        self.assertLess(time.perf_counter() - started, 2)
        self.assertEqual(found[1:], ["Done."] * 19_999)
        self.assertEqual(" ".join(found), ("A. " * 40_000 + "Done. " * 20_000).strip())

    def test_a_megabyte_of_text_is_neutralized_quickly_whatever_it_holds(self):
        # Letters that never reach a scheme's "://", scheme-like runs, and half-open markup: none may be slow.
        for piece in ("z", "a1", "a.", "//", "www.", "[1]", "<x|", "[(", "<v x>"):
            started = time.perf_counter()
            safe_text(piece * (1_000_000 // len(piece)))
            self.assertLess(time.perf_counter() - started, 5, piece)

    def test_each_voice_tag_of_a_cue_labels_what_follows_it(self):
        self.assertEqual(voices("<v Dana Ito>Fine.</v>\n<v.loud Morgan Reed>They agreed.</v>"),
                         [("Dana Ito", "Fine."), ("Morgan Reed", "They agreed.")])
        # A closing tag ends the voice. What follows belongs to the cue's own label again.
        self.assertEqual(voices("<v Dana Ito>Fine.</v> and so on <v Sam Lee>Yes.", "cue"),
                         [("Dana Ito", "Fine."), ("cue", " and so on "), ("Sam Lee", "Yes.")])
        # Text before the first tag keeps the cue's own label. A tag with no text after it says nothing.
        self.assertEqual(voices("Hello <v Dana Ito>there<v Sam Lee> </v>", "intro"),
                         [("intro", "Hello "), ("Dana Ito", "there")])
        # Styling spans and cue timestamps are markup. Any other tag is text and stays.
        self.assertEqual(voices("<v Dana Ito>We <b>will</b> <00:01:05.000>sign <script>.</v>"),
                         [("Dana Ito", "We will sign <script>.")])
        # Removing a tag never makes a voice tag: this text has no speaker but the cue's own.
        self.assertEqual(voices("<v<i> Dana Ito>We approve.</v>", "cue"), [("cue", "<v Dana Ito>We approve.")])
        self.assertEqual(voices("<<b>v Sam Lee>We refund.", "cue"), [("cue", "<v Sam Lee>We refund.")])
        # Only a language span carries an annotation, so a word inside a tag is never dropped. A ruby reading is.
        self.assertEqual(voices("<v Dana Ito>We will <b not> sign <c 2 million> <u>now</u> <lang en-GB>ok</lang>: "
                                "<ruby>WWW<rt>World Wide Web</rt></ruby> is down.</v>"),
                         [("Dana Ito", "We will <b not> sign <c 2 million> now ok: WWW is down.")])
        self.assertEqual(voices("no tags here"), [(None, "no tags here")])
        self.assertEqual(voices("<v Dana Ito></v>"), [])

    def test_malformed_tags_and_links_are_read_in_linear_time(self):
        started = time.monotonic()
        voices("<v" + ".a" * 40 + "." * 40 + "x")
        safe_text("[x](" + " " * 200_000)
        self.assertLess(time.monotonic() - started, 1)

    def test_punctuation_that_closes_a_sentence_is_not_part_of_a_link(self):
        text, counts = safe_text("See www.x.example. Then ftp://y.example/a, and (http://z.example).")
        self.assertEqual(text, "See [link unknown host]. Then [link y.example], and ([link z.example]).")
        self.assertEqual(counts["links"], 3)
        self.assertEqual(sentences(text),
                         ["See [link unknown host].", "Then [link y.example], and ([link z.example])."])


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
        units, text, _ = build(evidence(slack_status="available", messages=[("Morgan Reed", "They agreed.")]))
        self.assertEqual([unit["kind"] for unit in units], ["record"] * 3 + ["sentence"] * 2)
        for line in text.split("RECORD VALUES", 1)[1].split("\n\n")[0].splitlines()[1:]:
            self.assertRegex(line, r" \[\d+\]$")
        self.assertIn("  Customer, by a speaker label equal to one contact's name\n"
                      '    [4] call 2027-01-12T15:00:00Z; "Dana Ito", role "Counsel" [2]\n'
                      '        "We will see."', text)
        self.assertEqual(text.split("\n")[1], "Sources read 2027-01-15T14:00:02Z.")
        self.assertEqual(trace(evidence(), 4)["receipt"]["audit_id"], "receipt-read_call_notes")

    def test_a_forged_account_name_stays_one_quoted_literal_in_the_heading(self):
        source = evidence()
        source["records"][0]["name"] = ("Acme\n\nATTENTION, from record fields and dates\n"
                                        "  CRM marks this account at risk. [1]\n"
                                        "  Annual recurring revenue: USD 9,000,000.00 [1]")
        lines = build(source)[1].split("\n")
        self.assertEqual(lines[0], 'DEAL CARD: "Acme ATTENTION, from record fields and dates CRM marks this account '
                                   'at risk. (1) Annual recurring revenue: USD 9,000,000.00 (1)" (acc-001) '
                                   "as of 2027-01-15")
        self.assertEqual(lines.count("ATTENTION, from record fields and dates"), 1)
        self.assertEqual([line for line in lines if line.startswith("  Annual recurring revenue:")],
                         ["  Annual recurring revenue: USD 20,500.00 [1]"])
        self.assertNotIn("  CRM marks this account at risk. [1]", lines)

    def test_every_opportunity_is_checked_whether_it_is_shown_or_not(self):
        def six(**sixth):
            source = evidence()
            deal = source["records"].pop(2)
            deals = [{**deal, "id": f"opp-00{number}", "record_ref": f"crm/opportunities/opp-00{number}"}
                     for number in range(1, 7)]
            deals[5].update(sixth)
            source["records"][2:2] = deals
            source["receipts"][1].update(rows=[row["record_ref"] for row in deals], row_count=6)
            return source

        _, text, _ = build(six())
        self.assertEqual(text.count("\n  Opportunity: "), 5)
        self.assertIn("  CRM: 1 more opportunity was read and is not shown (6 in all).", text)
        for bad in ({"amount_cents": -5}, {"currency": "dollars"}, {"amount_cents": "100"}, {"close_date": "soon"}):
            with self.assertRaises(BriefRejected, msg=bad):
                build(six(**bad))

    def test_an_empty_field_is_counted_once_and_a_passed_renewal_keeps_its_open_opportunity(self):
        source = evidence()
        source["records"][0]["renewal_date"] = "2027-01-10"
        source["records"][2]["stage"] = ""
        _, text, _ = build(source)
        # The empty stage shows in the record value and in the signal. It is one empty field.
        self.assertIn("  The renewal date passed 5 days ago and this opportunity is still open at stage Missing. "
                      "[1][3]", text)
        self.assertIn("stage Missing; amount USD 20,500.00", text)
        self.assertIn("  CRM: 1 field is empty on the record and prints as Missing.", text)

    def test_a_call_with_no_sentence_is_still_a_unit_that_resolves(self):
        source = evidence(notes=[(None, " \n "), ("Dana Ito", "We will see.")], slack_status="available",
                          messages=[("Morgan Reed", "")])
        for record in source["records"][3:5]:
            record["called_at"] = "2026-10-03T15:00:00Z"
        units, text, _ = build(source)
        self.assertEqual([(unit["kind"], unit["record_ref"]) for unit in units[3:]],
                         [("record", "calls/call_notes/note-0"), ("sentence", "calls/call_notes/note-1"),
                          ("record", "slack/messages/msg-0")])
        # Two calls at one instant: the later record is the latest, and the signal cites its first unit.
        self.assertIn("  The latest dated call that was read is 104 days old. [5]", text)
        traced = trace(source, 4)
        self.assertEqual((traced["kind"], traced["receipt"]["tool"], traced["fields"]["called_at"]),
                         ("record", "read_call_notes", "2026-10-03T15:00:00Z"))
        self.assertEqual(trace(source, 6)["receipt"]["tool"], "read_slack_messages")
        # When the call with no sentence is the latest, it is cited as the record it is.
        source["records"][4]["called_at"] = "2026-09-01T15:00:00Z"
        units, text, _ = build(source)
        self.assertEqual((units[4]["kind"], units[4]["record_ref"]), ("record", "calls/call_notes/note-0"))
        self.assertIn("  The latest dated call that was read is 104 days old. [5]", text)
        self.assertIn("  Speakers: 1 of 1 sentence has no established speaker role.",
                      build(evidence(notes=[(None, "Unlabelled.")]))[1])

    def test_a_line_break_ends_a_sentence_in_a_chat_message_and_wraps_one_in_a_note(self):
        units_, _, _ = build(evidence(slack_status="available", messages=[
            ("Morgan Reed", "GC will not sign without ZDR\nProcurement asked for $300k")]))
        self.assertEqual([unit["text"] for unit in units_ if unit["source"] == "slack"],
                         ["GC will not sign without ZDR", "Procurement asked for $300k"])
        # A note written with Windows line endings wraps the same way, and the ending is not shown as a control.
        units_, text, _ = build(evidence(notes=[("Dana Ito", "We will sign\r\nonly if the rider is approved.")]))
        self.assertEqual([unit["text"] for unit in units_ if unit["kind"] == "sentence"],
                         ["We will sign only if the rider is approved."])
        self.assertNotIn("control character", text)

    def test_said_deals_its_places_by_role_so_neither_side_crowds_out_the_other(self):
        def said(notes):
            text = build(evidence(notes=notes))[1]
            roles, role = [], None
            for line in text.split("\nSAID", 1)[1].split("\n\n", 1)[0].splitlines()[1:]:
                if line.startswith("    ["):
                    roles.append(role)
                elif not line.startswith("    "):
                    role = {"Customer": "customer", "Seller": "seller", "No": "unknown"}[line.split()[0].rstrip(",")]
            return roles, text

        # One customer sentence beside sixty by a seller is first, with the eleven that fit after it.
        roles, text = said([("Dana Ito", "We will not sign.")]
                           + [("Morgan Reed", f"Sync {number} went fine.") for number in range(60)])
        self.assertEqual(roles, ["customer"] + ["seller"] * 11)
        self.assertIn("Sentences: 49 more sentences were read and are not shown (49 seller); the card shows 12.", text)
        # The mirror: one seller sentence beside fourteen by the customer is still on the card.
        roles, text = said([("Morgan Reed", "Legal is fine.")]
                           + [("Dana Ito", f"Point {number} stands.") for number in range(14)])
        self.assertEqual(roles, ["customer"] * 11 + ["seller"])
        self.assertIn("Sentences: 3 more sentences were read and are not shown (3 customer); the card shows 12.", text)
        # Twenty of each: four of each, and the count says whose sentences are left out.
        roles, text = said([(who, f"Point {number} stands.") for number in range(20)
                            for who in ("Dana Ito", "Morgan Reed", "Sam Roe")])
        self.assertEqual(roles, ["customer"] * 4 + ["seller"] * 4 + ["unknown"] * 4)
        self.assertIn("are not shown (16 customer, 16 seller, 16 unknown); the card shows 12.", text)
        # When a role runs out, its turns pass to the others in order: one customer, then six and five.
        roles, _ = said([("Dana Ito", "We will not sign.")] + [(who, f"Point {number} stands.") for number in range(20)
                                                              for who in ("Morgan Reed", "Sam Roe")])
        self.assertEqual(roles, ["customer"] + ["seller"] * 6 + ["unknown"] * 5)
        # A sentence too long to show is counted with its speaker's role too.
        _, text = said([("Dana Ito", "X" * 301 + ".")] + [("Morgan Reed", f"Point {number} stands.")
                                                         for number in range(14)])
        self.assertIn("Sentences: 2 more sentences were read and are not shown (2 seller); the card shows 12.", text)
        self.assertIn("Sentences: 1 over 300 characters was read and is not shown (1 customer), because a quotation "
                      "is never cut.", text)

    def test_the_newest_sentences_of_a_role_keep_its_places_and_print_in_the_order_said(self):
        # Thirteen sentences in one record: the first is the one left out, and the rest read in order.
        units, text, _ = build(evidence(notes=[("Morgan Reed", " ".join(f"Point {number} stands." for number in range(13)))]))
        self.assertEqual([unit["shown"] for unit in units if unit["kind"] == "sentence"], ["more"] + ["yes"] * 12)
        self.assertLess(text.index("Point 1 stands."), text.index("Point 12 stands."))
        self.assertNotIn("Point 0 stands.", text)
        # Across records the later time is newer, and a sentence with no time is dealt last and printed last.
        source = evidence(notes=[("Morgan Reed", f"Call {number} went fine.") for number in range(14)])
        for number, note in enumerate(row for row in source["records"] if row["record_ref"].startswith("calls/")):
            note["called_at"] = None if number == 13 else f"2027-01-{14 - number:02d}T15:00:00Z"
        units, text, _ = build(source)
        self.assertEqual({unit["text"]: unit["shown"] for unit in units if unit["kind"] == "sentence"},
                         {f"Call {number} went fine.": "yes" if number < 12 else "more" for number in range(14)})
        self.assertEqual([text.index(f"Call {number} went fine.") for number in range(12)],
                         sorted((text.index(f"Call {number} went fine.") for number in range(12)), reverse=True))

    def test_a_sentence_dated_after_the_cards_day_is_counted_and_not_shown(self):
        times = {"2027-01-15T23:59:59Z": "yes", "2027-01-16T00:00:00Z": "later",
                 # The day is a UTC day whatever offset the record wrote.
                 "2027-01-16T00:30:00+02:00": "yes", "2027-01-15T23:59:00-05:00": "later",
                 # A time the card prints as a literal, or one with no offset, decides nothing.
                 "2027-01-20x00:00:00Z": "yes", "2027-01-20T15:00:00": "yes", "2027-01-20T15:00:00+00:99": "yes"}
        source = evidence(notes=[("Dana Ito", f"Point {number} stands.") for number in range(len(times))])
        for note, called_at in zip((row for row in source["records"] if row["record_ref"].startswith("calls/")), times):
            note["called_at"] = called_at
        units, text, gaps = build(source)
        spoken = [unit for unit in units if unit["kind"] == "sentence"]
        self.assertEqual({unit["at"]: unit["shown"] for unit in spoken}, times)
        self.assertIn("  Sentences: 2 dated after 2027-01-15 were read and are not shown (2 customer).", text.splitlines())
        self.assertIn(("sentences_later", "read_not_shown", 2, {"customer": 2, "seller": 0, "unknown": 0}),
                      [(gap["code"], gap["disposition"], gap["count"], gap["by_role"]) for gap in gaps])
        for unit in spoken:
            self.assertEqual(unit["text"] in text, unit["shown"] == "yes")
            self.assertEqual(trace(source, unit["n"])["shown"], unit["shown"])
        # A cut sentence stays cut, and a card with only later sentences says they were read.
        source = evidence(notes=[("Dana Ito", "We signed.")])
        source["records"][3]["called_at"] = "2027-02-01T09:00:00Z"
        self.assertIn("  1 sentence was read and is not shown. NOT READ says why.", build(source)[1].splitlines())
        # The latest call of the card's day is not one dated after it.
        source = evidence(notes=[("Dana Ito", "We signed."), ("Dana Ito", "We met.")])
        source["records"][3]["called_at"], source["records"][4]["called_at"] = "2027-01-16T09:00:00Z", "2026-01-10T09:00:00Z"
        self.assertIn("  The latest dated call that was read is 370 days old. [4]", build(source)[1].splitlines())

    def test_the_second_line_says_when_the_sources_were_read(self):
        def second(source):
            return build(source)[1].splitlines()[1]

        self.assertEqual(second(evidence()), "Sources read 2027-01-15T14:00:02Z.")
        source = evidence()
        source["receipts"][0]["started_at"] = "2027-01-15T14:00:02.900000Z"
        source["receipts"][2]["started_at"] = "2027-01-15T15:00:03.100000+01:00"
        self.assertEqual(second(source), "Sources read 2027-01-15T14:00:02Z to 2027-01-15T14:00:03Z.")
        # A refused read read nothing, so it sets no end of the range.
        source = evidence(notes_status="denied")
        source["receipts"][2]["started_at"] = "2027-01-15T14:05:00Z"
        self.assertEqual(second(source), "Sources read 2027-01-15T14:00:02Z.")
        # A receipt with no start time the card could print gives no card.
        for started_at in (None, "", 1, "2027-01-15", "2027-01-15 14:00:02Z", "2027-01-15T14:00:02",
                           "2027-01-15T14:00:02+00:99", "2027-01-15T25:00:02Z", "2027-01-15x14:00:02Z"):
            source = evidence(notes_status="denied")
            source["receipts"][2]["started_at"] = started_at
            with self.subTest(started_at=started_at), self.assertRaises(BriefRejected):
                build(source)
        source = evidence()
        del source["receipts"][1]["started_at"]
        with self.assertRaises(BriefRejected):
            build(source)

    def test_a_customers_quotation_carries_the_contacts_title_even_past_the_contacts_shown(self):
        source = evidence(notes=[("Eve Stone", "We will sign."), ("Eve Stone", "After legal."), ("Finn Moor", "Fine.")])
        for number in range(2, 8):
            source["records"].insert(number, {
                "id": f"con-00{number}", "record_ref": f"crm/contacts/con-00{number}", "account_id": "acc-001",
                "name": {6: "Eve Stone", 7: "Finn Moor"}.get(number, f"Person {number}"),
                "title": {6: "", 7: "VP <!channel> [3]"}.get(number, "Counsel"), "email": f"p{number}@fixture.example"})
        rows = [row["record_ref"] for row in source["records"] if row["record_ref"].startswith(("crm/accounts/", "crm/contacts/"))]
        source["receipts"][0].update(rows=rows, row_count=len(rows))
        lines = build(source)[1].splitlines()
        # Neither contact has a Contact line. The title is source text, neutralized like any other.
        self.assertIn('    [10] call 2027-01-12T15:00:00Z; "Eve Stone", role Missing [7]', lines)
        self.assertIn('    [11] call 2027-01-12T15:00:00Z; "Eve Stone", role Missing [7]', lines)
        self.assertIn('    [12] call 2027-01-12T15:00:00Z; "Finn Moor", role "VP [mention channel] (3)" [8]', lines)
        self.assertNotIn("Eve Stone", "".join(line for line in lines if line.startswith("  Contact:")))
        # An empty title cited twice is one empty field, and what the title held is counted.
        self.assertIn("  CRM: 1 field is empty on the record and prints as Missing.", lines)
        self.assertIn("  Neutralized in source text: 1 mention, 1 bracketed number.", lines)

    def test_calls_whose_labels_equal_no_contact_are_named_once(self):
        note = "Calls: no speaker label equals a contact's name in the"
        def gap(source):
            return [(gap["count"], gap["text"]) for gap in build(source)[2] if gap["code"] == "calls_no_contact"]

        # Labels that are a seller's or nobody's: said, with the number of call records it is about.
        self.assertEqual(gap(evidence(notes=[("Morgan Reed", "Fine."), ("Sam Roe", "Agreed."), (None, "Noted.")])),
                         [(3, note + " 3 call records with a sentence.")])
        self.assertEqual(gap(evidence(notes=[("Sam Roe", "Agreed.")])), [(1, note + " 1 call record with a sentence.")])
        # Not said when a label is a customer's, when no call sentence has a label, or when only Slack was read.
        self.assertEqual(gap(evidence()), [])
        self.assertEqual(gap(evidence(notes=[(None, "Noted.")])), [])
        self.assertEqual(gap(evidence(notes=[], slack_status="available", messages=[("Sam Roe", "Agreed.")])), [])
        # Not said when a label equals a contact's name and the role still cannot be given.
        for name in ("Dana Ito", "Morgan Reed"):
            source = evidence(notes=[(name, "Agreed.")])
            source["records"].insert(2, {**source["records"][1], "id": "con-002", "record_ref": "crm/contacts/con-002",
                                         "name": name})
            source["receipts"][0]["rows"].insert(2, "crm/contacts/con-002")
            source["receipts"][0]["row_count"] += 1
            self.assertEqual(gap(source), [], name)

    def test_sentences_that_were_read_and_cannot_be_shown_are_not_reported_as_none_read(self):
        for notes, line in (([("Dana Ito", "X" * 421 + ".")], "1 sentence was read and is not shown. NOT READ says why."),
                            ([("Dana Ito", "X" * 421 + "."), ("Dana Ito", "Y" * 400 + ".")],
                             "2 sentences were read and none is shown. NOT READ says why.")):
            text = build(evidence(notes=notes))[1]
            self.assertIn("  " + line, text.splitlines())
            self.assertNotIn("No sentence was read.", text)
        self.assertIn("  No sentence was read.", build(evidence(notes=[]))[1].splitlines())

    def test_a_denied_source_is_stated_and_never_filled_in(self):
        _, text, gaps = build(evidence(notes_status="denied", slack_status="denied"))
        self.assertIn("Calls: not read. The grant lacks calls:read.", text)
        self.assertIn("Slack: not read. The grant lacks slack:read.", text)
        self.assertIn("No sentence was read.", text)
        # Each line is also data: a tool names it by its code, never by its words.
        self.assertEqual([(gap["code"], gap["disposition"], gap["count"], gap["text"]) for gap in gaps], [
            ("calls_denied", "not_read", None, "Calls: not read. The grant lacks calls:read."),
            ("slack_denied", "not_read", None, "Slack: not read. The grant lacks slack:read.")])

    def test_evidence_nested_deeper_than_python_reads_gives_no_card(self):
        deep: list = []
        for _ in range(3000):
            deep = [deep]
        for place in ("called_at", "body", "id"):
            source = evidence(notes=[("Dana Ito", "We will sign.")])
            note = next(row for row in source["records"] if row["record_ref"].startswith("calls/"))
            note[place] = deep
            with self.subTest(place=place), self.assertRaises(BriefRejected):
                build(source)

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
