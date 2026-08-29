import assert from 'node:assert/strict';
import test from 'node:test';

import { proxyAuthority, rewriteProxyAuthority } from './viteProxyAuthority.ts';

test('the development proxy rewrites Host and Origin to the target authority', () => {
  const headers = new Map<string, string>();
  rewriteProxyAuthority({
    setHeader(name, value) {
      headers.set(name, value);
    },
  }, proxyAuthority('http://127.0.0.1:19091'));

  assert.deepEqual(Object.fromEntries(headers), {
    host: '127.0.0.1:19091',
    origin: 'http://127.0.0.1:19091',
  });
});

test('proxy authority preserves HTTPS and omits its default port', () => {
  assert.deepEqual(proxyAuthority('https://control.example:443'), {
    host: 'control.example',
    origin: 'https://control.example',
  });
  assert.throws(() => proxyAuthority('ssh://control.example'), /HTTP or HTTPS/);
});
