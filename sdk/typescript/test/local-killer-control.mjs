// An isolated Windows-branch control; only this process's children are signalled.
import assert from 'node:assert/strict';
import childProcess from 'node:child_process';
import { once } from 'node:events';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { syncBuiltinESMExports } from 'node:module';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const root = await mkdtemp(join(tmpdir(), 'cutmy-owned-killers-'));
const nativeSpawn = childProcess.spawn, children = [], helpers = [];
let controller, mode, watchdog;
try {
  const target = join(root, 'target.mjs'), helper = join(root, 'helper.mjs');
  await writeFile(target, `console.log(JSON.stringify({outputs:[{path:'/published/earlier.mp4',size_bytes:1,actual_start_ms:0,actual_end_ms:1,segments:[],track_manifest:[]}]}));setInterval(()=>{},1000);`);
  await writeFile(helper, `const forced=process.argv.includes('/F');if(process.env.CUTMY_KILLER_MODE==='grace'||process.env.CUTMY_KILLER_MODE==='force'&&forced){try{process.kill(Number(process.argv[3]),'SIGKILL')}catch{}}setInterval(()=>{},1000);`);
  childProcess.spawn = (file, args, options) => {
    const isHelper = file.toLowerCase().endsWith('taskkill.exe');
    const child = isHelper ? nativeSpawn(process.execPath, [helper, ...args], { ...options, env: { ...process.env, CUTMY_KILLER_MODE: mode } }) : nativeSpawn(file, args, options);
    children.push(child);
    if (isHelper) helpers.push({ child, forced: args.includes('/F'), tree: args.includes('/T') });
    else child.stdout.once('data', () => setTimeout(() => controller.abort(), 10));
    return child;
  };
  syncBuiltinESMExports();
  Object.defineProperty(process, 'platform', { value: 'win32' });
  const { LocalCutmy } = await import(process.argv[2]);
  watchdog = setTimeout(() => { for (const child of children) { try { child.kill('SIGKILL'); } catch {} } }, 8000);
  for (mode of ['grace', 'force', 'fallback']) {
    controller = new AbortController();
    const before = helpers.length, started = performance.now();
    const local = new LocalCutmy({ binary: process.execPath, binaryArgs: [target], terminationGraceMs: 100 });
    await assert.rejects(local.executePlan({ version: 1, input: 'owned-input', ranges: [{ start_ms: 0, end_ms: 1 }] }, root, { signal: controller.signal, timeoutMs: 5000 }), error => error.code === 'cancelled' && error.result?.outputs[0].path === '/published/earlier.mp4');
    assert(performance.now() - started < 4000, 'Owned helper cleanup must remain bounded');
    const created = helpers.slice(before);
    assert(created.every(record => record.tree), 'Every request must preserve tree termination');
    assert.equal(created[0].forced, false);
    if (mode === 'grace') {
      // A slow native helper startup may legitimately require force escalation.
      assert(created.length === 1 || created.length === 2);
      if (created.length === 2) assert.equal(created[1].forced, true);
    } else {
      assert.equal(created.length, 2);
      assert.equal(created[1].forced, true);
    }
    assert(created.every(record => record.child.exitCode !== null || record.child.signalCode !== null), 'No helper may remain live after local settlement');
  }
  console.log(JSON.stringify({ ok: true, scenarios: 3, helpers: helpers.length, primary_code: 'cancelled', partial_output_retained: true }));
} finally {
  clearTimeout(watchdog);
  for (const child of children) {
    if (child.exitCode === null && child.signalCode === null) {
      const closed = once(child, 'close'); child.kill('SIGKILL'); await closed;
    }
  }
  await rm(root, { recursive: true, force: true });
}
