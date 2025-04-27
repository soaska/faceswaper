# Базовый образ для CPU
FROM python:3.9-slim as base
WORKDIR /app
RUN apt-get update && apt-get install -y \
    libgl1-mesa-glx \
    libglib2.0-0 \
    wget \
    g++ \
    ffmpeg \
    && rm -rf /var/lib/apt/lists/*
RUN mkdir -p models && \
wget -O ./models/inswapper_128.onnx https://huggingface.co/ezioruan/inswapper_128.onnx/resolve/main/inswapper_128.onnx && \
ls -la ./models/

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY . .

RUN mkdir -p temp
CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "7860"]

# NVIDIA версия
FROM nvidia/cuda:11.8.0-cudnn8-runtime-ubuntu22.04 as nvidia
WORKDIR /app
RUN apt-get update && apt-get install -y \
    python3.9 \
    python3-pip \
    libgl1-mesa-glx \
    libglib2.0-0 \
    wget \
    g++ \
    ffmpeg \
    && rm -rf /var/lib/apt/lists/*

COPY requirements.txt .
RUN pip3 install --no-cache-dir -r requirements.txt

RUN mkdir -p models && \
    wget -O ./models/inswapper_128.onnx https://huggingface.co/ezioruan/inswapper_128.onnx/resolve/main/inswapper_128.onnx && \
    ls -la ./models/
COPY . .

RUN mkdir -p temp

CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "7860"]

# Apple Silicon версия
FROM python:3.9-slim as apple

WORKDIR /app

RUN apt-get update && apt-get install -y \
    libgl1-mesa-glx \
    libglib2.0-0 \
    wget \
    g++ \
    ffmpeg \
    libomp-dev \
    && rm -rf /var/lib/apt/lists/*

# Устанавливаем переменные окружения для оптимизации
ENV OMP_NUM_THREADS=4
ENV MKL_NUM_THREADS=4
ENV OPENCV_VIDEOIO_MSMF_ENABLE_HW_TRANSFORMS=1
ENV METAL_DEVICE=0
ENV METAL_DEVICE_TYPE=GPU
ENV ORT_ENABLE_COREML=1
ENV ORT_COREML_DEVICE=GPU

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt && \
    pip install --no-cache-dir onnxruntime

RUN mkdir -p models && \
    wget -O ./models/inswapper_128.onnx https://huggingface.co/ezioruan/inswapper_128.onnx/resolve/main/inswapper_128.onnx && \
    ls -la ./models/

COPY . .

RUN mkdir -p temp

CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "7860"] 