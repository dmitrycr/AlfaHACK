"""Convert ML companies.json into Company Service CSV without losing products."""
import argparse
import csv
import json
from pathlib import Path

PRODUCTS = ('rko', 'guarantee', 'loan', 'leasing', 'deposit', 'zpp', 'factoring')
COLUMNS = ('id','name','okved_section','okved_class','okved_group','sector','region',
           'revenue_minor',*PRODUCTS,'fts_flag','fts_country')


def flag(value):
    if value is None:
        return ''
    if not isinstance(value, bool):
        raise ValueError('Product and FTS values must be boolean or null')
    return str(value).lower()


def export(companies_path: Path, csv_path: Path):
    companies = json.loads(companies_path.read_text(encoding='utf-8-sig'))
    if not isinstance(companies, list) or not companies:
        raise ValueError('Expected a nonempty Company JSON array')
    seen, rows = set(), []
    for company in companies:
        if not company.get('id') or company['id'] in seen:
            raise ValueError('Missing or duplicate company ID')
        seen.add(company['id'])
        row = {key:company[key] for key in ('id','name','sector','region')}
        row.update({f'okved_{key}':company['okved'][key] for key in ('section','class','group')})
        revenue = company.get('revenue_minor')
        if revenue is not None and (type(revenue) is not int or revenue < 0):
            raise ValueError('revenue_minor must be a nonnegative integer or null')
        row['revenue_minor'] = revenue
        products=company.get('products') or {}
        unknown=set(products)-set(PRODUCTS)
        if unknown:
            raise ValueError(f'Unmapped product fields: {sorted(unknown)}')
        row.update({key:flag(products.get(key)) for key in PRODUCTS})
        ved=company.get('ved') or {}
        row.update(fts_flag=flag(ved.get('fts_flag')),fts_country=ved.get('country'))
        rows.append(row)
    csv_path.parent.mkdir(parents=True,exist_ok=True)
    with csv_path.open('w',encoding='utf-8',newline='') as stream:
        writer=csv.DictWriter(stream,fieldnames=COLUMNS)
        writer.writeheader()
        writer.writerows(rows)
    return len(rows)


if __name__ == '__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--companies-json',type=Path,required=True)
    parser.add_argument('--output',type=Path,required=True)
    args=parser.parse_args()
    print(f'Exported {export(args.companies_json,args.output)} companies')
