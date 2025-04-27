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

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

app = FastAPI(title="Face Swap API")

TEMP_DIR = Path("temp")
if TEMP_DIR and os.path.exists(Path('temp/output.mp4')):
    shutil.rmtree(Path('temp/output.mp4'))
TEMP_DIR.mkdir(exist_ok=True)

DEVICE_TYPE = os.getenv("DEVICE_TYPE", "cpu").lower()
if DEVICE_TYPE == "nvidia":
    providers = ['CUDAExecutionProvider', 'CPUExecutionProvider']
    ctx_id = 0
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
    # Отимизации для Apple Silicon
    os.environ['OMP_NUM_THREADS'] = str(os.cpu_count())
    os.environ['MKL_NUM_THREADS'] = str(os.cpu_count())
    # Оптимизации OpenCV для Metal
    os.environ['OPENCV_VIDEOIO_MSMF_ENABLE_HW_TRANSFORMS'] = '1'
    # Оптимизации для Metal
    os.environ['METAL_DEVICE'] = '0'
    os.environ['METAL_DEVICE_TYPE'] = 'GPU'
    # Оптимизации ONNX Runtime для Apple Silicon
    os.environ['ORT_ENABLE_COREML'] = '1'
    os.environ['ORT_COREML_DEVICE'] = 'GPU'
else:
    providers = ['CPUExecutionProvider']
    ctx_id = -1

logger.info(f"Using device type: {DEVICE_TYPE}")
logger.info(f"Selected providers: {providers}")
logger.info(f"CPU cores: {os.cpu_count()}")

session_options = onnxruntime.SessionOptions()
if DEVICE_TYPE == "apple":
    # Оптимизации графа для Apple Silicon
    session_options.graph_optimization_level = onnxruntime.GraphOptimizationLevel.ORT_ENABLE_ALL
    # Оптимизации памяти
    session_options.enable_mem_pattern = True
    session_options.enable_mem_reuse = True
    # Устанавливаем количество потоков
    session_options.intra_op_num_threads = os.cpu_count()
    session_options.inter_op_num_threads = os.cpu_count()
    # Включаем логирование для отладки
    session_options.log_severity_level = 0
    session_options.log_verbosity_level = 0

try:
    face_analyzer = FaceAnalysis(name='buffalo_l', providers=providers, session_options=session_options)
    face_analyzer.prepare(ctx_id=ctx_id, det_size=(640, 640))
    logger.info("Face analyzer initialized successfully")
except Exception as e:
    logger.error(f"Error initializing face analyzer: {e}")
    raise

MODEL_PATH = os.path.join('models', 'inswapper_128.onnx')
if not os.path.exists(MODEL_PATH):
    raise RuntimeError(f"Model not found at {MODEL_PATH}")

try:
    swapper = insightface.model_zoo.get_model(MODEL_PATH, providers=providers, session_options=session_options)
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
        # Сохраняем загруженные файлы во временную директорию
        source_path = TEMP_DIR / source_image.filename
        target_path = TEMP_DIR / target_video.filename
        output_path = TEMP_DIR / "output.mp4"
        temp_output_path = TEMP_DIR / "temp_output.mp4"

        logger.info(f"Processing files: source={source_path}, target={target_path}")
        
        if output_path or os.path.exists(output_path):
            os.remove(output)
        # Сохраняем файлы
        with open(source_path, "wb") as f:
            shutil.copyfileobj(source_image.file, f)
        with open(target_path, "wb") as f:
            shutil.copyfileobj(target_video.file, f)

        # Выполняем замену лиц
        try:
            source_img = cv2.imread(str(source_path))
            if source_img is None:
                raise HTTPException(
                    status_code=400, 
                    detail="Не удалось загрузить исходное изображение"
                )
            source_img = cv2.cvtColor(source_img, cv2.COLOR_BGR2RGB)
            
            # Находим лицо на исходном изображении
            source_faces = face_analyzer.get(source_img)
            if len(source_faces) == 0:
                raise HTTPException(
                    status_code=400, 
                    detail="Лицо не найдено на исходном изображении"
                )
            source_face = source_faces[0]
            
            target_vid = cv2.VideoCapture(str(target_path))
            if not target_vid.isOpened():
                raise HTTPException(
                    status_code=400, 
                    detail="Не удалось открыть видео"
                )
            
            width = int(target_vid.get(cv2.CAP_PROP_FRAME_WIDTH))
            height = int(target_vid.get(cv2.CAP_PROP_FRAME_HEIGHT))
            fps = target_vid.get(cv2.CAP_PROP_FPS)
            
            logger.info(f"Video parameters: width={width}, height={height}, fps={fps}")
            
            # VideoWriter для сохранения результата (без звука)
            fourcc = cv2.VideoWriter_fourcc(*'mp4v')
            out = cv2.VideoWriter(str(temp_output_path), fourcc, fps, (width, height))
            
            frame_count = 0
            faces_found = False
            
            while True:
                ret, frame = target_vid.read()
                if not ret:
                    break
                
                frame_count += 1
                if frame_count % 10 == 0:
                    logger.info(f"Processing frame {frame_count}")
                
                # Конвертируем кадр в RGB
                frame_rgb = cv2.cvtColor(frame, cv2.COLOR_BGR2RGB)
                
                # Находим лица на кадре
                target_faces = face_analyzer.get(frame_rgb)
                
                if len(target_faces) > 0:
                    faces_found = True
                    # Заменяем каждое лицо на кадре
                    for target_face in target_faces:
                        # Получаем landmarks для лица
                        landmarks = target_face.kps
                        if landmarks is not None:
                            # Выполняем замену лица
                            frame_rgb = swapper.get(frame_rgb, target_face, source_face, paste_back=True)
                
                # Конвертируем обратно в BGR
                frame_bgr = cv2.cvtColor(frame_rgb, cv2.COLOR_RGB2BGR)
                out.write(frame_bgr)
            
            # Освобождаем ресурсы
            target_vid.release()
            out.release()
            
            logger.info(f"Processing completed. Total frames: {frame_count}")
            
            # Проверяем, нашли ли мы вообще лица в видео
            if frame_count > 0 and not faces_found:
                raise HTTPException(
                    status_code=400, 
                    detail="Не удалось обнаружить лица в видео"
                )
            
            # Добавляем звук из исходного видео
            try:
                cmd = [
                    'ffmpeg',
                    '-i', str(temp_output_path),  # Видео без звука
                    '-i', str(target_path),       # Исходное видео со звуком
                    '-c:v', 'libx264',           # Используем H.264 кодек
                    '-preset', 'medium',         # Баланс между качеством и скоростью
                    '-crf', '23',                # Качество видео (0-51, меньше - лучше)
                    '-c:a', 'aac',               # Кодируем звук в AAC
                    '-b:a', '128k',              # Битрейт аудио
                    '-map', '0:v:0',             # Берем видео из первого файла
                    '-map', '1:a:0',             # Берем звук из второго файла
                    '-movflags', '+faststart',   # Оптимизация для веб-воспроизведения
                    '-shortest',                 # Используем длительность самого короткого потока
                    str(output_path)             # Выходной файл
                ]
                
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
            except Exception as e:
                logger.error(f"Error adding audio: {str(e)}")
                raise HTTPException(
                    status_code=500, 
                    detail=f"Ошибка при добавлении звука к видео: {str(e)}"
                )
            
            # Очищаем временные файлы
            if source_path and os.path.exists(source_path):
                os.remove(source_path)
            if target_path and os.path.exists(target_path):
                os.remove(target_path)
            if temp_output_path and os.path.exists(temp_output_path):
                os.remove(temp_output_path)
            
            # Проверяем, что выходной файл существует и имеет размер
            if not os.path.exists(output_path):
                raise HTTPException(
                    status_code=500, 
                    detail="Выходной файл не был создан"
                )
            
            file_size = os.path.getsize(output_path)
            if file_size == 0:
                raise HTTPException(
                    status_code=500, 
                    detail="Выходной файл пуст"
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
            raise http_exc
        except Exception as e:
            logger.error(f"Error during face swap: {str(e)}")
            # Очищаем временные файлы в случае ошибки
            if source_path and os.path.exists(source_path):
                os.remove(source_path)
            if target_path and os.path.exists(target_path):
                os.remove(target_path)
            if temp_output_path and os.path.exists(temp_output_path):
                os.remove(temp_output_path)
            if output_path and os.path.exists(output_path):
                os.remove(output_path)
            raise HTTPException(
                status_code=500, 
                detail=f"Ошибка при замене лиц: {str(e)}"
            )
            
    except HTTPException as http_exc:
        raise http_exc
    except Exception as e:
        logger.error(f"Error during file handling: {str(e)}")
        # Очищаем временные файлы в случае ошибки
        if source_path and os.path.exists(source_path):
            os.remove(source_path)
        if target_path and os.path.exists(target_path):
            os.remove(target_path)
        if temp_output_path and os.path.exists(temp_output_path):
            os.remove(temp_output_path)
        if output_path and os.path.exists(output_path):
            os.remove(output_path)
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
    # Формируем путь к возможному файлу
    video_path = TEMP_DIR / f"{task_id}_output.mp4"
    
    # Проверяем существование файла
    if not os.path.exists(video_path):
        raise HTTPException(
            status_code=404, 
            detail=f"Видео с task_id {task_id} не найдено"
        )
    
    # Возвращаем видео
    return FileResponse(
        video_path,
        media_type="video/mp4",
        filename=f"face_swap_{task_id}.mp4"
    ) 