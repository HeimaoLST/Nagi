function credentialPattern(keys: Iterable<string | undefined>) {
  const secrets = [...new Set([...keys].filter((key): key is string => Boolean(key)))].sort((a, b) => b.length - a.length);
  const pattern = secrets.length ? new RegExp(secrets.map(secret => secret.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|'), 'g') : undefined;
  const marker = ['[credential]', '[REDACTED]', ''].find(value => !secrets.some(key => value.includes(key)))!;
  return { secrets, pattern, marker };
}

export function projectionRedactor(keys: Iterable<string | undefined>): (text: string) => string {
  const { pattern, marker } = credentialPattern(keys);
  return pattern ? text => text.replace(pattern, marker) : text => text;
}

export function streamingProjectionRedactor(keys: Iterable<string | undefined>): (text: string, final?: boolean) => string {
  const { pattern, secrets, marker } = credentialPattern(keys);
  if (!pattern) return text => text;
  let pending = '';
  return (fragment, final) => {
    const text = pending + fragment;
    let boundary = text.length;
    if (!final) {
      for (const secret of secrets) {
        const start = Math.max(0, text.length - secret.length + 1);
        for (let index = text.indexOf(secret[0], start); index !== -1 && index < boundary; index = text.indexOf(secret[0], index + 1)) {
          if (secret.startsWith(text.slice(index))) { boundary = index; break; }
        }
      }
    }
    pattern.lastIndex = 0;
    let consumed = 0;
    let projected = '';
    for (let match = pattern.exec(text); match && match.index < boundary; match = pattern.exec(text)) {
      projected += text.slice(consumed, match.index) + marker;
      consumed = pattern.lastIndex;
    }
    boundary = Math.max(boundary, consumed);
    pending = text.slice(boundary);
    return projected + text.slice(consumed, boundary);
  };
}

export function clipUTF8(text: string, limit: number, marker = '…'): string {
  if (Buffer.byteLength(text) <= limit) return text;
  const budget = limit - Buffer.byteLength(marker);
  if (budget < 0) return '';
  let bytes = 0;
  let end = 0;
  for (const point of text) {
    bytes += Buffer.byteLength(point);
    if (bytes > budget) break;
    end += point.length;
  }
  return text.slice(0, end) + marker;
}

export function clipEncodedText(text: string, budget: number): string {
  if (Buffer.byteLength(JSON.stringify(text)) - 2 <= budget) return text;
  const marker = '…';
  let available = budget - Buffer.byteLength(marker);
  if (available < 0) return '';
  let length = 0;
  for (const point of text) {
    available -= Buffer.byteLength(JSON.stringify(point)) - 2;
    if (available < 0) break;
    length += point.length;
  }
  return text.slice(0, length) + marker;
}
