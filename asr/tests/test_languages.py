"""Language aliases: the configured value is a name, a code or a native spelling."""

import unittest

from asr.languages import (
    QWEN_LANGUAGES,
    WHISPER_CODES,
    canonical_language,
    is_no_space_language,
    normalize_text,
    primary_language,
    qwen_language,
    whisper_language,
)


class CanonicalLanguageTest(unittest.TestCase):
    def test_display_names_round_trip(self):
        for name in QWEN_LANGUAGES:
            self.assertEqual(canonical_language(name), name)

    def test_display_names_are_case_insensitive(self):
        self.assertEqual(canonical_language("japanese"), "Japanese")
        self.assertEqual(canonical_language("ENGLISH"), "English")

    def test_iso_codes_resolve(self):
        self.assertEqual(canonical_language("zh"), "Chinese")
        self.assertEqual(canonical_language("ja"), "Japanese")
        self.assertEqual(canonical_language("yue"), "Cantonese")

    def test_native_names_resolve(self):
        self.assertEqual(canonical_language("中文"), "Chinese")
        self.assertEqual(canonical_language("粤语"), "Cantonese")
        self.assertEqual(canonical_language("日本語"), "Japanese")
        self.assertEqual(canonical_language("한국어"), "Korean")
        self.assertEqual(canonical_language("英语"), "English")

    def test_auto_values_mean_detect(self):
        for value in ("", "  ", "auto", "AUTO", "automatic", "none", "null", "-"):
            with self.subTest(value=value):
                self.assertEqual(canonical_language(value), "")
        self.assertEqual(canonical_language(None), "")

    def test_unknown_value_is_auto_not_an_error(self):
        # The service warns and continues; failing the job would be worse.
        self.assertEqual(canonical_language("Klingon"), "")
        self.assertEqual(canonical_language("xx-YY"), "")


class EngineLanguageTest(unittest.TestCase):
    def test_qwen_takes_the_display_name(self):
        self.assertEqual(qwen_language("zh"), "Chinese")
        self.assertEqual(qwen_language("auto"), "")

    def test_whisper_takes_the_code(self):
        self.assertEqual(whisper_language("Chinese"), "zh")
        self.assertEqual(whisper_language("zh"), "zh")
        self.assertEqual(whisper_language("Japanese"), "ja")

    def test_whisper_spelling_of_the_irregular_codes(self):
        self.assertEqual(whisper_language("Persian"), "fa")
        self.assertEqual(whisper_language("Greek"), "el")
        self.assertEqual(whisper_language("Filipino"), "tl")
        self.assertEqual(whisper_language("Cantonese"), "yue")

    def test_every_supported_language_has_a_whisper_code(self):
        missing = [name for name in QWEN_LANGUAGES if name not in WHISPER_CODES]
        self.assertEqual(missing, [])
        self.assertEqual(whisper_language("Klingon"), "")


class NoSpaceLanguageTest(unittest.TestCase):
    def test_wide_script_languages_are_detected(self):
        for value in ("Chinese", "zh", "zh-CN", "zh-hant", "Cantonese", "yue", "Japanese", "ja"):
            with self.subTest(value=value):
                self.assertTrue(is_no_space_language(value))

    def test_korean_keeps_its_spaces(self):
        # Korean is written in a wide script but separates words with spaces.
        self.assertFalse(is_no_space_language("Korean"))
        self.assertFalse(is_no_space_language("ko"))

    def test_other_languages_and_empty_are_not(self):
        for value in ("", None, "English", "en", "Russian"):
            with self.subTest(value=value):
                self.assertFalse(is_no_space_language(value))

    def test_a_mixed_list_uses_its_first_language(self):
        self.assertEqual(primary_language("Chinese,English"), "Chinese")
        self.assertTrue(is_no_space_language("Chinese,English"))
        self.assertFalse(is_no_space_language("English,Chinese"))


class NormalizeTextTest(unittest.TestCase):
    def test_whitespace_collapses(self):
        self.assertEqual(normalize_text("  hello \n world \t"), "hello world")

    def test_spaces_between_wide_characters_are_dropped(self):
        self.assertEqual(normalize_text("你 好 世 界", "zh"), "你好世界")

    def test_latin_spaces_survive_in_a_wide_script_language(self):
        self.assertEqual(normalize_text("你好 world", "Chinese"), "你好 world")
        self.assertEqual(normalize_text("hello world", "zh"), "hello world")

    def test_latin_spaces_survive_for_other_languages(self):
        self.assertEqual(normalize_text("hello  world", "English"), "hello world")
        self.assertEqual(normalize_text("hello world", "Korean"), "hello world")


if __name__ == "__main__":
    unittest.main()
