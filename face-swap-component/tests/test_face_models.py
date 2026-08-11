from pathlib import Path

from face_models import create_face_analyzer


def test_analyzer_loads_recognition_embedding_for_swapper():
    captured = {}

    def factory(**kwargs):
        captured.update(kwargs)
        return object()

    create_face_analyzer(factory, ["CUDAExecutionProvider"], Path("/models"))

    assert captured["allowed_modules"] == ["detection", "recognition"]
    assert captured["providers"] == ["CUDAExecutionProvider"]
    assert captured["root"] == "/models"
