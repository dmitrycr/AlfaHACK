# Scoring Service — F2 baseline

Stateless Go service: deterministic ranking without LLM calls. `RankEvents` accepts a trusted company/profile/event snapshot, returns sorted scores, channel contributions, selected IDs, exclusion counts and the effective threshold. It does not fetch news or persist a feed; orchestration and public API wiring remain separate work.

## Run

From the repository root:

```sh
go run ./services/scoring-service/cmd/app rank --input docs/api/examples/scoring-request.json --output scoring-result.json
```

The example is entirely synthetic. `serve` exposes internal gRPC on 9090 and `/healthz`, `/readyz`, `/metrics` on 8080. Set `INTERNAL_KEY` (at least 32 bytes). Docker Compose includes the service on the backend network. No database or Kafka is required. Caller claims must have audience `scoring-service`, `service=true`, and the same company ID as both the envelope and snapshot. Maximum request size is 3 MiB, maximum event count 500; callers must aggregate batches before applying a global feed fallback.

## Baseline formula

For each channel name, select the sector entry with the largest `weight × match`. Divide their sum by the sum of maximum profile weights per channel name; multiply by `importance / 5` and freshness. Every breakdown contribution includes those same multipliers and sums to the score. Multiple sector entries cannot multiply a channel's budget. This is an explicit initial heuristic, not a calibrated model.

Industry/competitor match the company sector or OKVED class; demand/supply match the channel sector; regional matches the company region. Regulatory additionally requires a regulatory/sanctions event type. Financial/currency use factor categories and explicit exposure where present; absent exposure uses the profile channel weight, but explicit null/zero does not match. Commodity requires explicit numeric factor exposure. Evidence text is never parsed into exposures.

Freshness is stepwise: up to 1/3/7/14 days gets 1/.8/.5/.3; older gets .15. `as_of` is explicit for reproducibility. Missing/future publication dates, stale tags, explicit noise and foreign-company events are excluded. Ingestion time must not replace publication time. Tags must reference the same event revision.

Threshold starts at .15 and decreases by .05 to .05 while fewer than five positive results pass. If still sparse, optional configured one-hop sector adjacency is tried at match .25. Defaults contain no adjacency matrix: the result warns when one is missing. Zero scores never pass. All eligible scores remain in `items` for audit; `selected_event_ids` is the feed selection. Sparse results are allowed rather than filled with unrelated events.

## Configuration and remaining work

`--config path.json` or `SCORING_CONFIG` overrides defaults with strict JSON. Fields: `ruleset_version`, `threshold`, `threshold_min`, `threshold_step`, `min_events`, `freshness_steps` (`max_age_days`, `weight`), `adjacent_weight`, `adjacent_sectors` (source sector to array of adjacent sector names). Change the ruleset version whenever rules change. Adjacency direction is explicit; reverse links are not inferred.

F2-01/02 have an executable baseline; F2-05 has threshold fallback and optional adjacency. F2-03 is **not calibrated**: a labeled company/event relevance set and a target recall are required. Every response carries `baseline_not_calibrated_on_golden_set`. Before production use, supply the agreed sector matrix, evaluate recall/precision on held-out labels, and wire the caller to authoritative profiles and events. The snapshot API trusts authenticated internal callers; it cannot verify provenance against storage.

The backend event model currently lacks authoritative publication time and regional tags. Callers must supply them from normalized news/tagging outputs. Rich ML tags require an explicit adapter to the backend `Tags` contract; arbitrary ML JSON is rejected. Existing profile features are accepted, but concentration boosts and payment-confidence modifiers are not part of this baseline formula.
