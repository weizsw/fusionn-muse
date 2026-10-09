import contextlib
import importlib.util
import io
import pathlib
import sys
import types
import unittest
from unittest import mock


SCRIPT = pathlib.Path(__file__).with_name("detect_hard_sub.py")


class FakeFrame:
    shape = (100, 100, 3)

    def __init__(self, second):
        self.second = second

    def __getitem__(self, _key):
        return self


class FakeVideo:
    def __init__(self, duration):
        self.duration = duration
        self.second = 0

    def isOpened(self):
        return True

    def get(self, prop):
        return self.duration if prop == 1 else 1

    def set(self, _prop, value):
        self.second = value

    def read(self):
        return True, FakeFrame(self.second)

    def release(self):
        pass


def run_detector(duration, recognize_text):
    cv2 = types.ModuleType("cv2")
    cv2.CAP_PROP_FRAME_COUNT = 1
    cv2.CAP_PROP_FPS = 2
    cv2.CAP_PROP_POS_FRAMES = 3
    cv2.VideoCapture = lambda _path: FakeVideo(duration)

    class FakeOCR:
        def __call__(self, frame):
            results = recognize_text(frame.second)
            return types.SimpleNamespace(
                txts=[text for text, _score in results],
                scores=[score for _text, score in results],
            )

    rapidocr = types.ModuleType("rapidocr")
    rapidocr.RapidOCR = FakeOCR
    spec = importlib.util.spec_from_file_location("detect_hard_sub", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    output = io.StringIO()
    with mock.patch.dict(sys.modules, {"cv2": cv2, "rapidocr": rapidocr}), mock.patch.object(
        sys, "argv", [str(SCRIPT), "movie.mp4"]
    ), contextlib.redirect_stdout(output):
        spec.loader.exec_module(module)
        module.main()
    return output.getvalue().strip()


class DetectHardSubtitleTest(unittest.TestCase):
    def test_ignores_text_in_first_thirty_minutes_of_long_video(self):
        self.assertEqual(
            run_detector(
                2 * 60 * 60,
                lambda second: [("中文字幕", 0.99)] if second < 30 * 60 else [],
            ),
            "false",
        )

    def test_samples_only_second_half_of_short_video(self):
        self.assertEqual(
            run_detector(
                20 * 60,
                lambda second: [("中文字幕", 0.99)] if second < 10 * 60 else [],
            ),
            "false",
        )

    def test_requires_text_in_multiple_time_regions(self):
        self.assertEqual(
            run_detector(
                2 * 60 * 60,
                lambda second: [("中文字幕", 0.99)]
                if 30 * 60 <= second < 60 * 60
                else [],
            ),
            "false",
        )

    def test_detects_text_in_separated_time_regions(self):
        self.assertEqual(
            run_detector(
                2 * 60 * 60,
                lambda second: [("中文字幕", 0.99)]
                if 30 * 60 <= second < 45 * 60 or second >= 90 * 60
                else [],
            ),
            "true",
        )

    def test_snos384_japanese_ocr_does_not_qualify_as_chinese_coverage(self):
        # Minimal real OCR evidence at samples 7 and 24 of this 11131-second video.
        results = {
            4132: [("1pt払ったら組み立ててもらえろとが", 0.9331)],
            9420: [("中平", 0.99435)],
        }
        self.assertEqual(run_detector(11131, lambda second: results.get(second, [])), "false")

    def test_rejects_japanese_with_hiragana_katakana_or_halfwidth_kana(self):
        for text in ("組み立て", "字幕テスト", "字幕ﾃｽﾄ"):
            with self.subTest(text=text):
                self.assertEqual(
                    run_detector(2 * 60 * 60, lambda _second: [(text, 0.99)]),
                    "false",
                )

    def test_detects_chinese_alongside_japanese_text(self):
        for text in ("请坐下来", "請坐下來"):
            with self.subTest(text=text):
                self.assertEqual(
                    run_detector(
                        2 * 60 * 60,
                        lambda _second: [("組み立て", 0.99), (text, 0.99)],
                    ),
                    "true",
                )

    def test_low_confidence_chinese_does_not_qualify(self):
        self.assertEqual(
            run_detector(2 * 60 * 60, lambda _second: [("中文字幕", 0.79)]),
            "false",
        )

    def test_no_text_does_not_qualify(self):
        self.assertEqual(run_detector(2 * 60 * 60, lambda _second: []), "false")


if __name__ == "__main__":
    unittest.main()
