import type { ToolDefinition } from '@earendil-works/pi-coding-agent';
import { Type, type Static, type TSchema } from 'typebox';
import { Value } from 'typebox/value';
import { RequestError, skillID, toolPresetSchema, type BotProfile } from './protocol.js';
import type { ToolTextResult } from './product-effects.js';

export const architectProfile: BotProfile = {
  name: 'PandoraBox',
  role: 'Bot Architect',
  purpose: 'Design, create, and refine focused Nagi Bots through conversation.',
  instructions: '',
  workspace: '',
  codingTools: [],
  starterPrompts: ['Create a writing and editing Bot', 'Build a strict code review Bot', 'Audit and refine my existing Bot Team'],
};

const requiredText = Type.String({ minLength: 1, pattern: '\\S' });
const architectCapabilities = {
  tool_preset: Type.Optional(toolPresetSchema),
  skill_ids: Type.Optional(Type.Array(skillID, { maxItems: 200 })),
};

export const architectInspectSchema = Type.Object({}, { additionalProperties: false });

export const architectCreateSchema = Type.Object({
  name: requiredText,
  purpose: requiredText,
  job: requiredText,
  voice: requiredText,
  anti_jobs: Type.Array(requiredText, { minItems: 1 }),
  workflow: Type.Optional(Type.String()),
  role: Type.Optional(Type.String()),
  starter_prompts: Type.Optional(Type.Array(Type.String())),
  ...architectCapabilities,
}, { additionalProperties: false });

export const architectUpdateSchema = Type.Object({
  bot_id: requiredText,
  name: Type.Optional(requiredText),
  purpose: Type.Optional(Type.String()),
  role: Type.Optional(Type.String()),
  job: Type.Optional(requiredText),
  voice: Type.Optional(requiredText),
  anti_jobs: Type.Optional(Type.Array(requiredText)),
  workflow: Type.Optional(Type.String()),
  instructions: Type.Optional(Type.String()),
  starter_prompts: Type.Optional(Type.Array(Type.String())),
  ...architectCapabilities,
}, { additionalProperties: false });

export type ArchitectCreateInput = Static<typeof architectCreateSchema>;
export type ArchitectUpdateInput = Static<typeof architectUpdateSchema>;

export type ArchitectToolsHost = {
  assertOwner: (signal?: AbortSignal) => string;
  cleanMessage: (error: RequestError) => string;
  toResult: (value: unknown) => ToolTextResult;
  errorResult: (code: string, message: string) => ToolTextResult;
  inspectTeam: () => unknown;
  createBot: (operationId: string, input: ArchitectCreateInput) => unknown;
  updateBot: (operationId: string, input: ArchitectUpdateInput) => unknown;
};

export function architectResult(bot: { botId: string; profile: Pick<BotProfile, 'name' | 'purpose'>; profileRevision: number }) {
  return { bot_id: bot.botId, name: bot.profile.name, purpose: bot.profile.purpose, profile_revision: bot.profileRevision };
}

export function buildArchitectTools(host: ArchitectToolsHost): ToolDefinition[] {
  const execute = <T extends TSchema>(
    schema: T,
    action: (input: Static<T>, executionId: string, callId: string) => unknown,
  ): ToolDefinition<T>['execute'] => async (callId, input, signal) => {
    if (!Value.Check(schema, input)) return host.errorResult('invalid_params', 'Invalid Bot Architect tool parameters');
    try {
      const executionId = host.assertOwner(signal);
      return host.toResult(action(input, executionId, callId));
    } catch (error) {
      if (!(error instanceof RequestError)) throw error;
      return host.errorResult(error.code, host.cleanMessage(error));
    }
  };
  return [
    {
      name: 'bot_inspect_team',
      label: 'Inspect Bot Team',
      description: 'Read the saved profiles of the available Bots before auditing or refining them.',
      parameters: architectInspectSchema,
      execute: execute(architectInspectSchema, () => host.inspectTeam()),
    },
    {
      name: 'bot_create',
      label: 'Create specialist Bot',
      description: 'Create the focused Bot blueprint after the user confirms it.',
      parameters: architectCreateSchema,
      execute: execute(architectCreateSchema, (input, executionId, callId) =>
        host.createBot(`${executionId}:bot_create:${callId}`, input)),
    },
    {
      name: 'bot_update',
      label: 'Update specialist Bot',
      description: 'Update a saved specialist Bot after inspecting its profile and getting user confirmation.',
      parameters: architectUpdateSchema,
      execute: execute(architectUpdateSchema, (input, executionId, callId) =>
        host.updateBot(`${executionId}:bot_update:${callId}`, input)),
    },
  ];
}
