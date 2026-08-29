export interface ProxyHeaderWriter {
  setHeader(name: string, value: string): unknown;
}

export interface ProxyAuthority {
  readonly host: string;
  readonly origin: string;
}

export function proxyAuthority(target: string): ProxyAuthority {
  const url = new URL(target);
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    throw new TypeError('control proxy target must use HTTP or HTTPS');
  }
  return { host: url.host, origin: url.origin };
}

export function rewriteProxyAuthority(
  request: ProxyHeaderWriter,
  authority: ProxyAuthority,
): void {
  request.setHeader('host', authority.host);
  request.setHeader('origin', authority.origin);
}
