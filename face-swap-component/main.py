import os
import shutil
import time
import uuid
import math
import glob
import logging
import subprocess
from pathlib import Path
from concurrent.futures import ThreadPoolExecutor, as_completed
from typing import Tuple, List, Optional, Dict

import cv2
import torch
import numpy as np
import insightface
import onnxruntime
from fastapi import FastAPI, UploadFile, File, HTTPException, Request
from fastapi.responses import JSONResponse
from insightface.app import FaceAnalysis

# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s'
)
logger = logging.getLogger(__name__)

# Initialize FastAPI app
app = FastAPI(
    title="Face Swap API",
    description="Advanced face swapping service with GPU/CPU support",
    version="2.0.0"
)

# Directory structure
TEMP_DIR = Path("/temp")
MODELS_DIR = TEMP_DIR / "models"
MEDIA_DIR = TEMP_DIR / "media"
CACHE_DIR = TEMP_DIR / "cache"

# Ensure directories exist
for directory in [TEMP_DIR, MODELS_DIR, MEDIA_DIR, CACHE_DIR]:
    directory.mkdir(exist_ok=True)

# Constants
MODEL_NAME = "inswapper_128.onnx"
DEVICE_TYPE = os.getenv("DEVICE_TYPE", "cpu").lower()
WEEK_SECONDS = 7 * 24 * 60 * 60
HOUR_SECONDS = 60 * 60
MAX_IMAGE_BYTES = int(os.getenv("MAX_IMAGE_BYTES", "0"))  # 0 = unlimited (previous behavior)
MAX_VIDEO_BYTES = int(os.getenv("MAX_VIDEO_BYTES", "0"))  # 0 = unlimited (previous behavior)
AUTH_SECRET = os.getenv("FACE_SWAP_SECRET", "")
PUBLIC_BASE_URL = os.getenv("FACE_SWAP_PUBLIC_URL", "").rstrip("/")
RATE_LIMIT_WINDOW = int(os.getenv("RATE_LIMIT_WINDOW", "60"))  # seconds
RATE_LIMIT_MAX = int(os.getenv("RATE_LIMIT_MAX", "0"))     # 0 disables rate limit by default

rate_limiter: Dict[str, List[float]] = {}


def enforce_auth(request: Request):
    if not AUTH_SECRET:
        return
    token = request.headers.get("X-API-KEY", "")
    if token != AUTH_SECRET:
        raise HTTPException(status_code=401, detail="Unauthorized")


def check_rate_limit(request: Request):
    if RATE_LIMIT_MAX <= 0 or RATE_LIMIT_WINDOW <= 0:
        return
    client_ip = request.client.host if request.client else "unknown"
    now = time.time()
    window_start = now - RATE_LIMIT_WINDOW
    entries = rate_limiter.get(client_ip, [])
    entries = [t for t in entries if t >= window_start]
    if len(entries) >= RATE_LIMIT_MAX:
        raise HTTPException(status_code=429, detail="Too many requests, slow down")
    entries.append(now)
    rate_limiter[client_ip] = entries


def build_download_url(path: Path) -> str:
    if PUBLIC_BASE_URL:
        relative = path.as_posix().lstrip("/")
        return f"{PUBLIC_BASE_URL}/{relative}"
    return str(path)


class VideoProcessor:
    """Advanced video processing with face swapping capabilities."""

    def __init__(self):
        self._setup_cache()
        self._initialize_models()

    def _setup_cache(self) -> bool:
        """Configure InsightFace cache directory."""
        try:
            os.environ['INSIGHTFACE_CACHE_DIR'] = str(CACHE_DIR)
            logger.info(f"Cache directory configured: {CACHE_DIR}")

            # Test cache accessibility
            test_file = CACHE_DIR / '.test'
            test_file.touch()
            test_file.unlink()
            return True
        except Exception as e:
            logger.error(f"Cache setup failed: {e}")
            return False

    def _get_device_config(self) -> Tuple[List, int]:
        """Configure device-specific settings for ONNX Runtime."""
        if DEVICE_TYPE != "nvidia":
            return ['CPUExecutionProvider'], -1

        try:
            if not torch.cuda.is_available():
                logger.warning("CUDA unavailable, falling back to CPU")
                return ['CPUExecutionProvider'], -1

            device = torch.cuda.current_device()
            memory_gb = torch.cuda.get_device_properties(device).total_memory / 1024**3
            logger.info(f"CUDA device {device} with {memory_gb:.2f}GB memory")

            providers = [
                ('CUDAExecutionProvider', {
                    'device_id': device,
                    'cudnn_conv_algo_search': 'EXHAUSTIVE',
                    'do_copy_in_default_stream': True
                }),
                'CPUExecutionProvider'
            ]

            # Optimize threading for CUDA
            os.environ.update({
                'CUDA_VISIBLE_DEVICES': str(device),
                'OMP_NUM_THREADS': str(max(1, os.cpu_count() - 1)),
                'MKL_NUM_THREADS': str(max(1, os.cpu_count() - 1))
            })

            return providers, device

        except Exception as e:
            logger.error(f"CUDA configuration failed: {e}")
            return ['CPUExecutionProvider'], -1

    def _initialize_models(self):
        """Initialize face analysis and swapping models."""
        model_path = MODELS_DIR / MODEL_NAME
        if not model_path.exists():
            raise RuntimeError(f"Model not found: {model_path}")

        providers, ctx_id = self._get_device_config()

        # Configure ONNX session options
        session_options = onnxruntime.SessionOptions()
        session_options.graph_optimization_level = onnxruntime.GraphOptimizationLevel.ORT_ENABLE_ALL
        session_options.enable_mem_pattern = True
        session_options.enable_mem_reuse = True
        session_options.intra_op_num_threads = max(1, os.cpu_count() - 1)
        session_options.inter_op_num_threads = max(1, os.cpu_count() - 1)

        # Initialize face analyzer
        self.face_analyzer = FaceAnalysis(
            name='buffalo_l',
            providers=providers,
            session_options=session_options,
            root=str(CACHE_DIR)
        )
        self.face_analyzer.prepare(ctx_id=ctx_id, det_size=(640, 640))

        # Initialize face swapper
        self.swapper = insightface.model_zoo.get_model(
            str(model_path),
            providers=providers,
            session_options=session_options
        )

        logger.info("Models initialized successfully")

    def _calculate_processing_params(self, width: int, height: int) -> Tuple[int, int]:
        """Calculate optimal workers and chunk size based on device capabilities."""
        threads_override = os.getenv("THREADS", "0")
        try:
            threads_env = int(threads_override)
            if threads_env > 0:
                logger.info(f"Using threads override from environment: {threads_env}")
                chunk_size = 20 if DEVICE_TYPE == "nvidia" else 30
                return threads_env, chunk_size
        except ValueError:
            logger.warning(f"Invalid THREADS environment value: {threads_override}, ignoring")

        if DEVICE_TYPE == "nvidia":
            try:
                total_vram = torch.cuda.get_device_properties(0).total_memory
                used_vram = torch.cuda.memory_allocated(0)
                free_vram_gb = (total_vram - used_vram) / (1024**3)

                logger.info(f"VRAM: {free_vram_gb:.2f}GB free, {used_vram/(1024**3):.2f}GB used")

                if free_vram_gb < 2.0:
                    logger.warning(f"Low VRAM: {free_vram_gb:.2f}GB. Consider CPU mode.")

                vram_workers = max(1, int(free_vram_gb / 3.0))
                max_workers = min(vram_workers, os.cpu_count(), 4)

                frame_memory = width * height * 3
                safety_factor = 0.6
                chunk_size = int((total_vram * safety_factor) / (frame_memory * max_workers * 4))
                chunk_size = max(1, min(chunk_size, 20))

                logger.info(f"GPU: {max_workers} workers, {chunk_size} frames/chunk")
                return max_workers, chunk_size

            except Exception as e:
                logger.warning(f"GPU calculation failed: {e}")
                return 1, 10
        else:
            cpu_workers = max(1, os.cpu_count() - 1)
            max_workers = min(cpu_workers, 4)
            chunk_size = 30

            logger.info(f"CPU: {max_workers} workers, {chunk_size} frames/chunk")
            return max_workers, chunk_size

    def _process_chunk(self, chunk_data: Tuple[List, object, int]) -> Tuple[int, List]:
        """Process a chunk of video frames with face swapping."""
        frames, source_face, start_idx = chunk_data
        processed_frames = []

        logger.info(f"Processing chunk {start_idx}: {len(frames)} frames")

        for i, frame in enumerate(frames):
            frame_rgb = cv2.cvtColor(frame, cv2.COLOR_BGR2RGB)
            target_faces = self.face_analyzer.get(frame_rgb)

            for face in target_faces:
                if face.kps is not None:
                    frame_rgb = self.swapper.get(frame_rgb, face, source_face, paste_back=True)

            processed_frames.append(cv2.cvtColor(frame_rgb, cv2.COLOR_RGB2BGR))

            if (i + 1) % 10 == 0:
                logger.info(f"Chunk {start_idx}: {i + 1}/{len(frames)} completed")

        logger.info(f"Chunk {start_idx} processing complete")
        return start_idx, processed_frames

    def process_video(self, source_path: Path, target_path: Path, output_path: Path) -> int:
        """Main video processing pipeline with streaming to avoid full buffering."""
        source_img = cv2.imread(str(source_path))
        if source_img is None:
            raise HTTPException(status_code=400, detail="Invalid source image")

        source_img_rgb = cv2.cvtColor(source_img, cv2.COLOR_BGR2RGB)
        source_faces = self.face_analyzer.get(source_img_rgb)

        if not source_faces:
            raise HTTPException(status_code=400, detail="No face detected in source image")

        source_face = source_faces[0]

        video_capture = cv2.VideoCapture(str(target_path))
        if not video_capture.isOpened():
            raise HTTPException(status_code=400, detail="Invalid target video")

        width = int(video_capture.get(cv2.CAP_PROP_FRAME_WIDTH))
        height = int(video_capture.get(cv2.CAP_PROP_FRAME_HEIGHT))
        fps = video_capture.get(cv2.CAP_PROP_FPS)

        if fps <= 0 or width == 0 or height == 0:
            video_capture.release()
            raise HTTPException(status_code=400, detail="Unable to read video properties")

        logger.info(f"Streaming processing at {width}x{height} fps={fps}")

        max_workers, chunk_size = self._calculate_processing_params(width, height)

        temp_output = output_path.parent / f"temp_{output_path.name}"
        fourcc = cv2.VideoWriter_fourcc(*'mp4v')
        video_writer = cv2.VideoWriter(str(temp_output), fourcc, fps, (width, height))

        next_write_idx = 0
        pending: Dict[int, List] = {}
        executor = ThreadPoolExecutor(max_workers=max_workers)
        futures: Dict[object, int] = {}

        def submit_chunk(frames_chunk: List, start_idx: int):
            future = executor.submit(self._process_chunk, (frames_chunk, source_face, start_idx))
            futures[future] = start_idx

        frames_buffer: List = []
        start_idx = 0

        try:
            while True:
                ret, frame = video_capture.read()
                if not ret:
                    break
                frames_buffer.append(frame)

                if len(frames_buffer) >= chunk_size:
                    submit_chunk(frames_buffer, start_idx)
                    start_idx += len(frames_buffer)
                    frames_buffer = []

            if frames_buffer:
                submit_chunk(frames_buffer, start_idx)

            for future in as_completed(list(futures.keys())):
                start_idx_completed, chunk_frames = future.result()
                futures.pop(future, None)
                pending[start_idx_completed] = chunk_frames
                while next_write_idx in pending:
                    ready_frames = pending.pop(next_write_idx)
                    for f in ready_frames:
                        video_writer.write(f)
                    next_write_idx += len(ready_frames)

        finally:
            video_capture.release()
            executor.shutdown(wait=True)
            video_writer.release()

        if next_write_idx == 0:
            raise HTTPException(status_code=400, detail="No frames found in video")

        self._finalize_video(temp_output, target_path, output_path)

        return max_workers

    def _finalize_video(self, video_path: Path, audio_source: Path, output_path: Path):
        """Combine video with original audio and optimize for compatibility."""
        if output_path.exists():
            output_path.unlink()

        probe_command = [
            'ffprobe', '-v', 'quiet', '-show_entries', 'stream=codec_type',
            '-of', 'csv=p=0', str(audio_source)
        ]

        try:
            probe_result = subprocess.run(probe_command, capture_output=True, text=True, check=True, timeout=30)
            has_audio = 'audio' in probe_result.stdout
        except subprocess.CalledProcessError:
            has_audio = False
        except subprocess.TimeoutExpired:
            has_audio = False
            logger.warning("ffprobe timed out, assuming no audio")

        if has_audio:
            command = [
                'ffmpeg', '-y',
                '-i', str(video_path),
                '-i', str(audio_source),
                '-c:v', 'copy',
                '-c:a', 'aac',
                '-ar', '44100',
                '-ac', '2',
                '-b:a', '128k',
                '-movflags', '+faststart',
                '-map', '0:v:0',
                '-map', '1:a:0',
                str(output_path)
            ]
        else:
            command = [
                'ffmpeg', '-y',
                '-i', str(video_path),
                '-c:v', 'copy',
                '-movflags', '+faststart',
                str(output_path)
            ]

        try:
            result = subprocess.run(command, capture_output=True, text=True, check=True, timeout=180)
            if result.stderr:
                logger.info(f"ffmpeg finalize stderr: {result.stderr}")
        except subprocess.TimeoutExpired:
            logger.error("ffmpeg finalize timed out")
            raise HTTPException(status_code=500, detail="Video finalize timeout")
        except subprocess.CalledProcessError as e:
            logger.error(f"ffmpeg finalize failed: {e.stderr}")
            raise HTTPException(status_code=500, detail="Video finalize failed")

    def process_image(self, source_path: Path, target_path: Path, output_path: Path) -> int:
        """Process single image face swap."""
        source_img = cv2.imread(str(source_path))
        if source_img is None:
            raise HTTPException(status_code=400, detail="Invalid source image")

        source_img_rgb = cv2.cvtColor(source_img, cv2.COLOR_BGR2RGB)
        source_faces = self.face_analyzer.get(source_img_rgb)

        if not source_faces:
            raise HTTPException(status_code=400, detail="No face detected in source image")

        source_face = source_faces[0]

        target_img = cv2.imread(str(target_path))
        if target_img is None:
            raise HTTPException(status_code=400, detail="Invalid target image")

        target_img_rgb = cv2.cvtColor(target_img, cv2.COLOR_BGR2RGB)
        target_faces = self.face_analyzer.get(target_img_rgb)

        if not target_faces:
            raise HTTPException(status_code=400, detail="No face detected in target image")

        logger.info(f"Processing image with {len(target_faces)} face(s)")

        for face in target_faces:
            if face.kps is not None:
                target_img_rgb = self.swapper.get(target_img_rgb, face, source_face, paste_back=True)

        result_img = cv2.cvtColor(target_img_rgb, cv2.COLOR_RGB2BGR)
        cv2.imwrite(str(output_path), result_img)

        logger.info(f"Image processing complete: {output_path}")

        return 1


class FileManager:
    """Handle file operations and cleanup."""

    @staticmethod
    def cleanup_old_files(directory: Path, max_age_seconds: int):
        """Remove files older than specified age."""
        if not directory.exists():
            return

        cutoff_time = time.time() - max_age_seconds
        removed_count = 0

        for item in directory.iterdir():
            try:
                if item.stat().st_mtime < cutoff_time:
                    if item.is_file():
                        item.unlink()
                    elif item.is_dir():
                        shutil.rmtree(item)
                    removed_count += 1
            except (OSError, FileNotFoundError) as e:
                logger.warning(f"Failed to remove {item}: {e}")

        if removed_count > 0:
            logger.info(f"Removed {removed_count} old files from {directory}")

    @staticmethod
    def generate_session_paths(session_id: str, source_filename: str, target_filename: str) -> Tuple[Path, Path, Path]:
        """Generate unique file paths for a processing session."""
        source_path = MEDIA_DIR / f"source_{session_id}_{source_filename}"
        target_path = MEDIA_DIR / f"target_{session_id}_{target_filename}"
        output_path = MEDIA_DIR / f"output_{session_id}.mp4"
        return source_path, target_path, output_path


# Initialize services
file_manager = FileManager()
processor = VideoProcessor()

# Perform initial cleanup
file_manager.cleanup_old_files(MEDIA_DIR, WEEK_SECONDS)

# Log version information
git_commit = os.getenv("GIT_COMMIT", "unknown")
git_message = os.getenv("GIT_MESSAGE", "unknown")
logger.info(f"🚀 Face Swap Component started")
logger.info(f"📦 Version: {git_commit[:8]} - {git_message}")
logger.info(f"🖥️  Device: {DEVICE_TYPE}")
logger.info(f"📁 Temp dir: {TEMP_DIR}")


@app.post("/swap")
async def swap_faces(
    request: Request,
    source_image: UploadFile = File(..., description="Source face image"),
    target_video: UploadFile = File(..., description="Target video for face replacement")
):
    """
    Advanced face swapping endpoint with multi-threading and GPU optimization.

    Returns:
        JSON response with video path, processing duration, and metadata
    """
    start_time = time.time()
    session_id = str(uuid.uuid4())[:8]

    try:
        enforce_auth(request)
        check_rate_limit(request)

        file_manager.cleanup_old_files(MEDIA_DIR, HOUR_SECONDS)

        source_path, target_path, output_path = file_manager.generate_session_paths(
            session_id, source_image.filename, target_video.filename
        )

        if source_image.content_type and not source_image.content_type.startswith("image"):
            raise HTTPException(status_code=400, detail="source_image must be an image")
        if target_video.content_type and not target_video.content_type.startswith("video"):
            raise HTTPException(status_code=400, detail="target_video must be a video")

        await save_upload_with_limit(source_image, source_path, MAX_IMAGE_BYTES)
        await save_upload_with_limit(target_video, target_path, MAX_VIDEO_BYTES)

        max_workers = processor.process_video(source_path, target_path, output_path)

        processing_duration = int(time.time() - start_time)

        if DEVICE_TYPE == "nvidia":
            billable_duration = processing_duration * max_workers
            logger.info(f"GPU processing: {processing_duration}s × {max_workers} workers = {billable_duration}s billable")
        else:
            billable_duration = math.ceil(processing_duration / 10)
            logger.info(f"CPU processing: {processing_duration}s ÷ 10 = {billable_duration}s billable")

        logger.info(f"Session {session_id} completed successfully")

        return JSONResponse({
            "video_path": str(output_path),
            "download_url": build_download_url(output_path),
            "duration_seconds": processing_duration,
            "filename": "output.mp4",
            "media_type": "video/mp4",
            "session_id": session_id,
            "processing_time": processing_duration,
            "workers_used": max_workers,
            "device_type": DEVICE_TYPE
        })

    except Exception as e:
        logger.error(f"Session {session_id} failed: {e}")
        raise HTTPException(status_code=500, detail=str(e))


@app.post("/swap-photo")
async def swap_faces_photo(
    request: Request,
    source_image: UploadFile = File(..., description="Source face image"),
    target_image: UploadFile = File(..., description="Target image for face replacement")
):
    """
    Face swapping for images.

    Returns:
        JSON response with image path, processing duration, and metadata
    """
    start_time = time.time()
    session_id = str(uuid.uuid4())[:8]

    try:
        enforce_auth(request)
        check_rate_limit(request)

        file_manager.cleanup_old_files(MEDIA_DIR, HOUR_SECONDS)

        source_path, target_path, output_path = file_manager.generate_session_paths(
            session_id, source_image.filename, target_image.filename
        )

        output_path = output_path.with_suffix('.jpg')

        if source_image.content_type and not source_image.content_type.startswith("image"):
            raise HTTPException(status_code=400, detail="source_image must be an image")
        if target_image.content_type and not target_image.content_type.startswith("image"):
            raise HTTPException(status_code=400, detail="target_image must be an image")

        await save_upload_with_limit(source_image, source_path, MAX_IMAGE_BYTES)
        await save_upload_with_limit(target_image, target_path, MAX_IMAGE_BYTES)

        max_workers = processor.process_image(source_path, target_path, output_path)

        processing_duration = int(time.time() - start_time)

        logger.info(f"Photo session {session_id} completed successfully")

        return JSONResponse({
            "image_path": str(output_path),
            "download_url": build_download_url(output_path),
            "duration_seconds": processing_duration,
            "filename": "output.jpg",
            "media_type": "image/jpeg",
            "session_id": session_id,
            "processing_time": processing_duration,
            "workers_used": max_workers,
            "device_type": DEVICE_TYPE
        })

    except Exception as e:
        logger.error(f"Photo session {session_id} failed: {e}")
        raise HTTPException(status_code=500, detail=str(e))


@app.get("/health")
async def health_check():
    """
    Service health check with detailed system information.
    """
    try:
        git_commit = os.getenv("GIT_COMMIT", "unknown")
        git_message = os.getenv("GIT_MESSAGE", "unknown")

        device_info = {
            "status": "healthy",
            "device_type": DEVICE_TYPE,
            "git_commit": git_commit,
            "git_message": git_message,
            "version": f"{git_commit[:8]} - {git_message}",
            "onnxruntime_version": onnxruntime.__version__,
            "torch_version": torch.__version__,
            "model_loaded": (MODELS_DIR / MODEL_NAME).exists(),
            "cache_directory": str(CACHE_DIR),
            "temp_directory": str(TEMP_DIR)
        }

        if DEVICE_TYPE == "nvidia" and torch.cuda.is_available():
            device_info.update({
                "cuda_available": True,
                "gpu_count": torch.cuda.device_count(),
                "current_device": torch.cuda.current_device(),
                "gpu_memory_gb": torch.cuda.get_device_properties(0).total_memory / 1024**3
            })

        return device_info

    except Exception as e:
        logger.error(f"Health check failed: {e}")
        raise HTTPException(status_code=500, detail=str(e))


async def save_upload_with_limit(upload: UploadFile, dest: Path, max_bytes: int):
    """Save upload to disk with size limits and streaming copy."""
    size = 0
    with open(dest, "wb") as f:
        while True:
            chunk = await upload.read(1024 * 1024)
            if not chunk:
                break
            size += len(chunk)
            if max_bytes > 0 and size > max_bytes:
                raise HTTPException(status_code=400, detail=f"File exceeds limit {max_bytes} bytes")
            f.write(chunk)
    upload.file.close()


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=7860)