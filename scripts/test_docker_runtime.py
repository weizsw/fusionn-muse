"""Image smoke tests: mount scripts and set MUSE_IMAGE_TEST=1 with --network none."""

import importlib.util
import os
import pathlib
import shutil
import subprocess
import sys
import unittest


@unittest.skipUnless(os.environ.get("MUSE_IMAGE_TEST") == "1", "Run explicitly in the Docker image")
class DockerRuntimeTest(unittest.TestCase):
    def test_modern_dependencies_without_legacy_runtime(self):
        for module in ["PySubtrans", "cv2", "rapidocr", "onnxruntime"]:
            with self.subTest(module=module):
                self.assertIsNotNone(importlib.util.find_spec(module))
        for module in ["openai", "faster_whisper", "ctranslate2", "av", "torch", "PySide6"]:
            with self.subTest(module=module):
                self.assertIsNone(importlib.util.find_spec(module))
        self.assertFalse(pathlib.Path("/app/videocaptioner").exists())
        for script in ["transcribe.py", "translate.py", "subtitle_processor.py"]:
            self.assertFalse(pathlib.Path("/app/scripts", script).exists())

    def test_media_tools_and_video_decoding(self):
        for tool in ["ffmpeg", "ffprobe", "bsdtar", "7z", "nrg2iso"]:
            with self.subTest(tool=tool):
                self.assertIsNotNone(shutil.which(tool))
        code = """
import cv2
import json
import pathlib
import subprocess
import tempfile
with tempfile.TemporaryDirectory() as root:
    video = str(pathlib.Path(root, 'test.mp4'))
    subprocess.run(['ffmpeg', '-v', 'error', '-f', 'lavfi', '-i',
                    'color=black:s=640x360:d=1', '-c:v', 'mpeg4', video], check=True)
    probe = subprocess.check_output(['ffprobe', '-v', 'error', '-show_streams', '-of', 'json', video])
    assert json.loads(probe)['streams']
    capture = cv2.VideoCapture(video)
    ok, frame = capture.read()
    capture.release()
    assert ok and frame.shape[:2] == (360, 640)
"""
        result = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_cpu_ocr_works_without_network_or_installed_fonts(self):
        code = """
import cv2
import numpy as np
from rapidocr import RapidOCR
cv2.setNumThreads(1)
image = np.full((240, 800, 3), 255, dtype=np.uint8)
cv2.putText(image, 'SAMPLE TEXT', (30, 150), cv2.FONT_HERSHEY_SIMPLEX, 2, (0, 0, 0), 3)
result = RapidOCR()(image)
assert result.txts and result.scores
"""
        result = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, timeout=90)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
