#!/usr/bin/env node
// memory-drain.mjs — move findings out of the ledger and into the vault. Nothing else.
//
// The problem: an agent emits a finding as prose, the prose lands in a chat transcript, the
// transcript ends. Persistence that depends on someone remembering to remember does not
// survive load — the declaration rate is low, and instructing the agent to remember does
// not move it.
//
// So agents append to .claude/memory-proposals.jsonl and this drains it.
//
// An earlier shape recalled the vault's neighbourhood before each write and held near-matches
// to avoid minting rival copies. That put a relevance judgment (a human task) on the critical
// path of a mechanism whose whole purpose is to not require one, and it was too slow to run
// inside a hook.
//
// So: this is a dumb, fast, unconditional pipe. Identity → write → archive → truncate.
// No recall, no relevance band, no hold. Cost is O(proposals appended since the last run).
//
// Identity is answered exactly rather than by similarity: every proposal carries a
// content-derived op_id and the server holds an idempotency receipt for it, so a re-drain
// returns the existing engram (`idempotent: true`) instead of minting a second. That is
// O(1), needs no embedder, and is the check a similarity heuristic was standing in for.
//
// Neighbourhood curation — "does this finding supersede one already in the vault?" — is a
// real task and it is NOT gone. It moves to a separate, explicitly human-run pass that
// reads the *vault* rather than the ledger. It is deferred, not dropped; see
// .claude/memory-protocol.md.
//
// Contract, in the order the properties matter:
//   1. Nothing is lost, including concurrently. A proposal that cannot be written this time
//      stays in the ledger; one that can never be written moves to the dead-letter file
//      with its reason; a proposal appended DURING a run survives it (see memory-ledger.mjs).
//   2. Idempotent. op_id is checked server-side, so re-draining and crash-recovery are
//      no-ops rather than duplicates.
//   3. Observable from outside. Every invocation writes a receipt, including no-op,
//      debounced and failure paths, so "never ran" is never confusable with "ran and found
//      nothing".
//
// Usage:
//   node .claude/hooks/memory-drain.mjs [--dry-run] [--base URL] [--max N]
//                                       [--trigger NAME] [--hook] [--debounce MINUTES]
//
//   --hook              exit 0 regardless; the receipt carries the truth. A hook that
//                       reports failure at every session close is a hook people disable.
//   --debounce MINUTES  skip (and record a `debounced` receipt) unless the ledger has
//                       changed since the last real run AND that run is older than MINUTES.
//   --timeout MS        per-request cap on every call to the daemon (default 10000).
//   --deadline SECONDS  stop starting new writes after this long and leave the rest queued
//                       (default 45, under the hooks' 60 s kill; 0 disables).
//
// Both bounds exist because a daemon that accepts the connection and never answers used to
// hang the drain forever: the harness killed it at 60 s, which left the lock file on disk
// and NO receipt — so `stat` returned the PREVIOUS receipt, fresh-looking and wrong, and
// the leaked lock then blocked producers for the full 10-minute stale-lock window. An
// unbounded call inside a bounded hook is a bug even when the bound is someone else's.

import { existsSync, readFileSync, statSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { validate, opIdFor, explain, WRITTEN_FIELDS, IDENTITY_FIELDS } from './memory-schema.mjs'
import { paths, acquireLock, readPrefix, spliceConsumed, appendRecords, writeReceipt, readReceipt } from './memory-ledger.mjs'

const P = paths()
const DRY = process.argv.includes('--dry-run')
const HOOK = process.argv.includes('--hook')
const BASE = argOf('--base') || process.env.MUNINN_MCP_URL || 'http://127.0.0.1:8125/mcp'
const MAX = Number(argOf('--max') || process.env.MUNINN_DRAIN_MAX || 500)
const TRIGGER = argOf('--trigger') || (HOOK ? 'hook' : 'manual')
const DEBOUNCE_MIN = Number(argOf('--debounce') || 0)
const TIMEOUT_MS = Number(argOf('--timeout') || process.env.MUNINN_DRAIN_TIMEOUT_MS || 10_000)
const DEADLINE_MS = Number(argOf('--deadline') ?? 45) * 1000

// Only an outcome that actually CONSUMED from the ledger may advance the debounce
// watermark. `last_run_at` moves forward and never back, and an untouched ledger's mtime
// does not move at all, so a single `locked`/`unreachable`/`error` run that advanced it
// used to satisfy `ledgerM <= lastRun` forever — permanently disabling the debounced `Stop`
// trigger for exactly the crash/kill case `Stop` exists to cover. Measured: daemon down for
// one Stop, then healthy; 30 days later the drain was still reporting "ledger unchanged
// since the last run", considered 0, with proposals stranded.
const ADVANCES_WATERMARK = new Set(['ok', 'partial', 'empty', 'no-ledger', 'rewrite-refused'])

function argOf(flag) {
  const i = process.argv.indexOf(flag)
  return i !== -1 ? process.argv[i + 1] : null
}

// Same credential convention the flush tooling uses: a per-vault key file, then the
// process-wide token. Never invent one, and never write without one.
function tokenFor(vault) {
  const keyFile = join(homedir(), '.muninn', 'flush-keys', `${vault}.token`)
  if (existsSync(keyFile)) {
    const t = readFileSync(keyFile, 'utf8').trim()
    if (t) return t
  }
  return process.env.MUNINN_MCP_TOKEN || process.env.MUNINN_TOKEN || null
}

let rpcSeq = 0
async function mcp(tool, token, args) {
  // One signal covers the request AND the body read: a server that returns headers and
  // then stalls the body is the same hang as one that never answers at all.
  const signal = AbortSignal.timeout(TIMEOUT_MS)
  let res, text
  try {
    res = await fetch(BASE, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      body: JSON.stringify({ jsonrpc: '2.0', id: ++rpcSeq, method: 'tools/call', params: { name: tool, arguments: args } }),
      signal,
    })
    text = await res.text()
  } catch (e) {
    const timedOut = e?.name === 'TimeoutError' || signal.aborted
    return { ok: false, status: 0, why: timedOut ? `no response within ${TIMEOUT_MS} ms` : `unreachable: ${e?.message || e}` }
  }
  if (!res.ok) return { ok: false, status: res.status, why: text.slice(0, 200) }
  let env
  try { env = JSON.parse(text) } catch { return { ok: false, status: res.status, why: 'unparseable envelope' } }
  if (env.error) return { ok: false, status: res.status, why: `${env.error.code}: ${env.error.message}` }
  const inner = env.result?.content?.[0]?.text
  let data = null
  try { data = inner ? JSON.parse(inner) : null } catch { data = { raw: inner } }
  return { ok: true, status: res.status, data }
}

function newReceipt() {
  return {
    at: new Date().toISOString(),
    trigger: TRIGGER,
    dry_run: DRY,
    outcome: 'unknown',
    why: null,
    // Set exactly once, in finalize(), from ADVANCES_WATERMARK — never here and never
    // mid-run. See that set for what one wrongly-advanced watermark cost.
    last_run_at: null,
    considered: 0,
    acted_on: 0,
    counts: { written: 0, idempotent: 0, dead_lettered: 0, failed: 0, retained: 0, unapplied_annotations: 0 },
    ledger: { bytes_considered: 0, lines_considered: 0, lines_remaining: null, appended_during_run: null, partial_tail_bytes: 0 },
    duration_ms: 0,
  }
}

async function run(receipt) {
  const lastRun = prevLastRunAt ? Date.parse(prevLastRunAt) : 0

  if (DEBOUNCE_MIN > 0) {
    const ledgerM = existsSync(P.ledger) ? statSync(P.ledger).mtimeMs : 0
    const quiet = Date.now() - lastRun < DEBOUNCE_MIN * 60_000
    if (lastRun && (ledgerM <= lastRun || quiet)) {
      receipt.outcome = 'debounced'
      receipt.why = ledgerM <= lastRun
        ? 'ledger unchanged since the last run'
        : `last run was under ${DEBOUNCE_MIN} min ago`
      return { code: 0, quiet: true }
    }
  }

  const lock = acquireLock(P.lock)
  if (!lock.ok) {
    receipt.outcome = 'locked'
    receipt.why = lock.why
    return { code: 0, quiet: true }
  }
  activeLock = lock

  try {
    const snap = readPrefix(P.ledger)
    receipt.ledger.bytes_considered = snap.bytes
    receipt.ledger.lines_considered = snap.lines.length
    receipt.ledger.partial_tail_bytes = snap.partialTail
    receipt.considered = snap.lines.length

    if (!snap.exists) { receipt.outcome = 'no-ledger'; receipt.why = 'ledger file does not exist'; return { code: 0, quiet: true } }
    if (!snap.lines.length) {
      receipt.outcome = 'empty'
      receipt.why = snap.partialTail
        ? `ledger has no complete proposals (${snap.partialTail} byte(s) of an unterminated line left for the next run)`
        : 'ledger has no proposals'
      return { code: 0, quiet: true }
    }

    const probeVault = (() => { try { return JSON.parse(snap.lines[0].raw).vault } catch { return null } })()
    const probeToken = tokenFor(probeVault || 'default')
    const health = await mcp('muninn_status', probeToken, {})
    if (!health.ok) {
      receipt.outcome = 'unreachable'
      receipt.why = `MuninnDB not usable at ${BASE} (${health.why || health.status})`
      receipt.counts.retained = snap.lines.length
      receipt.ledger.lines_remaining = snap.lines.length
      console.error(`memory-drain: ${receipt.why}. Ledger untouched, ${snap.lines.length} proposal(s) queued.`)
      return { code: 1, quiet: false }
    }

    const retained = []          // stays in the ledger — could not be written THIS time
    const dead = []              // can never be written — moves to the dead-letter file
    const archive = []
    const written = []
    const idempotent = []
    const failed = []
    const unapplied = []
    let budgetSpent = 0
    let deadlineHit = false
    const deadlineAt = DEADLINE_MS > 0 ? started + DEADLINE_MS : Infinity

    for (const { no, raw } of snap.lines) {
      if (budgetSpent >= MAX) { retained.push(raw); continue }
      if (Date.now() >= deadlineAt) { deadlineHit = true; retained.push(raw); continue }

      let p
      try { p = JSON.parse(raw) } catch {
        dead.push({ line: no, reason: 'unparseable JSON', raw })
        continue
      }

      const v = validate(p)
      if (!v.ok) {
        dead.push({ line: no, reason: explain(v.problems), raw })
        continue
      }

      const token = tokenFor(p.vault)
      if (!token) {
        // Transient by nature: a credential can appear. Keep the line.
        failed.push({ line: no, why: `no credential for vault '${p.vault}' (~/.muninn/flush-keys/${p.vault}.token or MUNINN_MCP_TOKEN)` })
        retained.push(raw)
        continue
      }

      const op_id = opIdFor(p)
      if (DRY) { written.push({ line: no, concept: p.concept, id: '(dry-run)' }); budgetSpent++; retained.push(raw); continue }

      const args = { op_id }
      for (const f of WRITTEN_FIELDS) if (p[f] !== undefined) args[f] = p[f]
      if (!args.type) args.type = 'fact'

      const w = await mcp('muninn_remember', token, args)
      budgetSpent++
      if (!w.ok) {
        failed.push({ line: no, why: `write failed (${w.why || w.status})` })
        retained.push(raw)   // a failed write never consumes its line
        continue
      }
      const id = w.data?.id || null
      // An idempotency hit returns the EXISTING engram untouched. Identity is
      // (vault, concept, content) — so a re-proposal that corrects `summary`, `type` or
      // `entities` writes none of them, and without this it was reported as "already had",
      // indistinguishable from a genuine re-drain. That is principle #1's failure class:
      // silent, plausible-looking, wrong. The correction is not applied here (that is
      // curation — muninn_evolve), but it is never silent, and the archive keeps the
      // corrected text so it is recoverable.
      const notApplied = w.data?.idempotent
        ? WRITTEN_FIELDS.filter((f) => !IDENTITY_FIELDS.includes(f) && p[f] !== undefined)
        : []
      if (w.data?.idempotent) {
        idempotent.push({ line: no, concept: p.concept, id, notApplied })
        if (notApplied.length) unapplied.push({ line: no, concept: p.concept, id, fields: notApplied })
      } else {
        written.push({ line: no, concept: p.concept, id })
      }
      archive.push({
        ...p, op_id, engram_id: id, idempotent: !!w.data?.idempotent,
        ...(notApplied.length ? { annotations_not_applied: notApplied } : {}),
        drained_at: new Date().toISOString(),
      })
    }

    // Order matters: a line leaves the ledger only after its destination file holds it.
    if (!DRY) {
      appendRecords(P.archive, archive)
      appendRecords(P.deadLetter, dead.map((d) => ({
        dead_lettered_at: new Date().toISOString(), line: d.line, reason: d.reason, proposal: d.raw,
      })))
    }

    receipt.counts.written = written.length
    receipt.counts.idempotent = idempotent.length
    receipt.counts.dead_lettered = dead.length
    receipt.counts.failed = failed.length
    receipt.counts.retained = retained.length
    receipt.counts.unapplied_annotations = unapplied.length
    receipt.acted_on = written.length + idempotent.length + dead.length

    if (!DRY) {
      const spl = spliceConsumed(P.ledger, { bytes: snap.bytes, prefix: snap.prefix, retained })
      receipt.ledger.appended_during_run = spl.appendedDuringRun
      if (!spl.ok) {
        receipt.outcome = 'rewrite-refused'
        receipt.why = spl.why
        console.error(`memory-drain: ${spl.why}`)
      }
      try { receipt.ledger.lines_remaining = readPrefix(P.ledger).lines.length } catch { /* best effort */ }
    } else {
      receipt.ledger.lines_remaining = snap.lines.length
    }

    if (receipt.outcome === 'unknown') {
      const whys = []
      if (failed.length) whys.push(`${failed.length} proposal(s) could not be written this time`)
      if (deadlineHit) whys.push(`run deadline (${DEADLINE_MS / 1000}s) reached — the remainder is still queued`)
      receipt.outcome = whys.length ? 'partial' : 'ok'
      receipt.why = whys.length ? whys.join('; ') : null
    }

    console.log(
      `memory-drain: ${written.length} written, ${idempotent.length} already present, ` +
      `${dead.length} dead-lettered, ${failed.length} failed, ${retained.length} still queued` +
      `${DRY ? ' (dry run — nothing changed)' : ''}`
    )
    for (const w of written) console.log(`  wrote        ${w.id}  ${w.concept}`)
    for (const w of idempotent) console.log(`  already had  ${w.id}  ${w.concept}`)
    for (const d of dead) console.log(`  DEAD-LETTER  line ${d.line}: ${d.reason}`)
    for (const f of failed) console.log(`  FAILED       line ${f.line}: ${f.why}`)
    if (dead.length) console.log(`\n${dead.length} permanently-invalid proposal(s) moved to ${P.deadLetter} — they are out of the queue, not deleted.`)
    if (unapplied.length) {
      console.log(`\n${unapplied.length} proposal(s) matched an engram that already exists, so their non-identity fields were NOT applied:`)
      for (const u of unapplied) console.log(`  NOT APPLIED  ${u.id}  ${u.concept}: ${u.fields.join(', ')}`)
      console.log(`Identity is (${IDENTITY_FIELDS.join(', ')}). To correct anything else on an existing memory, use muninn_evolve;`)
      console.log(`the proposed text is kept verbatim in ${P.archive}.`)
    }

    return { code: failed.length ? 1 : 0, quiet: false }
  } finally {
    activeLock = null
    lock.release()
  }
}

const started = Date.now()
const receipt = newReceipt()
const prevLastRunAt = readReceipt(P)?.last_run_at || null
let activeLock = null
let finalized = false

/**
 * The single exit. Releases the lock, decides the watermark, writes the receipt.
 *
 * It runs on the normal path AND on SIGTERM/SIGINT, because the hooks that invoke this
 * script have a 60 s timeout and the harness kills what overruns it. A killed drain used to
 * leave the lock file behind and no receipt at all — the two states this mechanism most
 * needs to never be in. Everything it does is synchronous, so it completes inside a signal
 * handler. SIGKILL is still unrecoverable; the stale-lock breaker is what covers that.
 */
function finalize(code, { signal = null } = {}) {
  if (finalized) return
  finalized = true
  try { activeLock?.release() } catch { /* best effort — the stale breaker covers it */ }
  activeLock = null
  receipt.duration_ms = Date.now() - started
  if (signal) {
    receipt.outcome = 'interrupted'
    receipt.why = `killed by ${signal} after ${receipt.duration_ms} ms — the ledger was not rewritten, so nothing was consumed; any engram already written is a no-op on the next run (op_id)`
    console.error(`memory-drain: ${receipt.why}`)
  }
  receipt.last_run_at = ADVANCES_WATERMARK.has(receipt.outcome) ? receipt.at : prevLastRunAt
  try { writeReceipt(P, receipt) } catch (e) { console.error('memory-drain: could not write receipt:', e?.message || e) }
  process.exit(HOOK ? 0 : code)
}

for (const sig of ['SIGTERM', 'SIGINT', 'SIGHUP']) process.on(sig, () => finalize(1, { signal: sig }))

let code = 0
try {
  const r = await run(receipt)
  code = r.code
  if (r.quiet && !HOOK) console.log(`memory-drain: ${receipt.outcome}${receipt.why ? ` — ${receipt.why}` : ''}`)
} catch (e) {
  receipt.outcome = 'error'
  receipt.why = String(e?.stack || e?.message || e).slice(0, 500)
  console.error('memory-drain: unexpected error, ledger untouched:', e?.message || e)
  code = 1
}
finalize(code)
