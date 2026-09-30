// Extract cookies for ONE target URL from the local Chrome profile, decrypted
// by the vendored @steipete/sweet-cookie 0.2.0 (./sweet-cookie, see
// internal/auth/sweetcookie/VENDOR.md; node:sqlite, no native addons, no npm).
// Input JSON on stdin: {target_url, chrome_profile, timeout_millis}
// Output JSON to $BOURSOBANK_OUTPUT_PATH: {cookie_header, cookie_count, profile, error}
// The Go side calls this twice (clients.boursobank.com + clients.boursorama.com)
// and concatenates — the dual-domain requirement. It also owns os.tmpdir():
// a private folder it deletes after node exits, even if node is killed.
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { importNodeSqlite } from './sweet-cookie/dist/util/nodeSqlite.js';

function out() { return process.env.BOURSOBANK_OUTPUT_PATH || ''; }
async function write(o) { const p = out(); if (!p) throw new Error('BOURSOBANK_OUTPUT_PATH missing'); await fs.writeFile(p, JSON.stringify(o), 'utf8'); }

async function profileRoot() {
  const h = os.homedir();
  const c = process.platform === 'darwin'
    ? [path.join(h, 'Library', 'Application Support', 'Google', 'Chrome')]
    : process.platform === 'linux'
      ? [path.join(h, '.config', 'google-chrome'), path.join(h, '.config', 'chromium')]
      : [path.join(process.env.LOCALAPPDATA || path.join(h, 'AppData', 'Local'), 'Google', 'Chrome', 'User Data')];
  for (const d of c) { try { await fs.stat(d); return d; } catch {} }
  return c[0];
}

// Auto-pick: when no profile is pinned, scan every Chrome profile and choose
// the one whose target-domain session is FRESHEST (max last_update_utc, then
// cookie count). Read-only metadata only (no values). Falls back to Default if
// node:sqlite is unavailable or nothing scores — never regresses.
function regDomain(targetUrl) {
  try { const p = new URL(targetUrl).hostname.split('.'); return p.slice(-2).join('.'); } catch { return ''; }
}
async function listProfiles(root) {
  const names = ['Default'];
  try { for (const e of await fs.readdir(root)) if (/^Profile \d+$/.test(e)) names.push(e); } catch {}
  return names;
}
async function cookieDbFor(root, name) {
  for (const f of [path.join(root, name, 'Network', 'Cookies'), path.join(root, name, 'Cookies')]) {
    try { if ((await fs.stat(f)).isFile()) return f; } catch {}
  }
  return null;
}
// null = node:sqlite unavailable (abort scan, caller falls back);
// undefined = this DB unreadable (skip, keep scanning); else {count,recent}.
async function scoreProfile(dbFile, dom) {
  let sqlite;
  // sweet-cookie's loader silences only node's "SQLite is experimental"
  // warning (noise on stderr for agents) and caches the module it uses too.
  try { sqlite = await importNodeSqlite(); } catch { return null; }
  const tmp = await fs.mkdtemp(path.join(os.tmpdir(), 'bb-scan-'));
  try {
    const cp = path.join(tmp, 'C');
    await snapshotDb(dbFile, cp); // + -wal/-shm so the score sees the live state
    // Not readOnly: the temp copy is disposable; writable lets SQLite apply
    // the WAL so MAX(last_update_utc) reflects the freshest session.
    const db = new sqlite.DatabaseSync(cp);
    try {
      // last_update_utc is a Chrome epoch (~1.3e16 µs) — too large for a JS
      // Number; read it as TEXT and compare as BigInt.
      const r = db.prepare(
        'SELECT COUNT(*) c, CAST(COALESCE(MAX(last_update_utc),0) AS TEXT) m FROM cookies WHERE host_key LIKE ?'
      ).get('%' + dom);
      return { count: Number(r.c || 0), recent: BigInt(r.m || '0') };
    } finally { db.close(); }
  } catch { return undefined; }
  finally { await fs.rm(tmp, { recursive: true, force: true }).catch(() => {}); }
}
// Returns the picked profile as a DIRECTORY PATH, not a bare name: on Linux
// the scan also covers ~/.config/chromium, but sweet-cookie resolves a bare
// name under ~/.config/google-chrome only. A path is unambiguous, and the Go
// side pins it in config so the scan runs once. Empty string = let
// sweet-cookie pick.
async function autoPickProfileName(targetUrl) {
  const root = await profileRoot();
  const dom = regDomain(targetUrl);
  if (!dom) return '';
  let best = null;
  for (const name of await listProfiles(root)) {
    const dbf = await cookieDbFor(root, name);
    if (!dbf) continue;
    const s = await scoreProfile(dbf, dom);
    if (s === null) break;        // no node:sqlite → fall back to Default
    if (s === undefined) continue; // unreadable DB → skip this profile
    if (s.count > 0 && (!best || s.recent > best.recent || (s.recent === best.recent && s.count > best.count))) {
      best = { name, dbf, ...s };
    }
  }
  if (best) {
    const dir = path.join(root, best.name);
    process.stderr.write(`bb: profil Chrome auto-sélectionné pour ${dom}: ${dir} (${best.count} cookies, le plus frais)\n`);
    return dir;
  }
  return '';
}
// Snapshot a Chromium cookie DB to `dest`, INCLUDING its `-wal`/`-shm`
// sidecars — used by scoreProfile to read live state. sweet-cookie does its
// own snapshot for the actual extraction.
async function snapshotDb(src, dest) {
  await fs.copyFile(src, dest);
  for (const sfx of ['-wal', '-shm']) {
    try { await fs.copyFile(src + sfx, dest + sfx); } catch { /* sidecar absent = already checkpointed */ }
  }
}

async function readStdin() {
  return new Promise((res, rej) => { let d = ''; process.stdin.setEncoding('utf8'); process.stdin.on('data', c => d += c); process.stdin.on('end', () => res(d)); process.stdin.on('error', rej); });
}

async function main() {
  const inp = JSON.parse((await readStdin()) || '{}');
  const targetUrl = String(inp.target_url || '').trim();
  if (!targetUrl) throw new Error('target_url missing');
  const timeoutMs = Number.isFinite(inp.timeout_millis) && inp.timeout_millis > 0 ? inp.timeout_millis : 5000;
  const explicit = inp.chrome_profile ? String(inp.chrome_profile).trim() : '';

  const profileName = explicit || await autoPickProfileName(targetUrl);
  const { getCookies, toCookieHeader } = await import('./sweet-cookie/dist/index.js');
  const opts = {
    url: targetUrl,
    browsers: ['chrome'],
    timeoutMs,
    ...(profileName ? { chromeProfile: profileName } : {}),
  };
  const { cookies, warnings } = await getCookies(opts);
  for (const w of warnings) process.stderr.write(`bb: sweet-cookie: ${w}\n`);
  const header = toCookieHeader(cookies, { dedupeByName: true });
  await write({ cookie_header: header, cookie_count: cookies.length, profile: profileName, error: '' });
}

main().catch(async (e) => { try { await write({ cookie_header: '', cookie_count: 0, profile: '', error: String(e && e.message || e) }); } catch {} process.exitCode = 1; });
