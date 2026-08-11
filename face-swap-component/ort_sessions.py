from __future__ import annotations

from contextlib import contextmanager
from typing import Any, Iterator


def create_session_options(onnxruntime: Any, threads: int = 1) -> Any:
    """Create deterministic ORT thread settings without CPU affinity pinning."""
    options = onnxruntime.SessionOptions()
    options.intra_op_num_threads = threads
    options.inter_op_num_threads = threads
    options.execution_mode = onnxruntime.ExecutionMode.ORT_SEQUENTIAL
    return options


@contextmanager
def configured_insightface_sessions(
    model_zoo_module: Any,
    session_options: Any,
) -> Iterator[None]:
    """Make InsightFace use ORT options it does not forward itself.

    InsightFace 0.7.3 filters ``sess_options`` in ``get_model`` before it
    reaches ``PickableInferenceSession``. Model loading is single-threaded at
    application startup, so a scoped replacement keeps the workaround local.
    """
    original_session = model_zoo_module.PickableInferenceSession

    class ConfiguredInferenceSession(original_session):
        def __init__(self, model_path: str, **kwargs: Any) -> None:
            kwargs.setdefault("sess_options", session_options)
            super().__init__(model_path, **kwargs)

    model_zoo_module.PickableInferenceSession = ConfiguredInferenceSession
    try:
        yield
    finally:
        model_zoo_module.PickableInferenceSession = original_session
