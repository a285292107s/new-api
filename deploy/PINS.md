# Deployment pins

Records which backend commit is paired with which user-center release, so the
two repositories never drift silently. No hosts, ports, credentials or secrets
belong in this file.

| Component | Repository | Pin |
| --- | --- | --- |
| Backend | `a285292107s/new-api` (this fork) | branch `feature/logdelta-20261102` @ `aeefaac0` |
| User center | `a285292107s/newapi-user-center` | commit `867cfd2` (release `20261102-1427`) |

## Why a pair

The user-center log page polls with
`GET /api/log/self?p=1&page_size=1&skip_total=1` (a one-row probe over the last
30 minutes) and only refetches the full page when the newest request id changes.

- **Backward compatible either way**: an older backend ignores `skip_total` and
  still runs `COUNT(*)`, so the frontend keeps working against an unpatched
  backend — it just loses the cost benefit.
- **The benefit needs both**: the probe only avoids `COUNT(*)` when the backend
  implements `skip_total`.

## Release order

1. Backend first: deploy the image, wait for `healthy`, check `/api/status`.
2. Frontend second: publish the static bundle atomically
   (`releases/<ts>/` + `current` symlink).
3. Update the table above in the same change that ships either side.

## Rollback

- Backend: redeploy the previous image tag/digest (the official
  `calciumion/new-api:v1.0.0-rc.47` image also works — the frontend degrades
  gracefully to whole-page polling).
- Frontend: repoint `current` at the previous release directory.
