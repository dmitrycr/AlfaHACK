"""D-01/D-03/D-04 offline data preparation. Run with --help."""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path

import numpy as np
import pandas as pd

FILES = {
    'companies': 'df_Uid_data_hack.csv',
    'payments': 'df_Uid_payment_hack.csv',
    'links': 'df_Fid_Uid_link_hack.csv',
}
REQUIRED = {
    'companies': ['SYNTHETIC_U', 'MAINOKVED_CODE', 'MAINOKVED_NAME'],
    'payments': ['PAYER', 'BENEFICIARY', 'PAY_AMT', 'PAY_CNT'],
    'links': ['SYNTHETIC_F', 'SYNTHETIC_U', 'CITY_NAME', 'RC_NAME'],
}

# Section/class table: https://www.nalog.gov.ru/html/sites/www.rn27.nalog.ru/Documents/2018/OKVED.pdf
# Structural reference only; this does not validate every detailed code or its edition.
SECTION_RANGES = [('A',1,3),('B',5,9),('C',10,33),('D',35,35),('E',36,39),
                  ('F',41,43),('G',45,47),('H',49,53),('I',55,56),('J',58,63),
                  ('K',64,66),('L',68,68),('M',69,75),('N',77,82),('O',84,84),
                  ('P',85,85),('Q',86,88),('R',90,93),('S',94,96),('T',97,98),('U',99,99)]
SECTION_BY_CLASS = {f'{number:02d}': section for section, first, last in SECTION_RANGES for number in range(first,last+1)}


def okved_levels(values: pd.Series) -> pd.DataFrame:
    """Structural prefixes only; no claim of validation against a classifier."""
    code = values.astype('string').str.strip()
    valid = code.str.fullmatch(r'\d{2}(?:\.\d(?:\d(?:\.\d{1,2})?)?)?', na=False)
    normalized = code.where(valid)
    cls = normalized.str.slice(0, 2)
    section = cls.map(SECTION_BY_CLASS).astype('string')
    group = normalized.where(normalized.str.len() >= 5).str.slice(0, 5)
    return pd.DataFrame({
        'OKVED_FORMAT_VALID': valid,
        'OKVED_SECTION_KNOWN': section.notna(),
        'OKVED_SECTION': section, 'OKVED_CLASS': cls, 'OKVED_GROUP': group,
        'OKVED_L1': section, 'OKVED_L2': cls, 'OKVED_L3': group,
    })


def resolve_profiles(companies: pd.DataFrame):
    # Identical copies are safe to collapse. Conflicting profiles are not.
    unique = companies.drop_duplicates()
    conflict_ids = set(unique.loc[unique.duplicated('SYNTHETIC_U', keep=False), 'SYNTHETIC_U'].dropna())
    bad = companies.SYNTHETIC_U.isna() | companies.SYNTHETIC_U.isin(conflict_ids)
    quarantine = companies.loc[bad].copy()
    quarantine['REASON'] = np.where(quarantine.SYNTHETIC_U.isna(), 'missing_company_id', 'conflicting_profiles')
    good = unique.loc[unique.SYNTHETIC_U.notna() & ~unique.SYNTHETIC_U.isin(conflict_ids)].copy()
    good = pd.concat([good, okved_levels(good.MAINOKVED_CODE)], axis=1)
    return good, quarantine, conflict_ids


def split_payments(payments: pd.DataFrame, conflict_ids: set):
    p = payments.copy()
    p['SOURCE_ROW'] = np.arange(2, len(p) + 2)
    amt = pd.to_numeric(p.PAY_AMT, errors='coerce').astype('float64')
    cnt = pd.to_numeric(p.PAY_CNT, errors='coerce').astype('float64')
    flags = pd.DataFrame({
        'missing_endpoint': p.PAYER.isna() | p.BENEFICIARY.isna(),
        'invalid_amount': ~np.isfinite(amt) | (amt <= 0),
        'invalid_count': ~np.isfinite(cnt) | (cnt <= 0) | (cnt % 1 != 0) | (cnt >= 2**63),
        'ambiguous_company': p.PAYER.isin(conflict_ids) | p.BENEFICIARY.isin(conflict_ids),
        'self_payment': p.PAYER.eq(p.BENEFICIARY).fillna(False),
        # No period / transaction ID: repeated aggregates cannot safely be summed.
        'repeated_pair': p.duplicated(['PAYER', 'BENEFICIARY'], keep=False),
    }, index=p.index)
    excluded = flags.any(axis=1)
    quarantine = pd.concat([p.loc[excluded], flags.loc[excluded]], axis=1)
    edges = p.loc[~excluded, ['PAYER', 'BENEFICIARY']].copy()
    edges['PAY_AMT'] = amt.loc[~excluded].astype('float64')
    edges['PAY_CNT'] = cnt.loc[~excluded].astype('int64')
    return edges.reset_index(drop=True), quarantine, flags


def graph_features(edges: pd.DataFrame, node_ids: pd.Index):
    features = pd.DataFrame(index=node_ids)
    for endpoint, other, prefix in [('PAYER', 'BENEFICIARY', 'OUT'), ('BENEFICIARY', 'PAYER', 'IN')]:
        grouped = edges.groupby(endpoint, sort=False)
        features[f'{prefix}_DEGREE'] = grouped[other].nunique().reindex(node_ids, fill_value=0)
        features[f'{prefix}_AMOUNT'] = grouped.PAY_AMT.sum().reindex(node_ids, fill_value=0)
        features[f'{prefix}_COUNT'] = grouped.PAY_CNT.sum().reindex(node_ids, fill_value=0)
        features[f'{prefix}_AVG_PAYMENT'] = features[f'{prefix}_AMOUNT'].div(features[f'{prefix}_COUNT'].replace(0, np.nan))
        features[f'{prefix}_TOP1_SHARE'] = grouped.PAY_AMT.max().reindex(node_ids).div(features[f'{prefix}_AMOUNT'].replace(0, np.nan))
    features['HAS_CLEAN_PAYMENTS'] = (features.IN_DEGREE + features.OUT_DEGREE) > 0
    source = node_ids.get_indexer(edges.PAYER)
    target = node_ids.get_indexer(edges.BENEFICIARY)
    order = np.lexsort((target, source))
    indptr = np.concatenate(([0], np.cumsum(np.bincount(source, minlength=len(node_ids)))))
    base = {'indices': target[order], 'indptr': indptr,
            'shape': np.array([len(node_ids), len(node_ids)]), 'format': np.array(b'csr')}
    amount = {**base, 'data': edges.PAY_AMT.to_numpy()[order]}
    count = {**base, 'data': edges.PAY_CNT.to_numpy()[order]}
    pairs = pd.MultiIndex.from_arrays([source, target])
    reverse = pd.MultiIndex.from_arrays([target, source])
    reciprocal = pairs.isin(reverse)
    features['RECIPROCAL_NEIGHBORS'] = np.bincount(source[reciprocal], minlength=len(node_ids))
    # Union-find on endpoints computes weak components without a dense matrix.
    parent = np.arange(len(node_ids))
    sizes = np.ones(len(node_ids), dtype=np.int64)
    def root(x):
        while parent[x] != x:
            parent[x] = parent[parent[x]]
            x = parent[x]
        return x
    for a, b in zip(source, target):
        a, b = root(a), root(b)
        if a != b:
            if sizes[a] < sizes[b]:
                a, b = b, a
            parent[b] = a
            sizes[a] += sizes[b]
    roots = np.array([root(i) for i in range(len(node_ids))], dtype=np.int64)
    _, labels = np.unique(roots, return_inverse=True)
    features['WEAK_COMPONENT'] = labels
    features['WEAK_COMPONENT_SIZE'] = np.bincount(labels)[labels] if len(labels) else []
    return features, amount, count


def read_inputs(directory: Path):
    data, sources = {}, {}
    for name, filename in FILES.items():
        path = directory / filename
        frame = pd.read_csv(path, sep=';', encoding='utf-8-sig', dtype='string')
        missing = set(REQUIRED[name]) - set(frame.columns)
        if missing:
            raise ValueError(f'{filename}: missing columns {sorted(missing)}')
        for col in ['SYNTHETIC_U', 'SYNTHETIC_F', 'PAYER', 'BENEFICIARY']:
            if col in frame:
                frame[col] = frame[col].str.strip().replace('', pd.NA)
        data[name] = frame
        digest = hashlib.sha256()
        with path.open('rb') as stream:
            for block in iter(lambda: stream.read(1024 * 1024), b''):
                digest.update(block)
        sources[name] = {'filename': filename, 'sha256': digest.hexdigest()}
    return data, sources


def save_csv(frame, path):
    frame.to_csv(path, sep=';', index=False, encoding='utf-8-sig')


def run(input_dir: Path, output_dir: Path):
    if output_dir.resolve() == input_dir.resolve():
        raise ValueError('Input and output directories must differ')
    data, sources = read_inputs(input_dir)
    companies, payments, links = (data[k] for k in ['companies', 'payments', 'links'])
    profiles, bad_profiles, conflict_ids = resolve_profiles(companies)
    edges, bad_payments, payment_flags = split_payments(payments, conflict_ids)
    node_ids = pd.Index(sorted(set(pd.concat([
        companies.SYNTHETIC_U, payments.PAYER, payments.BENEFICIARY, links.SYNTHETIC_U,
    ]).dropna())), name='SYNTHETIC_U')
    features, amount, count = graph_features(edges, node_ids)
    nodes = pd.DataFrame({'SYNTHETIC_U': node_ids, 'NODE_INDEX': np.arange(len(node_ids))})
    nodes['HAS_PROFILE_RECORD'] = nodes.SYNTHETIC_U.isin(companies.SYNTHETIC_U)
    nodes['HAS_USABLE_PROFILE'] = nodes.SYNTHETIC_U.isin(profiles.SYNTHETIC_U)
    nodes['PROFILE_CONFLICT'] = nodes.SYNTHETIC_U.isin(conflict_ids)
    nodes['HAS_RAW_PAYMENTS'] = nodes.SYNTHETIC_U.isin(pd.concat([payments.PAYER, payments.BENEFICIARY]))
    excluded_ids = set(pd.concat([bad_payments.PAYER, bad_payments.BENEFICIARY]).dropna())
    nodes['HAS_EXCLUDED_PAYMENTS'] = nodes.SYNTHETIC_U.isin(excluded_ids)
    nodes = nodes.merge(profiles, on='SYNTHETIC_U', how='left', validate='one_to_one')
    nodes = nodes.merge(features.reset_index(), on='SYNTHETIC_U', how='left', validate='one_to_one')
    valid_link = links.SYNTHETIC_F.notna() & links.SYNTHETIC_U.notna()
    clean_links = links.loc[valid_link].drop_duplicates().copy()
    clean_links['HAS_USABLE_PROFILE'] = clean_links.SYNTHETIC_U.isin(profiles.SYNTHETIC_U)
    clean_links['PROFILE_CONFLICT'] = clean_links.SYNTHETIC_U.isin(conflict_ids)
    nodes['F_LINK_COUNT'] = nodes.SYNTHETIC_U.map(clean_links.groupby('SYNTHETIC_U').SYNTHETIC_F.nunique()).fillna(0).astype('int64')

    # Independent conservation checks catch join multiplication and matrix loss.
    if len(edges) + len(bad_payments) != len(payments) or len(amount['data']) != len(edges):
        raise AssertionError('Payment partition or adjacency invariant failed')
    for actual in [amount['data'].sum(), nodes.IN_AMOUNT.sum(), nodes.OUT_AMOUNT.sum()]:
        if not np.isclose(float(actual), edges.PAY_AMT.sum(), rtol=1e-12):
            raise AssertionError('Amount reconciliation failed')
    for actual in [count['data'].sum(), nodes.IN_COUNT.sum(), nodes.OUT_COUNT.sum()]:
        if int(actual) != int(edges.PAY_CNT.sum()):
            raise AssertionError('Count reconciliation failed')

    report = {
        'sources': sources,
        'assumptions': {
            'period': None, 'currency': None, 'amount_unit': None,
            'okved_levels': ['section A-U', 'class XX', 'group XX.XX'],
            'okved_section_reference': 'https://www.nalog.gov.ru/html/sites/www.rn27.nalog.ru/Documents/2018/OKVED.pdf',
            'okved_dictionary_validated': False,
            'f_link_semantics': 'unknown',
            'graph': 'PAYER -> BENEFICIARY; observed aggregates; no temporal inference',
            'repeated_pair_policy': 'quarantine all rows; no safe deduplication key',
        },
        'raw': {k: {'rows': len(v), 'columns': len(v.columns),
                    'exact_duplicate_rows': int(v.duplicated().sum()),
                    'missing': {c: int(n) for c, n in v.isna().sum().items()},
                    'distinct': {c: int(n) for c, n in v.nunique().items()}}
                for k, v in data.items()},
        'profile_conflict_ids': sorted(conflict_ids),
        'excluded_payment_flags_overlapping': {k: int(v) for k, v in payment_flags.sum().items()},
        'graph': {
            'nodes_including_isolates': len(nodes),
            'active_nodes': int(nodes.HAS_CLEAN_PAYMENTS.sum()),
            'edges': len(edges), 'excluded_payment_rows': len(bad_payments),
            'amount': float(edges.PAY_AMT.sum()), 'payment_count': int(edges.PAY_CNT.sum()),
            'weak_components_including_isolates': int(nodes.WEAK_COMPONENT.nunique()),
        },
        'coverage': {
            'raw_payment_nodes_without_profile': len(set(pd.concat([payments.PAYER, payments.BENEFICIARY]).dropna()) - set(companies.SYNTHETIC_U.dropna())),
            'link_companies_without_profile': len(set(links.SYNTHETIC_U.dropna()) - set(companies.SYNTHETIC_U.dropna())),
            'invalid_link_rows': int((~valid_link).sum()),
        },
        'checks': {'amount_and_count_reconciled': True, 'payment_partition_reconciled': True},
    }
    output_dir.mkdir(parents=True, exist_ok=True)
    quarantine_dir = output_dir / 'quarantine'
    quarantine_dir.mkdir(exist_ok=True)
    for frame, filename in [(edges, 'payment_edges.csv'), (nodes, 'company_features.csv'),
                            (clean_links, 'f_u_links.csv')]:
        save_csv(frame, output_dir / filename)
    save_csv(bad_payments, quarantine_dir / 'payments.csv')
    save_csv(bad_profiles, quarantine_dir / 'company_profiles.csv')
    save_csv(links.loc[~valid_link], quarantine_dir / 'links.csv')
    save_csv(nodes[['NODE_INDEX', 'SYNTHETIC_U']], output_dir / 'node_index.csv')
    np.savez_compressed(output_dir / 'adjacency_amount.npz', **amount)
    np.savez_compressed(output_dir / 'adjacency_count.npz', **count)
    for col in ['MAINOKVED_CODE', 'OKVED_L1', 'OKVED_L2', 'OKVED_L3', 'REGION', 'CL_TYPE', 'ECO_MARK_LABEL']:
        if col in profiles:
            freq = profiles[col].value_counts(dropna=False).rename_axis(col).reset_index(name='COMPANY_COUNT')
            save_csv(freq, output_dir / f'distribution_{col.lower()}.csv')
    coverage_tables = []
    known = nodes.loc[nodes.HAS_USABLE_PROFILE]
    for level in ['OKVED_SECTION', 'OKVED_CLASS', 'OKVED_GROUP']:
        grouped = known.groupby(level, dropna=False)
        table = grouped.agg(COMPANY_COUNT=('SYNTHETIC_U', 'size'),
                            WITH_RAW_PAYMENTS=('HAS_RAW_PAYMENTS', 'sum'),
                            WITH_CLEAN_PAYMENTS=('HAS_CLEAN_PAYMENTS', 'sum')).reset_index().rename(columns={level:'OKVED_CODE'})
        table.insert(0, 'LEVEL', level)
        table['CLEAN_COMPANY_COVERAGE'] = table.WITH_CLEAN_PAYMENTS / table.COMPANY_COUNT
        coverage_tables.append(table)
    save_csv(pd.concat(coverage_tables, ignore_index=True), output_dir / 'coverage_by_okved.csv')
    numeric_summary = []
    for table, cols in [('companies', ['REVENUE_COMPANY']), ('payments', ['PAY_AMT', 'PAY_CNT'])]:
        for col in cols:
            if col in data[table]:
                values = pd.to_numeric(data[table][col], errors='coerce')
                numeric_summary.append({'TABLE': table, 'FIELD': col, **values.describe(percentiles=[.5, .9, .99]).to_dict()})
    save_csv(pd.DataFrame(numeric_summary), output_dir / 'numeric_summary.csv')
    (output_dir / 'quality_report.json').write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding='utf-8')
    print(json.dumps(report['graph'], ensure_ascii=False, indent=2))
    return report


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input-dir', type=Path, required=True)
    parser.add_argument('--output-dir', type=Path, required=True)
    args = parser.parse_args()
    run(args.input_dir, args.output_dir)
