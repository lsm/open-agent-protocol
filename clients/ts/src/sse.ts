
export interface SSEFrame {
  event: string;
  data: string;
  lastId: string;
  hasId: boolean;
}

const BOM = '﻿';

export class SSEParser {
  private readonly decoder = new TextDecoder('utf-8');
  private pending = '';
  private started = false;
  private eventName = '';
  private dataLines: string[] = [];
  private lastId = '';
  private hasId = false;

  push(chunk: Uint8Array): SSEFrame[] {
    this.pending += this.decoder.decode(chunk, { stream: true });
    return this.drain(false);
  }

  finish(): SSEFrame[] {
    this.pending += this.decoder.decode();
    const frames = this.drain(true);
    this.pending = '';
    return frames;
  }

  private drain(eof: boolean): SSEFrame[] {
    const frames: SSEFrame[] = [];
    for (;;) {
      const index = this.pending.search(/[\r\n]/);
      if (index === -1) break;
      const terminator = this.pending[index];
      let consumed = index + 1;
      if (terminator === '\r') {
        const next = this.pending[index + 1];
        if (next === undefined) {
          if (!eof) break;
        } else if (next === '\n') {
          consumed += 1;
        }
      }
      let line = this.pending.slice(0, index);
      this.pending = this.pending.slice(consumed);
      if (!this.started) {
        this.started = true;
        if (line.startsWith(BOM)) line = line.slice(BOM.length);
      }
      this.field(line, frames);
    }
    return frames;
  }

  private field(line: string, frames: SSEFrame[]): void {
    if (line.length === 0) {
      if (this.dataLines.length > 0) {
        const event = this.eventName === '' ? 'message' : this.eventName;
        frames.push({ event, data: this.dataLines.join('\n'), lastId: this.lastId, hasId: this.hasId });
      }
      this.reset();
      return;
    }
    if (line.startsWith(':')) return;
    const colon = line.indexOf(':');
    let name: string;
    let value: string;
    if (colon === -1) {
      name = line;
      value = '';
    } else {
      name = line.slice(0, colon);
      value = line.slice(colon + 1);
      if (value.startsWith(' ')) value = value.slice(1);
    }
    switch (name) {
      case 'event':
        this.eventName = value;
        break;
      case 'data':
        this.dataLines.push(value);
        break;
      case 'id':
        if (!value.includes('\u0000')) {
          this.lastId = value;
          this.hasId = true;
        }
        break;
      default:
        break;
    }
  }

  private reset(): void {
    this.eventName = '';
    this.dataLines = [];
    this.lastId = '';
    this.hasId = false;
  }
}
