"""Cue building and SRT output, checked against the shapes the rest of the repo reads."""

import os
import tempfile
import unittest
from pathlib import Path

from asr import srt
from asr.srt import Segment


def words(*texts, step=0.4, duration=0.3):
    """Segments a fixed apart, so gap and duration rules are easy to aim at."""
    return [
        Segment(text=text, start=index * step, end=index * step + duration)
        for index, text in enumerate(texts)
    ]


class TimestampTest(unittest.TestCase):
    def test_formats_hours_minutes_seconds_and_milliseconds(self):
        self.assertEqual(srt.format_timestamp(0), "00:00:00,000")
        self.assertEqual(srt.format_timestamp(1.5), "00:00:01,500")
        self.assertEqual(srt.format_timestamp(61.25), "00:01:01,250")
        self.assertEqual(srt.format_timestamp(3661.234), "01:01:01,234")
        self.assertEqual(srt.format_timestamp(360000), "100:00:00,000")

    def test_rounds_to_the_nearest_millisecond(self):
        self.assertEqual(srt.format_timestamp(1.9996), "00:00:02,000")
        self.assertEqual(srt.format_timestamp(0.0004), "00:00:00,000")

    def test_a_negative_time_is_clamped(self):
        self.assertEqual(srt.format_timestamp(-2), "00:00:00,000")


class SanitizeTest(unittest.TestCase):
    def test_empty_cues_are_dropped_and_text_is_collapsed(self):
        cues = srt.sanitize([Segment("  hello \n world ", 0, 1), Segment("", 1, 2), Segment("   ", 2, 3)])
        self.assertEqual([cue.text for cue in cues], ["hello world"])

    def test_overlap_is_removed_so_the_timeline_moves_forward(self):
        cues = srt.sanitize([Segment("one", 0.0, 5.0), Segment("two", 1.0, 2.0), Segment("three", 5.5, 6.0)])
        self.assertEqual([(cue.start, cue.end) for cue in cues], [(0.0, 5.0), (5.0, 5.0), (5.5, 6.0)])

    def test_a_negative_start_is_clamped_and_end_never_precedes_start(self):
        cues = srt.sanitize([Segment("one", -3.0, -1.0)])
        self.assertEqual([(cue.start, cue.end) for cue in cues], [(0.0, 0.0)])


class FormatTest(unittest.TestCase):
    def test_block_shape(self):
        text = srt.format_srt([Segment("hello", 0.0, 1.5), Segment("world", 1.5, 3.0)])
        self.assertEqual(
            text,
            "1\n00:00:00,000 --> 00:00:01,500\nhello\n\n"
            "2\n00:00:01,500 --> 00:00:03,000\nworld\n",
        )

    def test_nothing_to_write_is_empty_text(self):
        self.assertEqual(srt.format_srt([]), "")
        self.assertEqual(srt.format_srt([Segment("   ", 0, 1)]), "")

    def test_the_blocks_are_what_the_translation_step_parses(self):
        # scripts/llm_subtrans_translate.py splits the file on a blank line and
        # treats everything from the third line of a block as the cue text.
        text = srt.format_srt([Segment("你好", 0.0, 1.0), Segment("world", 1.0, 2.0)])
        blocks = [block for block in text.split("\n\n") if block.strip()]
        self.assertEqual(len(blocks), 2)
        for block in blocks:
            lines = block.split("\n")
            self.assertGreaterEqual(len(lines), 3)
            self.assertTrue(lines[0].isdigit())
            self.assertIn("-->", lines[1])


class WriteTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.path = Path(self.directory.name) / "movie.srt"

    def test_writes_utf8_with_lf_and_leaves_no_temporary_file(self):
        written = srt.write_srt(self.path, [Segment("你好，世界", 0.0, 1.0)])
        self.assertEqual(written, self.path)
        raw = self.path.read_bytes()
        self.assertEqual(raw.decode("utf-8"), srt.format_srt([Segment("你好，世界", 0.0, 1.0)]))
        self.assertNotIn(b"\r\n", raw)
        self.assertEqual(sorted(os.listdir(self.directory.name)), ["movie.srt"])

    def test_an_existing_file_is_replaced_in_place(self):
        self.path.write_text("stale", encoding="utf-8")
        srt.write_srt(self.path, [Segment("fresh", 0.0, 1.0)])
        self.assertIn("fresh", self.path.read_text(encoding="utf-8"))

    def test_no_cues_writes_no_file(self):
        self.assertIsNone(srt.write_srt(self.path, []))
        self.assertFalse(self.path.exists())


class GroupingTest(unittest.TestCase):
    def test_a_pause_starts_a_new_cue(self):
        cues = srt.group_segments([Segment("one", 0.0, 0.5), Segment("two", 2.0, 2.5)], language="English")
        self.assertEqual([cue.text for cue in cues], ["one", "two"])

    def test_a_sentence_end_starts_a_new_cue(self):
        cues = srt.group_segments(words("Hello", "world.", "Again", "here."), language="English")
        self.assertEqual([cue.text for cue in cues], ["Hello world.", "Again here."])

    def test_a_cjk_sentence_end_starts_a_new_cue(self):
        cues = srt.group_segments(words("你", "好", "。", "再", "见", "。"), language="Chinese")
        self.assertEqual([cue.text for cue in cues], ["你好。", "再见。"])

    def test_a_long_cue_is_split_by_duration(self):
        cues = srt.group_segments(
            [Segment("one", 0.0, 1.0), Segment("two", 5.0, 6.5)],
            language="English",
            max_duration_sec=6.0,
        )
        self.assertEqual([cue.text for cue in cues], ["one", "two"])

    def test_english_words_are_counted_against_the_word_limit(self):
        cues = srt.group_segments(words("a", "b", "c"), language="English", max_words=2)
        self.assertEqual([cue.text for cue in cues], ["a b", "c"])

    def test_the_word_limit_does_not_apply_to_a_wide_script_language(self):
        cues = srt.group_segments(words("你", "好", "世", "界"), language="Chinese", max_words=2)
        self.assertEqual([cue.text for cue in cues], ["你好世界"])

    def test_wide_characters_count_double_against_the_width_limit(self):
        cues = srt.group_segments(words(*"一二三四五六"), language="Chinese", max_chars=8)
        self.assertEqual([cue.text for cue in cues], ["一二三四", "五六"])

    def test_a_clause_ends_a_cue_once_it_is_half_full(self):
        cues = srt.group_segments(
            words("Hello", "there,", "again"), language="English", max_chars=20
        )
        self.assertEqual([cue.text for cue in cues], ["Hello there,", "again"])

    def test_a_width_break_balances_a_short_trailing_cue(self):
        cues = srt.group_segments(
            words("aaa", "bbb", "ccc", "ddd"), language="English", max_words=100, max_chars=13
        )
        self.assertEqual([cue.text for cue in cues], ["aaa bbb", "ccc ddd"])

    def test_a_width_break_keeps_the_first_cue_whole(self):
        cues = srt.group_segments(
            words("aaa", "bbb", "ccc"), language="English", max_words=100, max_chars=8
        )
        self.assertEqual([cue.text for cue in cues], ["aaa bbb", "ccc"])

    def test_cues_keep_the_first_and_last_timestamp_of_their_words(self):
        cues = srt.group_segments(
            [Segment("aaa", 0.0, 0.5), Segment("bbb", 2.0, 2.7)], language="English"
        )
        self.assertEqual([(cue.start, cue.end) for cue in cues], [(0.0, 0.5), (2.0, 2.7)])

    def test_nothing_in_gives_nothing_out(self):
        self.assertEqual(srt.group_segments([], language="English"), [])


class JoinTest(unittest.TestCase):
    def test_english_words_are_separated(self):
        cues = srt.group_segments(words("hello", "world"), language="English")
        self.assertEqual(cues[0].text, "hello world")

    def test_wide_characters_are_joined_without_a_separator(self):
        cues = srt.group_segments(words("你", "好"), language="Chinese")
        self.assertEqual(cues[0].text, "你好")

    def test_latin_words_keep_their_separator_inside_a_wide_script_cue(self):
        cues = srt.group_segments(words("你", "好", "hello", "world"), language="Chinese")
        self.assertEqual(cues[0].text, "你好hello world")

    def test_no_space_before_closing_punctuation_and_none_after_a_bracket(self):
        cues = srt.group_segments(words("hello", ",", "world"), language="English", max_words=100)
        self.assertEqual(cues[0].text, "hello, world")

    def test_the_join_survives_the_final_normalization(self):
        cues = srt.group_segments(words("你", "好", "。"), language="Chinese")
        self.assertEqual(cues[0].text, "你好。")


class PunctuationRestorationTest(unittest.TestCase):
    def test_trailing_punctuation_lands_on_the_last_word(self):
        segments = words("你", "好", "世", "界")
        restored = srt.restore_punctuation(segments, "你好，世界。")
        self.assertEqual([segment.text for segment in restored], ["你", "好，", "世", "界。"])

    def test_leading_punctuation_lands_on_the_first_word(self):
        segments = words("你", "好")
        restored = srt.restore_punctuation(segments, "“你好”")
        self.assertEqual([segment.text for segment in restored], ["“你", "好”"])

    def test_matching_ignores_case_and_whitespace(self):
        segments = words("hello", "world")
        restored = srt.restore_punctuation(segments, "Hello,  world!")
        self.assertEqual([segment.text for segment in restored], ["hello,", "world!"])

    def test_a_mismatch_keeps_the_segments_as_they_were(self):
        segments = words("你", "好")
        restored = srt.restore_punctuation(segments, "再见。")
        self.assertEqual([segment.text for segment in restored], ["你", "好"])

    def test_a_transcript_that_continues_past_the_segments_is_accepted(self):
        # The aligner's last item can end before the transcript does; the words
        # that matched still get their punctuation.
        segments = [Segment(text="你", start=0.0, end=0.2), Segment(text="好", start=0.2, end=0.4)]
        restored = srt.restore_punctuation(segments, "你好，世界。")
        self.assertEqual([segment.text for segment in restored], ["你", "好，"])

    def test_restoration_is_used_when_the_transcript_is_given(self):
        segments = [Segment(text="你", start=0.0, end=0.2), Segment(text="好", start=0.2, end=0.4)]
        cues = srt.group_segments(segments, language="Chinese", text="你好。")
        self.assertEqual([cue.text for cue in cues], ["你好。"])


class DisplayWidthTest(unittest.TestCase):
    def test_wide_characters_count_double(self):
        self.assertEqual(srt._display_width("abc"), 3)
        self.assertEqual(srt._display_width("你好"), 4)
        self.assertEqual(srt._display_width("你a"), 3)


if __name__ == "__main__":
    unittest.main()
