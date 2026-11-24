import { createHash, randomUUID } from 'node:crypto';
import { chmodSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { basename, dirname, join } from 'node:path';
import { DatabaseSync, type SQLOutputValue } from 'node:sqlite';
import { decodeProductProfile, importProductSnapshot, type PrivateConnection, type ProductProfilePaths } from './product-profile.js';
import type { ProductProfile } from './product.js';
import { canonicalJSON, RequestError } from './protocol.js';

const MIGRATIONS = [`
  CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT;
  CREATE TABLE bots (
    bot_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, profile_json TEXT NOT NULL,
    profile_revision INTEGER NOT NULL, archived INTEGER NOT NULL CHECK (archived IN (0, 1)),
    current_conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id) DEFERRABLE INITIALLY DEFERRED,
    retained_json TEXT
  ) STRICT;
  CREATE TABLE conversations (
    conversation_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE,
    bot_id TEXT NOT NULL REFERENCES bots(bot_id) DEFERRABLE INITIALLY DEFERRED, revision INTEGER NOT NULL,
    draft_json TEXT NOT NULL, binding_json TEXT,
    latest_execution_id TEXT REFERENCES executions(execution_id) DEFERRABLE INITIALLY DEFERRED
  ) STRICT;
  CREATE TABLE activities (
    activity_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE,
    owner_bot_id TEXT NOT NULL REFERENCES bots(bot_id), origin_conversation_id TEXT NOT NULL REFERENCES conversations(conversation_id),
    revision INTEGER NOT NULL, state TEXT NOT NULL, goal_json TEXT NOT NULL,
    requirements_json TEXT NOT NULL, questions_json TEXT NOT NULL, materials_json TEXT NOT NULL,
    input_files_json TEXT NOT NULL, execution_ids_json TEXT NOT NULL, results_json TEXT NOT NULL,
    binding_json TEXT, retained_json TEXT
  ) STRICT;
  CREATE TABLE executions (
    execution_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, revision INTEGER NOT NULL, state TEXT NOT NULL,
    operation_id TEXT NOT NULL, input_json TEXT NOT NULL, error_json TEXT, engine_run_id TEXT,
    imported_activity_id TEXT REFERENCES activities(activity_id) DEFERRABLE INITIALLY DEFERRED, retained_json TEXT
  ) STRICT;
  CREATE TABLE operations (
    operation_id TEXT PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, method_json TEXT NOT NULL, fingerprint TEXT NOT NULL,
    receipt_json TEXT NOT NULL, reply_json TEXT NOT NULL, reply_error_json TEXT,
    connection_revision INTEGER REFERENCES connections(revision), retained_json TEXT
  ) STRICT;
  CREATE TABLE connections (
    revision INTEGER PRIMARY KEY, sort_order INTEGER NOT NULL UNIQUE, source_id TEXT NOT NULL,
    configuration_json TEXT NOT NULL
  ) STRICT;
`, 'ALTER TABLE bots ADD COLUMN system_kind TEXT;'];
const LATEST_SCHEMA_VERSION = MIGRATIONS.length;

type Snapshot = { data: Buffer; digest: string };
type Mirror = { snapshot: Snapshot; previous: string | null; temp: string };

const encode = (value: unknown): string | null => JSON.stringify(value) ?? null;
const decode = (value: SQLOutputValue): unknown => value === null ? undefined : JSON.parse(String(value));
const sha256 = (data: Buffer) => createHash('sha256').update(data).digest('hex');

function snapshotFor(profile: ProductProfile): Snapshot {
  const data = Buffer.from(canonicalJSON(profile) + '\n');
  return { data, digest: sha256(data) };
}

export class ProductSQLite {
  private readonly databasePath: string;
  private readonly db: DatabaseSync;
  private digest?: string;

  constructor(private readonly profilePath: string) {
    const directory = dirname(profilePath);
    mkdirSync(directory, { recursive: true, mode: 0o700 });
    this.databasePath = basename(profilePath) === 'contacts.json' ? join(directory, 'nagi.db') : `${profilePath}.sqlite`;
    this.db = new DatabaseSync(this.databasePath);
    try {
      try { chmodSync(this.databasePath, 0o600); } catch {}
      this.db.exec('PRAGMA busy_timeout = 5000; PRAGMA foreign_keys = ON;');
      this.transaction(() => {
        const version = Number(this.db.prepare('PRAGMA user_version').get()?.user_version);
        if (version > LATEST_SCHEMA_VERSION) throw new RequestError('profile_unavailable', 'The SQLite profile uses a newer schema version');
        for (let index = version; index < LATEST_SCHEMA_VERSION; index++) {
          this.db.exec(MIGRATIONS[index]);
          this.db.exec(`PRAGMA user_version = ${index + 1}`);
        }
      });
      this.db.exec('PRAGMA journal_mode = WAL; PRAGMA synchronous = NORMAL;');
    } catch (error) {
      this.close();
      throw error;
    }
  }

  open(paths: ProductProfilePaths, credentials: PrivateConnection | undefined): ProductProfile {
    let mirror: Mirror | undefined;
    try {
      const profile = this.transaction(() => {
        for (const suffix of ['-wal', '-shm']) {
          try { chmodSync(this.databasePath + suffix, 0o600); } catch {}
        }
        const snapshot = this.readMirror();
        const previous = snapshot?.digest ?? null;
        const digest = this.meta('snapshot_sha256');
        const pending = this.meta('pending_snapshot_previous');
        if (digest !== undefined && (previous === null || previous === digest || pending === encode(previous))) {
          const profile = this.readProfile();
          this.digest = digest;
          if (pending !== undefined && previous !== digest) {
            const restored = snapshotFor(profile);
            if (restored.digest !== digest) throw new RequestError('profile_unavailable', 'The SQLite profile does not match its pending snapshot');
            mirror = this.stageMirror(restored, previous);
          } else if (pending !== undefined) {
            this.clearPending();
          }
          return profile;
        }
        if (!snapshot && !paths.fresh) throw new RequestError('profile_unavailable', 'The product profile is missing');
        const profile = importProductSnapshot(snapshot && JSON.parse(snapshot.data.toString('utf8')), credentials);
        mirror = this.stageMirror(snapshotFor(profile), previous);
        this.replaceProfile(profile, mirror);
        return profile;
      });
      if (mirror) this.publish(mirror);
      return profile;
    } catch (error) {
      if (mirror) rmSync(mirror.temp, { force: true });
      if (error instanceof RequestError) throw error;
      throw new RequestError('profile_unavailable', 'The saved product profile could not be read or imported');
    }
  }

  write(profile: ProductProfile): void {
    let mirror: Mirror | undefined;
    try {
      this.transaction(() => {
        if (this.meta('snapshot_sha256') !== this.digest) throw new RequestError('profile_unavailable', 'The SQLite profile changed in another worker');
        const previous = this.readMirror()?.digest ?? null;
        if (previous !== null && previous !== this.digest && this.meta('pending_snapshot_previous') !== encode(previous)) {
          throw new RequestError('profile_unavailable', 'The recovery snapshot changed while the profile was open');
        }
        mirror = this.stageMirror(snapshotFor(profile), previous);
        this.replaceProfile(profile, mirror);
      });
    } catch (error) {
      if (mirror) rmSync(mirror.temp, { force: true });
      throw error;
    }
    this.publish(mirror!);
  }

  close(): void {
    this.db.close();
  }

  private transaction<T>(action: () => T): T {
    this.db.exec('BEGIN IMMEDIATE');
    try {
      const result = action();
      this.db.exec('COMMIT');
      return result;
    } catch (error) {
      this.db.exec('ROLLBACK');
      throw error;
    }
  }

  private meta(key: string): string | undefined {
    return this.db.prepare('SELECT value FROM meta WHERE key = ?').get(key)?.value as string | undefined;
  }

  private readMirror(): Snapshot | undefined {
    try {
      const data = readFileSync(this.profilePath);
      return { data, digest: sha256(data) };
    } catch (error) {
      const code = (error as NodeJS.ErrnoException).code;
      if (code === 'ENOENT') return;
      if (code === 'EISDIR') throw new Error(`Cannot rename a recovery snapshot over directory ${this.profilePath}`);
      throw error;
    }
  }

  private stageMirror(snapshot: Snapshot, previous: string | null): Mirror {
    const temp = `${this.profilePath}.${randomUUID()}.tmp`;
    try { writeFileSync(temp, snapshot.data, { flag: 'wx', mode: 0o600, flush: true }); }
    catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'EEXIST') rmSync(temp, { force: true });
      throw error;
    }
    return { snapshot, previous, temp };
  }

  private publish(mirror: Mirror): void {
    this.digest = mirror.snapshot.digest;
    try {
      this.transaction(() => {
        if (this.meta('snapshot_sha256') !== this.digest || this.meta('pending_snapshot_previous') === undefined) return;
        const current = this.readMirror()?.digest ?? null;
        if (current !== mirror.previous && current !== this.digest) throw new Error('The recovery snapshot changed before publication');
        if (current !== this.digest) renameSync(mirror.temp, this.profilePath);
        this.clearPending();
      });
    } catch {
      process.stderr.write('The committed SQLite profile still has a pending recovery snapshot.\n');
    } finally {
      try { rmSync(mirror.temp, { force: true }); } catch {}
    }
  }

  private clearPending(): void {
    this.db.exec("DELETE FROM meta WHERE key = 'pending_snapshot_previous'");
  }

  private readProfile(): ProductProfile {
    const bots = this.db.prepare('SELECT * FROM bots ORDER BY sort_order').all().map(row => ({
      botId: decode(row.bot_id), profile: decode(row.profile_json), profileRevision: row.profile_revision,
      archived: row.archived === 1, currentConversationId: decode(row.current_conversation_id), retained: decode(row.retained_json),
      systemKind: row.system_kind ?? undefined,
    }));
    const conversations = this.db.prepare('SELECT * FROM conversations ORDER BY sort_order').all().map(row => ({
      conversationId: decode(row.conversation_id), botId: decode(row.bot_id), revision: row.revision, draft: decode(row.draft_json),
      binding: decode(row.binding_json), latestExecutionId: decode(row.latest_execution_id),
    }));
    const activities = this.db.prepare('SELECT * FROM activities ORDER BY sort_order').all().map(row => ({
      activityId: decode(row.activity_id), ownerBotId: decode(row.owner_bot_id), originConversationId: decode(row.origin_conversation_id),
      revision: row.revision, state: row.state, goal: decode(row.goal_json), requirements: decode(row.requirements_json),
      questions: decode(row.questions_json), materials: decode(row.materials_json), inputFiles: decode(row.input_files_json),
      executionIds: decode(row.execution_ids_json), results: decode(row.results_json), binding: decode(row.binding_json), retained: decode(row.retained_json),
    }));
    const executions = this.db.prepare('SELECT * FROM executions ORDER BY sort_order').all().map(row => ({
      executionId: decode(row.execution_id), revision: row.revision, state: row.state, operationId: decode(row.operation_id),
      input: decode(row.input_json), error: decode(row.error_json), engineRunId: decode(row.engine_run_id),
      importedActivityId: decode(row.imported_activity_id), retained: decode(row.retained_json),
    }));
    const operations = this.db.prepare('SELECT * FROM operations ORDER BY sort_order').all().map(row => ({
      method: decode(row.method_json), fingerprint: row.fingerprint, receipt: decode(row.receipt_json), reply: decode(row.reply_json),
      replyError: decode(row.reply_error_json), connectionRevision: row.connection_revision ?? undefined, retained: decode(row.retained_json),
    }));
    const connections = this.db.prepare('SELECT * FROM connections ORDER BY sort_order').all().map(row => ({
      revision: row.revision, sourceId: decode(row.source_id), configuration: decode(row.configuration_json),
    }));
    const read = (key: string) => decode(this.meta(key) ?? null);
    return decodeProductProfile({ version: 1, bots, conversations, activities, executions, operations, connections,
      savedRevision: read('saved_revision'), appliedRevision: read('applied_revision'), preferences: read('preferences_json'),
      issueToolScopes: read('issue_tool_scopes_json'), untargetedReceipts: read('untargeted_receipts_json'), recovery: read('recovery_json'),
    });
  }

  private replaceProfile(profile: ProductProfile, mirror: Mirror): void {
    this.db.exec(`DELETE FROM operations; DELETE FROM activities; DELETE FROM conversations;
      DELETE FROM executions; DELETE FROM bots; DELETE FROM connections;`);
    const bots = this.db.prepare('INSERT INTO bots VALUES (?, ?, ?, ?, ?, ?, ?, ?)');
    profile.bots.forEach((row, order) => bots.run(encode(row.botId), order, encode(row.profile), row.profileRevision,
      Number(row.archived), encode(row.currentConversationId), encode(row.retained), row.systemKind ?? null));
    const conversations = this.db.prepare('INSERT INTO conversations VALUES (?, ?, ?, ?, ?, ?, ?)');
    profile.conversations.forEach((row, order) => conversations.run(encode(row.conversationId), order, encode(row.botId), row.revision,
      encode(row.draft), encode(row.binding), encode(row.latestExecutionId)));
    const activities = this.db.prepare('INSERT INTO activities VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)');
    profile.activities.forEach((row, order) => activities.run(encode(row.activityId), order, encode(row.ownerBotId), encode(row.originConversationId),
      row.revision, row.state, encode(row.goal), encode(row.requirements), encode(row.questions), encode(row.materials), encode(row.inputFiles),
      encode(row.executionIds), encode(row.results), encode(row.binding), encode(row.retained)));
    const executions = this.db.prepare('INSERT INTO executions VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)');
    profile.executions.forEach((row, order) => executions.run(encode(row.executionId), order, row.revision, row.state, encode(row.operationId),
      encode(row.input), encode(row.error), encode(row.engineRunId), encode(row.importedActivityId), encode(row.retained)));
    const connections = this.db.prepare('INSERT INTO connections VALUES (?, ?, ?, ?)');
    profile.connections.forEach((row, order) => connections.run(row.revision, order, encode(row.sourceId), encode(row.configuration)));
    const operations = this.db.prepare('INSERT INTO operations VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)');
    profile.operations.forEach((row, order) => operations.run(encode(row.receipt.operationId), order, encode(row.method), row.fingerprint,
      encode(row.receipt), encode(row.reply), encode(row.replyError), row.connectionRevision ?? null, encode(row.retained)));
    const meta = this.db.prepare('INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)');
    const remove = this.db.prepare('DELETE FROM meta WHERE key = ?');
    for (const [key, value] of Object.entries({
      saved_revision: encode(profile.savedRevision), applied_revision: encode(profile.appliedRevision), preferences_json: encode(profile.preferences),
      issue_tool_scopes_json: encode(profile.issueToolScopes), untargeted_receipts_json: encode(profile.untargetedReceipts), recovery_json: encode(profile.recovery),
      snapshot_sha256: mirror.snapshot.digest, pending_snapshot_previous: encode(mirror.previous), updated_at: new Date().toISOString(),
    })) {
      if (value === null) remove.run(key);
      else meta.run(key, value);
    }
  }
}
