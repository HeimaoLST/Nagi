import { createHash, randomUUID } from 'node:crypto';
import { closeSync, constants, fstatSync, linkSync, lstatSync, mkdirSync, openSync, readFileSync, readdirSync, readSync,
  realpathSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, isAbsolute, join, relative, sep } from 'node:path';
import { parseFrontmatter } from '@earendil-works/pi-coding-agent';
import { Type, type Static } from 'typebox';
import { Value } from 'typebox/value';
import { clipUTF8 } from './product-redactor.js';
import { canonicalJSON, id, RequestError, skillID, skillManifest, skillScriptPath, skillSlugPattern,
  type BotProfile, type SkillApproval, type SkillDetail, type SkillListing, type SkillManifest, type SkillPermissions, type SkillSource } from './protocol.js';

const maxSkillBytes = 32 * 1024;
const maxScriptBytes = 64 * 1024;
const maxBundleBytes = 256 * 1024;
const maxManifestBytes = 32 * 1024;
const maxPrivateSkills = 20;
const maxLibrarySkills = 200;
const maxBackupBytes = 2 * 1024 * 1024;
const slug = new RegExp(skillSlugPattern);
const decoder = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true });
const digest = (data: string) => createHash('sha256').update(data).digest('hex');
const compare = (a: string, b: string) => a < b ? -1 : a > b ? 1 : 0;
const strict = { additionalProperties: false };

const backupFileFields = { byteLength: Type.Integer({ minimum: 0, maximum: maxScriptBytes }), sha256: Type.String({ pattern: '^[a-f0-9]{64}$' }),
  content: Type.String({ maxLength: maxScriptBytes }) };
export const privateSkillBundleSchema = Type.Object({ ownerBotId: id, skillId: skillID, relativePath: Type.Literal('SKILL.md'),
  ...backupFileFields, files: Type.Optional(Type.Array(Type.Object({ relativePath: Type.Union([Type.Literal('manifest.json'), skillScriptPath]),
    ...backupFileFields }, strict), { maxItems: 9 })),
}, strict);
export type PrivateSkillBundle = Static<typeof privateSkillBundleSchema>;
export type SkillOwner = { botId: string; profile: BotProfile };
export type SkillWrite = { source: 'private' | 'global'; botId?: string; skillId: string; markdown?: string; exclusive?: boolean; files?: Record<string, string> };
export type SkillBundle = { manifest: SkillManifest; files: Record<string, string> };

type Summary = Omit<SkillDetail, 'content' | 'markdown'>;
type Lookup = { source: SkillSource; skillId: string; botId?: string };

function contains(root: string, path: string): boolean {
  const diff = relative(root, path);
  return diff !== '..' && !diff.startsWith(`..${sep}`) && !isAbsolute(diff);
}

function safeOwner(id: string): boolean {
  return id !== '' && id !== '.' && id !== '..' && !/[/\\\x00]/.test(id) && Buffer.byteLength(id) <= 255;
}

function oneLine(text: string): string {
  return text.replace(/\s+/gu, ' ').trim();
}

function parseSkill(markdown: string) {
  const header = /^\uFEFF?---\r?\n[\s\S]*?\r?\n---(?:\r?\n|$)/.exec(markdown);
  if (Buffer.byteLength(markdown) > maxSkillBytes || markdown.includes('\0') || !header) {
    throw new RequestError('invalid_skill', 'Use a UTF-8 SKILL.md file of at most 32 KiB with YAML frontmatter');
  }
  try {
    const { frontmatter } = parseFrontmatter<{ name?: unknown; description?: unknown }>(header[0]);
    const name = typeof frontmatter.name === 'string' ? oneLine(frontmatter.name) : '';
    const description = typeof frontmatter.description === 'string' ? oneLine(frontmatter.description) : '';
    if (name && description) return { name, description, content: markdown.slice(header[0].length) };
  } catch {}
  throw new RequestError('invalid_skill', 'SKILL.md requires a name and a description in YAML frontmatter');
}

export function canonicalSkillOrigin(value: string): string {
  const url = URL.parse(value);
  if (!url || !['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.pathname !== '/' || url.search || url.hash) {
    throw new RequestError('invalid_skill', 'Use an HTTP(S) origin without a path or credentials');
  }
  return `${url.protocol}//${url.hostname}:${url.port || (url.protocol === 'https:' ? '443' : '80')}`;
}

export function pureSkill(permissions: SkillPermissions): boolean {
  return !permissions.workspaceRead && !permissions.workspaceWrite && !permissions.networkOrigins?.length && !permissions.credentialRefs?.length;
}

export function framedSkillDigest(fields: Iterable<string>): string {
  const hash = createHash('sha256');
  const length = Buffer.allocUnsafe(8);
  for (const field of fields) {
    length.writeBigUInt64BE(BigInt(Buffer.byteLength(field)));
    hash.update(length).update(field);
  }
  return hash.digest('hex');
}

export function bundleDigest(botId: string, source: SkillSource, skillId: string, files: Record<string, string>): string {
  return framedSkillDigest([canonicalJSON([botId, source]), skillId, ...Object.keys(files).sort(compare).flatMap(path => [path, files[path]])]);
}

export function skillGrantKey({ ownerBotId, source, skillId, approvalDigest, permissionsDigest }: Omit<SkillApproval, 'grantKey' | 'mode' | 'installationId' | 'approvedAt'>): string {
  return framedSkillDigest([ownerBotId, source, skillId, approvalDigest, permissionsDigest]);
}

export function executableBundle(files: Record<string, string>): SkillBundle | undefined {
  parseSkill(files['SKILL.md']);
  const entries = Object.keys(files).filter(path => path !== 'SKILL.md' && path !== 'manifest.json');
  if (!Object.hasOwn(files, 'manifest.json') && !entries.length) return;
  let manifest: unknown;
  try { manifest = JSON.parse(files['manifest.json']); } catch { throw new RequestError('invalid_skill', 'Invalid executable skill manifest'); }
  if (!Value.Check(skillManifest, manifest) || Buffer.byteLength(files['manifest.json']) > maxManifestBytes ||
      entries.length > 8 || entries.some(path => !Value.Check(skillScriptPath, path) || Buffer.byteLength(files[path]) > maxScriptBytes) ||
      Object.values(files).reduce((total, text) => total + Buffer.byteLength(text), 0) > maxBundleBytes ||
      Object.values(files).some(text => decoder.decode(Buffer.from(text)) !== text || text.includes('\0'))) {
    throw new RequestError('invalid_skill', 'The executable skill bundle has an invalid path, manifest, or size');
  }
  for (const [name, script] of Object.entries(manifest.scripts)) {
    if (script.entry !== `scripts/${name}.mjs` || !Object.hasOwn(files, script.entry) || !script.description.trim()) {
      throw new RequestError('invalid_skill', 'Each declared script must name its matching bundled entry');
    }
    for (const origin of script.permissions.networkOrigins ?? []) canonicalSkillOrigin(origin);
    if (script.permissions.credentialRefs?.some(ref => !/^mcp:[a-z0-9][a-z0-9_-]{0,62}:[A-Za-z0-9_-]+$/.test(ref)) ||
        script.permissions.credentialRefs?.length && !script.permissions.networkOrigins?.length) {
      throw new RequestError('invalid_skill', 'Credential references require declared HTTP origins and MCP header references');
    }
  }
  return { manifest, files };
}

export function checkedSkillFiles(files: Record<string, string>, redact: (text: string) => string): void {
  executableBundle(files);
  assertSkillSecretFree(files['SKILL.md'], redact);
  for (const [path, text] of Object.entries(files)) {
    if (redact(path) !== path || redact(text) !== text) throw new RequestError('skill_secret', 'Remove saved credentials before saving the skill');
  }
}

export function backupSkillFiles(row: PrivateSkillBundle): Record<string, string> {
  return Object.fromEntries([['SKILL.md', row.content], ...(row.files ?? []).map(file => [file.relativePath, file.content])]);
}

export function validateSkillBundles(bundles: PrivateSkillBundle[], botIds: Set<string>): void {
  let total = 0;
  const owners = new Map<string, Set<string>>();
  try {
    for (const row of bundles) {
      const ids = owners.get(row.ownerBotId) ?? new Set<string>();
      const files = [row, ...row.files ?? []];
      total += files.reduce((sum, file) => sum + file.byteLength, 0);
      if (!botIds.has(row.ownerBotId) || !safeOwner(row.ownerBotId) || ids.has(row.skillId) || ids.size >= maxPrivateSkills ||
          total > maxBackupBytes || new Set(files.map(file => file.relativePath)).size !== files.length || files.some(file =>
            Buffer.byteLength(file.content) !== file.byteLength || digest(file.content) !== file.sha256 ||
            decoder.decode(Buffer.from(file.content)) !== file.content)) throw new Error('Invalid private skill bundle');
      executableBundle(backupSkillFiles(row));
      ids.add(row.skillId);
      owners.set(row.ownerBotId, ids);
    }
  } catch {
    throw new RequestError('profile_unavailable', 'The recovery copy contains an invalid private skill bundle');
  }
}

function readSkillFile(path: string, limit = maxSkillBytes): string {
  const fd = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
  try {
    const stat = fstatSync(fd);
    if (!stat.isFile() || stat.size > limit || stat.nlink !== 1) throw new RequestError('invalid_skill', 'A skill file exceeds the size limit or is not a regular unlinked file');
    const bytes = Buffer.allocUnsafe(limit + 1);
    let size = 0;
    while (size < bytes.length) {
      const count = readSync(fd, bytes, size, bytes.length - size, null);
      if (!count) break;
      size += count;
    }
    if (size > limit) throw new RequestError('invalid_skill', 'A skill file exceeds the size limit');
    return decoder.decode(bytes.subarray(0, size));
  } finally {
    closeSync(fd);
  }
}

function summarize({ markdown, content, ...skill }: SkillDetail): Summary {
  return skill;
}

export class BotSkills {
  constructor(private directory: string) {}

  private ownedRoot(source: 'global' | 'private', botId?: string): string[] {
    if (source === 'global') return ['skills'];
    if (!botId || !safeOwner(botId)) throw new RequestError('scope_denied', 'The Bot has no accessible private skill directory');
    return ['bots', botId, 'skills'];
  }

  private ownedPath(parts: string[], create = false): string {
    let path = realpathSync(this.directory);
    for (const part of parts) {
      path = join(path, part);
      if (create) {
        try { mkdirSync(path, { mode: 0o700 }); }
        catch (error) { if ((error as NodeJS.ErrnoException).code !== 'EEXIST') throw error; }
      }
      const stat = lstatSync(path, { throwIfNoEntry: false });
      if (stat && (!stat.isDirectory() || stat.isSymbolicLink())) throw new RequestError('scope_denied', 'A skill directory is outside the owned root');
    }
    return path;
  }

  private roots(source: SkillSource, workspace: string, local: boolean, botId?: string, read = false): string[] {
    if (source === 'global' || source === 'private') return [this.ownedPath(this.ownedRoot(source, botId))];
    if (source === 'local' && !local || source === 'workspace' && !workspace) return [];
    const base = source === 'workspace' ? workspace : homedir();
    try {
      const anchor = realpathSync(base);
      const candidates = source === 'workspace' ? ['.nagi', '.agents'] : ['.agents'];
      return candidates.flatMap(name => {
        try {
          const declared = join(anchor, name, 'skills');
          const root = realpathSync(declared);
          if (declared === root && contains(anchor, root)) return [root];
          if (read) throw new RequestError('scope_denied', 'The skill root is outside the authorized directory');
          return [];
        } catch (error) { if (error instanceof RequestError) throw error; return []; }
      });
    } catch (error) { if (error instanceof RequestError) throw error; return []; }
  }

  private readAt(root: string, source: SkillSource, skillId: string): SkillDetail | undefined {
    const target = join(root, skillId, 'SKILL.md');
    try {
      const path = realpathSync(target);
      if (!contains(root, path) || path !== target) {
        throw new RequestError('scope_denied', 'The skill file is outside the authorized root');
      }
      const markdown = readSkillFile(path);
      return { source, skillId, ...parseSkill(markdown), sha256: digest(markdown), markdown };
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') return;
      if (error instanceof RequestError) throw error;
      throw new RequestError('skill_unavailable', 'The skill file could not be read');
    }
  }

  get({ source, botId, skillId }: Lookup, workspace: string, local: boolean): SkillDetail | undefined {
    if (!slug.test(skillId)) throw new RequestError('scope_denied', 'The skill is outside the authorized scope');
    for (const root of this.roots(source, workspace, local, botId, true)) {
      const skill = this.readAt(root, source, skillId);
      if (skill) return skill;
    }
  }

  bundle({ source, botId, skillId }: Lookup, workspace: string, local: boolean): SkillBundle | undefined {
    if (!slug.test(skillId)) throw new RequestError('scope_denied', 'The skill is outside the authorized scope');
    for (const root of this.roots(source, workspace, local, botId, true)) {
      const skill = this.readAt(root, source, skillId);
      if (!skill) continue;
      const directory = join(root, skillId);
      const manifest = join(directory, 'manifest.json');
      const scripts = join(directory, 'scripts');
      const scriptDir = lstatSync(scripts, { throwIfNoEntry: false });
      if (!lstatSync(manifest, { throwIfNoEntry: false })) {
        if (scriptDir && (!scriptDir.isDirectory() || realpathSync(scripts) !== scripts || readdirSync(scripts).length)) {
          throw new RequestError('invalid_skill', 'Bundled scripts require a manifest');
        }
        return;
      }
      if (scriptDir && (!scriptDir.isDirectory() || realpathSync(scripts) !== scripts) || realpathSync(manifest) !== manifest) {
        throw new RequestError('invalid_skill', 'The executable skill cannot contain symbolic links');
      }
      const paths = scriptDir ? readdirSync(scripts).map(name => `scripts/${name}`) : [];
      if (paths.length > 8 || paths.some(path => !Value.Check(skillScriptPath, path)) ||
          readdirSync(directory).some(name => !['SKILL.md', 'manifest.json', 'scripts'].includes(name))) {
        throw new RequestError('invalid_skill', 'The executable skill contains unsupported files');
      }
      const files = { 'SKILL.md': skill.markdown, 'manifest.json': readSkillFile(manifest, maxManifestBytes),
        ...Object.fromEntries(paths.map(path => {
          const file = join(directory, path);
          if (realpathSync(file) !== file) throw new RequestError('invalid_skill', 'The executable skill cannot contain symbolic links');
          return [path, readSkillFile(file, maxScriptBytes)];
        })) };
      return executableBundle(files);
    }
  }

  private inventory(source: SkillSource, workspace: string, local: boolean, botId?: string, strict = false): SkillDetail[] {
    const rows = new Map<string, SkillDetail>();
    const max = source === 'private' ? maxPrivateSkills : maxLibrarySkills;
    for (const root of this.roots(source, workspace, local, botId)) {
      let entries: string[];
      try { entries = readdirSync(root).filter(name => slug.test(name)).sort(compare); }
      catch (error) {
        if (!strict || (error as NodeJS.ErrnoException).code === 'ENOENT') continue;
        throw error;
      }
      for (const skillId of entries) {
        if (rows.has(skillId)) continue;
        try {
          const skill = this.readAt(root, source, skillId);
          if (skill) rows.set(skillId, skill);
        } catch (error) { if (strict) throw error; }
        if (rows.size > max && strict) throw new RequestError('skill_limit', 'The skill library exceeds its size limit');
        if (rows.size === max && !strict) break;
      }
      if (rows.size === max && !strict) break;
    }
    return [...rows.values()].sort((a, b) => compare(a.skillId, b.skillId));
  }

  mounted(owner: SkillOwner, workspace: string, discover: boolean): SkillListing['mounted'] {
    return (owner.profile.mountedSkills ?? []).map(ref => {
      let skill: SkillDetail | undefined;
      try { skill = this.get(ref, workspace, discover); } catch {}
      if (!skill || ref.expectedDigest && skill.sha256 !== ref.expectedDigest) return { ...ref, status: 'missing' };
      return { ...ref, status: 'available', skill: summarize(skill) };
    });
  }

  list(owner: SkillOwner | undefined, workspace: string, discover: boolean): SkillListing {
    return { global: this.inventory('global', workspace, discover).map(summarize),
      workspace: this.inventory('workspace', workspace, discover).map(summarize), local: this.inventory('local', workspace, discover).map(summarize),
      private: owner ? this.private(owner.botId) : [], mounted: owner ? this.mounted(owner, workspace, discover) : [], discoverLocalSkills: discover };
  }

  private(botId: string): Summary[] {
    try { return this.inventory('private', '', false, botId).map(summarize); }
    catch (error) { if (error instanceof RequestError && error.code === 'scope_denied') return []; throw error; }
  }

  catalog(owner: SkillOwner, workspace: string, discover: boolean, redact: (text: string) => string): string {
    const available = this.mounted(owner, workspace, discover).flatMap(row => row.skill ? [row.skill] : []).concat(this.private(owner.botId));
    const rows = available.sort((a, b) => compare(a.skillId, b.skillId) || compare(a.source, b.source));
    const lines: string[] = [];
    let bytes = 0;
    for (const row of rows) {
      if (redact(row.skillId) !== row.skillId) continue;
      const line = JSON.stringify({ skill_id: row.skillId, source: row.source, name: clipUTF8(redact(row.name), 128), description: clipUTF8(redact(row.description), 256) })
        .replaceAll('<', '\\u003c').replaceAll('>', '\\u003e');
      bytes += Buffer.byteLength(line) + 1;
      if (bytes > 8192) break;
      lines.push(line);
    }
    return lines.length ? `<available_skills>\n${lines.join('\n')}\n</available_skills>` : '';
  }

  plan(source: SkillWrite['source'], botId: string | undefined, skillId: string, redact: (text: string) => string,
      markdown?: string, exclusive = false, files?: Record<string, string>): SkillWrite {
    if (!slug.test(skillId)) throw new RequestError('scope_denied', 'The skill is outside the authorized scope');
    const directory = this.ownedPath([...this.ownedRoot(source, botId), skillId]);
    const existing = lstatSync(join(directory, 'SKILL.md'), { throwIfNoEntry: false });
    if (existing && !existing.isFile()) throw new RequestError('scope_denied', 'The skill file is outside the owned root');
    if (exclusive && existing) throw new RequestError('already_exists', 'A global skill with that ID already exists');
    if (markdown !== undefined) {
      assertSkillSecretFree(markdown, redact);
      if (files) checkedSkillFiles({ ...files, 'SKILL.md': markdown }, redact);
      if (redact(skillId) !== skillId) throw new RequestError('skill_secret', 'Remove saved credentials before saving the skill');
      const count = this.inventory(source, '', false, botId, true).length;
      if (!existing && count >= (source === 'private' ? maxPrivateSkills : maxLibrarySkills)) throw new RequestError('skill_limit', 'The skill library is full');
    }
    return { source, botId, skillId, markdown, exclusive, files };
  }

  stage(writes: SkillWrite[]): string {
    const name = `.skill-stage-${randomUUID()}`;
    for (const { source, botId, skillId, markdown, files } of writes) {
      this.ownedPath([...this.ownedRoot(source, botId), skillId]);
      if (markdown !== undefined) executableBundle({ ...files, 'SKILL.md': markdown });
    }
    const path = join(realpathSync(this.directory), name);
    try { writeFileSync(path, JSON.stringify(writes), { flag: 'wx', mode: 0o600, flush: true }); }
    catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'EEXIST') rmSync(path, { force: true });
      throw error;
    }
    return name;
  }

  publish(name: string): void {
    if (!/^\.skill-stage-[a-f0-9-]{36}$/.test(name)) throw new RequestError('profile_unavailable', 'The staged skill update is invalid');
    const path = join(realpathSync(this.directory), name);
    const fd = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW);
    let writes: SkillWrite[];
    try { writes = JSON.parse(readFileSync(fd, 'utf8')) as SkillWrite[]; } finally { closeSync(fd); }
    for (const { source, botId, skillId, markdown, exclusive, files } of writes) {
      if ((source !== 'private' && source !== 'global') || !slug.test(skillId)) throw new RequestError('profile_unavailable', 'The staged skill update is invalid');
      const directory = this.ownedPath([...this.ownedRoot(source, botId), skillId], markdown !== undefined);
      const file = join(directory, 'SKILL.md');
      if (markdown !== undefined) executableBundle({ ...files, 'SKILL.md': markdown });
      if (files || markdown === undefined) {
        const scriptDirectory = join(directory, 'scripts');
        const oldScripts = lstatSync(scriptDirectory, { throwIfNoEntry: false });
        if (oldScripts?.isDirectory() && !oldScripts.isSymbolicLink()) {
          for (const name of readdirSync(scriptDirectory)) {
            const path = `scripts/${name}`;
            if (Value.Check(skillScriptPath, path) && !Object.hasOwn(files ?? {}, path)) rmSync(join(directory, path), { force: true });
          }
        }
        for (const [relativePath, data] of Object.entries(files ?? {}).filter(([key]) => key !== 'manifest.json')) {
          const destination = this.ownedPath([...this.ownedRoot(source, botId), skillId, 'scripts'], true);
          this.replaceFile(join(destination, relativePath.slice('scripts/'.length)), data);
        }
        if (files?.['manifest.json'] !== undefined) this.replaceFile(join(directory, 'manifest.json'), files['manifest.json']);
        else rmSync(join(directory, 'manifest.json'), { force: true });
      }
      if (markdown === undefined) { rmSync(file, { force: true }); continue; }
      const temp = join(directory, `.skill-${randomUUID()}.tmp`);
      try {
        writeFileSync(temp, markdown, { flag: 'wx', mode: 0o600, flush: true });
        if (!exclusive) renameSync(temp, file);
        else {
          try { linkSync(temp, file); }
          catch (error) {
            if ((error as NodeJS.ErrnoException).code !== 'EEXIST' || this.readAt(dirname(directory), source, skillId)?.markdown !== markdown) throw error;
          }
        }
      } finally { rmSync(temp, { force: true }); }
    }
  }

  private replaceFile(file: string, data: string): void {
    const temp = `${file}.${randomUUID()}.tmp`;
    try {
      writeFileSync(temp, data, { flag: 'wx', mode: 0o600, flush: true });
      renameSync(temp, file);
    } finally { rmSync(temp, { force: true }); }
  }

  discard(name: string): void {
    if (/^\.skill-stage-[a-f0-9-]{36}$/.test(name)) rmSync(join(realpathSync(this.directory), name), { force: true });
  }

  backup(bots: SkillOwner[], redact: (text: string) => string): PrivateSkillBundle[] {
    const rows: PrivateSkillBundle[] = [];
    for (const { botId } of bots) {
      if (!safeOwner(botId)) continue;
      for (const row of this.inventory('private', '', false, botId, true)) {
        const bundle = this.bundle({ source: 'private', botId, skillId: row.skillId }, '', false);
        checkedSkillFiles(bundle?.files ?? { 'SKILL.md': row.markdown }, redact);
        const files = bundle && Object.entries(bundle.files).filter(([path]) => path !== 'SKILL.md').map(([relativePath, content]) =>
          ({ relativePath, content, byteLength: Buffer.byteLength(content), sha256: digest(content) }));
        rows.push({ ownerBotId: botId, skillId: row.skillId, relativePath: 'SKILL.md', byteLength: Buffer.byteLength(row.markdown), sha256: row.sha256,
          content: row.markdown, ...(files ? { files } : {}) });
      }
    }
    validateSkillBundles(rows, new Set(bots.map(bot => bot.botId)));
    return rows;
  }
}

export function formatSkill(name: string, description: string, content: string): string {
  return `---\nname: ${JSON.stringify(oneLine(name))}\ndescription: ${JSON.stringify(oneLine(description))}\n---\n${content}`;
}

export function assertSkillSecretFree(markdown: string, redact: (text: string) => string): void {
  const { name, description, content } = parseSkill(markdown);
  if ([markdown, name, description, content].some(text => redact(text) !== text)) {
    throw new RequestError('skill_secret', 'Remove saved credentials before saving the skill');
  }
}
