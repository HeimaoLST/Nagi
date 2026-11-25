import type { ToolDefinition } from '@earendil-works/pi-coding-agent';
import { Type, type Static, type TSchema } from 'typebox';
import { Value } from 'typebox/value';
import { RequestError, skillExecutable, skillId, skillScriptName, skillSource, skillText, type SkillSource } from './protocol.js';
import type { ToolTextResult } from './product-effects.js';

const strictTool = { additionalProperties: false };
const listSchema = Type.Object({}, strictTool);
const readSchema = Type.Object({ skill_id: Type.String(), source: Type.Optional(skillSource) }, strictTool);
const saveSchema = Type.Object({ skill_id: Type.String(), ...skillText, ...skillExecutable }, strictTool);
const executeSchema = Type.Object({
  skill_id: Type.String(),
  source: Type.Optional(skillSource),
  script: skillScriptName,
  input: Type.Optional(Type.Unknown()),
}, strictTool);
const removeSchema = Type.Object({ skill_id: Type.String() }, strictTool);

export type SkillToolCall = {
  botId: string;
  executionId: string;
  markFailed: () => void;
};

export type SkillToolsHost<Call extends SkillToolCall = SkillToolCall> = {
  isActivity: boolean;
  skillCall: (signal?: AbortSignal) => Call;
  toResult: (value: unknown) => ToolTextResult;
  listSkills: (call: Call) => object;
  readSkill: (call: Call, skillId: string, source?: SkillSource) => object;
  runSkill: (testOnly: boolean, call: Call, input: Static<typeof executeSchema>, callId: string, operationId: string, signal?: AbortSignal) => Promise<object>;
  saveSkill: (call: Call, operationId: string, input: Static<typeof saveSchema>) => object;
  deleteSkill: (call: Call, operationId: string, skillId: string) => object;
};

export function buildSkillTools<Call extends SkillToolCall>(host: SkillToolsHost<Call>): ToolDefinition[] {
  const tool = <T extends TSchema>(
    name: string,
    description: string,
    parameters: T,
    handler: (input: Static<T>, call: Call, operationId: string, callId: string, signal?: AbortSignal) => object | Promise<object>,
  ): ToolDefinition<T> => ({
    name,
    label: description,
    description,
    parameters,
    execute: async (callId, args, signal) => {
      const call = host.skillCall(signal);
      if (!Value.Check(parameters, args)) throw new RequestError('invalid_params', 'Invalid skill parameters');
      if ('skill_id' in args && !Value.Check(skillId, args.skill_id)) {
        throw new RequestError('scope_denied', 'The skill is outside the authorized scope');
      }
      return host.toResult(await handler(args, call, `${call.executionId}:${name}:${callId}`, callId, signal));
    },
  });

  const tools: ToolDefinition[] = [
    tool('skill_list', 'List mounted text skills and private skills for this Bot.', listSchema, (_args, call) => host.listSkills(call)),
    tool('skill_read', 'Read an authorized mounted or private SKILL.md on demand.', readSchema,
      ({ skill_id, source }, call) => host.readSkill(call, skill_id, source)),
    tool('skill_test', 'Test a declared skill script without workspace or HTTP access. Pure scripts activate only after a successful test.', executeSchema,
      (args, call, operationId, callId, signal) => host.runSkill(true, call, args, callId, operationId, signal)),
    tool('skill_exec', 'Run a declared skill script with its verified approval.', executeSchema,
      async (args, call, operationId, callId, signal) => {
        try {
          const result = await host.runSkill(false, call, args, callId, operationId, signal);
          if (host.isActivity && 'ok' in result && result.ok === false) call.markFailed();
          return result;
        } catch (error) {
          if (host.isActivity) call.markFailed();
          throw error;
        }
      }),
  ];
  if (!host.isActivity) {
    tools.push(
      tool('skill_save', 'Save a private SKILL.md and optional executable bundle. Privileged scripts require user approval.', saveSchema,
        (args, call, operationId) => host.saveSkill(call, operationId, args)),
      tool('skill_delete', 'Delete a private skill of this Bot when the user explicitly requests it.', removeSchema,
        ({ skill_id }, call, operationId) => host.deleteSkill(call, operationId, skill_id)),
    );
  }
  return tools;
}
