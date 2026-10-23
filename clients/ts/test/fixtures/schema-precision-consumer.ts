export const schemaPrecisionCases = [
  {
    name: 'valid-boundaries',
    accepts: true,
    source: `
const run: ShutdownRun = { session_id: 's1', run_id: 'r1', status: 'running' };
const supports: FeatureSupport[] = [
  { level: 'native' },
  { level: 'native', limits: {} },
  { level: 'native', limits: { transports: ['process'] } },
  { level: 'native', limits: { transports: ['process', 'remote'] } },
];
const responses: EndpointShutdownResponse[] = [
  { outcome: 'choose', active_runs: [run] },
  { outcome: 'choose', active_runs: [run, run], held: [], lost: [] },
  { outcome: 'draining', active_runs: [] },
  { outcome: 'stopping', active_runs: [] },
  { outcome: 'draining', active_runs: [run], held: [], lost: [] },
  { outcome: 'stopping', active_runs: [run], held: [], lost: [] },
];
void supports;
void responses;
`,
  },
  {
    name: 'empty-transports',
    accepts: false,
    source: `const value: FeatureSupport = { level: 'native', limits: { transports: [] } };`,
  },
  {
    name: 'empty-choose',
    accepts: false,
    source: `const value: EndpointShutdownResponse = { outcome: 'choose', active_runs: [] };`,
  },
  {
    name: 'narrowed-choose',
    accepts: true,
    source: `
function inspect(response: EndpointShutdownResponse): ShutdownRun | undefined {
  const held: ShutdownQueuedRun[] | undefined = response.held;
  const lost: ShutdownQueuedRun[] | undefined = response.lost;
  void held;
  void lost;
  if (response.outcome === 'choose') {
    const first: ShutdownRun = response.active_runs[0];
    return first;
  }
  return response.active_runs[0];
}
void inspect;
`,
  },
] as const;
