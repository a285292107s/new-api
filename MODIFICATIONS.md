# Modifications

This repository is a fork of [QuantumNous/new-api](https://github.com/QuantumNous/new-api)
(AGPL-3.0). It is **not** a rebranded redistribution: the project name, the
`LICENSE`, the `NOTICE` and the required UI attribution are preserved
unchanged. Only the changes listed below are ours, marked per AGPL-3.0 §7(c).

## Base

| | |
| --- | --- |
| Upstream | `QuantumNous/new-api` |
| Base commit | `e835b1b` (tag `v1.0.0-rc.47`, 2026-10-24) |
| Branch carrying the changes | `feature/logdelta-20261102` |

## Changes

Commit `feat(log): 日志接口增量能力与查询优化`:

1. **`GET /api/log/self` accepts an optional `skip_total=1|true`.**
   When set, the full-range `COUNT(*)` is skipped and `data.total` is returned as
   `-1`. Pagination, ordering, filters and the user-facing field projection are
   unchanged, and callers that do not pass the parameter keep the previous
   behaviour. Rationale: a client that only asks "is there anything newer than
   the last row I have" needs one row, and paying for `COUNT(*)` on every poll is
   the dominant server-side cost of polling a log page.

2. **`GET /api/log/self/stat` filters by `user_id` instead of `username`.**
   `username` is mutable, so a renamed account silently drifted to a different
   history. Numeric semantics are unchanged: `quota` covers the requested range,
   `rpm`/`tpm` always cover the last 60 seconds, and only consume logs count.
   It deliberately stays two queries — a single conditional-aggregate variant was
   measured slower on PostgreSQL (the planner abandons the composite index and
   does a sequential scan).

3. **Composite indexes** on `logs (user_id, created_at)`,
   `logs (username, type, created_at)` and `quota_data (user_id, created_at)`.
   On an existing large table, create them with `CREATE INDEX CONCURRENTLY`
   before upgrading so `AutoMigrate` finds them already present and does not
   block writes.

4. **Tests**: `model/log_page_test.go` (new) and one added case in
   `controller/log_export_test.go`, covering the `skip_total` contract, the
   numerical parity of the new stat query with the previous one, and the
   presence of the indexes.

## Build

No new build flags or base-image changes are required; the upstream `Dockerfile`
builds this branch as-is:

```bash
docker build -t new-api:custom .
```

The embedded admin frontend is unchanged.

## Keeping up with upstream

```bash
git fetch upstream
git rebase upstream/main
```

Conflicts, if any, are confined to `model/log.go`, `model/usedata.go` and
`controller/log.go`.

## Attribution

The upstream `LICENSE` and `NOTICE` are kept in place, including the AGPLv3 §7
additional terms: the notice "Frontend design and development by New API
contributors." and a visible link to <https://github.com/QuantumNous/new-api>
are preserved in the user interface.
