"""Matches of every pattern with a lookaround, the windowed searches of the sentence rules, the two large
corpora: safe_text and sentence splitting over seeded random strings, and every R0 function of the invented
pattern items."""

import ast
import json
from pathlib import Path
import random
import re
from typing import Any, Callable, Iterator

from gtm_connector import slack_callback, slack_mapping, units
from oracle.output import Writer
from oracle.unicode_tables import CLASS_CHANGED


ROOT = Path(__file__).resolve().parents[2]
FUZZ = ROOT / "tests_stateful" / "test_fuzz_inputs.py"
# The module-level string tuples of the fuzz tests that hold source text, transcripts, and dates.
FUZZ_NAMES = ("HOSTILE", "PLAIN", "DATES", "WORDS", "METADATA", "FRAGMENTS", "HEADS", "PIECES", "TAILS")
# The fuzz tests' sampled strings of speech and speaker names.
FUZZ_SAMPLED = ("SPEAKERS", "SPOKEN")
# Invented R0 items, one or more for each text pattern that real data showed.
PATTERNS = ROOT / "internal" / "realdata" / "testdata" / "patterns.jsonl"
RANDOM_SCANNER = 20_000
RANDOM_LARGE = 100_000
# A few code points from every class that decides a rule, so random text reaches each branch.
INTERESTING = (
    *range(0x00, 0x21), 0x7F, 0x85, 0xA0, 0xAD, 0x130, 0x131, 0x17F, 0x1C5, 0x300, 0x301, 0x316, 0x345, 0x37E,
    0x387, 0x3A3, 0x3C2, 0x660, 0x6F0, 0x966, 0x1680, 0x180E, 0x200B, 0x200D, 0x2028, 0x2029, 0x202E, 0x2060,
    0x2066, 0x2069, 0x212A, 0x2160, 0x24B6, 0x3000, 0xFEFF, 0xFF11, 0xFF21, 0xFF41, 0x1D7CE, 0xE0041, 0xE0001,
    0x10D70, 0x16FE4, *CLASS_CHANGED[:4], 0x1F642)
SENTENCE_PIECES = (
    "We", "we", "They", "the", "The", "And", "but", "Élan", "élan", "İstanbul", "ıi", "Zoë",
    "ΟΣ", "ß", "K", "ſ", "ǅ", "Dr.", "Mr.", "Ms.", "No.", "no.", "Nos.", "U.S.C.",
    "U.S.", "C.F.R.", "e.g.", "i.e.", "Inc.", "Ltd.", "Co.", "LLC.", "I.", "I", "Fig.", "S.", "Ct.", "v.", "vs.",
    "Jan.", "Sept.", "St.", "Haw.", "Rev.", "Stat.", "a.", "A.", "1.", "12.", "28", "1332", "117-58", "A-17",
    "F.Supp.2d", "abcdef.ghijkl.mnopqr.st.", "§", "¶", "(", ")", "[", "]", '"', "'", "“", "”",
    "‘", "’", "«", "»", "「", "」", "）", ".", ".", ".", "!", "?", "..", "...",
    "…", "。", "！", "？", "।", "؟", "۔", " ", " ", " ", "  ", "\t", "\n", "\n\n",
    "\n \n", " ", " ", "　", " ", "\x1c", "\x85", "x", "Approved?", "Ask", "Legal", "sign",
    "Friday", "(Approved? Ask Legal)", "Dana:", "Dana Reyes: ", "deal: ", "x" * 12)
LINK_PIECES = (
    "http://a.example/x", "<http://a\tb.example/x|deck>", "<http://evil.example\\@good.example/|deck>",
    "<https://A.Example:8443/p?q=1#f|Deck>", "HTTPS://WWW.Example.COM/", "www.www.example", "www.", "//x.example",
    "a//b", "mailto:dana@example.com", "MAILTO:x", "<mailto:a@b.example|mail>", "[deck](https://c.example/z)",
    "[deck]( http://d.example )", "<slack://open|app>", "ftp://files.example/a.", "http://[::1]/", "http://[v1.x]/",
    "http://user:pass@host.example:99999/", "http://%41.example/", "http://xn--bcher-kva.example/",
    "http://bücher.example/", "http://a.example\\b/", "http://a。example/", "http://" + "a" * 90 + ".example",
    "<#C123|general>", "<@U0ABC123>", "<!subteam^S1|@team>", "<!here>", "<!date^1|x>", "<x|y>", "<|>", "[7]",
    "[１]", "[12]x", "\r\n", "\r", "%", "%4", "%41", "%zz", "%%41", "?q=%E2%82%AC")


def _value(node: ast.expr, named: dict[str, tuple]) -> tuple:
    """A literal tuple of strings, with the forms the fuzz file uses: map(chr, ints), *NAME, and str * int."""
    if isinstance(node, ast.Tuple):
        found: list = []
        for element in node.elts:
            if isinstance(element, ast.Starred) and isinstance(element.value, ast.Call):
                call = element.value
                if (isinstance(call.func, ast.Name) and call.func.id == "map"
                        and isinstance(call.args[0], ast.Name) and call.args[0].id == "chr"):
                    found += [chr(code) for code in ast.literal_eval(call.args[1])]
                    continue
            if isinstance(element, ast.Starred) and isinstance(element.value, ast.Name):
                found += named[element.value.id]
                continue
            if isinstance(element, ast.Starred):
                raise ValueError("unsupported fuzz corpus form")
            found.append(_scalar(element))
        return tuple(found)
    raise ValueError("unsupported fuzz corpus form")


def _scalar(node: ast.expr) -> Any:
    if isinstance(node, ast.BinOp) and isinstance(node.op, (ast.Mult, ast.Add)):
        left, right = _scalar(node.left), _scalar(node.right)
        return left * right if isinstance(node.op, ast.Mult) else left + right
    if isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id == "chr":
        return chr(ast.literal_eval(node.args[0]))
    return ast.literal_eval(node)


def fuzz_corpus() -> list[str]:
    """The fuzz tests' strings, read from their source without running Hypothesis, then every ordered pair of the
    source-text pieces, as the fuzz tests join them."""
    tree = ast.parse(FUZZ.read_text())
    named: dict[str, tuple] = {}
    for node in tree.body:
        if isinstance(node, ast.Assign) and len(node.targets) == 1 and isinstance(node.targets[0], ast.Name):
            name = node.targets[0].id
            if name in FUZZ_NAMES:
                named[name] = _value(node.value, named)
            elif name in FUZZ_SAMPLED:
                named[name] = tuple(
                    value for call in ast.walk(node.value) if isinstance(call, ast.Call)
                    and isinstance(call.func, ast.Attribute) and call.func.attr == "sampled_from"
                    and isinstance(call.args[0], ast.Tuple)
                    for value in _value(call.args[0], named) if isinstance(value, str))
    missing = set(FUZZ_NAMES + FUZZ_SAMPLED) - set(named)
    if missing:
        raise ValueError(f"fuzz corpora not found: {sorted(missing)}")
    seen: dict[str, None] = {}
    for name in FUZZ_NAMES + FUZZ_SAMPLED:
        seen.update(dict.fromkeys(text for text in named[name] if isinstance(text, str)))
    pieces = named["HOSTILE"] + named["PLAIN"]
    seen.update(dict.fromkeys(first + second for first in pieces for second in pieces))
    return list(seen)


def _code(generator: random.Random) -> str:
    if generator.random() < 0.6:
        return chr(generator.choice(INTERESTING))
    while True:
        code = generator.randrange(0x110000)
        if not 0xD800 <= code <= 0xDFFF:
            return chr(code)


def random_strings(seed: int, count: int, pieces: tuple, *, longest: int = 14) -> list[str]:
    """Seeded strings of pieces and single code points, most of them apart by a space, as words are. Lone
    surrogates are left out: no source can hold one."""
    generator = random.Random(seed)
    found = []
    for _ in range(count):
        parts = []
        for _ in range(generator.randrange(longest + 1)):
            parts.append(_code(generator) if generator.random() < 0.15 else generator.choice(pieces))
            parts.append(" " if generator.random() < 0.6 else "")
        found.append("".join(parts[:-1]))
    return found


# ---- the scanners --------------------------------------------------------------------------------------------

def _spans(pattern: re.Pattern, text: str) -> list:
    return [[match.start(), match.end()] for match in pattern.finditer(text)]


def _sentence_end(text: str) -> list:
    return [[match.start(1), match.end(1), match.end(2)] for match in units._SENTENCE_END.finditer(text)]


def _opener(text: str) -> list:
    return [[at, found.end()] for at in range(len(text) + 1) if (found := units._OPENER.match(text, at))]


def _named(text: str) -> list | None:
    found = units._NAMED.match(text)
    return None if found is None else [found.start(1), found.end(1), found.end()]


def _target(text: str) -> int | None:
    found = units._IS_TARGET.match(text)
    return None if found is None else found.end()


class _SearchRecorder:
    """The re module as one package module sees it, keeping the compiled pattern of each re.search it makes."""

    def __init__(self) -> None:
        self.patterns: list[re.Pattern] = []

    def __getattr__(self, name: str) -> Any:
        return getattr(re, name)

    def search(self, pattern: str | re.Pattern, string: str, flags: int = 0) -> re.Match | None:
        # re.search compiles through the same cache, so this is the object the package's own call uses.
        compiled = re.compile(pattern, flags)
        self.patterns.append(compiled)
        return compiled.search(string)


def _searched(module: Any, parse: Callable[[], Any]) -> re.Pattern:
    """The one pattern a parser path searches with: run the path once on a valid input and keep what it used."""
    recorder = _SearchRecorder()
    saved = module.re
    module.re = recorder
    try:
        parse()
    finally:
        module.re = saved
    if len(recorder.patterns) != 1:
        raise AssertionError(f"{module.__name__} made {len(recorder.patterns)} re.search calls; the oracle expects one")
    return recorder.patterns[0]


def percent_patterns() -> dict[str, re.Pattern]:
    """The percent patterns as the two parser paths compile them: the thread link's query in
    parse_thread_selection, and the sign-in callback's query in _callback_fields."""
    return {
        "slack_mapping": _searched(slack_mapping, lambda: slack_mapping.parse_thread_selection(
            thread_url="https://acme.slack.com/archives/C0123ABCD/p1767225600000100?thread_ts=1767225600.000100",
            workspace_origin="https://acme.slack.com")),
        "slack_callback": _searched(slack_callback, lambda: slack_callback._callback_fields(
            b"GET /slack/callback?state=s&code=c HTTP/1.1\r\nHost: localhost:8123", port=8123, state="s")),
    }


PERCENT = percent_patterns()


SCANNERS: dict[str, tuple[str, Callable[[str], Any]]] = {
    "units_target": ("units.py:118 _TARGET as _IS_TARGET.match(text): the end of the match, or null", _target),
    "units_raw_link": ("units.py:122 _RAW_LINK.finditer(text): [start, end] of each match",
                       lambda text: _spans(units._RAW_LINK, text)),
    "units_sentence_end": ("units.py:230 _SENTENCE_END.finditer(line): [start, end of group 1, end of group 2]",
                           _sentence_end),
    "units_opener": ("units.py:261 _OPENER.match(line, pos) at every pos: [pos, end] where it matches", _opener),
    "units_named": ("units.py:350 _NAMED.match(line): [start, end] of group 1 and the match end, or null", _named),
    "slack_mapping_percent": (
        "slack_mapping.py:108 the pattern parse_thread_selection searches a thread link's query with, "
        f"{PERCENT['slack_mapping'].pattern}: [start, end] of each match",
        lambda text: _spans(PERCENT["slack_mapping"], text)),
    "slack_callback_percent": (
        "slack_callback.py:163 the pattern _callback_fields searches the sign-in callback's query with, "
        f"{PERCENT['slack_callback'].pattern}: [start, end] of each match",
        lambda text: _spans(PERCENT["slack_callback"], text)),
}
# The searches _continues and _unfinished make with a window. A pattern that opens with ^ must not match at
# pos > 0, and endpos ends the string as $ and \Z see it.
WINDOWED = ("_LIST_MARKER", "_DOTTED", "_MAY_END", "_ABBREVIATION", "_SECTION_SIGN", "_SHORT", "_SHORT_NEXT",
            "_NUMBER_SIGN", "_OPENER", "_ENDED", "_NEVER_LAST")
WINDOW_TEXTS = ("See abcdef.ghijkl.mnopqr.st. Next part.", "No.", "No. 14", "Mr. Lee", "U.S.C.", "28 U.S.C. 1332",
                "Acme Inc. We", "I. The", "Fig. 2", "e.g. this", "(No.", '"Dr." He', "a. B", "1.", " 12.)",
                "abc def.", "Pub. L. No. 117-58")
# The method each windowed pattern is called with.
WINDOW_METHODS = (("_LIST_MARKER", "fullmatch"), ("_DOTTED", "search"), ("_MAY_END", "search"),
                  ("_ABBREVIATION", "search"), ("_SECTION_SIGN", "match"), ("_SHORT", "search"),
                  ("_SHORT_NEXT", "match"), ("_NUMBER_SIGN", "search"), ("_NUMBER_SIGN", "fullmatch"),
                  ("_OPENER", "match"), ("_ENDED", "search"), ("_NEVER_LAST", "search"))


class _Recorder:
    """A compiled pattern that logs each call the sentence rules make with it."""

    def __init__(self, name: str, pattern: re.Pattern, calls: dict) -> None:
        self.name, self.pattern, self.calls = name, pattern, calls

    def _log(self, method: str, text: str, pos: int, endpos: int | None, found: re.Match | None) -> None:
        end = len(text) if endpos is None else endpos
        self.calls.setdefault((self.name, method, text, pos, end),
                              None if found is None else [found.start(), found.end()])

    def __getattr__(self, method: str) -> Callable:
        real = getattr(self.pattern, method)
        if method not in ("match", "search", "fullmatch"):
            return real

        def call(text: str, pos: int = 0, endpos: int | None = None) -> re.Match | None:
            found = real(text, pos) if endpos is None else real(text, pos, endpos)
            self._log(method, text, pos, endpos, found)
            return found
        return call


def _windowed(texts: list[str]) -> dict:
    calls: dict = {}
    saved = {name: getattr(units, name) for name in WINDOWED}
    try:
        for name, pattern in saved.items():
            setattr(units, name, _Recorder(name, pattern, calls))
        for text in texts:
            units.sentences(text)
            units._unfinished(text)
    finally:
        for name, pattern in saved.items():
            setattr(units, name, pattern)
    # Every window of the short texts, for each pattern that opens with the ^ alternative.
    for name in ("_DOTTED", "_ABBREVIATION", "_SHORT", "_MAY_END", "_NUMBER_SIGN", "_NEVER_LAST"):
        pattern = saved[name]
        for text in WINDOW_TEXTS:
            for start in range(len(text) + 1):
                for end in range(start, len(text) + 1):
                    found = pattern.search(text, start, end)
                    calls.setdefault((name, "search", text, start, end),
                                     None if found is None else [found.start(), found.end()])
    # Windows outside the text: a negative pos or endpos, either one past the end, and endpos before pos. re clamps
    # both to the text.
    for name, method in WINDOW_METHODS:
        search = getattr(saved[name], method)
        for text in (*WINDOW_TEXTS, "We", "§ 2"):
            size = len(text)
            for start, end in ((-3, 100), (-1, size), (0, size + 5), (size + 2, size + 9), (3, 1), (size, 0),
                               (-5, -1), (2, -2), (-100, 3), (1, size + 1), (-2, size - 1)):
                found = search(text, start, end)
                calls.setdefault((name, method, text, start, end),
                                 None if found is None else [found.start(), found.end()])
    lines: dict[str, int] = {}
    rows = []
    for (name, method, text, start, end), result in calls.items():
        rows.append([name, method, lines.setdefault(text, len(lines)), start, end, result])
    return {"lines": list(lines), "calls": rows}


def generate(writer: Writer, *, large: int = RANDOM_LARGE, scanner_random: int = RANDOM_SCANNER) -> None:
    fuzz = fuzz_corpus()
    inputs = fuzz + random_strings(1, scanner_random, SENTENCE_PIECES + LINK_PIECES + tuple(fuzz[:80]))
    writer.lines("scanners/inputs.jsonl.gz", inputs)
    for name, (_, scan) in SCANNERS.items():
        writer.lines(f"scanners/{name}.jsonl.gz", (scan(text) for text in inputs))
    windowed = _windowed(fuzz + inputs[len(fuzz):len(fuzz) + 2000])
    writer.json("scanners/windowed.json.gz", {
        "note": ("calls [pattern, method, line index, pos, endpos, [start, end] or null] that sentences() and "
                 "_unfinished make, and every window of a few short texts"), **windowed}, len(windowed["calls"]))
    writer.json("scanners/index.json", {
        "note": "scanners/inputs.jsonl.gz holds the inputs; each scanner file holds one result per input, in order",
        "fuzz_inputs": len(fuzz), "random_inputs": len(inputs) - len(fuzz), "random_seed": 1,
        "scanners": {name: description for name, (description, _) in SCANNERS.items()}}, len(SCANNERS))
    writer.lines("corpora/safe_text.jsonl.gz", _safe_text(fuzz, large))
    writer.lines("corpora/sentences.jsonl.gz", _sentences(large))
    patterns = _patterns()
    writer.json("corpora/patterns.json", {
        "note": ("every R0 function of each invented item in internal/realdata/testdata/patterns.jsonl, as "
                 "tools/oracle/real.py records it"), "cases": patterns}, len(patterns))


def _patterns() -> list[dict]:
    from oracle.real import text_outputs
    items = [json.loads(line) for line in PATTERNS.read_text(encoding="ascii").splitlines()]
    return [{"id": item["id"], "text": item["text"], "outputs": text_outputs(item["text"])} for item in items]


def _safe_text(fuzz: list[str], count: int) -> Iterator[list]:
    """[input, text, [mentions, links, controls, brackets]] for the fuzz strings, then the random ones."""
    for text in fuzz + random_strings(2, count, LINK_PIECES + SENTENCE_PIECES + tuple(fuzz[:80]), longest=10):
        safe, counts = units.safe_text(text)
        yield [text, safe, [counts["mentions"], counts["links"], counts["controls"], counts["brackets"]]]


def _sentences(count: int) -> Iterator[list]:
    """[input, sentences(input), sentences(input, lines=True), or null where that is the same list]."""
    for text in random_strings(3, count, SENTENCE_PIECES, longest=12):
        split, lines = units.sentences(text), units.sentences(text, lines=True)
        yield [text, split, None if lines == split else lines]
