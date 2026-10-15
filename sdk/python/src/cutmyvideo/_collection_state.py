"""Private, bounded state and exclusive publication for one durable project."""
from __future__ import annotations

import errno
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import secrets
import stat
from typing import Any, Callable

LIMIT = 4 << 20


def sync_directory(path: Path) -> None:
    if os.name != "posix":
        return
    fd = os.open(path, os.O_RDONLY | getattr(os, "O_DIRECTORY", 0))
    try:
        try:
            os.fsync(fd)
        except OSError as error:
            if error.errno not in (errno.EINVAL, getattr(errno, "ENOTSUP", errno.EINVAL)):
                raise
    finally:
        os.close(fd)


def regular(path: Path, *, private: bool = False, single_link: bool = False) -> os.stat_result:
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode):
        raise ValueError("Project file must be regular and cannot be a symlink")
    if single_link and info.st_nlink != 1:
        raise ValueError("Mutable project file must have exactly one hard link")
    if private and os.name == "posix" and (info.st_mode & 0o077 or info.st_uid != os.getuid()):
        raise ValueError("Project state and owner files must be private0600")
    return info


def single_link_fd(fd: int) -> None:
    info = os.fstat(fd)
    if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
        raise ValueError("Mutable project file must have exactly one hard link")


def secure_open(path: Path, flags: int, mode: int = 0o600, *, single_link: bool = False) -> int:
    before = None
    if path.exists() or path.is_symlink():
        before = regular(path, single_link=single_link)
    if single_link and flags & os.O_TRUNC:
        raise ValueError("Mutable file must be opened and verified before truncation")
    fd = os.open(path, flags | getattr(os, "O_NOFOLLOW", 0), mode)
    try:
        if single_link:
            single_link_fd(fd)
            identity = os.fstat(fd)
            if before is not None and (identity.st_dev, identity.st_ino) != (before.st_dev, before.st_ino):
                raise ValueError("Mutable project file identity changed")
        return fd
    except BaseException:
        os.close(fd)
        raise


def hash_file(path: Path, length: int | None = None, *, check: Callable[[], Any] | None = None,
              single_link: bool = False) -> tuple[int, str]:
    before = regular(path, single_link=single_link)
    fd = secure_open(path, os.O_RDONLY, single_link=single_link)
    digest, size = hashlib.sha256(), 0
    with os.fdopen(fd, "rb") as source:
        identity = os.fstat(source.fileno())
        if (identity.st_dev, identity.st_ino) != (before.st_dev, before.st_ino):
            raise ValueError("Project file identity changed")
        while length is None or size < length:
            if check is not None:
                check()
            chunk = source.read(64 << 10 if length is None else min(64 << 10, length - size))
            if not chunk:
                break
            digest.update(chunk)
            size += len(chunk)
        after = os.fstat(source.fileno())
        if single_link:
            single_link_fd(source.fileno())
        if (before.st_size, before.st_mtime_ns) != (after.st_size, after.st_mtime_ns):
            raise ValueError("Project file changed while verifying")
    if length is not None and size != length:
        raise ValueError("Project partial file is shorter than its checkpoint")
    return size, digest.hexdigest()


class Project:
    def __init__(self, directory: str | os.PathLike[str]):
        requested = Path(directory).absolute()
        if requested.is_symlink():
            raise ValueError("Project directory cannot be a symlink")
        # The caller may approve ancestor symlinks; resolve that root once.
        self.root = requested.parent.resolve() / requested.name
        self.root.mkdir(mode=0o700, exist_ok=True)
        if not self.root.is_dir() or self.root.is_symlink():
            raise ValueError("Project requires a real directory")
        if os.name == "posix":
            if self.root.stat().st_uid != os.getuid():
                raise ValueError("Project must belong to the current user")
            os.chmod(self.root, 0o700)
        self.fd: int | None = None

    def __enter__(self) -> Project:
        self.fd = secure_open(self.root / ".lock", os.O_CREAT | os.O_RDWR)
        try:
            if os.name == "posix":
                import fcntl
                fcntl.flock(self.fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            else:
                import msvcrt
                if os.fstat(self.fd).st_size == 0:
                    os.write(self.fd, b"\0")
                    os.fsync(self.fd)
                os.lseek(self.fd, 0, os.SEEK_SET)
                msvcrt.locking(self.fd, msvcrt.LK_NBLCK, 1)
        except OSError:
            os.close(self.fd)
            self.fd = None
            raise ValueError("Project has another active writer") from None
        try:
            for name in ("files", "parts"):
                path = self.root / name
                if path.is_symlink() or path.exists() and not path.is_dir():
                    raise ValueError("Project storage directory cannot be a symlink")
                path.mkdir(mode=0o700, exist_ok=True)
                if os.name == "posix":
                    os.chmod(path, 0o700)
        except BaseException:
            self.__exit__()
            raise
        return self

    def __exit__(self, *_: Any) -> None:
        if self.fd is not None:
            os.close(self.fd)
            self.fd = None

    def path(self, relative: str) -> Path:
        if (not isinstance(relative, str) or "\\" in relative or "\0" in relative
                or PurePosixPath(relative).is_absolute() or any(piece in ("", ".", "..") for piece in relative.split("/"))):
            raise ValueError("Project path is unsafe")
        path = self.root
        for piece in relative.split("/"):
            path = path / piece
            if path.is_symlink():
                raise ValueError("Project path cannot traverse a symlink")
        if path.exists():
            regular(path)
        return path

    def read(self, name: str) -> dict[str, Any]:
        path = self.path(name)
        regular(path, private=True)
        fd = secure_open(path, os.O_RDONLY)
        with os.fdopen(fd, "rb") as stream:
            blob = stream.read(LIMIT + 1)
        if len(blob) > LIMIT:
            raise ValueError("Project JSON exceeds4MiB")
        def pairs(items: list[tuple[str, Any]]) -> dict[str, Any]:
            result = {}
            for key, value in items:
                if key in result:
                    raise ValueError("Project JSON has repeated keys")
                result[key] = value
            return result
        def bad_number(_: str) -> None:
            raise ValueError("Project JSON has nonfinite numbers")
        try:
            data = json.loads(blob, object_pairs_hook=pairs, parse_constant=bad_number)
        except (UnicodeDecodeError, json.JSONDecodeError):
            raise ValueError("Project JSON is corrupt") from None
        if not isinstance(data, dict):
            raise ValueError("Project JSON must be an object")
        return data

    def write(self, name: str, data: dict[str, Any], *, exclusive: bool = False) -> None:
        body = json.dumps(data, ensure_ascii=False, allow_nan=False, separators=(",", ":")).encode()
        if len(body) > LIMIT:
            raise ValueError("Project JSON exceeds4MiB")
        target = self.path(name)
        temporary = self.root / (".state-" + secrets.token_hex(16) + ".tmp")
        try:
            fd = secure_open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL)
            with os.fdopen(fd, "wb") as stream:
                stream.write(body)
                stream.flush()
                os.fsync(stream.fileno())
            if exclusive:
                os.link(temporary, target)
                temporary.unlink()
            else:
                self.path(name)
                os.replace(temporary, target)
            sync_directory(self.root)
        finally:
            if temporary.exists():
                temporary.unlink()

    def publish(self, part: str, final: str, size: int, digest: str, *, check: Callable[[], Any] | None = None) -> None:
        pending, target = self.path(part), self.path(final)
        if target.exists():
            # Only this PREPARED receipt authorizes adopting a published file.
            target_info = regular(target)
            if target_info.st_nlink == 2:
                pending_info = regular(pending)
                if (pending_info.st_nlink != 2 or (pending_info.st_dev, pending_info.st_ino) != (target_info.st_dev, target_info.st_ino)):
                    raise ValueError("Prepared publication has an unrelated hard link")
            elif target_info.st_nlink != 1:
                raise ValueError("Prepared publication has unexpected hard links")
            if hash_file(target, check=check) != (size, digest):
                raise ValueError("Existing final file does not match prepared receipt")
        else:
            if hash_file(pending, check=check, single_link=True) != (size, digest):
                raise ValueError("Prepared partial file has changed")
            os.link(pending, target)
        sync_directory(target.parent)
