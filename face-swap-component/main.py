from fastapi import FastAPI, UploadFile, File, HTTPException
from fastapi.responses import FileResponse, JSONResponse
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
import time
import uuid

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

app = FastAPI(title="Face Swap API")

TEMP_DIR = Path("temp")
MODELS_DIR = TEMP_DIR / "models"
MEDIA_DIR = TEMP_DIR / "media"
CACHE_DIR = TEMP_DIR / "cache"

# Create directories
TEMP_DIR.mkdir(exist_ok=True)
MODELS_DIR.mkdir(exist_ok=True)
MEDIA_DIR.mkdir(exist_ok=True)
CACHE_DIR.mkdir(exist_ok=True)

def initial_cleanup():
    """
    Выполняет начальную очистку при запуске - удаляет все файлы из temp.
    """
    try:
        if MEDIA_DIR.exists():
            for item in MEDIA_DIR.iterdir():
                try:
                    if item.is_file():
                        item.unlink()
                        logger.info(f"Removed startup file: {item.name}")
                    elif item.is_dir():
                        shutil.rmtree(item)
                        logger.info(f"Removed startup directory: {item.name}")
                except (OSError, FileNotFoundError) as e:
                    logger.warning(f"Could not remove {item}: {e}")
        logger.info("Initial cleanup completed successfully")
    except Exception as e:
        logger.error(f"Initial cleanup failed: {e}")

# Perform initial cleanup on startup
initial_cleanup()

def cleanup_temp_files():
    """
    Очищает временные файлы из директории MEDIA_DIR.
    """
    try:
        for file_path in glob.glob(str(MEDIA_DIR / "*")):
            try:
                if os.path.isfile(file_path):
                    os.remove(file_path)
                elif os.path.isdir(file_path):
                    shutil.rmtree(file_path)
            except Exception as e:
                logger.warning(f"Failed to remove {file_path}: {e}")
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

setup_cache()
cleanup_temp_files()

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
            
            session_options = onnxruntime.SessionOptions()
            session_options.graph_optimization_level = onnxruntime.GraphOptimizationLevel.ORT_ENABLE_ALL
            session_options.enable_mem_pattern = True
            session_options.enable_mem_reuse = True
            session_options.intra_op_num_threads = os.cpu_count()
            session_options.inter_op_num_threads = os.cpu_count()
            
    except Exception as e:
        logger.error(f"Error configuring CUDA: {e}")
        providers = ['CPUExecutionProvider']
        ctx_id = -1
else:
    providers = ['CPUExecutionProvider']
    ctx_id = -1

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

def process_video_chunk(chunk_data):
    """
    Обрабатывает чанк видео, заменяя лица в каждом кадре.
    """
    chunk_frames, source_face, start_idx = chunk_data
    processed_frames = []
    
    logger.info(f"Processing chunk starting at frame {start_idx}, {len(chunk_frames)} frames")
    
    for i, frame in enumerate(chunk_frames):
        frame_rgb = cv2.cvtColor(frame, cv2.COLOR_BGR2RGB)
        target_faces = face_analyzer.get(frame_rgb)
        
        if len(target_faces) > 0:
            for target_face in target_faces:
                if target_face.kps is not None:
                    frame_rgb = swapper.get(frame_rgb, target_face, source_face, paste_back=True)
        
        frame_bgr = cv2.cvtColor(frame_rgb, cv2.COLOR_RGB2BGR)
        processed_frames.append(frame_bgr)
        
        if (i + 1) % 10 == 0:  # Log every 10 frames
            logger.info(f"Chunk {start_idx}: processed {i + 1}/{len(chunk_frames)} frames")
    
    logger.info(f"Completed chunk {start_idx}: {len(processed_frames)} frames processed")
    return start_idx, processed_frames

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
    start_time = time.time()
    try:
        cleanup_temp_files()
        model_path = ensure_model_exists()
        
        session_id = str(uuid.uuid4())[:8]
        source_path = MEDIA_DIR / f"source_{session_id}_{source_image.filename}"
        target_path = MEDIA_DIR / f"target_{session_id}_{target_video.filename}" 
        output_path = MEDIA_DIR / f"output_{session_id}.mp4"
        temp_output_path = MEDIA_DIR / f"temp_output_{session_id}.mp4"
        
        with open(source_path, "wb") as f:
            shutil.copyfileobj(source_image.file, f)
        with open(target_path, "wb") as f:
            shutil.copyfileobj(target_video.file, f)

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
        
        frames = []
        while True:
            ret, frame = target_vid.read()
            if not ret:
                break
            frames.append(frame)
        
        target_vid.release()
        
        if len(frames) == 0:
            raise HTTPException(status_code=400, detail="No frames read from video")
        
        if DEVICE_TYPE == "nvidia":
            frame_memory = width * height * 3
            available_memory = 2.5 * 1024 * 1024 * 1024
            chunk_size = int(available_memory / (frame_memory * 2.5))
            chunk_size = max(1, min(chunk_size, 30))
        else:
            chunk_size = 30
        
        logger.info(f"Processing video with chunk size: {chunk_size} frames")
        
        chunks = []
        for i in range(0, len(frames), chunk_size):
            chunk = frames[i:i + chunk_size]
            chunks.append((chunk, source_face, i))
        
        processed_frames = [None] * len(frames)
        with ThreadPoolExecutor(max_workers=os.cpu_count()) as executor:
            futures = [executor.submit(process_video_chunk, chunk) for chunk in chunks]
            for future in futures:
                start_idx, chunk_frames = future.result()
                processed_frames[start_idx:start_idx + len(chunk_frames)] = chunk_frames
        
        fourcc = cv2.VideoWriter_fourcc(*'mp4v')
        out = cv2.VideoWriter(str(temp_output_path), fourcc, fps, (width, height))
        
        for frame in processed_frames:
            out.write(frame)
        out.release()
        
        if os.path.exists(output_path):
            os.remove(output_path)
            
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
        
        subprocess.run(command, check=True)
        
        processing_duration = int(time.time() - start_time)
        logger.info(f"Face swap completed in {processing_duration} seconds")
        logger.info(f"Processing completed. Total frames: {len(frames)}")
        
        return JSONResponse({
            "video_path": str(output_path),
            "duration_seconds": processing_duration,
            "filename": "output.mp4",
            "media_type": "video/mp4"
        })
        
    except Exception as e:
        logger.error(f"Error processing video: {e}")
        raise HTTPException(status_code=500, detail=str(e))
    finally:
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
        return device_info
    except Exception as e:
        logger.error(f"Health check failed: {e}")
        raise HTTPException(status_code=500, detail=str(e))