"""Audio extraction: the WAV the Qwen route hands to upstream."""

import tempfile
import unittest
from pathlib import Path

from asr.media import MediaError, extract_wav, ffmpeg_available, wav_duration
from asr.tests.support import fake_ffmpeg, write_wav


class ExtractWavTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.source = self.root / "movie.mp4"
        self.source.write_bytes(b"not really a video")
        self.target = self.root / "movie.wav"

    def test_decodes_to_the_target_file(self):
        ffmpeg = fake_ffmpeg(self.root, seconds=2.0)
        written = extract_wav(self.source, self.target, ffmpeg=str(ffmpeg))
        self.assertEqual(written, self.target)
        self.assertAlmostEqual(wav_duration(self.target), 2.0, places=3)

    def test_a_failing_ffmpeg_reports_its_last_words(self):
        ffmpeg = fake_ffmpeg(self.root, exit_code=1, stderr="Invalid data found when processing input")
        with self.assertRaises(MediaError) as caught:
            extract_wav(self.source, self.target, ffmpeg=str(ffmpeg))
        message = str(caught.exception)
        self.assertIn("exited with 1", message)
        self.assertIn("Invalid data found", message)

    def test_a_missing_ffmpeg_says_so(self):
        with self.assertRaises(MediaError) as caught:
            extract_wav(self.source, self.target, ffmpeg=str(self.root / "nope"))
        self.assertIn("ffmpeg", str(caught.exception))

    def test_a_timeout_is_reported(self):
        ffmpeg = fake_ffmpeg(self.root, seconds=0.0, exit_code=0)
        # A zero-second budget cannot finish; the error names the limit.
        with self.assertRaises(MediaError) as caught:
            extract_wav(self.source, self.target, ffmpeg=str(ffmpeg), timeout_sec=0)
        self.assertIn("did not finish", str(caught.exception))

    def test_success_without_an_output_file_is_a_failure(self):
        ffmpeg = fake_ffmpeg(self.root, exit_code=0)
        target = self.root / "nested" / "movie.wav"
        with self.assertRaises(MediaError):
            extract_wav(self.source, target, ffmpeg=str(ffmpeg))


class DurationTest(unittest.TestCase):
    def test_reads_the_real_duration(self):
        path = write_wav(self.root_path() / "two.wav", seconds=2.5)
        self.assertAlmostEqual(wav_duration(path), 2.5, places=3)

    def root_path(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        return Path(directory.name)


class AvailabilityTest(unittest.TestCase):
    def test_a_path_is_checked_as_a_file(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.assertFalse(ffmpeg_available(str(Path(directory.name) / "ffmpeg")))

    def test_the_configured_binary_is_found_through_path(self):
        self.assertTrue(ffmpeg_available("python3") or ffmpeg_available("python"))

    def test_an_unknown_command_is_not_available(self):
        self.assertFalse(ffmpeg_available("muse-definitely-not-installed"))


if __name__ == "__main__":
    unittest.main()
