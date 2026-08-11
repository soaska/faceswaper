from __future__ import annotations

from pathlib import Path
from typing import Any, Callable


def create_face_analyzer(
    factory: Callable[..., Any],
    providers: list[str],
    cache_directory: Path,
) -> Any:
    # Detection finds the face; recognition supplies the normed_embedding
    # required by INSwapper.get(). Loading detection alone makes every valid
    # swap fail after face detection.
    return factory(
        name="buffalo_l",
        allowed_modules=["detection", "recognition"],
        providers=providers,
        root=str(cache_directory),
    )
