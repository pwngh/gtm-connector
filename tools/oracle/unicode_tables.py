"""Every code point under each predicate and class the evidence rules use, from this interpreter."""

import re
import unicodedata

from oracle.output import Writer, ranges


LAST = 0x10FFFF
EVERY = "".join(map(chr, range(LAST + 1)))
# Code points whose combining class is 0 in Unicode 16.0.0 and not 0 in 17.0.0. Measured once against
# golang.org/x/text/unicode/norm at Unicode 17.0.0 (v0.43.0); all 34 are unassigned in Unicode 16.
CLASS_CHANGED = (
    *range(0x1ACF, 0x1ADE), *range(0x1AE0, 0x1AEC), 0x10EFA, 0x10EFB, 0x1E6E3, 0x1E6E6, 0x1E6EE, 0x1E6EF, 0x1E6F5)
# The patterns a regex class is taken from, as the package writes them.
REGEX_CLASSES = {"\\s": r"\s", "\\d": r"\d", "\\w": r"\w"}
# What the package's IGNORECASE patterns hold: [a-z] and the letters of "mailto:" and "www.".
IGNORECASE = {"[a-z]": "[a-z]", **{letter: letter for letter in "abcdefghijklmnopqrstuvwxyz"}}
# Whole strings whose case depends on context: a final sigma, a cased letter around case-ignorable marks, and
# letters that lower to more than one code point.
CASE_STRINGS = (
    "Σ", "ΟΣ", "ΟΣ ", "ΟΣ.", "ΟΣΟ", "ΟΣ'", "'Σ",
    "Ο'Σ", "ΌΣ", "ΟΣ́", "ΟΣ́Ο", "AΣ", "1Σ",
    "ΣΣ", "ΟΣΣ", "a­Σ", "İ", "İSTANBUL", "İ", "ẞ", "K",
    "ſ", "ǅ", "Ǆǅǆ", "U.S.C.", "U.S.c.", "e.g.", "E.G.", "É.U.", "A.1.", "1.2.",
    "Fig.", "Ⅰ.Ⅱ.", "Ⓐ.Ⓑ.", "Α.Β.", "ʰ.", "ABCͅ", "ᾈ", "ᾼ",
    "", " ", ".", "a", "A", "aA", "Aa", "ß", "ŉ", "\U00010400", "\U0001e900")


def _probe_sigma(code: int) -> tuple[bool, bool]:
    """Whether a code point is case-ignorable, and whether it is cased, as str.lower reads it before a sigma."""
    character = chr(code)
    after_cased = ("A" + character + "Σ").lower()[-1] == "ς"
    before_end = ("AΣ" + character).lower()[1] == "ς"
    ignorable = after_cased and before_end
    return ignorable, after_cased and not ignorable


def _class_runs() -> list[list[int]]:
    runs: list[list[int]] = []
    for code in range(LAST + 1):
        value = unicodedata.combining(chr(code))
        if value and runs and runs[-1][1] == code - 1 and runs[-1][2] == value:
            runs[-1][1] = code
        elif value:
            runs.append([code, code, value])
    return runs


def _category_runs() -> list[list]:
    runs: list[list] = []
    for code in range(LAST + 1):
        value = unicodedata.category(chr(code))
        if runs and runs[-1][2] == value:
            runs[-1][1] = code
        else:
            runs.append([code, code, value])
    return runs


def _matches(pattern: str, flags: int = 0) -> list[list[int]]:
    hits = bytearray(LAST + 1)
    for match in re.finditer(pattern, EVERY, flags):
        hits[match.start()] = 1
    return ranges(map(bool, hits))


def generate(writer: Writer) -> None:
    writer.json("unicode/categories.json", {"note": "unicodedata.category of every code point, as runs",
                                            "runs": _category_runs()}, LAST + 1)
    # A character predicate applies to one character. Whole-string islower and isupper follow from these with the
    # titlecase set: no lower or title character, and at least one upper one, for isupper.
    title = [character.istitle() and not character.isupper() for character in EVERY]
    predicates = {
        "isspace": ranges(character.isspace() for character in EVERY),
        "isdigit": ranges(character.isdigit() for character in EVERY),
        "isdecimal": ranges(character.isdecimal() for character in EVERY),
        "isprintable": ranges(character.isprintable() for character in EVERY),
        "islower": ranges(character.islower() for character in EVERY),
        "isupper": ranges(character.isupper() for character in EVERY),
        "istitle_only": ranges(title),
        "isalpha": ranges(character.isalpha() for character in EVERY),
        "isascii": [[0, 127]],
    }
    # str.split() and str.strip() with no argument use the same set as isspace.
    split = ranges(len(("a" + character + "b").split()) == 2 for character in EVERY)
    strip = ranges(not (" " + character).strip() for character in EVERY)
    if split != predicates["isspace"] or strip != predicates["isspace"]:
        raise AssertionError("str.split or str.strip differs from str.isspace")
    writer.json("unicode/predicates.json", {"note": "str methods on one character, as [first, last] runs",
                                            **predicates}, len(predicates))
    classes = {name: _matches(pattern) for name, pattern in REGEX_CLASSES.items()}
    ignorecase = {name: _matches(pattern, re.IGNORECASE) for name, pattern in IGNORECASE.items()}
    writer.json("unicode/regex_classes.json", {
        "note": "re on str patterns: the code points each class matches, and each IGNORECASE letter",
        "classes": classes, "ignorecase": ignorecase}, len(classes) + len(ignorecase))
    lower = [[code, [ord(part) for part in chr(code).lower()]] for code in range(LAST + 1)
             if chr(code).lower() != chr(code)]
    sigma = [_probe_sigma(code) for code in range(LAST + 1)]
    strings = [[text, text.lower(), text.islower(), text.isupper()] for text in CASE_STRINGS]
    writer.json("unicode/case.json", {
        "note": ("str.lower per code point where it changes; the sets that decide a final sigma (a cased one is "
                 "read only where it is not case-ignorable); whole strings with lower, islower, isupper"),
        "lower": lower,
        "final_sigma": {"case_ignorable": ranges(flag for flag, _ in sigma),
                        "cased": ranges(flag for _, flag in sigma)},
        "strings": strings}, len(lower) + len(strings))
    _nfc(writer)


def _nfc(writer: Writer) -> None:
    decompositions, single, compositions = [], [], []
    for code in range(LAST + 1):
        character = chr(code)
        if 0xD800 <= code <= 0xDFFF:
            continue
        decomposed = unicodedata.normalize("NFD", character)
        # Hangul syllables decompose by arithmetic, which the corpus below checks on a sample.
        if decomposed != character and not 0xAC00 <= code <= 0xD7A3:
            decompositions.append([code, [ord(part) for part in decomposed]])
        composed = unicodedata.normalize("NFC", character)
        if composed != character:
            single.append([code, [ord(part) for part in composed]])
        mapping = unicodedata.decomposition(character)
        if mapping and not mapping.startswith("<"):
            parts = [int(part, 16) for part in mapping.split()]
            if len(parts) == 2 and unicodedata.normalize("NFC", chr(parts[0]) + chr(parts[1])) == character:
                compositions.append([parts[0], parts[1], code])
    hangul = ["가", "각", "힣", "가", "각", "각", "힣"]
    writer.json("unicode/nfc.json", {
        "note": ("combining classes as [first, last, class] runs; NFD and NFC of each code point where they change "
                 "(Hangul syllables by arithmetic, sampled); canonical pairs that compose"),
        "combining_class": _class_runs(), "nfd": decompositions, "nfc": single, "compositions": compositions,
        "hangul": [[[ord(part) for part in text], [ord(part) for part in unicodedata.normalize("NFC", text)],
                    [ord(part) for part in unicodedata.normalize("NFD", text)]] for text in hangul]},
        len(decompositions) + len(single) + len(compositions))
    changed = set(CLASS_CHANGED)
    marks = sorted(changed | {code for code in range(LAST + 1) if unicodedata.combining(chr(code))})
    rows = []
    for first in marks:
        for second in marks:
            if first in changed or second in changed:
                text = "a" + chr(first) + chr(second)
                result = unicodedata.normalize("NFC", text)
                rows.append([first, second, None if result == text else [ord(part) for part in result]])
    writer.lines("unicode/nfc_mark_pairs.jsonl.gz", rows)
