import http from 'k6/http';
import exec from 'k6/execution';
import { sleep } from 'k6';
import { Counter, Rate } from 'k6/metrics';

const correctness = (__ENV.MODE || 'correctness') === 'correctness';
if (!['correctness', 'throughput'].includes(__ENV.MODE || 'correctness')) {
  throw new Error('MODE must be correctness or throughput');
}
const rule = correctness ? 'sliding_window' : (__ENV.RULE || 'token_bucket');
const rate = Number(__ENV.RATE || 1000);
const keys = Number(__ENV.KEYS || 1000);
if (!['token_bucket', 'sliding_window', 'fixed_window'].includes(rule) ||
    !Number.isInteger(rate) || rate < 1 || !Number.isInteger(keys) || keys < 1) {
  throw new Error('Invalid RULE, RATE or KEYS');
}
const base = __ENV.BASE_URL || 'http://nginx:8080';
const allowed = new Counter('allowed');
const blocked = new Counter('blocked');
const invalid = new Rate('invalid_decisions');
const upstream = [0, 1, 2].map(i => new Counter(`upstream_${i}`));

export const options = {
  scenarios: {
    checks: correctness ? {
      executor: 'shared-iterations', vus: 8, iterations: 1000, maxDuration: '20s',
      gracefulStop: '5s',
    } : {
      executor: 'constant-arrival-rate', rate, timeUnit: '1s',
      duration: __ENV.DURATION || '20s', preAllocatedVUs: 64, maxVUs: 256,
      gracefulStop: '5s',
    },
  },
  // Quota denial is an expected, successful protocol outcome.
  thresholds: {
    invalid_decisions: ['rate==0'],
    dropped_iterations: ['count==0'],
    upstream_0: ['count>0'], upstream_1: ['count>0'], upstream_2: ['count>0'],
    'http_req_duration{phase:load}': correctness ? ['p(99)>=0'] : ['p(99)<10'],
    ...(correctness ? { allowed: ['count==100'], blocked: ['count==900'] } : {}),
  },
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
  systemTags: ['status', 'method', 'name', 'scenario', 'expected_response'],
};
http.setResponseCallback(http.expectedStatuses(200, 429));

export function setup() {
  const instances = [];
  for (let i = 0; i < 30; i++) {
    const r = http.get(`${base}/healthz`, { tags: { phase: 'setup' } });
    const instance = r.headers['X-Ratelimit-Instance'];
    if (r.status !== 200 || !instance) throw new Error('Unhealthy load balancer');
    if (!instances.includes(instance)) instances.push(instance);
  }
  if (instances.length !== 3) throw new Error(`Expected 3 upstreams, saw ${instances}`);
  // Exercise Lua loading and connection pools without consuming measured keys.
  const warmKey = `warmup:${Date.now()}`;
  for (let i = 0; i < 30; i++) {
    http.post(`${base}/check`, JSON.stringify({ key: warmKey, rule }), {
      headers: { 'Content-Type': 'application/json' }, tags: { phase: 'setup' },
      timeout: '2s', redirects: 0,
    });
    sleep(0.1);
  }
  sleep(5);
  return { instances, nonce: `${Date.now()}-${Math.random().toString(16).slice(2)}` };
}

export default function (data) {
  const key = correctness ? `correctness:${data.nonce}` :
    `load:${data.nonce}:${exec.scenario.iterationInTest % keys}`;
  const r = http.post(`${base}/check`, JSON.stringify({ key, rule }), {
    headers: { 'Content-Type': 'application/json' },
    tags: { phase: 'load', name: 'POST /check' }, timeout: '2s', redirects: 0,
  });
  const index = data.instances.indexOf(r.headers['X-Ratelimit-Instance']);
  if (index >= 0) upstream[index].add(1);
  let valid = false;
  try {
    const b = r.json();
    valid = index >= 0 && b.degraded !== true && b.limit === 100 &&
      Number.isInteger(b.remaining) && b.remaining >= 0 && b.remaining < 100 &&
      r.headers['X-Ratelimit-Limit'] === '100' &&
      r.headers['X-Ratelimit-Remaining'] === String(b.remaining) &&
      !!r.headers['X-Request-Id'] &&
      ((r.status === 200 && b.allowed === true && b.retry_after_ms === 0) ||
       (r.status === 429 && b.allowed === false && b.remaining === 0 &&
        b.retry_after_ms > 0 && Number(r.headers['Retry-After']) > 0));
  } catch (_) { /* Invalid JSON is an invalid decision. */ }
  invalid.add(!valid);
  allowed.add(valid && r.status === 200 ? 1 : 0);
  blocked.add(valid && r.status === 429 ? 1 : 0);
}

export function handleSummary(data) {
  const report = {
    metadata: { mode: correctness ? 'correctness' : 'throughput', rule,
      requested_rate: correctness ? null : rate, keys: correctness ? 1 : keys,
      duration: correctness ? '1000 iterations, 20s maximum' : (__ENV.DURATION || '20s'),
      timestamp: new Date().toISOString(), base_url: base },
    ...data,
  };
  const path = __ENV.OUTPUT || '/results/summary.json';
  return { [path]: JSON.stringify(report, null, 2), stdout: `${JSON.stringify(report)}\n` };
}
