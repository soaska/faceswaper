from __future__ import annotations

import logging
import shutil
from pathlib import Path

logger = logging.getLogger(__name__)


def remove_directory(directory: Path) -> None:
    shutil.rmtree(directory, ignore_errors=True)


def cleanup_contents(directory: Path) -> int:
    if not directory.exists():
        return 0
    removed = 0
    for item in directory.iterdir():
        try:
            remove_directory(item) if item.is_dir() else item.unlink()
            removed += 1
        except FileNotFoundError:
            continue
        except OSError as error:
            logger.warning(
                "Не удалось удалить временный файл при запуске %s: %s",
                item,
                error,
            )
    return removed
