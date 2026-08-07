#!/usr/bin/env python3
import sys

import cv2
from rapidocr import RapidOCR

MIN_CJK = 2
MIN_CONFIDENCE = 0.8
REQUIRED_HITS = 2


def cjk_count(text):
    return sum("\u3400" <= char <= "\u9fff" or "\uf900" <= char <= "\ufaff" for char in text)


def main():
    video = cv2.VideoCapture(sys.argv[1])
    frame_count = int(video.get(cv2.CAP_PROP_FRAME_COUNT))
    fps = video.get(cv2.CAP_PROP_FPS)
    if not video.isOpened() or frame_count <= 0 or fps <= 0:
        raise RuntimeError(f"cannot open video: {sys.argv[1]}")

    ocr = RapidOCR()
    hits = 0
    sampled = 0
    frame_indexes = [
        int(second * fps)
        for second in range(10, 301, 10)
        if second * fps < frame_count
    ]
    frame_indexes.extend(int(frame_count * percent) for percent in (0.25, 0.5, 0.75))
    for frame_index in frame_indexes:
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
            hits += 1
            if hits >= REQUIRED_HITS:
                break

    video.release()
    if sampled == 0:
        raise RuntimeError(f"cannot sample video: {sys.argv[1]}")
    print(str(hits >= REQUIRED_HITS).lower())


if __name__ == "__main__":
    main()
