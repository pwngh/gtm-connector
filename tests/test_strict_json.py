"""One parser for every document the connector reads."""

from decimal import Decimal
import unittest

from gtm_connector import strict_json


class StrictJSONTests(unittest.TestCase):
    def test_accepts_ordinary_documents_and_a_caller_float_parser(self):
        self.assertEqual(strict_json.loads(b'{"a": [1, 2.5, "x", null, true]}'), {"a": [1, 2.5, "x", None, True]})
        self.assertEqual(strict_json.loads('{"a": 1.10}', parse_float=Decimal), {"a": Decimal("1.10")})

    def test_refuses_duplicate_keys_non_finite_numbers_and_bad_utf8(self):
        for document in ('{"a": 1, "a": 2}', '{"a": {"b": 1, "b": 1}}', '{"a": NaN}', '{"a": Infinity}',
                         '{"a": -Infinity}', '{"a": 1e999}', '{"a": -1e999}', b'{"a": "\xff"}', "{"):
            with self.subTest(document=document), self.assertRaises(ValueError):
                strict_json.loads(document)

    def test_refuses_a_lone_surrogate_however_it_is_written(self):
        # Escaped, as raw surrogate bytes, as a raw character, as a key, nested, and in another encoding.
        for document in (b'{"a": "\\ud800"}', b'["\\udfff"]', b'"\\ud800"', b'{"\\ud800": 1}', b'{"a": [{"b": "x\\ud83d"}]}',
                         b'{"a": "\xed\xa0\x80"}', '{"a": "\ud800"}', '{"a": 1}'.encode("utf-16")):
            with self.subTest(document=document), self.assertRaises(ValueError):
                strict_json.loads(document)
        # A whole pair is one character, and a byte order mark before a document is not content.
        self.assertEqual(strict_json.loads(b'{"a": "\\ud83d\\ude80"}'), {"a": "\U0001f680"})
        self.assertEqual(strict_json.loads(b'\xef\xbb\xbf{"a": 1}'), {"a": 1})


if __name__ == "__main__":
    unittest.main()
