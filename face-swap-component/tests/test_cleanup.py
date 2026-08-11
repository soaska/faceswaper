from pathlib import Path

from temp_cleanup import cleanup_contents


def test_cleanup_media_on_startup_removes_all_entries(
    tmp_path: Path,
) -> None:
    media_directory = tmp_path / "media"
    media_directory.mkdir()
    (media_directory / "leftover.txt").write_text("test")
    abandoned_session = media_directory / "abandoned-session"
    abandoned_session.mkdir()
    (abandoned_session / "output.jpg").write_bytes(b"test")
    assert cleanup_contents(media_directory) == 2

    assert list(media_directory.iterdir()) == []
