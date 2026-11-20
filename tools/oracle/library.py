"""Python library behaviour the evidence rules lean on, recorded as explicit cases."""

from datetime import date, datetime
from decimal import Decimal
import json
from typing import Any
from urllib.parse import urlsplit

from gtm_connector import strict_json, units
from gtm_connector.salesforce import SalesforceReadError, SalesforceReadOnlyAdapter
from oracle.output import Writer
from oracle.scanners import LINK_PIECES, fuzz_corpus


HOSTS = (
    "http://a.example/x", "http://a\tb.example/x", "http://evil.example\\@good.example/", "https://A.Example:8443/p",
    "HTTPS://WWW.Example.COM/", "www.www.example", "www.", "//x.example", "x.example", "mailto:a@b.example",
    "MAILTO:x", "ftp://files.example/a", "slack://open", "http://[::1]/", "http://[v1.x]/", "http://[::1",
    "http://]x/", "http://user:pass@host.example:99999/", "http://user@host.example:abc/", "http://%41.example/",
    "http://xn--bcher-kva.example/", "http://b\u00fccher.example/", "http://a.example\\b/", "http://a\u3002example/",
    "http://" + "a" * 90 + ".example", "http://" + "a" * 75 + ".example", "http://a.example.", "http://.example",
    "http://a_b.example", "http://a b.example/", "http://a\nb.example/", "http://a\x00b.example/", "http://@/",
    "http://:80/", "http:", "http:/x.example", "http:///x", "https://www.www.example/", "https://wwwx.example/",
    "https://x.www.example/", "//", "//a", "a//b", "www.example.com/path?q=1", "http://\u0130.example/",
    "http://\u212a.example/", "http://EXAMPLE.\u0130/", "http://a.example#frag", "http://a.example?x#y",
    "http://\uff21.example/", "http://a\u200bb.example/", "http://a.example:0/", "http://a.example:65535/",
    "http://[fe80::1%25en0]/", "javascript:alert(1)", "data:text/html,x", "http://1.2.3.4/", "http://0x7f.1/",
    "C:\\path", "\\\\server\\share", "http://a.example%2F@b.example/", "http://a.example/@b.example/",
    # A netloc whose NFKC form gains / ? # @ or : is refused; other non-ASCII netlocs are read.
    "http://a\uff20b.example/", "http://a\u2100.example/", "http://us\uff0fer@good.example/",
    "http://evil\uff03@good.example/", "http://good.example\ufe55443/", "http://\ufe56x.example/",
    "http://\u00aa@good.example/", "http://user\u00e9@good.example/", "http://\u2460.example/",
    "http://\u2474.example/", "http://x\u2044y.example/")
JSON_VALUES: tuple[Any, ...] = (
    "", "plain", 'quote " and backslash \\ and slash /', "\x00\x01\x1f\x7f\x80\x9f", "\t\n\r\x0b\x0c\x08",
    "\u2028\u2029\u0085", "\u00e9\u00df\u4e2d\U0001f642", "\ufeff\u200b\u202e", "</script>&<>'", "\ud83d\ude00",
    0, -1, 1, 2 ** 53 + 1, -(2 ** 63), 2 ** 64, 10 ** 30, 0.0, -0.0, 0.1, 0.5, 1.0, 2.0, 1.5, 1e16, 1e17, 1e-7,
    1e-5, 0.0001, 123456789.123, 12.345678, 1e22, 1e300, 5e-324, 1.7976931348623157e308, 0.30000000000000004,
    3.0e-05, 100.0, 1234.5678, 0.016, True, False, None, [], {}, [[]], [{}], {"a": []}, {"a": {}},
    {"b": 1, "a": [1, "x", None, True, 0.5]}, {"\u00e9": "\u00e9", "k\"ey": "v\\al"}, [1, [2, [3, [4]]]],
    {"nested": {"list": [{"x": -0.0}], "empty": {}}})
NON_FINITE = (float("nan"), float("inf"), float("-inf"))
INSTANTS = (
    "2027-01-15", "2027-02-30", "0001-01-01", "9999-12-31", "2027-1-5", "2027-01-15T00:00:00Z",
    "0001-01-01T00:00:00+23:59", "9999-12-31T23:59:59-23:59", "2026-10-03T15:00:00", "2027-01-15T12:00:00z",
    "2027-01-15T12:00:00+00:00", "2027-01-15T12:00:00-00:00", "2027-01-15 12:00:00Z", "2027-01-15T12:00Z",
    "2027-01-15T12Z", "2027-01-15T12:00:00.5Z", "2027-01-15T12:00:00.123456Z", "2027-01-15T12:00:00.1234567Z",
    "2027-01-15T12:00:00,5Z", "2027-01-15T12:00:00+0530", "2027-01-15T12:00:00+05:30:15",
    "2027-01-15T12:00:00+05:30:15.5", "2027-01-15T12:00:00+24:00", "2027-01-15T23:59:60Z", "2027-01-15T24:00:00Z",
    "20270115T120000Z", "2027015", "2027-015", "2027-W03-5", "2027W035", "2027-W03", "+2027-01-15", "2027-01-15T",
    " 2027-01-15", "2027-01-15 ", "\uff12\uff10\uff12\uff17-01-15", "\u0662\u0660\u0662\u0667-01-15", "",
    "2027-01-15T12:00:00ZZ", "2027-01-15T12:00:00+00", "2027-01-15T12:00:00Z+00:00", "0000-01-01",
    "0001-01-01T00:00:00Z", "9999-12-31T23:59:59.999999-23:59", "2026-11-16T04:30:00+05:00",
    "2026-11-15T23:30:00-05:00", "last week", "2027-01-15T12:00:00.000Z", "2024-02-29", "2023-02-29")
DECIMALS = (
    "1", "0", "-0", "1.0", "1.50", "-0.0", "0.000001", "1e3", "1E-3", "1.23e+5", "1e28", "1e29", "1e-28", "1e-29",
    "123456789012345678901234567890.12", "1234567890123456789012345678", "12345678901234567890123456789",
    "0.1234567890123456789012345678", "9999999999999999999999999999e28", "1e400", "1e-400", "-1.5e-7",
    "450000.00", "120000", "12.30", "100.000", "5E+2", "-1E+28", "0e5", "0.0e-5", "1.0e0", "-12345.6789e-3")
DOCUMENTS: tuple[bytes | str, ...] = (
    b'{"a": 1}', b'{"a": 1, "a": 2}', b'{"a": 1, "\\u0061": 2}', b'{"a": {"b": 1, "b": 1}}', b'[NaN]',
    b'[Infinity]', b'[-Infinity]', b'[1e999]', b'[-1e999]', b'[1E+400]', b'[9' + b"9" * 400 + b'.0]',
    b'["\\ud800"]', b'["\\udc00"]', b'["\\ud83d\\ude00"]', b'{"\\ud800": 1}', b'["\\ud83d"]', b'["\\u0000"]',
    b'\xef\xbb\xbf{"a": 1}', b'\xef\xbb\xbf\xef\xbb\xbf{}', b'\xff\xfe{\x00}\x00', b'\xfe\xff\x00{\x00}',
    b"\xff\xfe" + '{"a": 1}'.encode("utf-16-le"),
    b"\xfe\xff" + '{"a": 1}'.encode("utf-16-be"), b"\xff\xfe\x00\x00" + '{"a": 1}'.encode("utf-32-le"),
    b"\x00\x00\xfe\xff" + '{"a": 1}'.encode("utf-32-be"), b'{"a": "\xff"}', b'{"a": "\xed\xa0\x80"}',
    b'{"a": "\xc0\xaf"}', b'["\xf4\x90\x80\x80"]', b'', b' ', b'1', b'-0', b'0.1', b'1e5', b'1.5e-10', b'[1,]',
    b'{"a":1,}', b"{'a': 1}", b'[1] [2]', b'tru', b'true', b'null', b'"\x01"', b'"\\x41"', b'[1.0, 2]',
    b'123456789012345678901234567890', b'1' + b'0' * 4299, b'1' + b'0' * 4300, b'[' * 1000 + b']' * 1000,
    '{"a": 1}', '\ufeff{"a": 1}', '["\ud800"]', '["\\ud800"]', '{"\u00e9": "\u00e9"}')
# Section 4 of the backend plan: oracle behaviour the Go port will change on purpose. The cases are recorded here
# as the oracle gives them; the Go side writes its own reviewed expectations.
DIFFERENCES = {
    "safe_text_twice": "We agree <#<@U1> will not sign> today.",
    "sentence_opener_quote": 'They said yes. "maybe later" was the reply.',
    "sentence_opener_accent": 'They said yes. "\u00e9lan" was the word.',
    "sentence_unicode_16_letter": "He agreed. \U00010d70xyz goes on.",
    "ambiguous_host": "<http://evil.example\\@good.example/|deck>",
    "control_in_host": "<http://a\tb.example/x|deck>",
}


def _outcome(function, *arguments) -> dict:
    try:
        return {"ok": function(*arguments)}
    except Exception as error:  # Each refusal is recorded by its type and message.
        return {"error": type(error).__name__, "message": str(error)}


def _split(target: str) -> dict:
    def parts():
        found = urlsplit(target)
        return [found.scheme, found.netloc, found.path, found.query, found.fragment]

    def hostname():
        return urlsplit(target if "//" in target[:40] else "//" + target).hostname

    def port():
        return urlsplit(target).port
    return {"input": target, "urlsplit": _outcome(parts), "hostname": _outcome(hostname), "port": _outcome(port),
            "host": units._host(target)}


def _dumps(value: Any) -> dict:
    variants = {
        "default": {},
        "ascii_false": {"ensure_ascii": False},
        "audit": {"ensure_ascii": False, "allow_nan": False},
        "artifact": {"ensure_ascii": False, "allow_nan": False, "indent": 2},
        "cli": {"ensure_ascii": False, "indent": 2},
    }
    return {"input": value if not isinstance(value, float) or value == value and abs(value) != float("inf")
            else repr(value),
            **{name: _outcome(lambda options=options: json.dumps(value, **options))
               for name, options in variants.items()}}


def _instant(text: str) -> dict:
    def parsed(value):
        moment = datetime.fromisoformat(value)
        offset = moment.utcoffset()
        return [moment.isoformat(), None if offset is None else offset.total_seconds()]

    def instant():
        moment = units._instant(text)
        return None if moment is None else moment.isoformat()

    def day():
        return units._day(text).isoformat()
    return {"input": text, "datetime.fromisoformat": _outcome(parsed, text),
            "datetime.fromisoformat Z": _outcome(parsed, text.replace("Z", "+00:00")),
            "date.fromisoformat": _outcome(lambda: date.fromisoformat(text).isoformat()),
            "units._instant": _outcome(instant), "units._day": _outcome(day)}


def _decimal(text: str) -> dict:
    def amount():
        row = strict_json.loads(f'{{"Amount": {text}}}'.encode(), parse_float=Decimal)
        row.update(Id="006000000000000AAA", AccountId=None, Name="x", StageName="x", CloseDate="2027-01-15",
                   IsClosed=False, IsWon=False)
        try:
            return SalesforceReadOnlyAdapter._opportunity(row, None)["amount_decimal"]
        except SalesforceReadError as error:
            return {"refused": error.code}
    return {"input": text, "format_f": _outcome(lambda: format(Decimal(text), "f")),
            "salesforce_amount": _outcome(amount)}


def _document(document: bytes | str) -> dict:
    # The value as ASCII JSON text, so a large number survives the fixture's own reading.
    kind = "str" if isinstance(document, str) else "bytes"
    raw = document.encode("utf-8", "surrogatepass") if isinstance(document, str) else document
    return {"kind": kind, "hex": raw.hex(),
            **_outcome(lambda: json.dumps(strict_json.loads(document), ensure_ascii=True))}


def generate(writer: Writer) -> None:
    targets = list(dict.fromkeys(HOSTS + tuple(text for text in fuzz_corpus() if "//" in text or ":" in text)[:300]
                                 + LINK_PIECES))
    writer.json("library/urlsplit.json", {
        "note": ("urlsplit parts, hostname as units._host asks for it, port, and the host a card shows; an error is "
                 "the exception's type and message"),
        "cases": [_split(target) for target in targets]}, len(targets))
    values = JSON_VALUES + NON_FINITE
    writer.json("library/json_dumps.json", {
        "note": ("json.dumps under the options the package uses: default (CLI errors and progress), ascii_false "
                 "(card quotations), audit (receipt columns), artifact (card files), cli (--json results); "
                 "a non-finite float is given as its repr"),
        "cases": [_dumps(value) for value in values]}, len(values))
    writer.json("library/datetime.json", {
        "note": "datetime.fromisoformat (as given and with Z read as +00:00), date.fromisoformat, units._instant, "
                "units._day",
        "cases": [_instant(text) for text in INSTANTS]}, len(INSTANTS))
    writer.json("library/decimal.json", {
        "note": ("format(Decimal(text), 'f'), and the amount_decimal the Salesforce adapter gives a JSON number "
                 "read with parse_float=Decimal"),
        "cases": [_decimal(text) for text in DECIMALS]}, len(DECIMALS))
    writer.json("library/strict_json.json", {
        "note": "strict_json.loads of each document (hex of its bytes, or of a str's UTF-8): the value, or the refusal",
        "cases": [_document(document) for document in DOCUMENTS]}, len(DOCUMENTS))
    once = units.safe_text(DIFFERENCES["safe_text_twice"])[0]
    writer.json("library/differences.json", {
        "note": "Backend plan section 4: what the oracle does where Go will differ on purpose",
        "safe_text_twice": {"input": DIFFERENCES["safe_text_twice"], "once": once,
                            "twice": units.safe_text(once)[0]},
        "sentences": {name: {"input": DIFFERENCES[name], "sentences": units.sentences(DIFFERENCES[name])}
                      for name in ("sentence_opener_quote", "sentence_opener_accent", "sentence_unicode_16_letter")},
        "hosts": {name: {"input": DIFFERENCES[name], "safe_text": units.safe_text(DIFFERENCES[name])[0]}
                  for name in ("ambiguous_host", "control_in_host")}}, len(DIFFERENCES))
