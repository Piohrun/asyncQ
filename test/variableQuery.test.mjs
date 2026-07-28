import assert from 'node:assert/strict';
import test from 'node:test';

import {
  VARIABLE_QUERY_DEFAULT_TIMEOUT_MS,
  VARIABLE_QUERY_MAX_TIMEOUT_MS,
  buildVariableQueryRequest,
  extractVariableQueryValues,
  normalizeVariableQuery,
  normalizeVariableQueryTimeout,
  variableQueryDefinition,
} from '../src/variableQuery.ts';

test('normalizes variable-query timeouts to a finite configured range', () => {
  assert.equal(normalizeVariableQueryTimeout('2500'), 2500);
  assert.equal(normalizeVariableQueryTimeout('0'), 1);
  assert.equal(normalizeVariableQueryTimeout('999999'), VARIABLE_QUERY_MAX_TIMEOUT_MS);
  assert.equal(normalizeVariableQueryTimeout('Infinity'), VARIABLE_QUERY_DEFAULT_TIMEOUT_MS);
  assert.equal(normalizeVariableQueryTimeout('1e3'), VARIABLE_QUERY_DEFAULT_TIMEOUT_MS);
});

test('builds a Grafana backend variable-query envelope with datasource and range identity', () => {
  const request = buildVariableQueryRequest(
    { queryText: 'select from t', timeOut: '2500' },
    'select from t where desk=`london',
    {
      scopedVars: {},
      range: {
        from: { valueOf: () => 1767225600000 },
        to: { valueOf: () => 1767229200000 },
        raw: { from: 'now-1h', to: 'now' },
      },
    },
    { uid: 'asyncq-main', type: 'asyncq-kdbbackend-datasource' }
  );

  assert.deepEqual(request, {
    from: '1767225600000',
    to: '1767229200000',
    queries: [
      {
        queryText: 'select from t where desk=`london',
        timeOut: 2500,
        refId: 'variable-query',
        executionMode: 'sync',
        datasource: { uid: 'asyncq-main', type: 'asyncq-kdbbackend-datasource' },
      },
    ],
  });
});

test('extracts the first field from array and legacy-vector frames', () => {
  const values = extractVariableQueryValues([
    { fields: [{ values: ['alpha', 2, null, undefined] }] },
    { fields: [{ values: { toArray: () => ['beta'] } }] },
    { fields: [] },
    { fields: [{ values: { toArray: () => 'not an array' } }] },
  ]);

  assert.deepEqual(values, [{ text: 'alpha' }, { text: '2' }, { text: 'beta' }]);
});

test('normalizes editor state and creates a stable definition', () => {
  const query = normalizeVariableQuery({ queryText: undefined, timeOut: 'not-a-number' });
  assert.deepEqual(query, { queryText: '', timeOut: String(VARIABLE_QUERY_DEFAULT_TIMEOUT_MS) });
  assert.equal(variableQueryDefinition(query), `Variable query (${VARIABLE_QUERY_DEFAULT_TIMEOUT_MS} ms)`);
});
