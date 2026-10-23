import unittest
import numpy as np
import pandas as pd
from pipeline import okved_levels, resolve_profiles, split_payments, graph_features


class PipelineTests(unittest.TestCase):
    def test_okved_preserves_zero_and_missing_detail(self):
        result = okved_levels(pd.Series(['01.11.1', '46.3', '68', '46.34', '46.3.2', None]))
        self.assertEqual(result.loc[0, 'OKVED_L3'], '01.11')
        self.assertEqual(result.loc[0, 'OKVED_L1'], 'A')
        self.assertEqual(result.loc[1, 'OKVED_L1'], 'G')
        self.assertEqual(result.loc[1, 'OKVED_L2'], '46')
        self.assertTrue(pd.isna(result.loc[1, 'OKVED_L3']))
        self.assertEqual(result.loc[2, 'OKVED_L2'], '68')
        self.assertTrue(pd.isna(result.loc[2, 'OKVED_L3']))
        self.assertFalse(result.loc[4, 'OKVED_FORMAT_VALID'])
        self.assertFalse(result.loc[5, 'OKVED_FORMAT_VALID'])

    def test_okved_section_gaps_are_not_filled(self):
        result=okved_levels(pd.Series(['04','34','99','26.20']))
        self.assertFalse(result.loc[0,'OKVED_SECTION_KNOWN'])
        self.assertFalse(result.loc[1,'OKVED_SECTION_KNOWN'])
        self.assertEqual(result.loc[2,'OKVED_SECTION'],'U')
        self.assertEqual(result.loc[3,'OKVED_SECTION'],'C')

    def test_conflicting_profiles_cannot_multiply_join(self):
        frame = pd.DataFrame({'SYNTHETIC_U': ['A', 'A', 'B', 'B'],
                              'MAINOKVED_CODE': ['01.11', '01.11', '46.3', '68.20'],
                              'MAINOKVED_NAME': ['x', 'x', 'y', 'z']})
        good, bad, conflicts = resolve_profiles(frame)
        self.assertEqual(good.SYNTHETIC_U.tolist(), ['A'])
        self.assertEqual(len(bad), 2)
        self.assertEqual(conflicts, {'B'})

    def test_bad_weights_self_loops_and_repeated_pairs_are_separated(self):
        rows = [('A', 'B', '10', '2'), ('A', 'A', '90', '1'),
                ('C', 'B', '-1', '1'), ('D', 'B', '5', '1.5'),
                ('E', 'B', '5', '1'), ('E', 'B', '5', '1'),
                ('F', 'B', 'inf', '1'), ('Z', 'B', '5', '1')]
        edges, excluded, flags = split_payments(pd.DataFrame(rows, columns=['PAYER','BENEFICIARY','PAY_AMT','PAY_CNT']), {'Z'})
        self.assertEqual(len(edges), 1)
        self.assertEqual(len(excluded), 7)
        self.assertEqual(int(flags.repeated_pair.sum()), 2)
        self.assertEqual(edges.iloc[0].PAY_CNT, 2)

    def test_direction_weighted_average_concentration_and_isolates(self):
        edges = pd.DataFrame([('A','B',10.,2), ('A','C',30.,3), ('B','A',5.,1)],
                             columns=['PAYER','BENEFICIARY','PAY_AMT','PAY_CNT'])
        f, amount, count = graph_features(edges, pd.Index(['A','B','C','D'], name='SYNTHETIC_U'))
        self.assertEqual(f.loc['A','OUT_AMOUNT'], 40)
        self.assertEqual(f.loc['A','IN_AMOUNT'], 5)
        self.assertEqual(f.loc['A','OUT_AVG_PAYMENT'], 8)
        self.assertEqual(f.loc['A','OUT_TOP1_SHARE'], .75)
        self.assertEqual(f.loc['A','RECIPROCAL_NEIGHBORS'], 1)
        self.assertEqual(f.loc['A','WEAK_COMPONENT_SIZE'], 3)
        self.assertEqual(f.loc['D','WEAK_COMPONENT_SIZE'], 1)
        self.assertTrue(np.isnan(f.loc['D','OUT_AVG_PAYMENT']))
        self.assertEqual(amount['data'][0], 10)
        self.assertEqual(amount['indices'][0], 1)
        self.assertEqual(amount['indptr'].tolist(), [0,2,3,3,3])
        self.assertEqual(count['data'].sum(), 6)

    def test_empty_graph(self):
        edges = pd.DataFrame({'PAYER': pd.Series(dtype='str'), 'BENEFICIARY': pd.Series(dtype='str'),
                              'PAY_AMT': pd.Series(dtype='float64'), 'PAY_CNT': pd.Series(dtype='int64')})
        features, amount, _ = graph_features(edges, pd.Index(['A'], name='SYNTHETIC_U'))
        self.assertEqual(len(amount['data']), 0)
        self.assertFalse(features.loc['A', 'HAS_CLEAN_PAYMENTS'])

    def test_missing_and_nonnumeric_values_in_string_inputs(self):
        frame = pd.DataFrame([('A','B',None,'1'), ('B','C','5','oops'),
                              (None,'C','5','1'), ('A','D','3',None)],
                             columns=['PAYER','BENEFICIARY','PAY_AMT','PAY_CNT'], dtype='string')
        edges, excluded, _ = split_payments(frame, set())
        self.assertEqual(len(edges), 0)
        self.assertEqual(len(excluded), 4)


if __name__ == '__main__':
    unittest.main()
