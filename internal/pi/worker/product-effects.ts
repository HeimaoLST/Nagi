import { resolve } from 'node:path';

export type ToolTextResult = {
  content: { type: 'text'; text: string }[];
  details: Record<string, never>;
  isError: boolean;
};

export function toolText(value: unknown, isError = false): ToolTextResult {
  return { content: [{ type: 'text' as const, text: typeof value === 'string' ? value : JSON.stringify(value) }], details: {}, isError };
}

export function confirmedFileEffect(cwd: string, name: string, input: unknown, content: unknown): string | undefined {
  if (name !== 'write' && name !== 'edit' || !input || typeof input !== 'object' || !('path' in input) ||
      typeof input.path !== 'string' || !input.path || input.path.includes('\0') || !Array.isArray(content) || content.length !== 1) return;
  const receipt: unknown = content[0];
  if (!receipt || typeof receipt !== 'object' || !('type' in receipt) || receipt.type !== 'text' || !('text' in receipt) || typeof receipt.text !== 'string') return;
  const requested = input.path;
  if (name === 'write') {
    if (receipt.text !== `Successfully wrote to ${requested}`) return;
  } else {
    const prefix = 'Successfully replaced ';
    const suffix = ` block(s) in ${requested}.`;
    if (!receipt.text.startsWith(prefix) || !receipt.text.endsWith(suffix) || !/^[1-9][0-9]*$/.test(receipt.text.slice(prefix.length, -suffix.length))) return;
  }
  const path = requested.replace(/^@/, '').replace(/[\u00a0\u2000-\u200a\u202f\u205f\u3000]/g, ' ');
  if (!path || path === '~' || path.startsWith('~/') || path.startsWith('file://')) return;
  return resolve(cwd, path);
}
