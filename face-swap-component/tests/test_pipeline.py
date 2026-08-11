from pathlib import Path
from types import SimpleNamespace

import pytest

from pipeline import MediaValidationError, VideoProcessor


class FakeCapture:
    def __init__(self, frames, cv2):
        self.frames = list(frames)
        self.cv2 = cv2
        self.index = 0
        self.released = False

    def isOpened(self):
        return True

    def get(self, prop):
        return {
            self.cv2.CAP_PROP_FRAME_WIDTH: 2,
            self.cv2.CAP_PROP_FRAME_HEIGHT: 2,
            self.cv2.CAP_PROP_FPS: 1,
            self.cv2.CAP_PROP_FRAME_COUNT: len(self.frames),
        }[prop]

    def read(self):
        if self.index >= len(self.frames):
            return False, None
        frame = self.frames[self.index]
        self.index += 1
        return True, frame

    def release(self):
        self.released = True


class FakeWriter:
    def __init__(self):
        self.frames = []
        self.released = False

    def isOpened(self):
        return True

    def write(self, frame):
        self.frames.append(frame)

    def release(self):
        self.released = True


class FakeCV2:
    CAP_PROP_FRAME_WIDTH = 1
    CAP_PROP_FRAME_HEIGHT = 2
    CAP_PROP_FPS = 3
    CAP_PROP_FRAME_COUNT = 4

    def __init__(self, frames):
        self.frames = frames
        self.writer = FakeWriter()

    def imread(self, path):
        return "source" if "source" in str(path) else "target"

    def VideoCapture(self, _):
        self.capture = FakeCapture(self.frames, self)
        return self.capture

    def VideoWriter(self, *_):
        return self.writer

    @staticmethod
    def VideoWriter_fourcc(*_):
        return 0

    @staticmethod
    def imwrite(*_):
        return True


class FakeAnalyzer:
    @staticmethod
    def get(frame):
        if frame == "source":
            return [SimpleNamespace(bbox=[0, 0, 2, 2])]
        return [SimpleNamespace(bbox=[0, 0, 1, 1])]


class FakeSwapper:
    @staticmethod
    def get(frame, *_args, **_kwargs):
        return f"swapped-{frame}"


class StubProcessor(VideoProcessor):
    @staticmethod
    def _finalize_video(_video_path, _audio_source, output_path):
        output_path.write_bytes(b"result")


def test_video_frames_are_written_in_order_without_batching(tmp_path: Path):
    cv2 = FakeCV2(["frame-1", "frame-2", "frame-3"])
    processor = StubProcessor(cv2, FakeAnalyzer(), FakeSwapper())
    result = processor.process_video(
        tmp_path / "source.jpg",
        tmp_path / "target.mp4",
        tmp_path / "output.mp4",
    )

    assert cv2.writer.frames == [
        "swapped-frame-1",
        "swapped-frame-2",
        "swapped-frame-3",
    ]
    assert cv2.capture.released
    assert cv2.writer.released
    assert result.frames_processed == 3
    assert result.workers_used == 1


def test_video_duration_limit_is_checked_before_processing(tmp_path: Path):
    cv2 = FakeCV2(["frame-1", "frame-2"])
    processor = StubProcessor(
        cv2,
        FakeAnalyzer(),
        FakeSwapper(),
        max_video_seconds=1,
    )

    with pytest.raises(MediaValidationError, match="длиннее"):
        processor.process_video(
            tmp_path / "source.jpg",
            tmp_path / "target.mp4",
            tmp_path / "output.mp4",
        )

    assert cv2.capture.released
    assert cv2.writer.frames == []


def test_target_without_faces_is_rejected(tmp_path: Path):
    class SourceOnlyAnalyzer:
        @staticmethod
        def get(frame):
            if frame == "source":
                return [SimpleNamespace(bbox=[0, 0, 2, 2])]
            return []

    cv2 = FakeCV2([])
    processor = StubProcessor(cv2, SourceOnlyAnalyzer(), FakeSwapper())

    with pytest.raises(MediaValidationError, match="не найдено лиц"):
        processor.process_image(
            tmp_path / "source.jpg",
            tmp_path / "target.jpg",
            tmp_path / "output.jpg",
        )
