from __future__ import annotations

import asyncio
import hmac
import logging
import os
import shutil
import subprocess
import time
import uuid
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Annotated

from fastapi import Depends, FastAPI, File, Header, HTTPException, Request, UploadFile
from fastapi.concurrency import run_in_threadpool
from fastapi.responses import FileResponse
from starlette.background import BackgroundTask

from pipeline import MediaValidationError

logging.basicConfig(
    level=os.getenv("LOG_LEVEL", "INFO"),
    format="%(asctime)s - %(name)s - %(levelname)s - %(message)s",
)
logger = logging.getLogger(__name__)

TEMP_DIR = Path(os.getenv("TEMP_DIR", "/temp"))
MODELS_DIR = TEMP_DIR / "models"
MEDIA_DIR = TEMP_DIR / "media"
CACHE_DIR = TEMP_DIR / "cache"
MODEL_NAME = "inswapper_128.onnx"
DEVICE_TYPE = os.getenv("DEVICE_TYPE", "cpu").lower()
API_KEY = os.getenv("FACE_SWAP_API_KEY", "")
MAX_CONCURRENT_JOBS = max(1, int(os.getenv("MAX_CONCURRENT_JOBS", "1")))
MAX_VIDEO_BYTES = int(os.getenv("MAX_VIDEO_BYTES", str(500 * 1024 * 1024)))
MAX_IMAGE_BYTES = int(os.getenv("MAX_IMAGE_BYTES", str(20 * 1024 * 1024)))
MAX_VIDEO_SECONDS = int(os.getenv("MAX_VIDEO_SECONDS", "300"))
MAX_FRAME_PIXELS = int(os.getenv("MAX_FRAME_PIXELS", str(1920 * 1080)))
MAX_IMAGE_PIXELS = int(os.getenv("MAX_IMAGE_PIXELS", "25000000"))
JOB_SEMAPHORE = asyncio.Semaphore(MAX_CONCURRENT_JOBS)

IMAGE_SUFFIXES = {".jpg", ".jpeg", ".png", ".webp"}
VIDEO_SUFFIXES = {".mp4", ".mov", ".mkv", ".webm"}


@asynccontextmanager
async def lifespan(app: FastAPI):
    if not API_KEY:
        raise RuntimeError("FACE_SWAP_API_KEY не задан")
    for directory in (TEMP_DIR, MODELS_DIR, MEDIA_DIR, CACHE_DIR):
        directory.mkdir(parents=True, exist_ok=True)
    model_path = MODELS_DIR / MODEL_NAME
    if not model_path.is_file():
        raise RuntimeError(f"Модель не найдена: {model_path}")

    cleanup_old_sessions(max_age_seconds=24 * 60 * 60)
    from runtime import create_processor

    app.state.processor = await run_in_threadpool(
        create_processor,
        model_path,
        CACHE_DIR,
        DEVICE_TYPE,
        MAX_VIDEO_SECONDS,
        MAX_FRAME_PIXELS,
        MAX_IMAGE_PIXELS,
    )
    logger.info("Face Swap Component запущен, устройство=%s", DEVICE_TYPE)
    yield


app = FastAPI(
    title="Face Swap API",
    version="3.0.0",
    lifespan=lifespan,
)


def require_api_key(
    x_api_key: Annotated[str | None, Header(alias="X-API-Key")] = None,
    authorization: Annotated[str | None, Header()] = None,
) -> None:
    supplied = x_api_key or ""
    if not supplied and authorization and authorization.startswith("Bearer "):
        supplied = authorization.removeprefix("Bearer ").strip()
    if not supplied or not hmac.compare_digest(supplied, API_KEY):
        raise HTTPException(status_code=401, detail="Неверный ключ API")


async def save_upload(
    upload: UploadFile,
    destination: Path,
    maximum_bytes: int,
) -> None:
    written = 0
    try:
        with destination.open("wb") as output:
            while chunk := await upload.read(1024 * 1024):
                written += len(chunk)
                if written > maximum_bytes:
                    raise HTTPException(status_code=413, detail="Файл превышает допустимый размер")
                output.write(chunk)
    finally:
        await upload.close()
    if written == 0:
        raise HTTPException(status_code=400, detail="Получен пустой файл")


def safe_suffix(filename: str | None, allowed: set[str]) -> str:
    suffix = Path(filename or "").suffix.lower()
    if suffix not in allowed:
        raise HTTPException(status_code=400, detail="Неподдерживаемый формат файла")
    return suffix


def cleanup_session(session_directory: Path) -> None:
    shutil.rmtree(session_directory, ignore_errors=True)


def cleanup_old_sessions(max_age_seconds: int) -> None:
    if not MEDIA_DIR.exists():
        return
    cutoff = time.time() - max_age_seconds
    for item in MEDIA_DIR.iterdir():
        try:
            if item.stat().st_mtime < cutoff:
                cleanup_session(item) if item.is_dir() else item.unlink()
        except FileNotFoundError:
            continue
        except OSError as error:
            logger.warning("Не удалось удалить старый файл %s: %s", item, error)


def result_response(
    output_path: Path,
    session_directory: Path,
    session_id: str,
    processing_seconds: int,
    workers_used: int,
    media_type: str,
    download_name: str,
) -> FileResponse:
    return FileResponse(
        output_path,
        media_type=media_type,
        filename=download_name,
        headers={
            "X-Session-ID": session_id,
            "X-Processing-Duration": str(processing_seconds),
            "X-Workers-Used": str(workers_used),
            "X-Device-Type": DEVICE_TYPE,
        },
        background=BackgroundTask(cleanup_session, session_directory),
    )


@app.post("/swap", dependencies=[Depends(require_api_key)])
async def swap_faces(
    request: Request,
    source_image: UploadFile = File(...),
    target_video: UploadFile = File(...),
):
    source_suffix = safe_suffix(source_image.filename, IMAGE_SUFFIXES)
    target_suffix = safe_suffix(target_video.filename, VIDEO_SUFFIXES)
    return await process_request(
        request,
        source_image,
        target_video,
        source_suffix,
        target_suffix,
        is_photo=False,
    )


@app.post("/swap-photo", dependencies=[Depends(require_api_key)])
async def swap_faces_photo(
    request: Request,
    source_image: UploadFile = File(...),
    target_image: UploadFile = File(...),
):
    source_suffix = safe_suffix(source_image.filename, IMAGE_SUFFIXES)
    target_suffix = safe_suffix(target_image.filename, IMAGE_SUFFIXES)
    return await process_request(
        request,
        source_image,
        target_image,
        source_suffix,
        target_suffix,
        is_photo=True,
    )


async def process_request(
    request: Request,
    source_upload: UploadFile,
    target_upload: UploadFile,
    source_suffix: str,
    target_suffix: str,
    *,
    is_photo: bool,
):
    session_id = uuid.uuid4().hex
    session_directory = MEDIA_DIR / session_id
    session_directory.mkdir(parents=True, exist_ok=False)
    source_path = session_directory / f"source{source_suffix}"
    target_path = session_directory / f"target{target_suffix}"
    output_path = session_directory / ("output.jpg" if is_photo else "output.mp4")
    started = time.monotonic()

    try:
        async with JOB_SEMAPHORE:
            await save_upload(source_upload, source_path, MAX_IMAGE_BYTES)
            await save_upload(
                target_upload,
                target_path,
                MAX_IMAGE_BYTES if is_photo else MAX_VIDEO_BYTES,
            )
            if await request.is_disconnected():
                raise HTTPException(status_code=499, detail="Клиент отключился")
            processor = request.app.state.processor
            if is_photo:
                result = await run_in_threadpool(
                    processor.process_image,
                    source_path,
                    target_path,
                    output_path,
                )
            else:
                result = await run_in_threadpool(
                    processor.process_video,
                    source_path,
                    target_path,
                    output_path,
                )
        duration = max(1, round(time.monotonic() - started))
        logger.info(
            "Сессия %s завершена за %s сек., кадров=%s",
            session_id,
            duration,
            result.frames_processed,
        )
        return result_response(
            output_path,
            session_directory,
            session_id,
            duration,
            result.workers_used,
            "image/jpeg" if is_photo else "video/mp4",
            "output.jpg" if is_photo else "output.mp4",
        )
    except HTTPException:
        cleanup_session(session_directory)
        raise
    except MediaValidationError as error:
        cleanup_session(session_directory)
        logger.info("Сессия %s отклонена: %s", session_id, error)
        raise HTTPException(status_code=400, detail=str(error)) from error
    except subprocess.CalledProcessError as error:
        cleanup_session(session_directory)
        logger.error("FFmpeg завершился с кодом %s, сессия %s", error.returncode, session_id)
        raise HTTPException(status_code=500, detail="Ошибка финальной обработки видео") from error
    except Exception as error:
        cleanup_session(session_directory)
        logger.exception("Сессия %s завершилась ошибкой", session_id)
        raise HTTPException(status_code=500, detail="Внутренняя ошибка обработки") from error


@app.get("/health")
async def health_check(request: Request):
    return {
        "status": "healthy",
        "device_type": DEVICE_TYPE,
        "model_loaded": hasattr(request.app.state, "processor"),
        "busy": JOB_SEMAPHORE.locked(),
        "max_concurrent_jobs": MAX_CONCURRENT_JOBS,
        "git_commit": os.getenv("GIT_COMMIT", "unknown"),
    }
