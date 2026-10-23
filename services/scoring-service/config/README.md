# PR-05: reviewed sector matrix

Source: `ml/config/sector_matrix.csv` and `okved_to_sector.json`, ML commit
`f9643d861be69f7893406d23e889a077e2a2f320`. The matrix is unchanged in the inspected
`feature/ml-friday` commit `1381a22`. Source copies are pinned here for reproducibility.
These are heuristic configuration data, not customer payment records.

Run `python tools/data/build_sector_rules.py` from the repository root to validate
the vocabulary, direction, roles, duplicate keys, weights and rationale, and regenerate
`scoring.json` and `matrix-review.json`. Reviewed: 78 relations across 16 sectors;
74 accepted, 4 excluded with individual explanations in the report. No inferred
reverse relations, transitive links or numeric weights have been added.

`from_sector` is the company sector. For `demand`, `to_sector` buys its output;
for `supply`, it supplies the company. Finance-as-buyer links justified solely by
loans/insurance and transport-as-buyer justified by export shipping are excluded.
They need distinct finance/supply modeling instead of a generic demand edge.
The remaining rationales are economically plausible hypotheses, not observed facts.
Weights are inherited heuristics; this review does not establish their accuracy.

F2-05 uses these relations only after threshold lowering fails to find enough results:
match = `adjacent_weight × strength`, charged to an EXISTING channel of the same role.
The score denominator and channel budgets stay fixed. An exact sector match wins;
matrix links never create channels or boost industry, competitor, or regulatory.
The reason records the matrix role, target sector and rationale; the response records
the ruleset version. `source`/`evidence` continue to describe the original profile weight.
Unknown company sectors and profiles without demand/supply channels receive no matrix
fallback. The matrix is a single hop from the company sector, not from its counterparties.

The Docker image loads `/etc/alfa/scoring.json` by default. For local execution:

```sh
go run ./services/scoring-service/cmd/app rank --config services/scoring-service/config/scoring.json --input docs/api/examples/scoring-request.json
```

The older `adjacent_sectors` setting is retained for compatibility but is empty in
this reviewed configuration. Do not flatten the directed matrix into that setting:
doing so would lose roles and could incorrectly label suppliers as competitors.

Remaining: held-out relevance labels for F2-03, agreed sector vocabulary with ML,
live caller integration and demo-company validation. The sample request uses a
synthetic sector spelling; real callers must use canonical ML sector names.
