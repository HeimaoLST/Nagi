import { RequestError } from './protocol.js';

export async function readBoundedBytes(body: ReadableStream<Uint8Array> | null, limit: number, errorCode: string, errorMessage: string): Promise<Buffer> {
  const chunks: Uint8Array[] = [];
  let size = 0;
  const reader = body?.getReader();
  if (!reader) return Buffer.alloc(0);
  try {
    for (;;) {
      const next = await reader.read();
      if (next.done) break;
      size += next.value.byteLength;
      if (size > limit) throw new RequestError(errorCode, errorMessage);
      chunks.push(next.value);
    }
  } finally {
    await reader.cancel().catch(() => {});
  }
  return Buffer.concat(chunks, size);
}

export function extractCatalogModels(catalog: unknown, redact: (value: string) => string): string[] {
  if (!catalog || typeof catalog !== 'object' || !('data' in catalog) || !Array.isArray(catalog.data)) {
    throw new RequestError('provider_test_failed', 'The Provider model catalog is invalid');
  }
  const models = catalog.data
    .map(row => (row && typeof row === 'object' && 'id' in row ? row.id : undefined))
    .filter((model): model is string => typeof model === 'string' && model.length <= 256 && !!model.trim() && redact(model) === model);
  return [...new Set(models)];
}
