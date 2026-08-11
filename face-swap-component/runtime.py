from __future__ import annotations

import logging
from pathlib import Path

import cv2
import insightface
import onnxruntime
from insightface.app import FaceAnalysis

from pipeline import VideoProcessor

logger = logging.getLogger(__name__)


def create_processor(
    model_path: Path,
    cache_directory: Path,
    device_type: str,
    max_video_seconds: int,
    max_frame_pixels: int,
    max_image_pixels: int,
) -> VideoProcessor:
    available_providers = onnxruntime.get_available_providers()
    if device_type == "nvidia":
        if "CUDAExecutionProvider" not in available_providers:
            raise RuntimeError("CUDAExecutionProvider недоступен")
        providers = ["CUDAExecutionProvider", "CPUExecutionProvider"]
        context_id = 0
    else:
        providers = ["CPUExecutionProvider"]
        context_id = -1

    analyzer = FaceAnalysis(
        name="buffalo_l",
        allowed_modules=["detection"],
        providers=providers,
        root=str(cache_directory),
    )
    analyzer.prepare(ctx_id=context_id, det_size=(640, 640))
    swapper = insightface.model_zoo.get_model(
        str(model_path),
        providers=providers,
    )
    logger.info("Модели загружены, providers=%s", providers)
    return VideoProcessor(
        cv2,
        analyzer,
        swapper,
        max_video_seconds=max_video_seconds,
        max_frame_pixels=max_frame_pixels,
        max_image_pixels=max_image_pixels,
    )
