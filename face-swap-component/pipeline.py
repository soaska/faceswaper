from __future__ import annotations

import math
import subprocess
from dataclasses import dataclass
from pathlib import Path
from typing import Any


class MediaValidationError(ValueError):
    """The uploaded media cannot be processed safely."""


@dataclass(frozen=True)
class ProcessingResult:
    workers_used: int
    frames_processed: int


class VideoProcessor:
    """Bounded-memory face swap pipeline.

    OpenCV, the analyzer and the swapper are injected so importing this module
    does not initialize CUDA and the frame lifecycle can be unit tested.
    """

    def __init__(
        self,
        cv2_module: Any,
        face_analyzer: Any,
        swapper: Any,
        *,
        max_video_seconds: int = 300,
        max_frame_pixels: int = 1920 * 1080,
        max_image_pixels: int = 25_000_000,
    ) -> None:
        self.cv2 = cv2_module
        self.face_analyzer = face_analyzer
        self.swapper = swapper
        self.max_video_seconds = max_video_seconds
        self.max_frame_pixels = max_frame_pixels
        self.max_image_pixels = max_image_pixels

    def process_video(
        self,
        source_path: Path,
        target_path: Path,
        output_path: Path,
    ) -> ProcessingResult:
        source_face = self._load_source_face(source_path)
        capture = self.cv2.VideoCapture(str(target_path))
        if not capture.isOpened():
            raise MediaValidationError("Не удалось открыть целевое видео")

        width = int(capture.get(self.cv2.CAP_PROP_FRAME_WIDTH))
        height = int(capture.get(self.cv2.CAP_PROP_FRAME_HEIGHT))
        fps = float(capture.get(self.cv2.CAP_PROP_FPS))
        if width <= 0 or height <= 0 or width * height > self.max_frame_pixels:
            capture.release()
            raise MediaValidationError("Недопустимое разрешение видео")
        if not math.isfinite(fps) or fps <= 0:
            capture.release()
            raise MediaValidationError("Не удалось определить частоту кадров видео")

        reported_frames = int(capture.get(self.cv2.CAP_PROP_FRAME_COUNT))
        max_frames = max(1, math.ceil(self.max_video_seconds * fps))
        if reported_frames > max_frames:
            capture.release()
            raise MediaValidationError(
                f"Видео длиннее допустимых {self.max_video_seconds} секунд"
            )

        temporary_output = output_path.with_name(f"encoded-{output_path.name}")
        writer = self.cv2.VideoWriter(
            str(temporary_output),
            self.cv2.VideoWriter_fourcc(*"mp4v"),
            fps,
            (width, height),
        )
        if not writer.isOpened():
            capture.release()
            raise RuntimeError("Не удалось открыть файл результата для записи")

        frame_count = 0
        try:
            while True:
                has_frame, frame = capture.read()
                if not has_frame:
                    break
                frame_count += 1
                if frame_count > max_frames:
                    raise MediaValidationError(
                        f"Видео длиннее допустимых {self.max_video_seconds} секунд"
                    )
                writer.write(self._swap_faces(frame, source_face))
        finally:
            capture.release()
            writer.release()

        if frame_count == 0:
            temporary_output.unlink(missing_ok=True)
            raise MediaValidationError("В видео не найдено кадров")

        try:
            self._finalize_video(temporary_output, target_path, output_path)
        finally:
            temporary_output.unlink(missing_ok=True)

        return ProcessingResult(workers_used=1, frames_processed=frame_count)

    def process_image(
        self,
        source_path: Path,
        target_path: Path,
        output_path: Path,
    ) -> ProcessingResult:
        source_face = self._load_source_face(source_path)
        target_image = self.cv2.imread(str(target_path))
        if target_image is None:
            raise MediaValidationError("Не удалось открыть целевое изображение")
        self._validate_image_size(target_image)
        result = self._swap_faces(target_image, source_face)
        if not self.cv2.imwrite(str(output_path), result):
            raise RuntimeError("Не удалось сохранить обработанное изображение")
        return ProcessingResult(workers_used=1, frames_processed=1)

    def _load_source_face(self, source_path: Path) -> Any:
        source_image = self.cv2.imread(str(source_path))
        if source_image is None:
            raise MediaValidationError("Не удалось открыть исходное изображение")
        self._validate_image_size(source_image)
        faces = self.face_analyzer.get(source_image)
        if not faces:
            raise MediaValidationError("На исходном изображении не найдено лицо")
        return max(faces, key=self._face_area)

    def _validate_image_size(self, image: Any) -> None:
        shape = getattr(image, "shape", None)
        if shape is not None and len(shape) >= 2:
            if int(shape[0]) * int(shape[1]) > self.max_image_pixels:
                raise MediaValidationError("Слишком большое разрешение изображения")

    def _swap_faces(self, frame: Any, source_face: Any) -> Any:
        result = frame
        target_faces = self.face_analyzer.get(frame)
        for target_face in target_faces:
            result = self.swapper.get(
                result,
                target_face,
                source_face,
                paste_back=True,
            )
        return result

    @staticmethod
    def _face_area(face: Any) -> float:
        bbox = getattr(face, "bbox", None)
        if bbox is None or len(bbox) < 4:
            return 0.0
        return max(0.0, float(bbox[2] - bbox[0])) * max(
            0.0, float(bbox[3] - bbox[1])
        )

    @staticmethod
    def _finalize_video(
        video_path: Path,
        audio_source: Path,
        output_path: Path,
    ) -> None:
        probe = subprocess.run(
            [
                "ffprobe",
                "-v",
                "error",
                "-select_streams",
                "a",
                "-show_entries",
                "stream=index",
                "-of",
                "csv=p=0",
                str(audio_source),
            ],
            capture_output=True,
            text=True,
            timeout=30,
            check=False,
        )
        has_audio = probe.returncode == 0 and bool(probe.stdout.strip())
        command = ["ffmpeg", "-y", "-i", str(video_path)]
        if has_audio:
            command.extend(
                [
                    "-i",
                    str(audio_source),
                    "-map",
                    "0:v:0",
                    "-map",
                    "1:a:0",
                    "-c:v",
                    "copy",
                    "-c:a",
                    "aac",
                    "-b:a",
                    "128k",
                ]
            )
        else:
            command.extend(["-map", "0:v:0", "-c:v", "copy"])
        command.extend(["-movflags", "+faststart", str(output_path)])
        subprocess.run(
            command,
            capture_output=True,
            text=True,
            timeout=600,
            check=True,
        )
