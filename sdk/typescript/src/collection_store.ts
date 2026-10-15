/** Node-only private durable storage and an automatically released OS writer lock. */
import { createHash, randomUUID } from "node:crypto";
import { constants } from "node:fs";
import { lstat, mkdir, open, readdir, realpath, rename, unlink } from "node:fs/promises";
import type { FileHandle } from "node:fs/promises";
import { hostname } from "node:os";
import { createServer } from "node:net";
import { basename, dirname, join, resolve, sep } from "node:path";
import { ApiError } from "./errors.js";
import { object } from "./protocol.js";

export const STATE_LIMIT = 4 << 20;
const NOFOLLOW = constants.O_NOFOLLOW ?? 0;
const posix = process.platform !== "win32";
const EMPTY_HASH = createHash("sha256").digest("hex");
export { EMPTY_HASH };

export function fail(code: string): never { throw new ApiError("Saved collection work needs attention", { code }); }
export function requireValue(value: unknown, code: string): asserts value { if (!value) fail(code); }
export function safeID(value: unknown): value is string { return typeof value === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(value); }
export function clipID(value: unknown): value is string { return typeof value === "string" && /^[a-z0-9][a-z0-9_-]{0,63}$/.test(value); }
export function sha256(value: string | Uint8Array): string { return createHash("sha256").update(value).digest("hex"); }
export function canonical(value: unknown): string {
  if (Array.isArray(value)) return "[" + value.map(canonical).join(",") + "]";
  if (object(value)) return "{" + Object.keys(value).sort().filter(key => value[key] !== undefined).map(key => JSON.stringify(key) + ":" + canonical(value[key])).join(",") + "}";
  return JSON.stringify(value);
}

function privateMode(info: { mode: number; uid: number }): boolean {
  return !posix || ((info.mode & 0o077) === 0 && (typeof process.getuid !== "function" || info.uid === process.getuid()));
}
export async function privateDirectory(path: string, create = false): Promise<void> {
  if (create) { try { await mkdir(path, { mode: 0o700 }); } catch (error) { if (!object(error) || error.code !== "EEXIST") throw error; } }
  const info = await lstat(path);
  requireValue(info.isDirectory() && !info.isSymbolicLink() && privateMode(info), "unsafe_directory");
}
export async function projectDirectory(directory: string, create: boolean): Promise<string> {
  requireValue(typeof directory === "string" && directory.length > 0 && directory.length <= 4096 && !directory.includes("\0"), "invalid_directory");
  const absolute = resolve(directory);
  // Resolve caller-approved ancestors once; the project itself must be a real directory.
  const parent = await realpath(dirname(absolute));
  const root = join(parent, basename(absolute));
  await privateDirectory(root, create);
  for (const child of ["files", "parts"]) await privateDirectory(join(root, child), create);
  return root;
}
export function childPath(root: string, relative: string): string {
  requireValue(typeof relative === "string" && relative.length <= 512 && /^[a-zA-Z0-9_.\/-]+$/.test(relative) && !relative.split("/").includes("..") && !relative.startsWith("/"), "unsafe_path");
  const path = resolve(root, relative);
  requireValue(path.startsWith(root + sep), "unsafe_path");
  return path;
}
export async function regularFile(path: string, privateFile = true): Promise<FileHandle> {
  const before = await lstat(path);
  requireValue(before.isFile() && !before.isSymbolicLink() && before.nlink === 1 && (!privateFile || privateMode(before)), "unsafe_file");
  const file = await open(path, constants.O_RDONLY | NOFOLLOW);
  try {
    const info = await file.stat();
    requireValue(info.isFile() && info.nlink === 1 && info.ino === before.ino && info.dev === before.dev && (!privateFile || privateMode(info)), "unsafe_file");
    return file;
  } catch (error) { await file.close(); throw error; }
}
export async function existsRegular(path: string): Promise<boolean> {
  try {
    const info = await lstat(path);
    requireValue(info.isFile() && !info.isSymbolicLink() && info.nlink === 1 && privateMode(info), "unsafe_file");
    return true;
  } catch (error) { if (object(error) && error.code === "ENOENT") return false; throw error; }
}
export async function readJSON(path: string, maximum = STATE_LIMIT): Promise<unknown> {
  // Exclusive checkpoint publication may be killed between link and unlink.
  // Recover only its exact private temporary inode while the caller holds the OS lock.
  if (["state.json", "owner.json", "initialize.json", "writer.json"].includes(basename(path))) {
    const info = await lstat(path);
    if (info.isFile() && !info.isSymbolicLink() && privateMode(info) && info.nlink === 2) {
      const names = await readdir(dirname(path));
      requireValue(names.length <= 256, "unverified_checkpoint");
      const linked: string[] = [];
      for (const name of names.filter(name => /^\.checkpoint-[a-f0-9-]{36}$/.test(name))) {
        const candidate = join(dirname(path), name), temporary = await lstat(candidate);
        if (temporary.isFile() && !temporary.isSymbolicLink() && privateMode(temporary) && temporary.nlink === 2 && temporary.ino === info.ino && temporary.dev === info.dev) linked.push(candidate);
      }
      requireValue(linked.length === 1, "unverified_checkpoint");
      await unlink(linked[0]!); await syncDirectory(dirname(path));
    }
  }
  const file = await regularFile(path);
  try {
    requireValue((await file.stat()).size <= maximum, "state_too_large");
    return JSON.parse(await file.readFile("utf8")) as unknown;
  } catch (error) { if (error instanceof ApiError) throw error; fail("invalid_state"); }
  finally { await file.close(); }
}
export async function syncDirectory(path: string): Promise<void> {
  if (!posix) return;
  const file = await open(path, constants.O_RDONLY);
  try { await file.sync(); } finally { await file.close(); }
}
export async function atomicJSON(root: string, filename: string, value: unknown, exclusive = false): Promise<void> {
  await privateDirectory(root);
  const destination = childPath(root, filename);
  const data = Buffer.from(JSON.stringify(value));
  requireValue(data.length <= STATE_LIMIT, "state_too_large");
  const temporary = join(root, ".checkpoint-" + randomUUID());
  const file = await open(temporary, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | NOFOLLOW, 0o600);
  try { await file.writeFile(data); await file.sync(); } finally { await file.close(); }
  try {
    if (exclusive) {
      const { link } = await import("node:fs/promises");
      await link(temporary, destination);
      await unlink(temporary);
    } else {
      if (await existsRegular(destination)) { /* Refuse nonregular/symlink destinations above. */ }
      await rename(temporary, destination);
    }
    await syncDirectory(root);
  } finally { try { await unlink(temporary); } catch (error) { if (!object(error) || error.code !== "ENOENT") throw error; } }
}
export async function openPart(path: string, create: boolean): Promise<FileHandle> {
  const before = create ? undefined : await lstat(path);
  if (before) requireValue(before.isFile() && !before.isSymbolicLink() && before.nlink === 1 && privateMode(before), "unsafe_file");
  const flags = constants.O_RDWR | NOFOLLOW | (create ? constants.O_CREAT | constants.O_EXCL : 0);
  const file = await open(path, flags, 0o600);
  try {
    const info = await file.stat();
    requireValue(info.isFile() && info.nlink === 1 && privateMode(info) && (!before || (info.ino === before.ino && info.dev === before.dev)), "unsafe_file");
    return file;
  } catch (error) { await file.close(); throw error; }
}
export async function hashFile(path: string, expectedSize?: number, signal?: AbortSignal): Promise<{ hash: string; size: number }> {
  const file = await regularFile(path);
  try {
    const size = (await file.stat()).size;
    requireValue(Number.isSafeInteger(size) && (expectedSize === undefined || size === expectedSize), "file_tampered");
    const hash = createHash("sha256"), buffer = Buffer.alloc(64 << 10);
    let offset = 0;
    while (offset < size) {
      if (signal?.aborted) fail("cancelled");
      const count = await file.read(buffer, 0, Math.min(buffer.length, size - offset), offset);
      requireValue(count.bytesRead > 0, "file_tampered");
      hash.update(buffer.subarray(0, count.bytesRead)); offset += count.bytesRead;
    }
    return { hash: hash.digest("hex"), size };
  } finally { await file.close(); }
}

/** A kernel loopback bind fences writers and is released even after SIGKILL.
 * Hash collisions refuse safely; the private receipt additionally protects host scope.
 */
export async function writerLock(root: string): Promise<() => Promise<void>> {
  const port = 10000 + parseInt(sha256(root).slice(0, 8), 16) % 20000;
  const server = createServer(socket => socket.destroy());
  try {
    await new Promise<void>((yes, no) => { server.once("error", no); server.listen({ host: "127.0.0.1", port, exclusive: true }, () => { server.removeListener("error", no); yes(); }); });
  } catch { server.close(); fail("project_busy"); }
  server.unref();
  const path = childPath(root, "writer.json"), token = randomUUID();
  try {
    if (await existsRegular(path)) {
      const old = await readJSON(path, 4096);
      requireValue(object(old) && old.schema_version === 1 && old.host === hostname() && Number.isInteger(old.pid) && Number(old.pid) > 0 && Number(old.pid) <= 2147483647 && typeof old.token === "string" && /^[a-f0-9-]{36}$/.test(old.token), "unverified_writer_lock");
      try { process.kill(old.pid as number, 0); fail("project_busy"); }
      catch (error) { if (!object(error) || error.code !== "ESRCH") throw error; }
    }
    await atomicJSON(root, "writer.json", { schema_version: 1, pid: process.pid, host: hostname(), token }, false);
  } catch (error) { await new Promise<void>(done => server.close(() => done())); throw error; }
  return async () => {
    try {
      const current = await readJSON(path, 4096);
      requireValue(object(current) && current.token === token && current.pid === process.pid && current.host === hostname(), "writer_lock_changed");
      await unlink(path); await syncDirectory(root);
    } finally { await new Promise<void>(done => server.close(() => done())); }
  };
}
