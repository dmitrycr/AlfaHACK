"""Validate the pinned ML draft and build reviewed scoring rules (stdlib only)."""
import csv
import hashlib
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CONFIG = ROOT / 'services/scoring-service/config'
EXCLUDED = {
    ('Строительство', 'Финансы', 'demand'): 'Финансирование покупателей не доказывает покупку строительной продукции банком.',
    ('Автомобили', 'Финансы', 'demand'): 'Автокредитование не является прямой покупкой автомобилей банком.',
    ('Здравоохранение и социальная сфера', 'Финансы', 'demand'): 'Страховое финансирование требует отдельной модели; покупатель услуги не установлен.',
    ('Агро и пищевая промышленность', 'Транспорт и логистика', 'demand'): 'Экспортная перевозка — услуга для агрокомпании, а не покупка её продукции перевозчиком.',
}

def build():
    source = CONFIG / 'sector_matrix.source.csv'
    taxonomy = json.loads((CONFIG / 'okved_to_sector.source.json').read_text(encoding='utf-8'))
    sectors = set(taxonomy['classes'].values()) | {taxonomy['default']}
    seen, accepted, rejected = set(), [], []
    with source.open(encoding='utf-8-sig', newline='') as stream:
        for row in csv.DictReader(stream):
            key = tuple(row[k].strip() for k in ('from_sector', 'to_sector', 'role'))
            weight = float(row['strength'])
            if key in seen or key[0] not in sectors or key[1] not in sectors or key[0] == key[1] or key[2] not in ('demand','supply') or not 0 < weight <= 1 or not row['rationale'].strip():
                raise ValueError(f'Invalid relation: {row}')
            seen.add(key)
            relation = dict(zip(('from_sector','to_sector','role'), key), strength=weight, rationale=row['rationale'].strip())
            if key in EXCLUDED:
                rejected.append(dict(relation, review_reason=EXCLUDED[key]))
            else:
                accepted.append(relation)
    if not set(EXCLUDED).issubset(seen):
        raise ValueError('Pinned source changed: review exclusions again')
    accepted.sort(key=lambda r: (r['from_sector'],r['role'],r['to_sector']))
    config = dict(ruleset_version='f2-sector-matrix-v1', threshold=.15, threshold_min=.05, threshold_step=.05, min_events=5,
        freshness_steps=[dict(max_age_days=d,weight=w) for d,w in [(1,1),(3,.8),(7,.5),(14,.3),(9999,.15)]],
        adjacent_weight=.25, adjacent_sectors={}, sector_relations=accepted)
    report = dict(source_commit='f9643d861be69f7893406d23e889a077e2a2f320', source_sha256_lf_utf8=hashlib.sha256(source.read_text(encoding='utf-8-sig').encode('utf-8')).hexdigest(),
        input_relations=len(seen), accepted_relations=len(accepted), sectors=len(sectors), excluded=rejected,
        limitations=['Heuristic review, not empirical calibration.', 'No reverse edges or transitive closure.',
        'Matrix fallback uses existing demand/supply channel budgets only.', 'Golden-set recall remains unmeasured.'])
    for name,data in [('scoring.json',config),('matrix-review.json',report)]:
        if '--check' in sys.argv:
            if json.loads((CONFIG/name).read_text(encoding='utf-8')) != data:
                raise ValueError(f'{name} is out of date; regenerate and review')
        else:
            (CONFIG/name).write_text(json.dumps(data,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
    print(f'Validated {len(seen)} relations; accepted {len(accepted)}; excluded {len(rejected)}; sectors {len(sectors)}')

if __name__ == '__main__':
    build()
