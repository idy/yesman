// Grant permission IDs in display order, from the Go source of truth, so
// browser fixtures and row counts never carry a copied list.
import { readFileSync } from 'node:fs';

export function permissionIds() {
  const source = readFileSync(new URL('../internal/nativepermissions/permissions.go', import.meta.url), 'utf8');
  const names = Object.fromEntries([...source.matchAll(/^\s*(\w+)\s+ID = "([a-z-]+)"/gm)].map(m => [m[1], m[2]]));
  const order = /var ids = \[\.\.\.\]ID\{([^}]*)\}/.exec(source)?.[1] ?? '';
  const ids = order.split(',').map(name => name.trim()).filter(Boolean).map(name => names[name]);
  if (!ids.length || ids.some(id => !id) || new Set(ids).size !== ids.length) throw new Error('Cannot read permission IDs: ' + ids);
  return ids;
}
