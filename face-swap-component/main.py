from fastapi import FastAPI, UploadFile, File, HTTPException
from fastapi.responses import FileResponse
import os
import shutil
from pathlib import Path
import cv2
import torch
import insightface
from insightface.app import FaceAnalysis
import onnxruntime
import logging
import subprocess
from concurrent.futures import ThreadPoolExecutor
import numpy as np
import glob
import magic
from typing import Optional
import threading

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

app = FastAPI(title="Face Swap API")

TEMP_DIR = Path("temp")
MODELS_DIR = TEMP_DIR / "models"
MEDIA_DIR = TEMP_DIR / "media"
CACHE_DIR = TEMP_DIR / "cache"

def safe_mkdir_and_cleanup(path: Path):
    """
    Безопасно создает директорию и очищает её от старых файлов.
    """
    try:
        path.mkdir(exist_ok=True)
        if path == MEDIA_DIR:
            for item in path.iterdir():
                try:
                    if item.is_file():
                        if item.stat().st_mtime < (os.path.getmtime('.') - 3600):
                            item.unlink()
                    elif item.is_dir():
                        shutil.rmtree(item)
                except (OSError, FileNotFoundError) as e:
                    logger.warning(f"Could not remove old temp file {item}: {e}")
    except Exception as e:
        logger.error(f"Error setting up directory {path}: {e}")
        raise

safe_mkdir_and_cleanup(TEMP_DIR)
safe_mkdir_and_cleanup(MODELS_DIR)
safe_mkdir_and_cleanup(MEDIA_DIR)
safe_mkdir_and_cleanup(CACHE_DIR)

MAX_FILE_SIZE = 100 * 1024 * 1024
MAX_VIDEO_DURATION = 300
MAX_FRAMES = 3000
ALLOWED_IMAGE_TYPES = {'image/jpeg', 'image/png'}
ALLOWED_VIDEO_TYPES = {'video/mp4', 'video/avi', 'video/mov'}

processing_lock = threading.Lock()

def validate_file_size(file: UploadFile) -> bool:
    """
    Проверяет размер файла.
    """
    if hasattr(file, 'size') and file.size and file.size > MAX_FILE_SIZE:
        return False
    return True

def validate_file_type(file_content: bytes, expected_types: set) -> bool:
    """
    Проверяет MIME тип файла по его содержимому.
    """
    try:
        mime = magic.from_buffer(file_content, mime=True)
        return mime in expected_types
    except Exception as e:
        logger.warning(f"Error detecting MIME type: {e}")
        return False

def validate_video_properties(video_path: str) -> bool:
    """
    Проверяет свойства видео на безопасность.
    """
    try:
        cap = cv2.VideoCapture(str(video_path))
        if not cap.isOpened():
            return False
            
        frame_count = int(cap.get(cv2.CAP_PROP_FRAME_COUNT))
        duration = frame_count / cap.get(cv2.CAP_PROP_FPS) if cap.get(cv2.CAP_PROP_FPS) > 0 else 0
        
        cap.release()
        
        return duration <= MAX_VIDEO_DURATION and frame_count <= MAX_FRAMES
    except Exception:
        return False

def cleanup_temp_files(force: bool = False):
    """
    Очищает временные файлы из директории MEDIA_DIR.
    """
    try:
        if not MEDIA_DIR.exists():
            MEDIA_DIR.mkdir(exist_ok=True)
            return
            
        for item in MEDIA_DIR.iterdir():
            try:
                if item.is_file():
                    if force or item.stat().st_size == 0 or not item.name.startswith('output'):
                        item.unlink()
                elif item.is_dir():
                    shutil.rmtree(item)
            except (OSError, FileNotFoundError) as e:
                logger.warning(f"Failed to remove {item}: {e}")
                
        logger.info("Temporary media files cleaned up successfully")
    except Exception as e:
        logger.error(f"Error during cleanup: {e}")

def ensure_model_exists():
    """
    Проверяет наличие модели inswapper_128.onnx в директории MODELS_DIR.
    """
    model_path = MODELS_DIR / 'inswapper_128.onnx'
    if not model_path.exists():
        logger.error(f"Model not found at {model_path}")
        raise RuntimeError(f"Model not found at {model_path}")
    return model_path

def setup_cache():
    """
    Настраивает кэш для InsightFace.
    """
    try:
        os.environ['INSIGHTFACE_CACHE_DIR'] = str(CACHE_DIR)
        logger.info(f"Using InsightFace cache directory: {CACHE_DIR}")
        
        test_file = CACHE_DIR / '.test'
        test_file.touch()
        test_file.unlink()
        
        return True
    except Exception as e:
        logger.error(f"Error setting up cache: {e}")
        return False

try:
    setup_cache()
    cleanup_temp_files(force=True)
except Exception as e:
    logger.error(f"Startup cleanup failed: {e}")
    raise RuntimeError(f"Failed to initialize service: {e}")

session_options = onnxruntime.SessionOptions()
session_options.graph_optimization_level = onnxruntime.GraphOptimizationLevel.ORT_ENABLE_ALL
session_options.enable_mem_pattern = True
session_options.enable_mem_reuse = True
session_options.intra_op_num_threads = os.cpu_count()
session_options.inter_op_num_threads = os.cpu_count()

DEVICE_TYPE = os.getenv("DEVICE_TYPE", "cpu").lower()
if DEVICE_TYPE == "nvidia":
    try:
        if not torch.cuda.is_available():
            logger.warning("CUDA is not available, falling back to CPU")
            providers = ['CPUExecutionProvider']
            ctx_id = -1
        else:
            cuda_device = torch.cuda.current_device()
            cuda_memory = torch.cuda.get_device_properties(cuda_device).total_memory / 1024**3
            logger.info(f"Using CUDA device with {cuda_memory:.2f}GB memory")
            
            providers = [
                ('CUDAExecutionProvider', {
                    'device_id': cuda_device,
                    'cudnn_conv_algo_search': 'EXHAUSTIVE',
                    'do_copy_in_default_stream': True
                }),
                'CPUExecutionProvider'
            ]
            ctx_id = cuda_device
            
            os.environ['CUDA_VISIBLE_DEVICES'] = str(cuda_device)
            os.environ['OMP_NUM_THREADS'] = str(os.cpu_count())
            os.environ['MKL_NUM_THREADS'] = str(os.cpu_count())
            
    except Exception as e:
        logger.error(f"Error configuring CUDA: {e}")
        providers = ['CPUExecutionProvider']
        ctx_id = -1
else:
    providers = ['CPUExecutionProvider']
    ctx_id = -1

face_analyzer = None
swapper = None

def initialize_models():
    global face_analyzer, swapper
    if face_analyzer is None or swapper is None:
        with processing_lock:
            if face_analyzer is None or swapper is None:
                try:
                    face_analyzer = FaceAnalysis(
                        name='buffalo_l',
                        providers=providers,
                        session_options=session_options,
                        root=str(CACHE_DIR)
                    )
                    face_analyzer.prepare(ctx_id=ctx_id, det_size=(640, 640))
                    
                    MODEL_PATH = MODELS_DIR / 'inswapper_128.onnx'
                    if not MODEL_PATH.exists():
                        raise RuntimeError(f"Model not found at {MODEL_PATH}")
                    
                    swapper = insightface.model_zoo.get_model(
                        str(MODEL_PATH),
                        providers=providers,
                        session_options=session_options
                    )
                    logger.info("Models initialized successfully")
                except Exception as e:
                    logger.error(f"Error initializing models: {e}")
                    raise RuntimeError(f"Failed to initialize models: {e}")

def process_video_chunk(chunk_data):
    """
    Обрабатывает чанк видео, заменяя лица в каждом кадре.
    """
    try:
        chunk_frames, source_face, start_idx = chunk_data
        processed_frames = []
        
        for frame in chunk_frames:
            try:
                frame_rgb = cv2.cvtColor(frame, cv2.COLOR_BGR2RGB)
                target_faces = face_analyzer.get(frame_rgb)
                
                if len(target_faces) > 0:
                    for target_face in target_faces:
                        if target_face.kps is not None:
                            frame_rgb = swapper.get(frame_rgb, target_face, source_face, paste_back=True)
                
                frame_bgr = cv2.cvtColor(frame_rgb, cv2.COLOR_RGB2BGR)
                processed_frames.append(frame_bgr)
            except Exception as e:
                logger.warning(f"Error processing frame in chunk {start_idx}: {e}")
                processed_frames.append(frame)
        
        return start_idx, processed_frames
    except Exception as e:
        logger.error(f"Error processing chunk {start_idx}: {e}")
        return start_idx, chunk_frames

@app.post("/swap")
async def swap_faces(
    source_image: UploadFile = File(...),
    target_video: UploadFile = File(...)
):
    """
    Эндпоинт для замены лиц в видео.
    
    Args:
        source_image (UploadFile): Изображение с исходным лицом
        target_video (UploadFile): Видео, в котором нужно заменить лица
        
    Returns:
        FileResponse: Обработанное видео с замененными лицами
        
    Raises:
        HTTPException: При ошибках обработки файлов или недостаточном количестве лиц
    """
    source_path = None
    target_path = None
    temp_output_path = None
    output_path = None
    target_vid = None
    out = None
    try:
        with processing_lock:
            initialize_models()
            cleanup_temp_files()
            model_path = ensure_model_exists()
            
            if not validate_file_size(source_image):
                raise HTTPException(status_code=413, detail="Source image too large")
            if not validate_file_size(target_video):
                raise HTTPException(status_code=413, detail="Target video too large")
            
            source_content = await source_image.read()
            target_content = await target_video.read()
            
            if not validate_file_type(source_content, ALLOWED_IMAGE_TYPES):
                raise HTTPException(status_code=400, detail="Invalid image format")
            if not validate_file_type(target_content, ALLOWED_VIDEO_TYPES):
                raise HTTPException(status_code=400, detail="Invalid video format")
            
            import uuid
            session_id = str(uuid.uuid4())[:8]
            source_path = MEDIA_DIR / f"source_{session_id}_{source_image.filename}"
            target_path = MEDIA_DIR / f"target_{session_id}_{target_video.filename}"
            output_path = MEDIA_DIR / f"output_{session_id}.mp4"
            temp_output_path = MEDIA_DIR / f"temp_output_{session_id}.mp4"
            
            with open(source_path, "wb") as f:
                f.write(source_content)
            with open(target_path, "wb") as f:
                f.write(target_content)
            
            if not validate_video_properties(str(target_path)):
                raise HTTPException(status_code=400, detail="Video exceeds duration or frame limits")

            source_img = cv2.imread(str(source_path))
            if source_img is None:
                raise HTTPException(status_code=400, detail="Failed to load source image")
            source_img = cv2.cvtColor(source_img, cv2.COLOR_BGR2RGB)
            source_faces = face_analyzer.get(source_img)
            if len(source_faces) == 0:
                raise HTTPException(status_code=400, detail="No face detected in source image")
            source_face = source_faces[0]

            target_vid = cv2.VideoCapture(str(target_path))
            if not target_vid.isOpened():
                raise HTTPException(status_code=400, detail="Failed to open video")
                
            width = int(target_vid.get(cv2.CAP_PROP_FRAME_WIDTH))
            height = int(target_vid.get(cv2.CAP_PROP_FRAME_HEIGHT))
            fps = target_vid.get(cv2.CAP_PROP_FPS)
            
            if width <= 0 or height <= 0 or fps <= 0:
                target_vid.release()
                raise HTTPException(status_code=400, detail="Invalid video properties")
            
            frames = []
            frame_count = 0
            while True:
                ret, frame = target_vid.read()
                if not ret:
                    break
                frames.append(frame)
                frame_count += 1
                if frame_count > MAX_FRAMES:
                    target_vid.release()
                    raise HTTPException(status_code=400, detail=f"Video has too many frames (max {MAX_FRAMES})")
            
            target_vid.release()
            
            if len(frames) == 0:
                raise HTTPException(status_code=400, detail="No frames read from video")
            
            if DEVICE_TYPE == "nvidia":
                try:
                    available_vram = torch.cuda.get_device_properties(0).total_memory
                    used_vram = torch.cuda.memory_allocated(0)
                    free_vram = available_vram - used_vram
                    
                    frame_memory = width * height * 3
                    safety_factor = 0.8
                    chunk_size = int((free_vram * safety_factor) / (frame_memory * 4))
                    chunk_size = max(1, min(chunk_size, 100))
                    
                    logger.info(f"VRAM: {free_vram/1024**3:.1f}GB free, chunk size: {chunk_size}")
                except Exception as e:
                    logger.warning(f"Error calculating VRAM: {e}, using default chunk size")
                    chunk_size = 10
            else:
                chunk_size = 30
            
            logger.info(f"Processing video with chunk size: {chunk_size} frames")
            
            chunks = []
            for i in range(0, len(frames), chunk_size):
                chunk = frames[i:i + chunk_size]
                chunks.append((chunk, source_face, i))
            
            processed_frames = [None] * len(frames)
            if DEVICE_TYPE == "nvidia":
                max_workers = 1
            else:
                max_workers = min(os.cpu_count(), 4)
            with ThreadPoolExecutor(max_workers=max_workers) as executor:
                try:
                    futures = [executor.submit(process_video_chunk, chunk) for chunk in chunks]
                    for future in futures:
                        start_idx, chunk_frames = future.result(timeout=300)
                        processed_frames[start_idx:start_idx + len(chunk_frames)] = chunk_frames
                except Exception as e:
                    logger.error(f"Error in thread pool processing: {e}")
                    raise HTTPException(status_code=500, detail="Video processing failed")
            
            fourcc = cv2.VideoWriter_fourcc(*'mp4v')
            out = cv2.VideoWriter(str(temp_output_path), fourcc, fps, (width, height))
            
            if not out.isOpened():
                raise HTTPException(status_code=500, detail="Failed to create video writer")
            
            try:
                for frame in processed_frames:
                    if frame is not None:
                        out.write(frame)
            finally:
                out.release()
            
            if os.path.exists(output_path):
                os.remove(output_path)
            
            probe_cmd = ['ffprobe', '-i', str(target_path), '-show_streams', '-select_streams', 'a', '-loglevel', 'quiet']
            probe_result = subprocess.run(probe_cmd, capture_output=True, text=True)
            has_audio = probe_result.returncode == 0 and 'codec_type=audio' in probe_result.stdout
            
            if has_audio:
                command = [
                    'ffmpeg', '-y',
                    '-i', str(temp_output_path),
                    '-i', str(target_path),
                    '-c:v', 'copy',
                    '-c:a', 'aac',
                    '-map', '0:v:0',
                    '-map', '1:a:0',
                    str(output_path)
                ]
            else:
                command = [
                    'ffmpeg', '-y',
                    '-f', 'lavfi', '-i', 'anullsrc=channel_layout=stereo:sample_rate=44100',
                    '-i', str(temp_output_path),
                    '-c:v', 'copy',
                    '-c:a', 'aac',
                    '-shortest',
                    str(output_path)
                ]
            
            result = subprocess.run(command, capture_output=True, text=True, timeout=600)
            if result.returncode != 0:
                raise HTTPException(status_code=500, detail=f"FFmpeg error: {result.stderr}")
            
            if not os.path.exists(output_path) or os.path.getsize(output_path) == 0:
                raise HTTPException(status_code=500, detail="Output video not created properly")
            
            verify_cmd = ['ffprobe', '-i', str(output_path), '-show_streams', '-loglevel', 'quiet']
            verify_result = subprocess.run(verify_cmd, capture_output=True, text=True)
            if verify_result.returncode != 0:
                raise HTTPException(status_code=500, detail="Output video is corrupted")
            
            return FileResponse(
                str(output_path),
                media_type='video/mp4',
                filename='output.mp4'
            )
            
    except subprocess.TimeoutExpired:
        raise HTTPException(status_code=408, detail="Video processing timeout")
    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Error processing video: {e}")
        raise HTTPException(status_code=500, detail="Internal server error")
    finally:
        if target_vid is not None:
            target_vid.release()
        if out is not None:
            out.release()
        cleanup_temp_files()

@app.get("/health")
async def health_check():
    """
    Эндпоинт для проверки состояния сервиса.
    
    Returns:
        dict: Информация о состоянии сервиса, включая тип устройства,
              доступные провайдеры и версии используемых библиотек
        
    Raises:
        HTTPException: При ошибках получения информации о состоянии
    """
    try:
        device_info = {
            "status": "healthy",
            "device_type": DEVICE_TYPE,
            "providers": [p[0] if isinstance(p, tuple) else p for p in providers],
            "onnxruntime_version": onnxruntime.__version__,
            "torch_version": torch.__version__
        }
        if DEVICE_TYPE == "nvidia" and torch.cuda.is_available():
            device_info["vram_total_gb"] = torch.cuda.get_device_properties(0).total_memory / 1024**3
            device_info["vram_allocated_gb"] = torch.cuda.memory_allocated(0) / 1024**3
            device_info["vram_free_gb"] = (torch.cuda.get_device_properties(0).total_memory - torch.cuda.memory_allocated(0)) / 1024**3
        
        return device_info
    except Exception as e:
        logger.error(f"Health check failed: {e}")
        raise HTTPException(status_code=500, detail=str(e))