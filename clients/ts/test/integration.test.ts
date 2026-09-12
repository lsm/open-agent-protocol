/**
 * The integration test: builds the oap binary, boots it with the built-in
 * memory adapter on a loopback port, and drives the full lifecycle — open →
 * submit → gates → terminal → disconnect/resume — through the platform
 * fetch. It skips (not fails) when the go or node prerequisites are
 * missing, and spawns no real agent processes: the memory adapter is
 * deterministic and in-process.
 */

import assert from 'node:assert/strict';
import { spawn, spawnSync, type ChildProcessByStdio } from 'node:child_process';
import type { Readable } from 'node:stream';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { dial, type FetchLike, type FetchResponse, type StreamReader } from '../src/client.js';
import { finalText, OapSession } from '../src/session.js';
import {
  EnvelopeType,
  payload,
  type Envelope,
  type PermissionRequestedPayload,
  type UserInputRequestedPayload,
} from '../src/protocol.js';
import { findRepoRoot } from './transport.js';

const repoRoot = findRepoRoot(dirname(fileURLToPath(import.meta.url)));

interface GoToolchain {
  binary: string;
  env: NodeJS.ProcessEnv;
}

/**
 * Resolves the go toolchain: the OAP_GO override, then PATH, then the
 * runtime locations this repo's environment documents. No GOTOOLCHAIN
 * pinning: a candidate older than go.mod's requirement must be free to
 * select the module's toolchain the way a bare `go build` would.
 */
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

const go = process.env.OAP_TS_SKIP_INTEGRATION === '1' ? null : findGo();
const skip = go === null ? 'go toolchain not available (set OAP_TS_SKIP_INTEGRATION=1 to silence)' : false;

test(
  'integration: full lifecycle against oap serve',
  { skip },
  async (t) => {
    assert.ok(go);
    const workdir = mkdtempSync(join(tmpdir(), 'oap-ts-'));
    const binary = join(workdir, 'oap');
    t.after(() => rmSync(workdir, { recursive: true, force: true }));

    const build = spawnSync(go.binary, ['build', '-o', binary, './cmd/oap'], {
      cwd: repoRoot,
      env: go.env,
      encoding: 'utf8',
    });
    assert.equal(build.status, 0, `go build failed: ${build.stderr}`);

    const daemon = spawn(binary, ['serve', '--addr', '127.0.0.1:0'], {
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    t.after(() => {
      if (!daemon.killed) daemon.kill('SIGTERM');
    });

    const address = await waitForListening(daemon);
    const client = dial(address);

    // Discovery.
    const adapters = await client.adapters();
    assert.ok(adapters.some((adapter) => adapter.name === 'memory'));
    const caps = await client.capabilities('memory');
    assert.equal(caps.revision, 'reference-memory-v1');
    assert.equal(caps.descriptor.endpoint.id, 'reference.memory');

    // An unknown adapter is a coded refusal.
    await assert.rejects(client.open('nope'), /unknown_adapter/);

    // One session, subscribed before submitting so the run's first envelope
    // cannot be missed.
    const session = await client.open('memory', { sessionId: 'ts-integration-a' });
    const events = session.events();
    await events.ready;
    const admission = await session.submit({
      messages: [{ role: 'user', content: 'run the golden script' }],
      delivery: 'auto',
    });
    assert.equal(admission.accepted, true);
    assert.ok(admission.run_id);

    // Consume the run, resolving the scripted gates as they arrive.
    const seen: string[] = [];
    const sequences: number[] = [];
    for await (const envelope of events) {
      seen.push(envelope.type);
      sequences.push(envelope.sequence ?? 0);
      await resolveGate(session, envelope);
    }
    assert.equal(seen[0], EnvelopeType.RunStarted);
    assert.equal(seen[seen.length - 1], EnvelopeType.RunCompleted);
    assert.equal(seen.filter((type) => type === EnvelopeType.RunCompleted).length, 1);
    assert.deepEqual(sequences, Array.from({ length: 12 }, (_, index) => index + 1));

    // The run's events were contiguous and unique.
    const replay = session.eventsAfter(admission.run_id ?? '', 10);
    const tail: Envelope[] = [];
    for await (const envelope of replay) tail.push(envelope);
    assert.deepEqual(tail.map((envelope) => envelope.sequence), [11, 12]);
    assert.equal(finalText(tail[1]), 'The golden script completed.');

    // State settles back to idle; the close contract is 204.
    const state = await session.state();
    assert.equal(state.status, 'idle');
    await session.close();
  },
);

test(
  'integration: a dropped connection resumes from the cursor with no duplicates',
  { skip },
  async (t) => {
    assert.ok(go);
    const workdir = mkdtempSync(join(tmpdir(), 'oap-ts-'));
    const binary = join(workdir, 'oap');
    t.after(() => rmSync(workdir, { recursive: true, force: true }));

    const build = spawnSync(go.binary, ['build', '-o', binary, './cmd/oap'], {
      cwd: repoRoot,
      env: go.env,
      encoding: 'utf8',
    });
    assert.equal(build.status, 0, `go build failed: ${build.stderr}`);

    const daemon = spawn(binary, ['serve', '--addr', '127.0.0.1:0'], {
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    t.after(() => {
      if (!daemon.killed) daemon.kill('SIGTERM');
    });
    const address = await waitForListening(daemon);

    // Wrap the platform fetch: the first /events connection is aborted once
    // four envelopes have passed, exactly like a connection drop mid-run.
    const client = dial(address, { fetch: sabotagingFetch() });
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
    // The golden run is twelve envelopes; the resume replayed the suffix
    // after the drop with no duplicates and no gaps.
    assert.deepEqual(sequences, Array.from({ length: 12 }, (_, index) => index + 1));

    await session.close();
  },
);

/** Resolves the memory adapter's scripted gates as their envelopes arrive. */
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

/** Waits for the daemon's "listening on http://…" banner and returns the address. */
function waitForListening(daemon: ChildProcessByStdio<null, Readable, Readable>): Promise<string> {
  return new Promise((resolve, reject) => {
    let buffered = '';
    const deadline = setTimeout(() => {
      cleanup();
      reject(new Error(`oap serve did not start: ${buffered || 'no output'}`));
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
      reject(new Error(`oap serve exited early with code ${code}: ${buffered}`));
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

/** A fetch wrapper that aborts the daemon's first event-stream connection after four envelopes pass. */
function sabotagingFetch(): FetchLike {
  let eventsConnections = 0;
  const decoder = new TextDecoder();
  return async (url, init) => {
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
    let delivered = 0;
    const sabotaged: StreamReader = {
      read: async () => {
        const result = await reader.read();
        if (!result.done && result.value) {
          delivered += (decoder.decode(result.value).match(/"sequence":/g) ?? []).length;
          if (delivered >= 4) controller.abort();
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
}
