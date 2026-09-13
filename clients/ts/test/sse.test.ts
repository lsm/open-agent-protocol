/**
 * Parser unit tests, one per WHATWG rule that matters on this wire — the
 * TypeScript port of the Go client's sse_test.go scenarios.
 */

import assert from 'node:assert/strict';
import { test } from 'node:test';
import { SSEParser, type SSEFrame } from '../src/sse.js';

const encoder = new TextEncoder();

/** Parses a whole document in one chunk the way the daemon writes them. */
function scanFrames(document: string): SSEFrame[] {
  const parser = new SSEParser();
  const frames = parser.push(encoder.encode(document));
  frames.push(...parser.finish());
  return frames;
}

/** Parses a document one byte at a time: every rule must survive chunk splits. */
function scanFramesByteWise(document: string): SSEFrame[] {
  const parser = new SSEParser();
  const bytes = encoder.encode(document);
  const frames: SSEFrame[] = [];
  for (const byte of bytes) frames.push(...parser.push(new Uint8Array([byte])));
  frames.push(...parser.finish());
  return frames;
}

function data(frame: SSEFrame): string {
  return frame.data;
}

test('simple frames dispatch at their blank lines', () => {
  const frames = scanFrames('data: one\n\ndata: two\n\n');
  assert.equal(frames.length, 2);
  for (const [index, want] of ['one', 'two'].entries()) {
    assert.equal(frames[index].event, 'message');
    assert.equal(data(frames[index]), want);
  }
});

test('multiple data lines join with newlines', () => {
  const frames = scanFrames('data: first\ndata: second\ndata:\n\n');
  assert.equal(frames.length, 1);
  assert.equal(data(frames[0]), 'first\nsecond\n');
});

test('named events and ids set, then reset between frames', () => {
  const frames = scanFrames('event: oap-overflow\nid: 42\ndata: {}\n\ndata: next\n\n');
  assert.equal(frames.length, 2);
  const first = frames[0];
  assert.equal(first.event, 'oap-overflow');
  assert.equal(first.lastId, '42');
  assert.ok(first.hasId);
  // Event name and id reset between frames; the default name returns.
  const second = frames[1];
  assert.equal(second.event, 'message');
  assert.ok(!second.hasId);
});

test('comments and keepalives are ignored', () => {
  const frames = scanFrames(': keepalive\n\ndata: one\n: mid-frame comment\ndata: two\n\n');
  assert.equal(frames.length, 1);
  assert.equal(data(frames[0]), 'one\ntwo');
});

test('CR, LF, and CRLF all terminate lines', () => {
  const document = 'data: lf\n\rdata: crlf\r\n\rdata: cr\r\rdata: tail\r\r';
  const frames = scanFrames(document);
  assert.equal(frames.length, 4);
  for (const [index, want] of ['lf', 'crlf', 'cr', 'tail'].entries()) {
    assert.equal(data(frames[index]), want);
  }
});

test('a leading UTF-8 BOM is stripped', () => {
  const frames = scanFrames('﻿data: one\n\n');
  assert.equal(frames.length, 1);
  assert.equal(data(frames[0]), 'one');
});

test('field rules: one optional space dropped, further spaces kept; unknown fields ignored', () => {
  const frames = scanFrames('data:  two spaces\ndata:one\nretry: 100\nunknown: x\nnosolondata\n\n');
  assert.equal(frames.length, 1);
  assert.equal(data(frames[0]), ' two spaces\none');
});

test('an id containing NUL is discarded', () => {
  const frames = scanFrames('id: a\0b\ndata: one\n\nid: 7\ndata: two\n\n');
  assert.equal(frames.length, 2);
  assert.ok(!frames[0].hasId, 'id containing NUL must be discarded');
  assert.ok(frames[1].hasId);
  assert.equal(frames[1].lastId, '7');
});

test('no data means no dispatch, but empty data still dispatches', () => {
  const frames = scanFrames('\n\nid: 1\n\ndata:\n\n');
  assert.equal(frames.length, 1);
  assert.equal(data(frames[0]), '');
});

test('an unterminated trailing frame is discarded', () => {
  const frames = scanFrames('data: one\n\ndata: two\n');
  assert.equal(frames.length, 1);
  assert.equal(data(frames[0]), 'one');
});

test('every rule survives byte-at-a-time chunking', () => {
  const document =
    '﻿event: oap-overflow\nid: 42\rdata: multi\r\ndata: line\ndata:\n: comment\nretry: 9\n\nid: a\0b\ndata: x\n\ndata: tail\n';
  const frames = scanFramesByteWise(document);
  assert.equal(frames.length, 2);
  assert.equal(frames[0].event, 'oap-overflow');
  assert.equal(frames[0].lastId, '42');
  assert.equal(data(frames[0]), 'multi\nline\n');
  assert.ok(!frames[1].hasId);
  assert.equal(data(frames[1]), 'x');
});

test('multi-byte UTF-8 split across chunks decodes correctly', () => {
  const parser = new SSEParser();
  const bytes = encoder.encode('data: héllo ✓\n\n');
  const split = 8; // inside the é sequence
  const first = parser.push(bytes.slice(0, split));
  const second = parser.push(bytes.slice(split));
  parser.finish();
  assert.equal(first.length, 0);
  assert.equal(second.length, 1);
  assert.equal(second[0].data, 'héllo ✓');
});
