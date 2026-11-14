import assert from 'node:assert/strict';
import { test } from 'node:test';
import { imageFileError, imagePayload, imageURL, readImageFile, MAX_IMAGE_BYTES, MAX_TOTAL_BYTES } from '../web/images.js';

const png = { name: 'screen.png', type: 'image/png', size: 100 };

test('attachment limits include existing draft images and accept supported formats', () => {
  for (const type of ['image/png', 'image/jpeg', 'image/gif', 'image/webp']) {
    assert.equal(imageFileError([{ ...png, type }]), null);
  }
  assert.match(imageFileError([{ ...png, type: 'image/svg+xml' }]), /Use PNG/);
  assert.match(imageFileError([{ ...png, size: 0 }]), /empty/);
  assert.equal(imageFileError([{ ...png, size: MAX_IMAGE_BYTES }]), null);
  assert.match(imageFileError([{ ...png, size: MAX_IMAGE_BYTES + 1 }]), /5 MB/);
  assert.equal(imageFileError([png], Array(3).fill(png)), null);
  assert.match(imageFileError([png], Array(4).fill(png)), /4 images/);
  assert.match(imageFileError([png], [{ size: MAX_TOTAL_BYTES }]), /10 MB/);
});

test('file reader produces image content and payload excludes local preview metadata', async () => {
  globalThis.FileReader = class {
    readAsDataURL() { this.result = 'data:image/png;base64,aGVsbG8='; this.onload(); }
  };
  const image = await readImageFile(png);
  assert.deepEqual(imagePayload([image]), [{ type: 'image', mimeType: 'image/png', data: 'aGVsbG8=' }]);
  assert.equal(imageURL(image), 'data:image/png;base64,aGVsbG8=');
  assert.equal(image.name, 'screen.png');
  assert.equal(imageURL({ mimeType: 'image/svg+xml', data: '<svg/>' }), null);
  delete globalThis.FileReader;
});

test('file reader rejects errors and empty results', async () => {
  globalThis.FileReader = class { readAsDataURL() { this.onerror(); } };
  await assert.rejects(readImageFile(png), /Could not read/);
  globalThis.FileReader = class { readAsDataURL() { this.result = ''; this.onload(); } };
  await assert.rejects(readImageFile(png), /Could not read/);
  delete globalThis.FileReader;
});
