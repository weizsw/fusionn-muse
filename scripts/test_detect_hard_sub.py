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


def run_detector(duration, has_text):
    cv2 = types.ModuleType("cv2")
    cv2.CAP_PROP_FRAME_COUNT = 1
    cv2.CAP_PROP_FPS = 2
    cv2.CAP_PROP_POS_FRAMES = 3
    cv2.VideoCapture = lambda _path: FakeVideo(duration)

    class FakeOCR:
        def __call__(self, frame):
            present = has_text(frame.second)
            return types.SimpleNamespace(
                txts=["中文字幕"] if present else [],
                scores=[0.99] if present else [],
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
        self.assertEqual(run_detector(2 * 60 * 60, lambda second: second < 30 * 60), "false")

    def test_samples_only_second_half_of_short_video(self):
        self.assertEqual(run_detector(20 * 60, lambda second: second < 10 * 60), "false")

    def test_requires_text_in_multiple_time_regions(self):
        self.assertEqual(
            run_detector(2 * 60 * 60, lambda second: 30 * 60 <= second < 60 * 60),
            "false",
        )

    def test_detects_text_in_separated_time_regions(self):
        self.assertEqual(
            run_detector(
                2 * 60 * 60,
                lambda second: 30 * 60 <= second < 45 * 60 or second >= 90 * 60,
            ),
            "true",
        )


if __name__ == "__main__":
    unittest.main()
