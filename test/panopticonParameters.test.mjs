import assert from 'node:assert/strict';
import test from 'node:test';

import {
  Q_TEMPLATE_SUBSTITUTION_LIMITS,
  assertQTemplateExpansionLength,
  expandPanopticonDashboardParameters,
  formatQTemplateVariable,
  interpolateQTemplateVariables,
} from '../src/panopticonParameters.ts';

const grafanaVariablePatternSource =
  String.raw`\$(\w+)|\[\[(\w+?)(?::(\w+))?\]\]|\${(\w+)(?:\.([^:^}]+))?(?::([^}]+))?}`;

function makeLegacyGrafanaReplace(dashboardValues = {}) {
  return (target, scopedVars, formatter, interpolations) => {
    const variablePattern = new RegExp(grafanaVariablePatternSource, 'g');
    return target.replace(
      variablePattern,
      (match, dollarName, bracketName, bracketFormat, braceName, fieldPath, braceFormat) => {
        const variableName = dollarName || bracketName || braceName;
        const format = bracketFormat || braceFormat || formatter;
        let rawValue;
        let hasValue = false;
        const scoped = scopedVars?.[variableName];
        if (scoped?.value !== null && scoped?.value !== undefined) {
          rawValue = scoped.value;
          hasValue = true;
        } else if (
          Object.hasOwn(dashboardValues, variableName) &&
          dashboardValues[variableName] !== null &&
          dashboardValues[variableName] !== undefined
        ) {
          rawValue = dashboardValues[variableName];
          hasValue = true;
        }

        let value = match;
        if (hasValue) {
          // Grafana 13.1.1 stringifies non-array objects before a custom formatter.
          const formatterValue =
            typeof rawValue === 'object' && rawValue !== null && !Array.isArray(rawValue)
              ? `${rawValue}`
              : rawValue;
          value =
            typeof format === 'function'
              ? format(formatterValue, { name: variableName }, () => '')
              : Array.isArray(formatterValue)
                ? formatterValue.join(',')
                : String(formatterValue);
        }
        interpolations.push({
          match,
          variableName,
          fieldPath: fieldPath || undefined,
          format,
          value,
          found: value !== match,
        });
        return value;
      }
    );
  };
}

const legacyGrafanaReplace = makeLegacyGrafanaReplace();

function panopticonVariable(name, value) {
  return {
    name,
    current: { value },
  };
}

function interpolate(
  input,
  scopedVars = {},
  {
    dashboardVariables = [],
    grafanaReplace = legacyGrafanaReplace,
    panopticonMode = false,
  } = {}
) {
  return interpolateQTemplateVariables(
    input,
    scopedVars,
    dashboardVariables,
    grafanaReplace,
    panopticonMode
  );
}

function assertUnsafe(value, messagePattern = /must be one conservative ASCII q token/) {
  assert.throws(
    () => formatQTemplateVariable(value, { name: 'dashboardValue' }),
    (error) => {
      assert.match(error.message, messagePattern);
      if (typeof value === 'string' && value.length > 0) {
        assert.equal(error.message.includes(value), false, 'error must not echo the rejected value');
      }
      return true;
    }
  );
}

test('accepts conservative identifier, numeric, interval, ISO, and q temporal tokens', () => {
  const safeValues = [
    'AAPL',
    '_internal',
    'BRK.B',
    'region.eu_west',
    '0',
    '-42',
    '12.50',
    '6.02e+23',
    '1e-9',
    '5ms',
    '30s',
    '15m',
    '2h',
    '7d',
    '1w',
    '1M',
    '1y',
    '2026-07-28',
    '2026-07-28T19:30:45Z',
    '2026-07-28T19:30:45.123456789+02:00',
    '2026.07.28',
    '2026.07.28D19:30:45.123456789',
    '19:30',
    '19:30:45.123',
  ];

  for (const value of safeValues) {
    assert.equal(formatQTemplateVariable(value, { name: 'safeValue' }), value);
  }
  assert.equal(formatQTemplateVariable(42, { name: 'safeValue' }), '42');
  assert.equal(formatQTemplateVariable(-0, { name: 'safeValue' }), '-0');
  assert.equal(formatQTemplateVariable(1.5e21, { name: 'safeValue' }), '1.5e+21');
});

test('rejects q syntax, comments, quotes, escaping, whitespace, controls, and Unicode', () => {
  const unsafeValues = [
    '',
    '.namespace.function',
    'a..b',
    'a-b',
    '1-2',
    '1+2',
    'a;b',
    '"AAPL"',
    "'AAPL'",
    'a\\b',
    'a/b',
    '/ comment',
    'a b',
    'a\tb',
    'a\nb',
    'a\0b',
    'a\u001fb',
    'a\u007fb',
    'a\u0085b',
    'a\u202eb',
    'Łódź',
    'ＡＡＰＬ',
    'A\u00a0B',
    'A😀',
    'a,b',
    'a[b]',
    '{a}',
    '$other',
  ];

  for (const value of unsafeValues) {
    assertUnsafe(value, value === '' ? /must not be empty/ : undefined);
  }
});

test('rejects nonfinite numbers, objects, wrappers, and unsupported scalar types', () => {
  for (const value of [Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY]) {
    assertUnsafe(value, /must be a finite number/);
  }
  for (const value of ['NaN', 'Infinity', '-Infinity', '+Infinity', '1e309']) {
    assertUnsafe(value);
  }
  for (const value of [
    true,
    false,
    null,
    undefined,
    1n,
    Symbol('x'),
    () => 'AAPL',
    new Date('2026-07-28T00:00:00Z'),
    { arbitrary: 'AAPL' },
    { value: 'AAPL', text: 'Apple' },
  ]) {
    assertUnsafe(value, /must be a string or finite number/);
  }
});

test('does not invoke object coercion or traverse a huge legacy value/text wrapper', () => {
  let coercions = 0;
  let textReads = 0;
  const coercible = {
    toString() {
      coercions++;
      return 'AAPL';
    },
  };
  const hugeWrapper = {
    value: 'AAPL',
    text: Array(Q_TEMPLATE_SUBSTITUTION_LIMITS.arrayElements + 1).fill('label'),
  };
  const accessorWrapper = {
    value: 'AAPL',
    get text() {
      textReads++;
      return 'x'.repeat(Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes + 1);
    },
  };
  let adapterCalled = false;
  const adapter = (...args) => {
    adapterCalled = true;
    return legacyGrafanaReplace(...args);
  };

  assert.throws(
    () => interpolate('$symbol', { symbol: { value: coercible } }, { grafanaReplace: adapter }),
    /must be a string or finite number/
  );
  assert.equal(adapterCalled, false);
  assert.equal(coercions, 0);

  assert.throws(
    () => interpolate('$symbol', { symbol: { value: hugeWrapper } }, { grafanaReplace: adapter }),
    /must be a string or finite number/
  );
  assert.throws(
    () =>
      interpolate(
        '$symbol',
        { symbol: { value: accessorWrapper } },
        { grafanaReplace: adapter }
    ),
    /must be a string or finite number/
  );
  assert.equal(adapterCalled, false);
  assert.equal(textReads, 0);
});

test('formats scalar and flat multi-value variables', () => {
  assert.equal(formatQTemplateVariable('AAPL', { name: 'symbol' }), 'AAPL');
  assert.equal(formatQTemplateVariable(['AAPL'], { name: 'symbols' }), 'AAPL');
  assert.equal(formatQTemplateVariable(['AAPL', 'MSFT'], { name: 'symbols' }), 'AAPL,MSFT');
  assert.equal(formatQTemplateVariable([1, 'AAPL', 2.5], { name: 'mixed' }), '1,AAPL,2.5');
  assert.equal(
    formatQTemplateVariable(['AAPL', 'MSFT'], { name: 'symbols' }, () => ''),
    'AAPL,MSFT'
  );
});

test('matches Grafana legacy dollar, bracket, and brace reference shapes', () => {
  assert.equal(
    interpolate('$word|[[word]]|${word}|$word|${missing}|[[missing:json]]', {
      word: { value: 'AAPL' },
    }),
    'AAPL|AAPL|AAPL|AAPL|${missing}|[[missing:json]]'
  );
});

test('rejects found authored formats because they bypass the validated formatter', () => {
  for (const input of ['[[word:json]]', '${word:raw}', '${word:csv}']) {
    assert.throws(
      () => interpolate(input, { word: { value: 'AAPL' } }),
      /bypassed the validated formatter/
    );
  }
});

test('rejects Grafana field paths before invoking the adapter', () => {
  let adapterCalled = false;
  assert.throws(
    () =>
      interpolate(
        '${word.fieldPath:raw}',
        { word: { value: 'AAPL' } },
        {
          grafanaReplace: () => {
            adapterCalled = true;
            return 'AAPL';
          },
        }
      ),
    /field-path interpolation is not supported/
  );
  assert.equal(adapterCalled, false);
});

test('requires every duplicate occurrence to have one ordered formatter call and audit record', () => {
  assert.equal(
    interpolate('$word/$word/${word}/[[word]]', { word: { value: 'AAPL' } }),
    'AAPL/AAPL/AAPL/AAPL'
  );

  const swappedAudit = (target, scopedVars, formatter, interpolations) => {
    const value = legacyGrafanaReplace(target, scopedVars, formatter, interpolations);
    interpolations.reverse();
    return value;
  };
  assert.throws(
    () => interpolate('$first|$second', {
      first: { value: 'AAPL' },
      second: { value: 'MSFT' },
    }, { grafanaReplace: swappedAudit }),
    /audit order mismatch/
  );
});

test('rejects registry macro and custom-All bypasses even when their output is safe', () => {
  const registryMacro = (_target, _scopedVars, formatter, interpolations) => {
    interpolations.push({
      match: '$__from',
      variableName: '__from',
      fieldPath: undefined,
      format: formatter,
      value: '1785267045000',
      found: true,
    });
    return '1785267045000';
  };
  assert.throws(
    () => interpolate('$__from', {}, { grafanaReplace: registryMacro }),
    /bypassed the validated formatter/
  );

  const customAll = (_target, _scopedVars, formatter, interpolations) => {
    interpolations.push({
      match: '$symbols',
      variableName: 'symbols',
      fieldPath: undefined,
      format: formatter,
      value: 'AAPL',
      found: true,
    });
    return 'AAPL';
  };
  assert.throws(
    () =>
      interpolate(
        '$symbols',
        { symbols: { value: 'AAPL' } },
        { grafanaReplace: customAll }
      ),
    /bypassed the validated formatter/
  );
});

test('rejects scene interpolation before invoking the Grafana adapter', () => {
  let adapterCalled = false;
  assert.throws(
    () =>
      interpolate(
        '$word',
        {
          __sceneObject: { value: {} },
          word: { value: 'AAPL' },
        },
        {
          grafanaReplace: () => {
            adapterCalled = true;
            return 'AAPL';
          },
        }
      ),
    /scene interpolation is not supported/
  );
  assert.equal(adapterCalled, false);

  const inheritedSceneScope = Object.create({
    __sceneObject: { value: {} },
  });
  inheritedSceneScope.word = { value: 'AAPL' };
  assert.throws(
    () =>
      interpolate('$word', inheritedSceneScope, {
        grafanaReplace: () => {
          adapterCalled = true;
          return 'AAPL';
        },
      }),
    /scene interpolation is not supported/
  );
  assert.equal(adapterCalled, false);

  const previousWindow = globalThis.window;
  globalThis.window = {
    __grafanaSceneContext: { isActive: true },
  };
  try {
    assert.throws(
      () =>
        interpolate('$word', { word: { value: 'AAPL' } }, {
          grafanaReplace: () => {
            adapterCalled = true;
            return 'AAPL';
          },
        }),
      /scene interpolation is not supported/
    );
  } finally {
    if (previousWindow === undefined) {
      delete globalThis.window;
    } else {
      globalThis.window = previousWindow;
    }
  }
  assert.equal(adapterCalled, false);
});

test('rejects audit count, audit shape, audit order, and result mismatches', () => {
  const missingAudit = (target, scopedVars, formatter, interpolations) => {
    const value = legacyGrafanaReplace(target, scopedVars, formatter, interpolations);
    interpolations.pop();
    return value;
  };
  const extraAudit = (target, scopedVars, formatter, interpolations) => {
    const value = legacyGrafanaReplace(target, scopedVars, formatter, interpolations);
    interpolations.push(interpolations[0]);
    return value;
  };
  const malformedAudit = (_target, _scopedVars, formatter, interpolations) => {
    interpolations.push({
      match: '$word',
      variableName: 'word',
      format: formatter,
      value: 'AAPL',
    });
    return 'AAPL';
  };
  const mismatchedResult = (target, scopedVars, formatter, interpolations) => {
    legacyGrafanaReplace(target, scopedVars, formatter, interpolations);
    return 'MSFT';
  };

  for (const [adapter, pattern] of [
    [missingAudit, /audit count mismatch/],
    [extraAudit, /audit count mismatch/],
    [malformedAudit, /audit record is malformed|missing a required property/],
    [mismatchedResult, /does not match its audit records/],
  ]) {
    assert.throws(
      () => interpolate('$word', { word: { value: 'AAPL' } }, { grafanaReplace: adapter }),
      pattern
    );
  }
});

test('rejects accessor-backed audit metadata without executing it', () => {
  let getterCalls = 0;
  const accessorAudit = (_target, _scopedVars, formatter, interpolations) => {
    const record = {
      match: '$word',
      variableName: 'word',
      fieldPath: undefined,
      format: formatter,
      value: 'AAPL',
    };
    Object.defineProperty(record, 'found', {
      get() {
        getterCalls++;
        return true;
      },
    });
    interpolations.push(record);
    formatter('AAPL', { name: 'word' }, () => '');
    return 'AAPL';
  };

  assert.throws(
    () =>
      interpolate(
        '$word',
        { word: { value: 'AAPL' } },
        { grafanaReplace: accessorAudit }
      ),
    /accessor property/
  );
  assert.equal(getterCalls, 0);
});

test('rejects reordered formatter inputs even when the adapter returns plausible output', () => {
  const reorderedCalls = (_target, _scopedVars, formatter, interpolations) => {
    const second = formatter('MSFT', { name: 'second' }, () => '');
    const first = formatter('AAPL', { name: 'first' }, () => '');
    interpolations.push(
      {
        match: '$first',
        variableName: 'first',
        fieldPath: undefined,
        format: formatter,
        value: second,
        found: true,
      },
      {
        match: '$second',
        variableName: 'second',
        fieldPath: undefined,
        format: formatter,
        value: first,
        found: true,
      }
    );
    return `${second}|${first}`;
  };
  assert.throws(
    () =>
      interpolate(
        '$first|$second',
        {
          first: { value: 'AAPL' },
          second: { value: 'MSFT' },
        },
        { grafanaReplace: reorderedCalls }
      ),
    /formatter order mismatch|coerced or replaced/
  );
});

test('rejects throwing and non-string Grafana adapters without leaking values', () => {
  assert.throws(
    () =>
      interpolate('$word', { word: { value: 'AAPL' } }, {
        grafanaReplace: () => {
          throw new Error('secret-adapter-detail');
        },
      }),
    (error) => {
      assert.match(error.message, /interpolation adapter failed/);
      assert.equal(error.message.includes('secret-adapter-detail'), false);
      return true;
    }
  );
  assert.throws(
    () => interpolate('1+1', {}, { grafanaReplace: () => ({ value: 'AAPL' }) }),
    /returned a non-string result/
  );
});

test('expands Panopticon scalar and array parameters with trusted delimiters', () => {
  const scopedVars = {
    symbol: { value: 'AAPL' },
    symbols: { value: ['AAPL', 'MSFT'] },
  };
  const input =
    'single:{symbol}; comma:{symbols}; spaces:{symbols: }; semicolons:{symbols:;}; unicode:{symbols:·}';
  assert.equal(
    expandPanopticonDashboardParameters(input, scopedVars),
    'single:AAPL; comma:AAPL,MSFT; spaces:AAPL MSFT; semicolons:AAPL;MSFT; unicode:AAPL·MSFT'
  );
});

test('uses scoped values before dashboard values and fails closed on explicit nulls', () => {
  const dashboardVariables = [panopticonVariable('symbol', 'MSFT')];
  assert.equal(
    expandPanopticonDashboardParameters(
      '{symbol}',
      { symbol: { value: 'AAPL' } },
      dashboardVariables
    ),
    'AAPL'
  );
  assert.throws(
    () =>
      expandPanopticonDashboardParameters(
        '{symbol}',
        { symbol: { value: null } },
        dashboardVariables
      ),
    /must be a string or finite number/
  );
});

test('audits standard Grafana variables against the dashboard value snapshot', () => {
  const dashboardVariables = [panopticonVariable('symbol', 'MSFT')];
  assert.equal(
    interpolate('$symbol', {}, {
      dashboardVariables,
      grafanaReplace: makeLegacyGrafanaReplace({ symbol: 'MSFT' }),
    }),
    'MSFT'
  );
  assert.throws(
    () =>
      interpolate('$symbol', {}, {
        dashboardVariables,
        grafanaReplace: makeLegacyGrafanaReplace({ symbol: 'AAPL' }),
      }),
    /coerced or replaced/
  );
});

test('rejects ambiguous duplicate dashboard variable sources', () => {
  const dashboardVariables = [
    panopticonVariable('symbol', 'AAPL'),
    panopticonVariable('symbol', 'MSFT'),
  ];
  assert.throws(
    () =>
      interpolate('$symbol', {}, {
        dashboardVariables,
        grafanaReplace: makeLegacyGrafanaReplace({ symbol: 'AAPL' }),
      }),
    /ambiguous dashboard sources/
  );
  assert.throws(
    () => expandPanopticonDashboardParameters('{symbol}', {}, dashboardVariables),
    /ambiguous dashboard sources/
  );
});

test('leaves missing parameters and reserved backend brace macros untouched', () => {
  const input = [
    '{missing}',
    '{missing:,}',
    '{Query}',
    '{TimeWindowStart}',
    '{TimeWindowEnd}',
    '{Snapshot}',
    '{FocusTime}',
    '{Start}',
    '{End}',
    '{From}',
    '{To}',
    '{TimeWindowStartText}',
    '{Interval}',
    '{IntervalNs}',
    '{IntervalMs}',
    '{MaxDataPoints}',
    '{RefID}',
    '{OrgID}',
    '{UserLogin}',
    '{DatasourceUID}',
    '{TimeWindowStart:yyyy-MM-dd HH:mm:ss.SSS}',
  ].join('|');
  const reservedVariables = [
    panopticonVariable('TimeWindowStart', 'AAPL;unsafe'),
    panopticonVariable('Query', 'AAPL;unsafe'),
  ];
  assert.equal(expandPanopticonDashboardParameters(input, undefined, reservedVariables), input);
});

test('protects reserved dollar macro prefixes exactly like the Go backend', () => {
  const scopedVars = {
    TimeWindowStart: { value: '1;unsafe' },
    TimeWindowStartSuffix: { value: 'SHOULD_NOT_REPLACE' },
    SnapshotText: { value: 'SHOULD_NOT_REPLACE' },
    FocusTimeTail: { value: 'SHOULD_NOT_REPLACE' },
    first: { value: 'second' },
    second: { value: 'MSFT' },
  };
  const input =
    '$TimeWindowStart|$TimeWindowStartSuffix|$SnapshotText|$FocusTimeTail|${first}|{first}|$second';
  assert.equal(
    interpolate(input, scopedVars, { panopticonMode: true }),
    '$TimeWindowStart|$TimeWindowStartSuffix|$SnapshotText|$FocusTimeTail|second|second|MSFT'
  );
});

test('does not recursively expand external values', () => {
  assert.throws(
    () =>
      interpolate(
        '$first',
        { first: { value: '{second}' }, second: { value: 'MSFT' } },
        { panopticonMode: true }
      ),
    /must be one conservative ASCII q token/
  );
  assert.throws(
    () =>
      interpolate(
        '{first}',
        { first: { value: '$second' }, second: { value: 'MSFT' } },
        { panopticonMode: true }
      ),
    /must be one conservative ASCII q token/
  );
});

test('supports safe Grafana built-in timestamp and interval scalar shapes when formatter-audited', () => {
  const scopedVars = {
    __from: { value: 1785267045000 },
    __to_iso: { value: '2026-07-28T19:30:45Z' },
    __interval: { value: '5m' },
    __interval_ms: { value: 300000 },
  };
  assert.equal(
    interpolate('$__from|$__to_iso|$__interval|$__interval_ms', scopedVars),
    '1785267045000|2026-07-28T19:30:45Z|5m|300000'
  );
});

test('rejects empty, nested, null, boolean, and object array values', () => {
  for (const value of [
    [],
    ['AAPL', ['MSFT']],
    ['AAPL', null],
    ['AAPL', true],
    ['AAPL', { value: 'MSFT' }],
  ]) {
    assert.throws(
      () => expandPanopticonDashboardParameters('{symbols}', { symbols: { value } }),
      /must contain at least one value|must be a string or finite number/
    );
  }
});

test('enforces parameter name and delimiter boundaries exactly', () => {
  const nameAtLimit = `v${'a'.repeat(Q_TEMPLATE_SUBSTITUTION_LIMITS.parameterNameBytes - 1)}`;
  const nameOverLimit = `${nameAtLimit}a`;
  assert.equal(
    expandPanopticonDashboardParameters(`{${nameAtLimit}}`, {
      [nameAtLimit]: { value: 'AAPL' },
    }),
    'AAPL'
  );
  assert.throws(
    () =>
      expandPanopticonDashboardParameters(`{${nameOverLimit}}`, {
        [nameOverLimit]: { value: 'AAPL' },
      }),
    /parameter name must be at most 128 bytes/
  );

  const delimiterAtLimit = ','.repeat(Q_TEMPLATE_SUBSTITUTION_LIMITS.delimiterBytes);
  const delimiterOverLimit = `${delimiterAtLimit},`;
  assert.equal(
    expandPanopticonDashboardParameters(`{symbols:${delimiterAtLimit}}`, {
      symbols: { value: ['A', 'B'] },
    }),
    `A${delimiterAtLimit}B`
  );
  assert.throws(
    () =>
      expandPanopticonDashboardParameters(`{symbols:${delimiterOverLimit}}`, {
        symbols: { value: ['A', 'B'] },
      }),
    /delimiter exceeds the 32-byte limit/
  );

  const twoByteDelimiterAtLimit = '·'.repeat(
    Q_TEMPLATE_SUBSTITUTION_LIMITS.delimiterBytes / 2
  );
  assert.equal(
    expandPanopticonDashboardParameters(`{symbols:${twoByteDelimiterAtLimit}}`, {
      symbols: { value: ['A', 'B'] },
    }),
    `A${twoByteDelimiterAtLimit}B`
  );
  assert.throws(
    () =>
      expandPanopticonDashboardParameters(`{symbols:${twoByteDelimiterAtLimit}·}`, {
        symbols: { value: ['A', 'B'] },
      }),
    /delimiter exceeds the 32-byte limit/
  );
});

test('rejects braces and control characters in author-controlled delimiters', () => {
  for (const delimiter of ['{', '\n', '\0', '\u001f', '\u007f', '\u0085', '\u202e']) {
    assert.throws(
      () =>
        expandPanopticonDashboardParameters(`{symbols:${delimiter}}`, {
          symbols: { value: ['A', 'B'] },
        }),
      /delimiter must not contain braces or control characters/
    );
  }
});

test('enforces array-count and element-size boundaries before large allocations', () => {
  const countAtLimit = Array(Q_TEMPLATE_SUBSTITUTION_LIMITS.arrayElements).fill('A');
  const countOverLimit = Array(Q_TEMPLATE_SUBSTITUTION_LIMITS.arrayElements + 1).fill('A');
  assert.equal(
    formatQTemplateVariable(countAtLimit, { name: 'symbols' }).split(',').length,
    Q_TEMPLATE_SUBSTITUTION_LIMITS.arrayElements
  );
  assert.throws(
    () => formatQTemplateVariable(countOverLimit, { name: 'symbols' }),
    /more than 1024 values/
  );

  const elementAtLimit = 'a'.repeat(Q_TEMPLATE_SUBSTITUTION_LIMITS.elementBytes);
  const elementOverLimit = `${elementAtLimit}a`;
  assert.equal(formatQTemplateVariable(elementAtLimit), elementAtLimit);
  assert.throws(
    () => formatQTemplateVariable(elementOverLimit),
    /exceeds the 256-byte value limit/
  );
});

test('bounds the dashboard scan before reading dashboard metadata', () => {
  let oversizedMetadataReads = 0;
  const oversizedName = {
    name: 'v'.repeat(Q_TEMPLATE_SUBSTITUTION_LIMITS.parameterNameBytes + 1),
    get current() {
      oversizedMetadataReads++;
      return { value: 'AAPL' };
    },
  };
  assert.equal(
    interpolate('1+1', {}, { dashboardVariables: [oversizedName] }),
    '1+1'
  );
  assert.equal(oversizedMetadataReads, 0);

  const variablesAtLimit = Array.from(
    { length: Q_TEMPLATE_SUBSTITUTION_LIMITS.dashboardVariables },
    (_, index) => panopticonVariable(`v${index}`, index === 0 ? 'AAPL' : 'MSFT')
  );
  assert.equal(
    interpolate('$v0', {}, {
      dashboardVariables: variablesAtLimit,
      grafanaReplace: makeLegacyGrafanaReplace({ v0: 'AAPL' }),
    }),
    'AAPL'
  );

  let metadataReads = 0;
  const variablesOverLimit = new Array(
    Q_TEMPLATE_SUBSTITUTION_LIMITS.dashboardVariables + 1
  );
  Object.defineProperty(variablesOverLimit, '0', {
    get() {
      metadataReads++;
      return panopticonVariable('v0', 'AAPL');
    },
  });
  assert.throws(
    () =>
      interpolate('$v0', {}, {
        dashboardVariables: variablesOverLimit,
        grafanaReplace: makeLegacyGrafanaReplace({ v0: 'AAPL' }),
      }),
    /dashboard exceeds the 4096-variable scan limit/
  );
  assert.equal(metadataReads, 0);
});

test('enforces interpolation-count, per-replacement, and final-output boundaries', () => {
  const missingAtLimit = '$x'.repeat(Q_TEMPLATE_SUBSTITUTION_LIMITS.interpolations);
  assert.equal(interpolate(missingAtLimit), missingAtLimit);
  assert.throws(
    () => interpolate(`${missingAtLimit}$x`),
    /exceeds the 4096-interpolation limit/
  );

  const mixedAtLimit =
    '{missing}'.repeat(Q_TEMPLATE_SUBSTITUTION_LIMITS.interpolations / 2) +
    '$missing'.repeat(Q_TEMPLATE_SUBSTITUTION_LIMITS.interpolations / 2);
  assert.equal(
    interpolate(mixedAtLimit, {}, { panopticonMode: true }),
    mixedAtLimit
  );
  assert.throws(
    () => interpolate(`${mixedAtLimit}$missing`, {}, { panopticonMode: true }),
    /exceeds the 4096-interpolation limit/
  );

  const element = 'a'.repeat(Q_TEMPLATE_SUBSTITUTION_LIMITS.elementBytes);
  const exactReplacement = Array(
    Q_TEMPLATE_SUBSTITUTION_LIMITS.replacementBytes /
      Q_TEMPLATE_SUBSTITUTION_LIMITS.elementBytes
  ).fill(element);
  assert.equal(
    expandPanopticonDashboardParameters('{values:}', {
      values: { value: exactReplacement },
    }).length,
    Q_TEMPLATE_SUBSTITUTION_LIMITS.replacementBytes
  );
  assert.throws(
    () =>
      expandPanopticonDashboardParameters('{values:}', {
        values: { value: [...exactReplacement, element] },
      }),
    /exceeds the 65536-byte replacement limit/
  );

  const mixedReplacementAtLimit =
    '{values:}' +
    '$value'.repeat(
      (Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes -
        Q_TEMPLATE_SUBSTITUTION_LIMITS.replacementBytes) /
        Q_TEMPLATE_SUBSTITUTION_LIMITS.elementBytes
    );
  const mixedScopedVars = {
    value: { value: element },
    values: { value: exactReplacement },
  };
  assert.equal(
    interpolate(mixedReplacementAtLimit, mixedScopedVars, {
      panopticonMode: true,
    }).length,
    Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes
  );
  assert.throws(
    () =>
      interpolate(`${mixedReplacementAtLimit}$value`, mixedScopedVars, {
        panopticonMode: true,
      }),
    /substitution budget|output limit/
  );

  const finalAtLimit = 'a'.repeat(Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes);
  assert.equal(assertQTemplateExpansionLength(finalAtLimit), finalAtLimit);
  assert.throws(
    () => assertQTemplateExpansionLength(`${finalAtLimit}a`),
    /exceeds the 262144-byte output limit/
  );

  const placeholderAtLimit = '$value'.repeat(
    Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes /
      Q_TEMPLATE_SUBSTITUTION_LIMITS.elementBytes
  );
  assert.equal(
    interpolate(placeholderAtLimit, { value: { value: element } }).length,
    Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes
  );
  assert.throws(
    () => interpolate(`${placeholderAtLimit}$value`, { value: { value: element } }),
    /substitution budget|output limit/
  );
});

test('checks UTF-16 length before UTF-8 encoding and rejects oversize source templates', () => {
  const asciiAtLimit = 'a'.repeat(Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes);
  assert.equal(interpolate(asciiAtLimit), asciiAtLimit);
  assert.throws(
    () => interpolate(`${asciiAtLimit}a`),
    /exceeds the 262144-byte output limit/
  );

  const twoByteOverLimit = '·'.repeat(
    Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes / 2 + 1
  );
  assert.throws(
    () => interpolate(twoByteOverLimit),
    /exceeds the 262144-byte output limit/
  );
});

test('is deterministic across repeated calls and does not mutate source arrays', () => {
  const values = ['AAPL', 'MSFT', 'GOOG'];
  const scopedVars = { symbols: { value: values } };
  const expected = 'AAPL|MSFT|GOOG/AAPL,MSFT,GOOG';

  for (let iteration = 0; iteration < 50; iteration++) {
    assert.equal(
      expandPanopticonDashboardParameters('{symbols:|}/{symbols}', scopedVars),
      expected
    );
  }
  assert.deepEqual(values, ['AAPL', 'MSFT', 'GOOG']);
});
