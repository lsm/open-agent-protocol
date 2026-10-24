const TOKEN = /"(?:[^"\\]|\\.)*"|-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/g;
const STRING = /"(?:[^"\\]|\\.)*"/g;
const MAX_UINT64 = 18446744073709551615n;
const USAGE_FIELDS = ['input_tokens', 'output_tokens', 'total_tokens', 'cache_read_input_tokens', 'cache_write_input_tokens', 'reasoning_tokens'];
const USAGE_TYPES = new Set(['content.completed', 'run.completed', 'run.failed', 'run.cancelled', 'inference.completed', 'inference.failed']);
const STATE_TYPES = new Set(['session.open.response', 'session.state.response', 'session.state.updated']);
type Path = string[];

class WireNumber {
  constructor(readonly token: string) {}
}

function parseTokens(data: string): unknown {
  const strings: string[] = [];
  const indices = new Map<string, number>();
  const numbers: string[] = [];
  const tagged = data.replace(TOKEN, (token: string, offset: number) => {
    if (token.startsWith('"')) {
      const value = JSON.parse(token) as string;
      let index = indices.get(value);
      if (index === undefined) {
        index = strings.length;
        strings.push(value);
        indices.set(value, index);
      }
      return JSON.stringify('s' + index);
    }
    if (/^\s*:/.test(data.slice(offset + token.length))) throw new SyntaxError('numeric object key');
    numbers.push(token);
    return JSON.stringify('n' + (numbers.length - 1));
  });
  function restore(value: unknown): unknown {
    if (typeof value === 'string') {
      const index = Number(value.slice(1));
      return value.startsWith('s') ? strings[index] : new WireNumber(numbers[index]);
    }
    if (Array.isArray(value)) return value.map(restore);
    if (value !== null && typeof value === 'object') {
      return Object.fromEntries(Object.entries(value).map(([key, child]) => [strings[Number(key.slice(1))], restore(child)]));
    }
    return value;
  }
  return restore(JSON.parse(tagged));
}

function uint64(token: string, canonical: boolean): bigint {
  if (canonical && !/^(0|[1-9][0-9]*)$/.test(token)) throw new TypeError('usage count is not a canonical uint64');
  const match = /^(-?)(\d+)(?:\.(\d+))?(?:[eE]([+-]?\d+))?$/.exec(token);
  if (!match) throw new TypeError('count is not a number');
  let digits = (match[2] + (match[3] ?? '')).replace(/^0+/, '');
  if (digits === '') return 0n;
  if (match[1]) throw new RangeError('count is negative');
  const scale = BigInt(match[4] ?? '0') - BigInt((match[3] ?? '').length);
  if (scale < 0n) {
    const remaining = BigInt(digits.length) + scale;
    if (remaining <= 0n || !/^0*$/.test(digits.slice(Number(remaining)))) throw new TypeError('count is not an integer');
    digits = digits.slice(0, Number(remaining));
  } else {
    if (BigInt(digits.length) + scale > 20n) throw new RangeError('count exceeds uint64');
    digits += '0'.repeat(Number(scale));
  }
  const count = BigInt(digits);
  if (count > MAX_UINT64) throw new RangeError('count exceeds uint64');
  return count;
}

function fields(value: unknown, path: Path, apply: (object: Record<string, unknown>, key: string) => void): void {
  if (value === null || typeof value !== 'object' || path.length === 0) return;
  const [key, ...tail] = path;
  if (key === '*') {
    if (Array.isArray(value)) for (const item of value) fields(item, tail, apply);
    return;
  }
  const object = value as Record<string, unknown>;
  if (!Object.hasOwn(object, key)) return;
  if (tail.length === 0) apply(object, key);
  else fields(object[key], tail, apply);
}

function envelopePaths(value: unknown): { path: Path; canonical: boolean }[] {
  const paths = [{ path: ['sequence'], canonical: false }];
  if (value === null || typeof value !== 'object') return paths;
  const type = (value as Record<string, unknown>).type;
  if (typeof type !== 'string') return paths;
  if (USAGE_TYPES.has(type)) {
    fields(value, ['payload', 'usage'], (object, key) => {
      if (object[key] !== undefined && (object[key] === null || typeof object[key] !== 'object' || Array.isArray(object[key]))) {
        throw new TypeError('usage is not an object');
      }
    });
    for (const field of USAGE_FIELDS) paths.push({ path: ['payload', 'usage', field], canonical: true });
  }
  if (STATE_TYPES.has(type)) for (const path of [
    ['active_runs', '*', 'as_of_sequence'], ['as_of', 'settled', '*', 'sequence'], ['as_of', 'model_run_sequence', 'sequence'],
  ]) paths.push({ path: ['payload', ...path], canonical: false });
  if (type === 'models.response') paths.push({ path: ['payload', 'as_of_model_event', 'sequence'], canonical: false });
  if (type === 'session.message.submit.response') paths.push({ path: ['payload', 'target_sequence'], canonical: false });
  if (type === 'run.compaction.started' || type === 'run.compaction.ended') paths.push({ path: ['payload', 'history_tokens'], canonical: false });
  return paths;
}

function materialize(value: unknown): unknown {
  if (value instanceof WireNumber) return Number(value.token);
  if (Array.isArray(value)) return value.map(materialize);
  if (value !== null && typeof value === 'object') {
    const object = value as Record<string, unknown>;
    for (const key of Object.keys(object)) object[key] = materialize(object[key]);
  }
  return value;
}

function decode(value: unknown, paths: { path: Path; canonical: boolean }[]): unknown {
  for (const { path, canonical } of paths) fields(value, path, (object, key) => {
    const number = object[key];
    if (!(number instanceof WireNumber)) throw new TypeError(path.join('.') + ' is not a number');
    object[key] = uint64(number.token, canonical);
  });
  return materialize(value);
}

export function decodeEnvelope(data: string): unknown {
  const value = parseTokens(data);
  return decode(value, envelopePaths(value));
}

export function decodeSignal(data: string): unknown {
  const value = parseTokens(data);
  for (const key of ['last_sequence', 'requested_after', 'oldest_available', 'latest_available']) fields(value, [key], (object, field) => {
    const number = object[field];
    if (number instanceof WireNumber) {
      try { object[field] = uint64(number.token, false); } catch {}
    }
  });
  return materialize(value);
}

export function encodeEnvelope(value: unknown): string {
  const counters = new WeakMap<object, Map<string, bigint>>();
  for (const { path } of envelopePaths(value)) fields(value, path, (object, key) => {
    const count = object[key];
    if (count === undefined) return;
    if (typeof count !== 'bigint') throw new TypeError(path.join('.') + ' must be bigint');
    if (count < 0n || count > MAX_UINT64) throw new RangeError(path.join('.') + ' is outside uint64');
    const keys = counters.get(object) ?? new Map<string, bigint>();
    keys.set(key, count);
    counters.set(object, keys);
  });
  const tagged = JSON.stringify(value, function (key: string, child: unknown) {
    const counter = counters.get(this)?.get(key);
    if (counter !== undefined) return 'b' + counter;
    if (typeof child === 'bigint') throw new TypeError('bigint is only supported for uint64 fields');
    if (typeof child === 'string' || child instanceof String) return 's' + String(child);
    return child;
  });
  if (tagged === undefined) throw new TypeError('envelope is not serializable');
  return tagged.replace(STRING, (token: string, offset: number) => {
    if (/^\s*:/.test(tagged.slice(offset + token.length))) return token;
    const string = JSON.parse(token) as string;
    return string.startsWith('b') ? string.slice(1) : JSON.stringify(string.slice(1));
  });
}
