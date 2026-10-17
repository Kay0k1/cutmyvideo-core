import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';
import test from 'node:test';

const { ApiError, exportPortableProject, importPortableProject } = await import(process.env.CUTMY_SDK_JS_ENTRY
  ? pathToFileURL(process.env.CUTMY_SDK_JS_ENTRY).href : '../dist/index.js');
const corpus = JSON.parse(await readFile(process.env.CUTMY_PROJECT_CONTRACTS
  ?? new URL('../../../api/testdata/project-contracts.json', import.meta.url), 'utf8'));
assert.equal(corpus.schema_version, 1);
for (const group of ['collection_cases', 'portable_cases']) {
  assert(corpus[group].length > 0);
  assert.equal(new Set(corpus[group].map(value => value.id)).size, corpus[group].length);
}
const invalidEdit = error => error instanceof TypeError || error instanceof ApiError;

test('shared collection examples check SDK structure, leaving media and budgets to the core', async t => {
  for (const value of corpus.collection_cases) await t.test(value.id, () => {
    assert.equal(typeof value.sdk_valid, 'boolean');
    const original = structuredClone(value);
    if (value.sdk_valid) {
      const document = exportPortableProject(value.request);
      assert.deepEqual(document.plan, value.request.plan);
      for (const key of ['clip_ids', 'rendition']) {
        if (Object.hasOwn(value.request, key)) assert.deepEqual(document[key], value.request[key]);
        else assert(!Object.hasOwn(document, key));
      }
    } else assert.throws(() => exportPortableProject(value.request), invalidEdit);
    assert.deepEqual(value, original);
  });
});

test('shared portable examples require a selected source and reject unknown edit fields', async t => {
  for (const value of corpus.portable_cases) await t.test(value.id, () => {
    const original = structuredClone(value);
    if (Object.hasOwn(value, 'export_valid')) {
      assert.equal(typeof value.export_valid, 'boolean');
      if (value.export_valid) assert.deepEqual(exportPortableProject(value.request, value.source), value.expected_document);
      else assert.throws(() => exportPortableProject(value.request, value.source), invalidEdit);
    }
    if (Object.hasOwn(value, 'import_valid')) {
      assert.equal(typeof value.import_valid, 'boolean');
      if (value.import_valid) assert.deepEqual(importPortableProject(value.document, value.trusted_source), value.expected_request);
      else assert.throws(() => importPortableProject(value.document, value.trusted_source), invalidEdit);
    }
    assert.deepEqual(value, original);
  });
});
