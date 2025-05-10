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
    """Clean up temporary media files"""
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

def ensure_model_exists():
    """Ensure the face swap model exists in the models directory"""
    model_path = MODELS_DIR / 'inswapper_128.onnx'
    if not model_path.exists():
        logger.error(f"Model not found at {model_path}")
        raise RuntimeError(f"Model not found at {model_path}. Please ensure the model is present in the models directory.")
    return model_path

def setup_cache():
    """Setup and verify cache directory"""
    try:
        # Set InsightFace cache directory
        os.environ['INSIGHTFACE_CACHE_DIR'] = str(CACHE_DIR)
        logger.info(f"Using InsightFace cache directory: {CACHE_DIR}")
        
        # Verify cache directory is writable
        test_file = CACHE_DIR / '.test'
        test_file.touch()
        test_file.unlink()
        
        return True
    except Exception as e:
        logger.error(f"Error setting up cache: {e}")
        return False

# Initialize cache and cleanup
setup_cache()
cleanup_temp_files()

# Configure GPU settings
DEVICE_TYPE = os.getenv("DEVICE_TYPE", "cpu").lower()
if DEVICE_TYPE == "nvidia":
    try:
        if not torch.cuda.is_available():
            logger.warning("CUDA is not available, falling back to CPU")
            providers = ['CPUExecutionProvider']
            ctx_id = -1
        else:
            cuda_device = torch.cuda.current_device()
            cuda_memory = torch.cuda.get_device_properties(cuda_device).total_memory / 1024**3  # Convert to GB
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
            
    except Exception as e:
        logger.error(f"Error configuring CUDA: {e}")
        providers = ['CPUExecutionProvider']
        ctx_id = -1
else:
    providers = ['CPUExecutionProvider']
    ctx_id = -1

# Initialize face analyzer
face_analyzer = FaceAnalysis(
    name='buffalo_l',
    providers=providers,
    session_options=session_options,
    root=str(CACHE_DIR)
)
face_analyzer.prepare(ctx_id=ctx_id, det_size=(640, 640))

# Load swapper model
MODEL_PATH = MODELS_DIR / 'inswapper_128.onnx'
if not MODEL_PATH.exists():
    raise RuntimeError(f"Model not found at {MODEL_PATH}")

swapper = insightface.model_zoo.get_model(
    str(MODEL_PATH),
    providers=providers,
    session_options=session_options
)

def process_video_chunk(chunk_data):
    """Process a chunk of video frames"""
    chunk_frames, source_face, start_idx = chunk_data
    processed_frames = []
    
    for frame in chunk_frames:
        frame_rgb = cv2.cvtColor(frame, cv2.COLOR_BGR2RGB)
        target_faces = face_analyzer.get(frame_rgb)
        
        if len(target_faces) > 0:
            for target_face in target_faces:
                if target_face.kps is not None:
                    frame_rgb = swapper.get(frame_rgb, target_face, source_face, paste_back=True)
        
        frame_bgr = cv2.cvtColor(frame_rgb, cv2.COLOR_RGB2BGR)
        processed_frames.append(frame_bgr)
    
    return start_idx, processed_frames

@app.post("/swap")
async def swap_faces(
    source_image: UploadFile = File(...),
    target_video: UploadFile = File(...)
):
    try:
        # Clean up any existing temporary files
        cleanup_temp_files()
        
        # Ensure model exists
        model_path = ensure_model_exists()
        
        # Save uploaded files
        source_path = MEDIA_DIR / source_image.filename
        target_path = MEDIA_DIR / target_video.filename
        output_path = MEDIA_DIR / "output.mp4"
        temp_output_path = MEDIA_DIR / "temp_output.mp4"
        
        with open(source_path, "wb") as f:
            shutil.copyfileobj(source_image.file, f)
        with open(target_path, "wb") as f:
            shutil.copyfileobj(target_video.file, f)

        # Process source image
        source_img = cv2.imread(str(source_path))
        if source_img is None:
            raise HTTPException(status_code=400, detail="Failed to load source image")
        source_img = cv2.cvtColor(source_img, cv2.COLOR_BGR2RGB)
        source_faces = face_analyzer.get(source_img)
        if len(source_faces) == 0:
            raise HTTPException(status_code=400, detail="No face detected in source image")
        source_face = source_faces[0]

        # Process target video
        target_vid = cv2.VideoCapture(str(target_path))
        if not target_vid.isOpened():
            raise HTTPException(status_code=400, detail="Failed to open video")
            
        width = int(target_vid.get(cv2.CAP_PROP_FRAME_WIDTH))
        height = int(target_vid.get(cv2.CAP_PROP_FRAME_HEIGHT))
        fps = target_vid.get(cv2.CAP_PROP_FPS)
        
        # Read all frames
        frames = []
        while True:
            ret, frame = target_vid.read()
            if not ret:
                break
            frames.append(frame)
        
        target_vid.release()
        
        if len(frames) == 0:
            raise HTTPException(status_code=400, detail="No frames read from video")
        
        # Calculate chunk size based on GPU memory
        if DEVICE_TYPE == "nvidia":
            # Estimate memory per frame (rough estimation)
            frame_memory = width * height * 3  # RGB bytes per frame
            # Calculate available memory (using 2.5GB as base)
            available_memory = 2.5 * 1024 * 1024 * 1024  # 2.5GB in bytes
            # Calculate how many frames we can process at once
            chunk_size = int(available_memory / (frame_memory * 2.5))  # 2.5x buffer for processing
            chunk_size = max(1, min(chunk_size, 30))  # Limit between 1 and 30 frames
        else:
            chunk_size = 30  # Default chunk size for CPU
        
        logger.info(f"Processing video with chunk size: {chunk_size} frames")
        
        # Split frames into chunks
        chunks = []
        for i in range(0, len(frames), chunk_size):
            chunk = frames[i:i + chunk_size]
            chunks.append((chunk, source_face, i))
        
        # Process chunks in parallel
        processed_frames = [None] * len(frames)
        with ThreadPoolExecutor(max_workers=os.cpu_count()) as executor:
            futures = [executor.submit(process_video_chunk, chunk) for chunk in chunks]
            for future in futures:
                start_idx, chunk_frames = future.result()
                processed_frames[start_idx:start_idx + len(chunk_frames)] = chunk_frames
        
        # Create video writer
        fourcc = cv2.VideoWriter_fourcc(*'mp4v')
        out = cv2.VideoWriter(str(temp_output_path), fourcc, fps, (width, height))
        
        # Write processed frames
        for frame in processed_frames:
            out.write(frame)
        
        out.release()
        
        # Add audio using FFmpeg
        cmd = [
            'ffmpeg',
            '-y',  # Force overwrite output file
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
        
        subprocess.run(cmd, check=True)
        
        return FileResponse(
            path=output_path,
            media_type="video/mp4",
            filename="face_swap.mp4"
        )
        
    except Exception as e:
        logger.error(f"Error during face swap: {str(e)}")
        raise HTTPException(status_code=500, detail=str(e))
    finally:
        # Clean up temporary files
        cleanup_temp_files()

@app.get("/health")
async def health_check():
    return {
        "status": "healthy",
        "device_type": DEVICE_TYPE,
        "providers": providers,
        "onnxruntime_version": onnxruntime.__version__,
        "torch_version": torch.__version__,
        "cache_directory": str(CACHE_DIR),
        "models_directory": str(MODELS_DIR)
    } 