import { createHash } from 'node:crypto';
import { constants } from 'node:fs';
import { open, realpath, type FileHandle } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { parseSessionEntries, type SessionEntry } from '@earendil-works/pi-coding-agent';
import { RequestError } from './protocol.js';

export const bindingEntryType = 'nagi-context-v1';
export const turnEntryType = 'nagi-turn-v1';
const blockSize = 64 * 1024;
const maxRecordBytes = 8 * 1024 * 1024;

export type TranscriptOwner = { contextId: string; botId: string; purpose: 'conversation' | 'activity'; activityId?: string;
  selectionRevision?: number; cwd?: string };
type TranscriptStep = { entry?: SessionEntry; position: number; bytes: number };
export type Transcript = {
  source: string;
  high: number;
  edge: (position: number) => Promise<string>;
  step: (position: number, high: number, tail: boolean) => Promise<TranscriptStep>;
  close: () => Promise<void>;
};

const unavailable = () => new RequestError('history_unavailable', 'The saved session transcript is unavailable');

export async function matchesTranscriptOwner(found: unknown, expected: TranscriptOwner): Promise<boolean> {
  if (!found || typeof found !== 'object') return false;
  const owner = found as Partial<TranscriptOwner>;
  return owner.contextId === expected.contextId && owner.botId === expected.botId && owner.purpose === expected.purpose &&
    owner.activityId === expected.activityId && (expected.selectionRevision === undefined || owner.selectionRevision === expected.selectionRevision) &&
    (expected.cwd === undefined || owner.cwd === expected.cwd || owner.cwd === await realpath(expected.cwd));
}

export function liveTranscript(source: string, entries: SessionEntry[]): Transcript {
  return {
    source: `live:${source}`, high: entries.length,
    edge: async position => position === 0 ? '' : createHash('sha256').update(entries[position - 1]?.id ?? '').digest('base64url'),
    step: async (position, _high, tail) => {
      const index = tail ? position - 1 : position;
      const entry = entries[index];
      return { entry, position: tail ? index : index + 1, bytes: 0 };
    },
    close: async () => {},
  };
}

class DiskTranscript implements Transcript {
  private block = Buffer.alloc(blockSize);
  private blockStart = -1;
  private blockLength = 0;

  constructor(private file: FileHandle, readonly source: string, readonly high: number) {}

  async close(): Promise<void> {
    await this.file.close();
  }

  async edge(position: number): Promise<string> {
    if (position === 0) return '';
    const start = Math.max(0, position - 256);
    const bytes = await this.readRange(start, position);
    if (bytes.at(-1) !== 10) throw new RequestError('invalid_cursor', 'The transcript cursor is no longer available');
    return createHash('sha256').update(bytes).digest('base64url');
  }

  private async load(position: number): Promise<number> {
    const start = Math.floor(position / blockSize) * blockSize;
    if (start !== this.blockStart) {
      const { bytesRead } = await this.file.read(this.block, 0, blockSize, start);
      this.blockStart = start;
      this.blockLength = bytesRead;
    }
    if (position >= start + this.blockLength) throw unavailable();
    return position - start;
  }

  private async readRange(start: number, end: number): Promise<Buffer> {
    const result = Buffer.allocUnsafe(end - start);
    let offset = 0;
    while (start + offset < end) {
      const index = await this.load(start + offset);
      offset += this.block.copy(result, offset, index, Math.min(this.blockLength, index + result.length - offset));
    }
    return result;
  }

  async step(position: number, high: number, tail: boolean): Promise<TranscriptStep> {
    let start = position;
    let end = position;
    if (tail) {
      let scan = end - 2;
      while (scan >= 0) {
        if (end - scan > maxRecordBytes + 1) throw unavailable();
        const index = await this.load(scan);
        const newline = this.block.lastIndexOf(10, index);
        if (newline !== -1) {
          scan = this.blockStart + newline;
          break;
        }
        scan = this.blockStart - 1;
      }
      start = scan + 1;
    } else {
      while (end < high) {
        if (end - start > maxRecordBytes) throw unavailable();
        const index = await this.load(end);
        const newline = this.block.subarray(0, this.blockLength).indexOf(10, index);
        if (newline !== -1 && this.blockStart + newline < high) {
          end = this.blockStart + newline + 1;
          break;
        }
        end = Math.min(high, this.blockStart + this.blockLength);
      }
    }
    const bytes = end - start;
    if (bytes > maxRecordBytes) throw unavailable();
    const data = await this.readRange(start, end);
    if (data.at(-1) !== 10) throw unavailable();
    const [entry] = parseSessionEntries(data.toString('utf8'));
    if (!entry || typeof entry !== 'object') throw unavailable();
    return { entry: entry.type === 'session' ? undefined : entry, position: tail ? start : end, bytes };
  }
}

export async function openDiskTranscript(directory: string, sessionRef: string, expected: TranscriptOwner): Promise<Transcript> {
  let file: FileHandle | undefined;
  try {
    const path = resolve(sessionRef);
    if (dirname(path) !== directory || await realpath(path) !== path) throw unavailable();
    file = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
    const info = await file.stat();
    if (!info.isFile() || !info.size) throw unavailable();
    const prefix = Buffer.alloc(blockSize);
    const { bytesRead } = await file.read(prefix, 0, prefix.length, 0);
    const bytes = prefix.subarray(0, bytesRead);
    const end = bytes.lastIndexOf(10);
    const entries = parseSessionEntries(bytes.subarray(0, end + 1).toString('utf8'));
    const header = entries[0];
    const binding = entries.find(entry => entry.type === 'custom' && entry.customType === bindingEntryType);
    if (header?.type !== 'session' || !header.id || binding?.type !== 'custom' || !await matchesTranscriptOwner(binding.data, expected)) throw unavailable();
    const source = JSON.stringify([info.dev, info.ino, info.birthtimeMs, header.id, binding.id]);
    return new DiskTranscript(file, `disk:${createHash('sha256').update(source).digest('base64url')}`, info.size);
  } catch {
    await file?.close();
    throw unavailable();
  }
}

export async function* transcriptEntries(transcript: Transcript): AsyncGenerator<SessionEntry> {
  try {
    for (let position = 0; position < transcript.high;) {
      const row = await transcript.step(position, transcript.high, false);
      position = row.position;
      if (row.entry) yield row.entry;
    }
  } finally {
    await transcript.close();
  }
}
