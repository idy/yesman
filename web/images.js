// Keep file limits in step with internal/agent/images.go.
export const IMAGE_TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp'];
export const IMAGE_ACCEPT = IMAGE_TYPES.join(',');
export const MAX_IMAGES = 4;
export const MAX_IMAGE_BYTES = 5 * 1024 * 1024;
export const MAX_TOTAL_BYTES = 10 * 1024 * 1024;

export function imageFileError(files, existing = []) {
  if (files.length + existing.length > MAX_IMAGES) return 'Attach at most 4 images.';
  let total = existing.reduce((n, image) => n + image.size, 0);
  for (const file of files) {
    if (!IMAGE_TYPES.includes(file.type)) return 'Use PNG, JPEG, GIF or WebP images.';
    if (!file.size) return 'This image is empty.';
    if (file.size > MAX_IMAGE_BYTES) return 'Each image must be 5 MB or smaller.';
    total += file.size;
  }
  return total > MAX_TOTAL_BYTES ? 'Images must be 10 MB or smaller in total.' : null;
}

export function readImageFile(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(new Error('Could not read the image. Try selecting it again.'));
    reader.onabort = reader.onerror;
    reader.onload = () => {
      const data = String(reader.result).split(',')[1];
      if (!data) return reader.onerror();
      resolve({ type: 'image', mimeType: file.type, data, name: file.name || 'Image', size: file.size });
    };
    reader.readAsDataURL(file);
  });
}

export function imageURL(image) {
  // Never turn an arbitrary session MIME or URL into active document content.
  return IMAGE_TYPES.includes(image.mimeType) && typeof image.data === 'string'
    ? `data:${image.mimeType};base64,${image.data}` : null;
}

export const imagePayload = (images) => images.map(({ type, mimeType, data }) => ({ type, mimeType, data }));
