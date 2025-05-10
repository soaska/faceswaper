from fastapi import FastAPI, UploadFile, File, HTTPException, Form
from fastapi.responses import FileResponse, Response
import os
import tempfile
import shutil
from pathlib import Path
import cv2
import numpy as np
import torch
import insightface
from insightface.app import FaceAnalysis
import onnxruntime
import logging
import requests
import json
import subprocess
import shutil
import glob

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

app = FastAPI(title="Face Swap API")

# Create necessary directories
TEMP_DIR = Path("temp")
MODELS_DIR = TEMP_DIR / "models"
MEDIA_DIR = TEMP_DIR / "media"
CACHE_DIR = TEMP_DIR / "cache"

TEMP_DIR.mkdir(exist_ok=True)
MODELS_DIR.mkdir(exist_ok=True)
MEDIA_DIR.mkdir(exist_ok=True)
CACHE_DIR.mkdir(exist_ok=True)

def cleanup_temp_files():
    try:
        # Clean up only media files, keep models and cache
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

cleanup_temp_files()

# Set InsightFace cache directory
os.environ['INSIGHTFACE_CACHE_DIR'] = str(CACHE_DIR)
logger.info(f"Using InsightFace cache directory: {CACHE_DIR}")

DEVICE_TYPE = os.getenv("DEVICE_TYPE", "cpu").lower()
if DEVICE_TYPE == "nvidia":
    try:
        # Check CUDA availability
        if not torch.cuda.is_available():
            logger.warning("CUDA is not available, falling back to CPU")
            providers = ['CPUExecutionProvider']
            ctx_id = -1
        else:
            # Get CUDA device info
            cuda_device = torch.cuda.current_device()
            cuda_name = torch.cuda.get_device_name(cuda_device)
            cuda_memory = torch.cuda.get_device_properties(cuda_device).total_memory / 1024**3  # Convert to GB
            logger.info(f"Using CUDA device: {cuda_name} with {cuda_memory:.2f}GB memory")
            
            # Set CUDA providers with options
            providers = [
                ('CUDAExecutionProvider', {
                    'device_id': cuda_device,
                    'arena_extend_strategy': 'kNextPowerOfTwo',
                    'gpu_mem_limit': int(cuda_memory * 0.8 * 1024**3),  # Use 80% of GPU memory
                    'cudnn_conv_algo_search': 'EXHAUSTIVE',
                    'do_copy_in_default_stream': True
                }),
                'CPUExecutionProvider'
            ]
            ctx_id = cuda_device
            
            # Set CUDA environment variables
            os.environ['CUDA_VISIBLE_DEVICES'] = str(cuda_device)
            os.environ['OMP_NUM_THREADS'] = str(os.cpu_count())
            os.environ['MKL_NUM_THREADS'] = str(os.cpu_count())
            
            # Configure session options for CUDA
            session_options = onnxruntime.SessionOptions()
            session_options.graph_optimization_level = onnxruntime.GraphOptimizationLevel.ORT_ENABLE_ALL
            session_options.enable_mem_pattern = True
            session_options.enable_mem_reuse = True
            session_options.intra_op_num_threads = os.cpu_count()
            session_options.inter_op_num_threads = os.cpu_count()
            session_options.log_severity_level = 0
            session_options.log_verbosity_level = 0
            
            # Enable CUDA graph optimization
            session_options.add_session_config_entry('session.load_model_format', 'ONNX')
            session_options.add_session_config_entry('session.use_deterministic_compute', '0')
            session_options.add_session_config_entry('session.use_cuda_graph', '1')
            
    except Exception as e:
        logger.error(f"Error configuring CUDA: {e}")
        logger.warning("Falling back to CPU")
        providers = ['CPUExecutionProvider']
        ctx_id = -1
elif DEVICE_TYPE == "apple":
    try:
        available_providers = onnxruntime.get_available_providers()
        logger.info(f"Available ONNX Runtime providers: {available_providers}")
        
        if 'CoreMLExecutionProvider' in available_providers:
            providers = ['CoreMLExecutionProvider', 'CPUExecutionProvider']
            logger.info("Using CoreML provider for Metal acceleration")
        else:
            providers = ['CPUExecutionProvider']
            logger.warning("CoreML provider not available, falling back to CPU")
    except Exception as e:
        logger.error(f"Error checking ONNX Runtime providers: {e}")
        providers = ['CPUExecutionProvider']
    
    ctx_id = -1
    os.environ['OMP_NUM_THREADS'] = str(os.cpu_count())
    os.environ['MKL_NUM_THREADS'] = str(os.cpu_count())
    os.environ['OPENCV_VIDEOIO_MSMF_ENABLE_HW_TRANSFORMS'] = '1'
    os.environ['METAL_DEVICE'] = '0'
    os.environ['METAL_DEVICE_TYPE'] = 'GPU'
    os.environ['ORT_ENABLE_COREML'] = '1'
    os.environ['ORT_COREML_DEVICE'] = 'GPU'
else:
    providers = ['CPUExecutionProvider']
    ctx_id = -1

logger.info(f"Using device type: {DEVICE_TYPE}")
logger.info(f"Selected providers: {providers}")
logger.info(f"CPU cores: {os.cpu_count()}")

# Initialize face analyzer with appropriate settings
try:
    face_analyzer = FaceAnalysis(
        name='buffalo_l',
        providers=providers,
        session_options=session_options,
        root=str(CACHE_DIR)
    )
    face_analyzer.prepare(ctx_id=ctx_id, det_size=(640, 640))
    logger.info("Face analyzer initialized successfully")
except Exception as e:
    logger.error(f"Error initializing face analyzer: {e}")
    raise

# Load swapper model with appropriate settings
MODEL_PATH = MODELS_DIR / 'inswapper_128.onnx'
if not MODEL_PATH.exists():
    logger.info(f"Model not found at {MODEL_PATH}, downloading...")
    MODEL_PATH.parent.mkdir(exist_ok=True)
    raise RuntimeError(f"Model not found at {MODEL_PATH}. Please ensure the model is present in the models directory.")

try:
    swapper = insightface.model_zoo.get_model(
        str(MODEL_PATH),
        providers=providers,
        session_options=session_options
    )
    logger.info("Swapper model loaded successfully")
except Exception as e:
    logger.error(f"Error loading swapper model: {e}")
    raise

@app.post("/swap")
async def swap_faces(
    source_image: UploadFile = File(...),
    target_video: UploadFile = File(...)
):
    source_path = None
    target_path = None
    output_path = None
    temp_output_path = None
    try:
        cleanup_temp_files()
        
        source_path = MEDIA_DIR / source_image.filename
        target_path = MEDIA_DIR / target_video.filename
        output_path = MEDIA_DIR / "output.mp4"
        temp_output_path = MEDIA_DIR / "temp_output.mp4"

        logger.info(f"Processing files: source={source_path}, target={target_path}")
        
        if output_path and os.path.exists(output_path):
            os.remove(output_path)
            
        with open(source_path, "wb") as f:
            shutil.copyfileobj(source_image.file, f)
        with open(target_path, "wb") as f:
            shutil.copyfileobj(target_video.file, f)

        try:
            # Load source image and detect face
            source_img = cv2.imread(str(source_path))
            if source_img is None:
                raise HTTPException(
                    status_code=400, 
                    detail="Не удалось загрузить исходное изображение"
                )
            source_img = cv2.cvtColor(source_img, cv2.COLOR_BGR2RGB)
            
            source_faces = face_analyzer.get(source_img)
            if len(source_faces) == 0:
                raise HTTPException(
                    status_code=400, 
                    detail="Лицо не найдено на исходном изображении"
                )
            source_face = source_faces[0]

            # Initialize video capture
            target_vid = cv2.VideoCapture(str(target_path))
            if not target_vid.isOpened():
                raise HTTPException(
                    status_code=400, 
                    detail="Не удалось открыть видео"
                )
            
            width = int(target_vid.get(cv2.CAP_PROP_FRAME_WIDTH))
            height = int(target_vid.get(cv2.CAP_PROP_FRAME_HEIGHT))
            fps = target_vid.get(cv2.CAP_PROP_FPS)
            total_frames = int(target_vid.get(cv2.CAP_PROP_FRAME_COUNT))
            
            logger.info(f"Video parameters: width={width}, height={height}, fps={fps}, total_frames={total_frames}")
            
            # Create video writer
            fourcc = cv2.VideoWriter_fourcc(*'mp4v')
            out = cv2.VideoWriter(str(temp_output_path), fourcc, fps, (width, height))
            
            # Read all frames at once
            frames = []
            frame_count = 0
            faces_found = False
            
            logger.info("Reading video frames...")
            while True:
                ret, frame = target_vid.read()
                if not ret:
                    break
                frames.append(frame)
                frame_count += 1
                if frame_count % 100 == 0:
                    logger.info(f"Read {frame_count} frames")
            
            target_vid.release()
            logger.info(f"Total frames read: {frame_count}")
            
            if frame_count == 0:
                raise HTTPException(
                    status_code=400,
                    detail="Не удалось прочитать кадры из видео"
                )
            
            # Process all frames at once
            logger.info("Processing frames...")
            frames_rgb = [cv2.cvtColor(frame, cv2.COLOR_BGR2RGB) for frame in frames]
            
            # Process frames in chunks to manage memory
            chunk_size = 30  # Process 30 frames at a time
            for i in range(0, len(frames_rgb), chunk_size):
                chunk = frames_rgb[i:i + chunk_size]
                logger.info(f"Processing frames {i} to {i + len(chunk)}")
                
                for frame_rgb in chunk:
                    target_faces = face_analyzer.get(frame_rgb)
                    
                    if len(target_faces) > 0:
                        faces_found = True
                        for target_face in target_faces:
                            landmarks = target_face.kps
                            if landmarks is not None:
                                frame_rgb = swapper.get(frame_rgb, target_face, source_face, paste_back=True)
                    
                    frame_bgr = cv2.cvtColor(frame_rgb, cv2.COLOR_RGB2BGR)
                    out.write(frame_bgr)
            
            out.release()
            logger.info("Video processing completed")
            
            if not faces_found:
                raise HTTPException(
                    status_code=400, 
                    detail="Не удалось обнаружить лица в видео"
                )
            
            try:
                cmd = [
                    'ffmpeg',
                    '-i', str(temp_output_path),
                    '-i', str(target_path),
                    '-c:v', 'libx264',
                    '-preset', 'medium',
                    '-crf', '23',
                    '-c:a', 'aac',
                    '-b:a', '128k',
                    '-map', '0:v:0',
                    '-map', '1:a:0',
                    '-movflags', '+faststart',
                    '-shortest',
                    str(output_path)
                ]
                
                logger.info(f"Running FFmpeg command: {' '.join(cmd)}")
                
                process = subprocess.Popen(
                    cmd,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE
                )
                stdout, stderr = process.communicate()
                
                if process.returncode != 0:
                    stderr_output = stderr.decode() if stderr else "Unknown error"
                    logger.error(f"FFmpeg error: {stderr_output}")
                    raise HTTPException(
                        status_code=500, 
                        detail=f"Ошибка при добавлении звука: {stderr_output[:200]}..."
                    )
                
                logger.info("Audio successfully added to the video")
                
                # Verify output file exists and has content
                if not os.path.exists(output_path):
                    logger.error(f"Output file not found at {output_path}")
                    raise HTTPException(
                        status_code=500,
                        detail="Выходной файл не был создан после FFmpeg обработки"
                    )
                
                file_size = os.path.getsize(output_path)
                logger.info(f"Output file size: {file_size} bytes")
                
                if file_size == 0:
                    logger.error("Output file is empty")
                    raise HTTPException(
                        status_code=500,
                        detail="Выходной файл пуст после FFmpeg обработки"
                    )
                
                # Verify temp output file exists and has content
                if not os.path.exists(temp_output_path):
                    logger.error(f"Temp output file not found at {temp_output_path}")
                    raise HTTPException(
                        status_code=500,
                        detail="Временный выходной файл не был создан"
                    )
                
                temp_file_size = os.path.getsize(temp_output_path)
                logger.info(f"Temp output file size: {temp_file_size} bytes")
                
                if temp_file_size == 0:
                    logger.error("Temp output file is empty")
                    raise HTTPException(
                        status_code=500,
                        detail="Временный выходной файл пуст"
                    )
                
                return FileResponse(
                    path=output_path,
                    media_type="video/mp4",
                    filename="face_swap.mp4",
                    background=None, 
                    headers={
                        "Content-Disposition": "attachment; filename=face_swap.mp4",
                        "Content-Type": "video/mp4",
                        "Accept-Ranges": "bytes",
                        "Content-Length": str(file_size)
                    }
                )
                
            except HTTPException as http_exc:
                logger.error(f"HTTP Exception during video processing: {str(http_exc)}")
                raise http_exc
            except Exception as e:
                logger.error(f"Error during video processing: {str(e)}")
                raise HTTPException(
                    status_code=500,
                    detail=f"Ошибка при обработке видео: {str(e)}"
                )
            
        except HTTPException as http_exc:
            raise http_exc
        except Exception as e:
            cleanup_temp_files()
            logger.error(f"Error during face swap: {str(e)}")
            raise HTTPException(
                status_code=500,
                detail=f"Ошибка при обработке видео: {str(e)}"
            )
            
    except HTTPException as http_exc:
        raise http_exc
    except Exception as e:
        cleanup_temp_files()
        logger.error(f"Error during file handling: {str(e)}")
        raise HTTPException(
            status_code=400, 
            detail=f"Ошибка при обработке файлов: {str(e)}"
        )

@app.get("/health")
async def health_check():
    return {
        "status": "healthy",
        "device_type": DEVICE_TYPE,
        "providers": providers,
        "onnxruntime_version": onnxruntime.__version__,
        "torch_version": torch.__version__
    }

@app.get("/videos/{task_id}")
async def get_video_by_task_id(task_id: str):
    """
    Получить обработанное видео по task_id
    """
    video_path = MEDIA_DIR / f"{task_id}_output.mp4"
    
    if not os.path.exists(video_path):
        raise HTTPException(
            status_code=404, 
            detail=f"Видео с task_id {task_id} не найдено"
        )
    
    return FileResponse(
        video_path,
        media_type="video/mp4",
        filename=f"face_swap_{task_id}.mp4"
    ) 