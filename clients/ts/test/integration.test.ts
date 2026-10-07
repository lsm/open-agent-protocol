
import assert from 'node:assert/strict';
import { spawn, spawnSync, type ChildProcessByStdio } from 'node:child_process';
import type { Readable } from 'node:stream';
import { mkdtempSync, rmSync } from 'node:fs';
import { connect } from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test, type TestContext } from 'node:test';
import { dial, type FetchLike, type FetchResponse, type StreamReader } from '../src/client.js';
import { finalText, OapSession } from '../src/session.js';
import {
  EnvelopeType,
  payload,
  type Envelope,
  type PermissionRequestedPayload,
  type RunStartedPayload,
  type UserInputRequestedPayload,
} from '../src/protocol.js';
import { ServerError } from '../src/errors.js';
import { findRepoRoot } from './transport.js';

const repoRoot = findRepoRoot(dirname(fileURLToPath(import.meta.url)));

interface GoToolchain {
  binary: string;
  env: NodeJS.ProcessEnv;
}

function findGo(): GoToolchain | null {
  const candidates: string[] = [];
  if (process.env.OAP_GO) candidates.push(process.env.OAP_GO);
  candidates.push('go', '/tmp/runtimes/go1.27/bin/go', '/tmp/go/bin/go');
  for (const binary of candidates) {
    const probe = spawnSync(binary, ['version'], { stdio: 'ignore' });
    if (probe.status === 0 && !probe.error) return { binary, env: process.env };
  }
  return null;
}

const hub = process.env.OAP_TS_HUB ?? '';
const go = process.env.OAP_TS_SKIP_INTEGRATION === '1' || hub !== '' ? null : findGo();
const skip =
  process.env.OAP_TS_SKIP_INTEGRATION === '1'
    ? 'OAP_TS_SKIP_INTEGRATION=1'
    : hub === '' && go === null
      ? 'go toolchain not available (set OAP_TS_SKIP_INTEGRATION=1 to silence)'
      : false;

async function startHub(t: TestContext): Promise<string> {
  let binary = hub;
  if (binary === '') {
    assert.ok(go);
    const workdir = mkdtempSync(join(tmpdir(), 'oap-ts-'));
    binary = join(workdir, 'goap');
    t.after(() => rmSync(workdir, { recursive: true, force: true }));
    const build = spawnSync(go.binary, ['build', '-o', binary, './go/cmd/goap'], {
      cwd: repoRoot,
      env: go.env,
      encoding: 'utf8',
    });
    assert.equal(build.status, 0, `go build failed: ${build.stderr}`);
  }
  const daemon = spawn(binary, ['serve', '--addr', '127.0.0.1:0', '--session-history='], {
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  t.after(() => {
    if (!daemon.killed) daemon.kill('SIGTERM');
  });
  return waitForListening(daemon);
}

test(
  'integration: full lifecycle against the hub',
  { skip },
  async (t) => {
    const address = await startHub(t);
    const client = dial(address);

    const adapters = await client.adapters();
    assert.ok(adapters.some((adapter) => adapter.name === 'memory'));
    const caps = await client.capabilities('memory');
    assert.equal(caps.revision, 'reference-memory-v17');
    assert.equal(caps.descriptor.endpoint.id, 'reference.memory');

    await assert.rejects(client.open('nope'), /unknown_adapter/);

    const session = await client.open('memory', {
      sessionId: 'ts-integration-a',
      toolSources: [{ id: 'ts-integration-mcp', kind: 'local', protocol: 'mcp', endpoint: 'stdio:ts-integration' }],
    });

    const listing = await session.tools();
    assert.equal(listing.revision, caps.revision);
    const catalog = listing.tools;
    assert.equal(catalog.session_id, 'ts-integration-a');
    const attached = catalog.sources?.find((source) => source.id === 'ts-integration-mcp');
    assert.ok(attached, `attached source missing from ${JSON.stringify(catalog.sources)}`);
    assert.equal(attached.kind, 'local');
    const scripted = catalog.tools.find((tool) => tool.name === 'scripted_tool');
    assert.ok(scripted, 'scripted_tool missing from the catalog');
    assert.ok(
      catalog.sources?.some((source) => source.id === scripted.source),
      `tool source ${String(scripted.source)} resolves to no declared source`,
    );

    const opened = await session.state();
    assert.ok(opened.sources?.some((source) => source.id === 'ts-integration-mcp'));

    const models = await session.models();
    assert.equal(models.models.session_id, session.id);
    assert.equal(models.revision, caps.revision);
    assert.deepEqual(
      models.models.models.map((descriptor) => descriptor.id),
      ['reference-model-a', 'reference-model-b'],
    );
    assert.equal(models.models.models.filter((descriptor) => descriptor.default).length, 1);

    const events = session.events();
    await events.ready;
    const admission = await session.submit({
      messages: [{ role: 'user', content: 'run the golden script' }],
      delivery: 'auto',
      model_id: 'reference-model-a',
    });
    assert.equal(admission.accepted, true);
    assert.ok(admission.run_id);
    assert.equal(admission.model_id, 'reference-model-a');

    await assert.rejects(
      session.submit({
        messages: [{ role: 'user', content: 'pick another' }],
        delivery: 'auto',
        model_id: 'model-the-catalog-lacks',
      }),
      (error: unknown) => {
        assert.ok(error instanceof ServerError, `want a ServerError, got ${String(error)}`);
        assert.equal(error.code, 'model_not_found');
        assert.equal(error.details?.model_id, 'model-the-catalog-lacks');
        return true;
      },
    );

    const seen: string[] = [];
    const sequences: number[] = [];
    let firstEnvelope: Envelope | undefined;
    for await (const envelope of events) {
      firstEnvelope ??= envelope;
      seen.push(envelope.type);
      sequences.push(envelope.sequence ?? 0);
      await resolveGate(session, envelope);
    }
    assert.equal(seen[0], EnvelopeType.RunStarted);
    assert.ok(firstEnvelope);
    assert.equal(payload<RunStartedPayload>(firstEnvelope).model_id, 'reference-model-a');
    assert.equal(seen[seen.length - 1], EnvelopeType.RunCompleted);
    assert.equal(seen.filter((type) => type === EnvelopeType.RunCompleted).length, 1);
    assert.deepEqual(sequences, Array.from({ length: 12 }, (_, index) => index + 1));

    const replay = session.eventsAfter(admission.run_id ?? '', 10);
    const tail: Envelope[] = [];
    for await (const envelope of replay) tail.push(envelope);
    assert.deepEqual(tail.map((envelope) => envelope.sequence), [11, 12]);
    assert.equal(finalText(tail[1]), 'The golden script completed.');

    const state = await session.state();
    assert.equal(state.status, 'idle');
    await session.close();

    assert.equal(caps.descriptor.limits?.max_queued_runs_per_session, 1);
    const queued = await client.open('memory', { sessionId: 'ts-integration-queue' });
    const started = await queued.submit({
      messages: [{ role: 'user', content: 'parks at the gate' }],
      delivery: 'auto',
    });
    const reservation = await queued.submit({
      messages: [{ role: 'user', content: 'after you' }],
      delivery: 'queue',
    });
    assert.equal(reservation.admission, 'queued');
    assert.equal(reservation.effective_delivery, 'queue');
    const busy = await queued.state();
    assert.equal(busy.active_runs?.length, 2);
    assert.equal(busy.active_runs?.[0].run_id, started.run_id);
    assert.equal(busy.active_runs?.[1].run_id, reservation.run_id);
    assert.equal(busy.active_runs?.[1].queue_position, 1);
    assert.equal(busy.active_run_id, started.run_id);
    await queued.cancel(reservation.run_id ?? '');
    await queued.cancel(started.run_id ?? '');
    await queued.close();
  },
);

test(
  'integration: a dropped connection resumes from the cursor with no duplicates',
  { skip },
  async (t) => {
    const address = await startHub(t);

    const sabotage = sabotagedEventsFetch();
    const client = dial(address, { fetch: sabotage.fetch });
    const session = await client.open('memory', { sessionId: 'ts-integration-b' });
    const events = session.events();
    await events.ready;
    await session.submit({
      messages: [{ role: 'user', content: 'run the golden script' }],
      delivery: 'auto',
    });

    const sequences: number[] = [];
    for await (const envelope of events) {
      sequences.push(envelope.sequence ?? 0);
      await resolveGate(session, envelope);
    }
    assert.deepEqual(sequences, Array.from({ length: 12 }, (_, index) => index + 1));
    assert.ok(sabotage.connections() >= 2, `expected a reconnect, saw ${sabotage.connections()} connections`);

    await session.close();
  },
);

test(
  'integration: a request body cut short by a half-close is refused request_read',
  { skip },
  async (t) => {
    const address = new URL(await startHub(t));
    const answer = await new Promise<string>((resolve, reject) => {
      const socket = connect(Number(address.port), address.hostname);
      let received = '';
      socket.setEncoding('utf8');
      socket.on('data', (chunk: string) => {
        received += chunk;
      });
      socket.on('end', () => resolve(received));
      socket.on('error', reject);
      socket.on('connect', () => {
        socket.write(
          `POST /adapters/memory/sessions HTTP/1.1\r\nHost: ${address.host}\r\nContent-Type: application/json\r\nContent-Length: 4096\r\n\r\n{"truncated":`,
        );
        socket.end();
      });
    });
    const [head, body] = answer.split('\r\n\r\n', 2);
    assert.match(head, /^HTTP\/1\.1 400 /);
    const envelope = JSON.parse(body) as Envelope;
    assert.equal(envelope.type, EnvelopeType.ErrorResponse);
    assert.equal(payload<{ error: { code: string } }>(envelope).error.code, 'request_read');
  },
);

async function resolveGate(session: OapSession, envelope: Envelope): Promise<void> {
  if (envelope.type === EnvelopeType.ActionPermissionRequested) {
    const requested = payload<PermissionRequestedPayload>(envelope);
    await session.resolvePermission({
      interaction_id: requested.interaction_id,
      requested_by: requested.requested_by,
      run_id: requested.run_id,
      choice_id: 'approve',
      granted: true,
    });
    return;
  }
  if (envelope.type === EnvelopeType.UserInputRequested) {
    const requested = payload<UserInputRequestedPayload>(envelope);
    await session.resolveInput({
      interaction_id: requested.interaction_id,
      requested_by: requested.requested_by,
      run_id: requested.run_id,
      answers: [{ question_id: requested.questions[0].id, selected_option_ids: ['yes'] }],
    });
  }
}

function waitForListening(daemon: ChildProcessByStdio<null, Readable, Readable>): Promise<string> {
  return new Promise((resolve, reject) => {
    let buffered = '';
    const deadline = setTimeout(() => {
      cleanup();
      reject(new Error(`the hub did not start: ${buffered || 'no output'}`));
    }, 15000);
    const onData = (chunk: Buffer): void => {
      buffered += chunk.toString('utf8');
      const match = buffered.match(/listening on (http:\/\/\S+)/);
      if (match) {
        cleanup();
        resolve(match[1]);
      }
    };
    const onError = (err: Error): void => {
      cleanup();
      reject(err);
    };
    const onExit = (code: number | null): void => {
      cleanup();
      reject(new Error(`the hub exited early with code ${code}: ${buffered}`));
    };
    const cleanup = (): void => {
      clearTimeout(deadline);
      daemon.stdout.off('data', onData);
      daemon.stderr.off('data', onData);
      daemon.off('error', onError);
      daemon.off('exit', onExit);
    };
    daemon.stdout.on('data', onData);
    daemon.stderr.on('data', onData);
    daemon.once('error', onError);
    daemon.once('exit', onExit);
  });
}

function sabotagedEventsFetch(): { fetch: FetchLike; connections(): number } {
  let eventsConnections = 0;
  const decoder = new TextDecoder();
  const fetchLike: FetchLike = async (url, init) => {
    if (!url.includes('/events')) {
      const response = await fetch(url, init);
      return response as unknown as FetchResponse;
    }
    eventsConnections += 1;
    if (eventsConnections > 1) {
      const response = await fetch(url, init);
      return response as unknown as FetchResponse;
    }
    const controller = new AbortController();
    const response = await fetch(url, { ...init, signal: controller.signal });
    const reader = response.body?.getReader();
    if (!reader) return response as unknown as FetchResponse;
    let seen = '';
    const sabotaged: StreamReader = {
      read: async () => {
        const result = await reader.read();
        if (!result.done && result.value) {
          seen += decoder.decode(result.value, { stream: true });
          if ((seen.match(/"sequence":/g) ?? []).length >= 4) {
            controller.abort();
            throw new Error('integration sabotage: connection dropped mid-run');
          }
        }
        return result;
      },
      cancel: (reason?: unknown) => reader.cancel(reason),
    };
    return {
      status: response.status,
      ok: response.ok,
      headers: response.headers,
      body: { getReader: () => sabotaged },
      text: () => response.text(),
    };
  };
  return { fetch: fetchLike, connections: () => eventsConnections };
}

test(
  'integration: the work profile starts, reads, sends to, lists and stops a session',
  { skip },
  async (t) => {
    const client = dial(await startHub(t));

    const started = await client.workStart('memory', { message: 'hello there', title: 'first task' });
    assert.equal(started.ref.adapter, 'memory');
    assert.equal(started.title, 'first task');
    assert.ok(['queued', 'running', 'needs_you', 'done'].includes(started.status ?? ''), started.status);
    const sessionId = started.ref.session_id ?? '';
    assert.ok(sessionId);

    await assert.rejects(client.workStart('memory', { message: 'x', directory: '/nowhere' }), /invalid_request/);
    await assert.rejects(client.workStatus('nope'), /unknown_session/);

    await client.workSend(sessionId, 'second');
    const turns = await client.workRead(sessionId);
    assert.deepEqual(
      turns.filter((turn) => turn.role === 'user').map((turn) => turn.text),
      ['hello there', 'second'],
    );
    const paged = await client.workRead(sessionId, { after: 0, limit: 1 });
    assert.equal(paged.length, 1);
    assert.equal(paged[0]?.index, 1);

    const { groups } = await client.workList();
    assert.ok(groups.some((group) => group.work.some((piece) => piece.ref.session_id === sessionId)));

    const stopped = await client.workStop(sessionId);
    assert.equal(stopped.ref.session_id, sessionId);
  },
);
