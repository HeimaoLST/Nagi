import type { ToolDefinition } from '@earendil-works/pi-coding-agent';
import { Type, type Static, type TSchema } from 'typebox';
import { Value } from 'typebox/value';
import { maxLogBytes, memoryDate, memorySearchLimit, memorySearchQuery, memoryTags, memoryText } from './bot-memory.js';
import { RequestError } from './protocol.js';
import type { ToolTextResult } from './product-effects.js';

const strictTool = { additionalProperties: false };
const requiredText = Type.String({ minLength: 1, pattern: '\\S' });
const noneSchema = Type.Object({}, strictTool);
const ruleIdSchema = Type.Object({ rule_id: requiredText }, strictTool);
const ruleSchema = Type.Object({ text: memoryText }, strictTool);
const proposalSchema = Type.Object({
  text: memoryText,
  reason: Type.String({ minLength: 1, maxLength: maxLogBytes, pattern: '\\S' }),
}, strictTool);
const logSchema = Type.Object({
  summary: memoryText,
  date: Type.Optional(memoryDate),
  tags: Type.Optional(memoryTags),
}, strictTool);
const searchSchema = Type.Object({
  query: memorySearchQuery,
  date_from: Type.Optional(memoryDate),
  date_to: Type.Optional(memoryDate),
  limit: Type.Optional(memorySearchLimit),
}, strictTool);

export type MemoryToolsHost = {
  isActivity: boolean;
  activeCall: (learningTool: boolean, signal?: AbortSignal) => { botId: string; executionId: string };
  toResult: (value: unknown) => ToolTextResult;
  forgetRule: (botId: string, ruleId: string, operationId: string) => unknown;
  listRules: (botId: string) => unknown;
  logWork: (botId: string, executionId: string, operationId: string, input: Static<typeof logSchema>) => unknown;
  proposeLearning: (botId: string, executionId: string, operationId: string, input: Static<typeof proposalSchema>) => unknown;
  saveRule: (botId: string, operationId: string, text: string) => unknown;
  searchWorkLogs: (botId: string, input: Static<typeof searchSchema>) => unknown;
};

export function buildMemoryTools(host: MemoryToolsHost): ToolDefinition[] {
  const scoped = <T extends TSchema>(
    name: string,
    description: string,
    parameters: T,
    action: (args: Static<T>, botId: string, executionId: string, operationId: string) => unknown,
  ): ToolDefinition<T> => ({
    name,
    label: description,
    description,
    parameters,
    execute: async (callId, args, signal) => {
      const learningTool = name === 'memory_save_rule' || name === 'memory_propose_learning';
      const { botId, executionId } = host.activeCall(learningTool, signal);
      if (!Value.Check(parameters, args)) throw new RequestError('memory_invalid', 'Invalid memory tool parameters');
      return host.toResult(action(args, botId, executionId, `${executionId}:${name}:${callId}`));
    },
  });

  const searchLogs = scoped('memory_search_log', 'Search the dates, summaries, and tags of this Bot’s work log.', searchSchema,
    (input, botId) => host.searchWorkLogs(botId, input));
  if (host.isActivity) return [searchLogs];
  return [
    scoped('memory_forget_rule', 'Remove a lasting rule by ID when the user asks to forget it.', ruleIdSchema,
      (input, botId, _executionId, operationId) => host.forgetRule(botId, input.rule_id, operationId)),
    scoped('memory_list_rules', 'Read the saved rules and count of pending learnings for this Bot.', noneSchema,
      (_input, botId) => host.listRules(botId)),
    scoped('memory_log_work', 'Record a concise dated fact or completed work for this Bot.', logSchema,
      (input, botId, executionId, operationId) => host.logWork(botId, executionId, operationId, input)),
    scoped('memory_propose_learning', 'Queue an inferred lasting preference for the user to review.', proposalSchema,
      (input, botId, executionId, operationId) => host.proposeLearning(botId, executionId, operationId, input)),
    scoped('memory_save_rule', 'Save a lasting rule when the user explicitly asks you to remember it.', ruleSchema,
      (input, botId, _executionId, operationId) => host.saveRule(botId, operationId, input.text)),
    searchLogs,
  ];
}
