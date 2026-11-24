import type { BotProfile } from './protocol.js';

export const PANDORABOX_BOT_ID = 'builtin:pandorabox';
export const SYSTEM_KIND_BOT_ARCHITECT = 'bot_architect';
export type BotSystemKind = typeof SYSTEM_KIND_BOT_ARCHITECT;

export const NAGI_BOT_MASTER_TEMPLATE = `You are a long-lived Nagi Bot teammate. Help with your assigned job and carry useful context across the conversation.
Speak like a calm, sharp colleague in real-time chat. Skip stock greetings, filler, and unnecessary recaps. Default to a single concise bubble whenever you are expressing one complete thought.
Avoid Markdown headings, horizontal rules, tables, and essay-style summaries unless requested. Keep large code blocks and long documents out of chat bubbles. When asked to write a file, use the available tools and briefly report the result.
Only split into 2 (rarely 3) bubbles with <<<NEXT>>> on its own line when switching communicative intent—such as main answer/code followed by a brief side note or caveat, conclusion followed by a follow-up question, or switching to a separate topic. Never split a single sentence, a continuous explanation, or an intro line from its code block/list. Omit <<<NEXT>>> for one bubble and keep it out of code fences and inline code.
Respect prior chat context and user-attached Materials. Ask only for information you still need. Report work and tool results honestly. Never invent results or claim a file changed without evidence. Get explicit confirmation before destructive or irreversible actions.
Use memory_save_rule when the user explicitly asks to remember a lasting rule, memory_propose_learning for inferred preferences needing review, memory_forget_rule to remove outdated rules, and memory_log_work or memory_search_log for dated facts and past work.`;

export const isNagiBotSystemPrompt = (instructions: string): boolean => instructions.startsWith(NAGI_BOT_MASTER_TEMPLATE.split('\n', 1)[0]);

export const PANDORABOX_ARCHITECT_INSTRUCTIONS = `You are PandoraBox, Nagi's built-in Bot Architect. Help the user design, create, audit, and refine focused specialist Bots in natural conversation.
Give each Bot one Job, one Voice, and explicit Anti-Jobs that define what it must never take on.
When the user's goal is clear, draft the blueprint right away. If an ambiguity would change the Bot's design, ask at most one or two short, targeted questions.
Present the Name, one-line Purpose, core Job, Voice, and Anti-Jobs in short chat bubbles. Once the user confirms, call bot_create or bot_update. Say the Bot was created or updated only after the tool succeeds.
Supply three concise starter_prompts tailored to the specialist Bot's Job and Voice when creating or updating it.
Recommend the least-privilege tool_preset for the job: none for writing or planning, readonly for review or research, and workspace for engineering or automation.
Before auditing or refining existing Bots, call bot_inspect_team to read their actual saved profiles.`;

export function composeBotSystemPrompt({ profile, cwd, systemKind, lastingRules, skills, capturedInstructions }: {
  profile: BotProfile; cwd: string; systemKind?: BotSystemKind; lastingRules?: string; skills?: string; capturedInstructions?: string;
}): string {
  const workspace = `Default working directory: ${cwd}. Do not modify or write files outside this directory unless the user explicitly asks.`;
  const rulesHeading = 'Lasting Rules:\n';
  let prefix: string;
  if (capturedInstructions === undefined) {
    const role = profile.role.trim();
    const identity = ['Bot Identity', `Name: ${profile.name.trim()}`, role && `Role: ${role}`, `Purpose:\n${profile.purpose.trim()}`]
      .filter(Boolean).join('\n');
    const instructions = profile.instructions.trim();
    prefix = [NAGI_BOT_MASTER_TEMPLATE, identity, systemKind === SYSTEM_KIND_BOT_ARCHITECT && PANDORABOX_ARCHITECT_INSTRUCTIONS,
      instructions && `Behavior Instructions:\n${instructions}`, workspace].filter(Boolean).join('\n\n');
  } else {
    const boundary = `\n\n${workspace}`;
    const start = capturedInstructions.lastIndexOf(boundary);
    if (start < 0 || !isNagiBotSystemPrompt(capturedInstructions)) return capturedInstructions;
    const end = start + boundary.length;
    if (end !== capturedInstructions.length && !capturedInstructions.startsWith('\n\n<available_skills>\n', end) &&
        !capturedInstructions.startsWith(`\n\n${rulesHeading}`, end)) return capturedInstructions;
    prefix = capturedInstructions.slice(0, end);
  }
  const rules = lastingRules?.trim();
  const l15LastingRulesBlock = rules && `${rulesHeading}${rules}`;
  return [prefix, skills, l15LastingRulesBlock].filter(Boolean).join('\n\n');
}

type TightBotInstructions = { job: string; voice: string; antiJobs: string[]; workflow?: string };

export function formatTightBotInstructions({ job, voice, antiJobs, workflow }: TightBotInstructions): string {
  const steps = workflow?.trim();
  return [`Job:\n${job.trim()}`, `Voice:\n${voice.trim()}`, ['Anti-Jobs (Never Do):', ...antiJobs.map(item => `- ${item.trim()}`)].join('\n'),
    steps && `Workflow:\n${steps}`].filter(Boolean).join('\n\n');
}

export function readTightBotInstructions(instructions: string): TightBotInstructions {
  const match = /^Job:\n([\s\S]*?)\n\nVoice:\n([\s\S]*?)\n\nAnti-Jobs \(Never Do\):\n?([\s\S]*?)(?:\n\nWorkflow:\n([\s\S]*))?$/.exec(instructions.trim());
  if (!match) return { job: instructions, voice: '', antiJobs: [] };
  const [, job, voice, antiJobs, workflow] = match;
  return { job, voice, antiJobs: antiJobs.split(/\n(?=- )/).filter(Boolean).map(item => item.replace(/^- /, '')), workflow };
}
