import csv
import json
from pathlib import Path
import tempfile
import unittest

from export_company_import import export


class ExportTests(unittest.TestCase):
    def test_all_flags_and_null_survive_csv(self):
        company={'id':'C1','name':'Name, quoted','sector':'Trade','region':'R',
                 'okved':{'section':'G','class':'46','group':'46.51'},
                 'products':{'rko':True,'guarantee':False,'loan':None,'leasing':True,
                             'deposit':False,'zpp':True,'factoring':True},
                 'ved':{'fts_flag':None,'country':None},'revenue_minor':None}
        with tempfile.TemporaryDirectory() as directory:
            src,dst=Path(directory)/'companies.json',Path(directory)/'companies.csv'
            src.write_text(json.dumps([company]),encoding='utf-8')
            self.assertEqual(export(src,dst),1)
            with dst.open(encoding='utf-8',newline='') as stream:
                row=next(csv.DictReader(stream))
            self.assertEqual(row['name'],'Name, quoted')
            self.assertEqual(row['zpp'],'true')
            self.assertEqual(row['guarantee'],'false')
            self.assertEqual(row['loan'],'')
            self.assertEqual(row['fts_flag'],'')
            self.assertEqual(row['revenue_minor'],'')
            company['products']['new_product']=True
            src.write_text(json.dumps([company]),encoding='utf-8')
            with self.assertRaises(ValueError): export(src,dst)


if __name__=='__main__': unittest.main()
