import { Type, type Static } from 'typebox';
import { Value } from 'typebox/value';
import type { Writable } from 'node:stream';

export const MAX_FRAME = 8 * 1024 * 1024;
const id = Type.String({ minLength: 1 });
const stringList = Type.Array(Type.String());
const target = { contextId: id, runId: id };
const content = Type.Array(Type.Union([Type.Object({ type: Type.Literal('text'), text: Type.String() }), Type.Object({ type: Type.Literal('image'), data: Type.String(), mimeType: id })]));
const domainTool = Type.Object({ name: id, description: Type.String(), parameters: Type.Record(Type.String(), Type.Unknown()) });
const request = Type.Object({ v: Type.Literal(1), id, method: id, params: Type.Record(Type.String(), Type.Unknown()) });
const commands = Type.Union([
  Type.Object({ method: Type.Literal('configure'), params: Type.Object({ wireApi: Type.Union([Type.Literal('openai-completions'), Type.Literal('openai-responses')]), baseUrl: id, apiKey: id, model: id, contextTokens: Type.Integer({ minimum: 1 }), outputTokens: Type.Integer({ minimum: 1 }) }) }),
  Type.Object({ method: Type.Literal('context.open'), params: Type.Object({ contextId: id, botId: id, purpose: Type.Union([Type.Literal('conversation'), Type.Literal('activity')]), activityId: Type.Optional(id), selectionRevision: Type.Integer({ minimum: 0, maximum: Number.MAX_SAFE_INTEGER }), instructions: Type.String(), cwd: id, resumeRef: Type.Optional(id), used: Type.Boolean(), codingTools: Type.Union([Type.Null(), stringList]), domainTools: Type.Union([Type.Null(), Type.Array(domainTool)]) }) }),
  Type.Object({ method: Type.Literal('context.history'), params: Type.Object({ contextId: id, cursor: Type.Optional(id), limit: Type.Integer({ minimum: 0, maximum: 200 }) }) }),
  Type.Object({ method: Type.Literal('context.inspect'), params: Type.Object({ contextId: id }) }),
  Type.Object({ method: Type.Literal('context.close'), params: Type.Object({ contextId: id }) }),
  Type.Object({ method: Type.Literal('run.start'), params: Type.Object({ ...target, text: id, context: Type.Optional(Type.Array(Type.Object({ name: id, text: Type.String() }))), correlation: Type.Optional(Type.Object({ turnId: Type.Optional(id), executionId: Type.Optional(id), scopeRef: Type.Optional(id) })) }) }),
  Type.Object({ method: Type.Literal('run.cancel'), params: Type.Object(target) }),
  Type.Object({ method: Type.Literal('tool.result'), params: Type.Object({ ...target, workerEpoch: id, callId: id, content: Type.Optional(content), details: Type.Optional(Type.Unknown()), error: Type.Optional(Type.Object({ code: id, message: Type.String() })) }) }),
  Type.Object({ method: Type.Literal('shutdown'), params: Type.Object({}) }),
]);
export type Command = Static<typeof commands>;
export type OpenParams = Extract<Command, { method: 'context.open' }>['params'];
export type ConfigureParams = Extract<Command, { method: 'configure' }>['params'];
export type ToolResult = Extract<Command, { method: 'tool.result' }>['params'];
export type StartParams = Extract<Command, { method: 'run.start' }>['params'];
export class RequestError extends Error {
  constructor(readonly code: string, message: string, readonly requestId?: string) { super(message); }
}
export function parseRequest(data: Buffer): { id: string; command: Command } {
  const value: unknown = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(data));
  if (!Value.Check(request, value)) throw new Error('Invalid protocol envelope');
  if (!Value.Check(commands, value)) throw new RequestError('invalid_params', 'Unsupported method or invalid parameters', value.id);
  return { id: value.id, command: value };
}
export function message(error: unknown): string { return error instanceof Error ? error.message : String(error); }
export function frameBytes(value: object): number { return Buffer.byteLength(JSON.stringify(value)) + 1; }
export class Writer {
  private queued = 0;
  private tail: Promise<void> = Promise.resolve();
  private failed = false;
  constructor(private output: Writable, private onFailure: () => void) {
    output.on('error', () => this.fail());
  }
  private fail(): void {
    if (this.failed) return;
    this.failed = true;
    this.onFailure();
  }
  send(value: object): Promise<void> {
    const data = Buffer.from(JSON.stringify(value) + '\n');
    if (data.length > MAX_FRAME) throw new RequestError('frame_too_large', 'Result exceeds protocol frame limit');
    if (this.failed || this.queued + data.length > 4 * MAX_FRAME) {
      this.fail();
      return Promise.reject(new Error('Protocol output unavailable'));
    }
    this.queued += data.length;
    const write = this.tail.then(() => new Promise<void>((resolve, reject) => {
      this.output.write(data, error => error ? reject(error) : resolve());
    })).finally(() => { this.queued -= data.length; });
    this.tail = write.catch(() => this.fail());
    return write;
  }
  async flush(): Promise<void> {
    await this.tail;
    if (this.failed) throw new Error('Protocol output unavailable');
  }
}
