// Node writer locks listen on 127.0.0.1 at a port hashed from the project path,
// and a port held by anything else is reported as project_busy
// (docs/sdk-collections.md). node --test runs test files in parallel
// processes, so two random temporary projects could hash onto one port and a
// lock from another file would make a fresh project look busy. Every test file
// that takes writer locks owns one residue class of ports, a port is handed out
// once per process, and only while nothing listens on it.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtemp, realpath, rm } from 'node:fs/promises';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { basename, dirname, join, resolve } from 'node:path';

const owners = ['collections', 'sources', 'media'], issued = new Set();

/** The loopback port the SDK writer lock binds for this project directory. */
export async function lockPort(project) {
  const absolute = resolve(project), root = join(await realpath(dirname(absolute)), basename(absolute));
  return 10000 + parseInt(createHash('sha256').update(root).digest('hex').slice(0, 8), 16) % 20000;
}

export async function listenLoopback(server, port) {
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen({ host: '127.0.0.1', port, exclusive: true }, () => { server.removeListener('error', reject); resolve(); });
  });
}

async function free(port) {
  const server = createServer();
  try { await listenLoopback(server, port); } catch { return false; }
  await new Promise(resolve => server.close(resolve));
  return true;
}

/** A new temporary directory whose project (`project` inside it, or the directory itself) has a lock port reserved for `owner`. */
export async function lockSafeTempDirectory(owner, prefix, project) {
  const slot = owners.indexOf(owner);
  assert(slot >= 0, 'Unknown writer lock test file: ' + owner);
  for (let attempt = 0; attempt < 1000; attempt++) {
    const directory = await mkdtemp(join(tmpdir(), prefix));
    const port = await lockPort(project ? join(directory, project) : directory);
    if (port % owners.length === slot && !issued.has(port) && await free(port)) { issued.add(port); return directory; }
    await rm(directory, { recursive: true, force: true });
  }
  throw new Error('No temporary directory with a free writer lock port');
}
