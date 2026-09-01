#!/usr/bin/env python3
import sys

import cv2
from rapidocr import RapidOCR

MIN_CJK = 2
MIN_CONFIDENCE = 0.8
REQUIRED_REGIONS = 2
REGION_COUNT = 3
START_SECONDS = 30 * 60
SAMPLE_COUNT = 30

# ponytail: CJK is not language detection; add a calibrated classifier if persistent Japanese text causes false skips.


def cjk_count(text):
    return sum("\u3400" <= char <= "\u9fff" or "\uf900" <= char <= "\ufaff" for char in text)


def main():
    video = cv2.VideoCapture(sys.argv[1])
    frame_count = int(video.get(cv2.CAP_PROP_FRAME_COUNT))
    fps = video.get(cv2.CAP_PROP_FPS)
    if not video.isOpened() or frame_count <= 0 or fps <= 0:
        raise RuntimeError(f"cannot open video: {sys.argv[1]}")

    ocr = RapidOCR()
    regions_with_hits = set()
    sampled = 0
    if frame_count > START_SECONDS * fps:
        start_frame = int(START_SECONDS * fps)
    else:
        start_frame = frame_count // 2
    remaining_frames = frame_count - start_frame
    frame_indexes = [
        start_frame + int(remaining_frames * (index + 0.5) / SAMPLE_COUNT)
        for index in range(SAMPLE_COUNT)
    ]
    for sample_index, frame_index in enumerate(frame_indexes):
        video.set(cv2.CAP_PROP_POS_FRAMES, frame_index)
        ok, frame = video.read()
        if not ok:
            continue
        sampled += 1
        result = ocr(frame[int(frame.shape[0] * 0.6) :])
        if any(
            score >= MIN_CONFIDENCE and cjk_count(text) >= MIN_CJK
            for text, score in zip(result.txts or [], result.scores or [])
        ):
            regions_with_hits.add(sample_index * REGION_COUNT // SAMPLE_COUNT)
            if len(regions_with_hits) >= REQUIRED_REGIONS:
                break

    video.release()
    if sampled == 0:
        raise RuntimeError(f"cannot sample video: {sys.argv[1]}")
    print(str(len(regions_with_hits) >= REQUIRED_REGIONS).lower())


if __name__ == "__main__":
    main()
