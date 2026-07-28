import type { ScopedVars, TypedVariableModel } from '@grafana/data';
import type { VariableInterpolation } from '@grafana/runtime';

export const Q_TEMPLATE_SUBSTITUTION_LIMITS = {
  parameterNameBytes: 128,
  delimiterBytes: 32,
  arrayElements: 1024,
  elementBytes: 256,
  replacementBytes: 64 * 1024,
  outputBytes: 256 * 1024,
  interpolations: 4096,
  dashboardVariables: 4096,
} as const;

export type QTemplateVariableFormatter = (
  value: unknown,
  variable?: unknown,
  formatVariableValue?: unknown
) => string;

export type GrafanaTemplateReplace = (
  target: string,
  scopedVars: ScopedVars | undefined,
  formatter: QTemplateVariableFormatter,
  interpolations: VariableInterpolation[]
) => unknown;

const panopticonParameterPatternSource =
  String.raw`\{([A-Za-z_][A-Za-z0-9_.-]*)(?::([^}]*))?\}`;
const panopticonParameterNamePattern = /^[A-Za-z_][A-Za-z0-9_.-]*$/;
const safeDottedIdentifierPattern =
  /^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*$/;
const finiteDecimalPattern = /^-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?$/;
const grafanaIntervalPattern = /^\d+(?:ms|[smhdwMy])$/;
const isoDateOrTimestampPattern =
  /^\d{4}-\d{2}-\d{2}(?:T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})?)?$/;
const qDateOrTimestampPattern =
  /^\d{4}\.\d{2}\.\d{2}(?:D\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?)?$/;
const qTimePattern = /^\d{2}:\d{2}(?::\d{2}(?:\.\d{1,9})?)?$/;
const unsafeDelimiterPattern = /[{}\p{Cc}\p{Cf}\p{Cs}\p{Zl}\p{Zp}]/u;
const nonFiniteNumberTextPattern = /^(?:NaN|[+-]?Infinity)$/i;
const utf8Encoder = new TextEncoder();

// This is Grafana 13.1.1's legacy variableRegex. A fresh instance is used per scan.
const grafanaVariablePatternSource =
  String.raw`\$(\w+)|\[\[(\w+?)(?::(\w+))?\]\]|\${(\w+)(?:\.([^:^}]+))?(?::([^}]+))?}`;

const reservedPanopticonParameters = new Set([
  'Query',
  'TimeWindowStart',
  'TimeWindowEnd',
  'Snapshot',
  'FocusTime',
  'Start',
  'End',
  'From',
  'To',
  'TimeWindowStartText',
  'TimeWindowEndText',
  'SnapshotText',
  'FocusTimeText',
  'Interval',
  'IntervalNs',
  'IntervalMs',
  'MaxDataPoints',
  'RefID',
  'OrgID',
  'UserName',
  'UserLogin',
  'UserEmail',
  'DatasourceName',
  'DatasourceUID',
]);

// The Go backend uses strings.NewReplacer, so these substrings are protected even
// when they prefix a longer token such as $TimeWindowStartSuffix.
const reservedPanopticonDollarMacros = [
  '$TimeWindowStart',
  '$TimeWindowEnd',
  '$Snapshot',
  '$FocusTime',
] as const;
const reservedPanopticonDollarMacroSet = new Set<string>(reservedPanopticonDollarMacros);
const reservedPanopticonDollarMacroSplitPattern =
  /(\$TimeWindowStart|\$TimeWindowEnd|\$Snapshot|\$FocusTime)/;
// Grafana can synthesize these built-ins outside getVariables(). Their formatter
// inputs are still grammar-validated and audit-bound, but no dashboard snapshot
// exists for an additional raw-value equality check.
const rawUnknownBuiltinNames = new Set([
  '__from',
  '__to',
  '__interval',
  '__interval_ms',
  '__rate_interval',
]);

interface ParameterLookup {
  found: boolean;
  value?: unknown;
}

interface DashboardValue {
  duplicate: boolean;
  value: unknown;
}

interface ParsedGrafanaInterpolation {
  end: number;
  fieldPath?: string;
  format?: string;
  match: string;
  rawValue: ParameterLookup;
  start: number;
  variableName: string;
}

interface FormatterCall {
  input: unknown;
  output: string;
  variableName?: string;
}

interface InterpolationBudget {
  count: number;
  replacementBytes: number;
}

interface AuditedReplaceResult {
  bytes: number;
  value: string;
}

interface AuditRecord {
  fieldPath?: unknown;
  format?: unknown;
  found: boolean;
  match: string;
  value: string;
  variableName: string;
}

type DashboardValueIndex = Map<string, DashboardValue>;

export function interpolateQTemplateVariables(
  input: string,
  scopedVars: ScopedVars | undefined,
  dashboardVariables: TypedVariableModel[] | undefined,
  grafanaReplace: GrafanaTemplateReplace,
  panopticonMode = false
): string {
  if (typeof input !== 'string' || typeof grafanaReplace !== 'function') {
    throw new Error('Executable q template interpolation received malformed input');
  }
  assertNoSceneInterpolation(scopedVars);
  boundedUtf8ByteLength(
    input,
    Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes,
    `Executable q template exceeds the ${Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes}-byte output limit`
  );

  const dashboardValueIndex = buildDashboardValueIndex(dashboardVariables);
  const budget: InterpolationBudget = { count: 0, replacementBytes: 0 };
  const withPanopticonParameters = panopticonMode
    ? expandPanopticonDashboardParametersWithIndex(
        input,
        scopedVars,
        dashboardValueIndex,
        budget
      )
    : input;
  const segments = panopticonMode
    ? withPanopticonParameters.split(reservedPanopticonDollarMacroSplitPattern)
    : [withPanopticonParameters];
  const outputSegments: string[] = [];
  let totalOutputBytes = 0;

  for (const segment of segments) {
    let expanded: AuditedReplaceResult;
    if (panopticonMode && reservedPanopticonDollarMacroSet.has(segment)) {
      incrementInterpolationCount(budget);
      const remaining = Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes - totalOutputBytes;
      expanded = {
        bytes: boundedUtf8ByteLength(
          segment,
          remaining,
          'Expanded q template exceeds the output limit'
        ),
        value: segment,
      };
    } else {
      expanded = replaceGrafanaVariablesWithAudit(
        segment,
        scopedVars,
        dashboardValueIndex,
        grafanaReplace,
        budget,
        Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes - totalOutputBytes
      );
    }
    totalOutputBytes = checkedByteTotal(
      totalOutputBytes,
      expanded.bytes,
      Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes,
      'Expanded q template exceeds the output limit'
    );
    outputSegments.push(expanded.value);
  }

  return outputSegments.join('');
}

export function formatQTemplateVariable(
  value: unknown,
  variable?: unknown,
  _formatVariableValue?: unknown
): string {
  const variableName = formatterVariableName(variable);
  const label = variableName ? `Grafana variable "${variableName}"` : 'Grafana variable';
  return formatExternalQValue(value, ',', label);
}

export function expandPanopticonDashboardParameters(
  input: string,
  scopedVars?: ScopedVars,
  dashboardVariables?: TypedVariableModel[]
): string {
  if (typeof input !== 'string') {
    throw new Error('Panopticon q template must be a string');
  }
  return expandPanopticonDashboardParametersWithIndex(
    input,
    scopedVars,
    buildDashboardValueIndex(dashboardVariables)
  );
}

export function assertQTemplateExpansionLength(
  input: string,
  label = 'Expanded q template'
): string {
  if (typeof input !== 'string') {
    throw new Error(`${label} must be a string`);
  }
  boundedUtf8ByteLength(
    input,
    Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes,
    `${label} exceeds the ${Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes}-byte output limit`
  );
  return input;
}

function replaceGrafanaVariablesWithAudit(
  source: string,
  scopedVars: ScopedVars | undefined,
  dashboardValueIndex: DashboardValueIndex,
  grafanaReplace: GrafanaTemplateReplace,
  budget: InterpolationBudget,
  outputByteLimit: number
): AuditedReplaceResult {
  const expected = parseGrafanaInterpolations(
    source,
    scopedVars,
    dashboardValueIndex,
    budget
  );
  for (const interpolation of expected) {
    if (interpolation.rawValue.found) {
      validateRawExternalValue(
        interpolation.rawValue.value,
        `Grafana variable "${interpolation.variableName}"`
      );
    }
  }

  const interpolations: VariableInterpolation[] = [];
  const formatterCalls: FormatterCall[] = [];
  let formatterError: Error | undefined;
  let formatterAttempts = 0;
  const formatter: QTemplateVariableFormatter = (value, variable, formatVariableValue) => {
    formatterAttempts++;
    if (formatterAttempts > expected.length) {
      formatterError = new Error('Grafana q variable formatter was invoked unexpectedly');
      throw formatterError;
    }
    try {
      const output = formatQTemplateVariable(value, variable, formatVariableValue);
      budget.replacementBytes = checkedByteTotal(
        budget.replacementBytes,
        boundedUtf8ByteLength(
          output,
          Q_TEMPLATE_SUBSTITUTION_LIMITS.replacementBytes,
          `Grafana variable replacement exceeds the ${Q_TEMPLATE_SUBSTITUTION_LIMITS.replacementBytes}-byte limit`
        ),
        Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes,
        'Grafana variable replacements exceed the q template substitution budget'
      );
      formatterCalls.push({
        input: value,
        output,
        variableName: formatterVariableName(variable),
      });
      return output;
    } catch (error) {
      formatterError =
        error instanceof Error
          ? error
          : new Error('Grafana q variable formatter rejected an invalid value');
      throw error;
    }
  };

  let replaced: unknown;
  try {
    replaced = grafanaReplace(source, scopedVars, formatter, interpolations);
  } catch {
    if (formatterError) {
      throw formatterError;
    }
    throw new Error('Grafana q variable interpolation adapter failed');
  }
  if (formatterError) {
    throw formatterError;
  }
  if (typeof replaced !== 'string') {
    throw new Error('Grafana q variable interpolation adapter returned a non-string result');
  }
  const replacedBytes = boundedUtf8ByteLength(
    replaced,
    outputByteLimit,
    'Expanded q template exceeds the output limit'
  );

  if (interpolations.length !== expected.length) {
    throw new Error('Grafana q variable interpolation audit count mismatch');
  }

  const reconstructed: string[] = [];
  let reconstructedBytes = 0;
  let sourceOffset = 0;
  let formatterIndex = 0;
  for (let index = 0; index < expected.length; index++) {
    const expectedInterpolation = expected[index];
    const record = readAuditRecord(interpolations[index]);
    assertAuditRecordMatchesExpected(record, expectedInterpolation, formatter);

    const literal = source.slice(sourceOffset, expectedInterpolation.start);
    reconstructedBytes = appendBoundedOutput(
      reconstructed,
      literal,
      reconstructedBytes,
      outputByteLimit
    );
    reconstructedBytes = appendBoundedOutput(
      reconstructed,
      record.value,
      reconstructedBytes,
      outputByteLimit
    );
    sourceOffset = expectedInterpolation.end;

    if (!record.found) {
      if (record.value !== record.match) {
        throw new Error('Unresolved Grafana interpolation did not remain literal');
      }
      continue;
    }

    const call = formatterCalls[formatterIndex++];
    if (!call || call.output !== record.value) {
      throw new Error('Found Grafana interpolation bypassed the validated formatter');
    }
    if (call.variableName && call.variableName !== record.variableName) {
      throw new Error('Grafana interpolation formatter order mismatch');
    }
    if (expectedInterpolation.rawValue.found) {
      if (!sameFlatExternalValue(expectedInterpolation.rawValue.value, call.input)) {
        throw new Error('Grafana interpolation coerced or replaced the raw variable value');
      }
    } else if (!rawUnknownBuiltinNames.has(record.variableName)) {
      throw new Error('Found Grafana interpolation has no verifiable raw scalar source');
    }
  }

  const tail = source.slice(sourceOffset);
  reconstructedBytes = appendBoundedOutput(
    reconstructed,
    tail,
    reconstructedBytes,
    outputByteLimit
  );
  if (
    formatterIndex !== formatterCalls.length ||
    formatterAttempts !== formatterCalls.length
  ) {
    throw new Error('Grafana interpolation formatter call count mismatch');
  }
  const reconstructedValue = reconstructed.join('');
  if (reconstructedBytes !== replacedBytes || reconstructedValue !== replaced) {
    throw new Error('Grafana interpolation result does not match its audit records');
  }
  return { bytes: replacedBytes, value: replaced };
}

function parseGrafanaInterpolations(
  source: string,
  scopedVars: ScopedVars | undefined,
  dashboardValueIndex: DashboardValueIndex,
  budget: InterpolationBudget
): ParsedGrafanaInterpolation[] {
  const pattern = new RegExp(grafanaVariablePatternSource, 'g');
  const parsed: ParsedGrafanaInterpolation[] = [];
  let match: RegExpExecArray | null;
  while ((match = pattern.exec(source)) !== null) {
    budget.count = checkedByteTotal(
      budget.count,
      1,
      Q_TEMPLATE_SUBSTITUTION_LIMITS.interpolations,
      `Executable q template exceeds the ${Q_TEMPLATE_SUBSTITUTION_LIMITS.interpolations}-interpolation limit`
    );
    const variableName = match[1] || match[2] || match[4];
    const format = match[3] || match[6] || undefined;
    const fieldPath = match[5] || undefined;
    if (
      typeof variableName !== 'string' ||
      variableName.length === 0 ||
      variableName.length > Q_TEMPLATE_SUBSTITUTION_LIMITS.parameterNameBytes
    ) {
      throw new Error('Grafana variable name exceeds the executable q template limit');
    }
    if (fieldPath !== undefined) {
      throw new Error('Grafana field-path interpolation is not supported in executable q');
    }
    parsed.push({
      end: match.index + match[0].length,
      fieldPath,
      format,
      match: match[0],
      rawValue: standardGrafanaRawValueLookup(
        variableName,
        scopedVars,
        dashboardValueIndex
      ),
      start: match.index,
      variableName,
    });
  }
  return parsed;
}

function assertAuditRecordMatchesExpected(
  record: AuditRecord,
  expected: ParsedGrafanaInterpolation,
  formatter: QTemplateVariableFormatter
): void {
  if (
    record.match !== expected.match ||
    record.variableName !== expected.variableName ||
    record.fieldPath !== expected.fieldPath
  ) {
    throw new Error('Grafana q variable interpolation audit order mismatch');
  }
  if (expected.format === undefined) {
    if (record.format !== formatter) {
      throw new Error('Grafana interpolation did not retain the validated formatter');
    }
  } else if (record.format !== expected.format) {
    throw new Error('Grafana interpolation audit format mismatch');
  }
}

function readAuditRecord(value: unknown): AuditRecord {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('Grafana q variable interpolation audit record is malformed');
  }
  const match = requiredOwnDataProperty(value, 'match');
  const variableName = requiredOwnDataProperty(value, 'variableName');
  const fieldPath = optionalOwnDataProperty(value, 'fieldPath');
  const format = optionalOwnDataProperty(value, 'format');
  const output = requiredOwnDataProperty(value, 'value');
  const found = requiredOwnDataProperty(value, 'found');
  if (
    typeof match !== 'string' ||
    typeof variableName !== 'string' ||
    typeof output !== 'string' ||
    typeof found !== 'boolean'
  ) {
    throw new Error('Grafana q variable interpolation audit record is malformed');
  }
  if (
    match.length > Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes ||
    variableName.length > Q_TEMPLATE_SUBSTITUTION_LIMITS.parameterNameBytes ||
    output.length > Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes
  ) {
    throw new Error('Grafana q variable interpolation audit record exceeds its limits');
  }
  if (found !== (output !== match)) {
    throw new Error('Grafana q variable interpolation audit found flag is inconsistent');
  }
  return {
    fieldPath,
    format,
    found,
    match,
    value: output,
    variableName,
  };
}

function appendBoundedOutput(
  output: string[],
  value: string,
  currentBytes: number,
  limit: number
): number {
  const remaining = limit - currentBytes;
  const valueBytes = boundedUtf8ByteLength(
    value,
    remaining,
    'Grafana interpolation audit output exceeds the q template limit'
  );
  output.push(value);
  return checkedByteTotal(
    currentBytes,
    valueBytes,
    limit,
    'Grafana interpolation audit output exceeds the q template limit'
  );
}

function expandPanopticonDashboardParametersWithIndex(
  source: string,
  scopedVars: ScopedVars | undefined,
  dashboardValueIndex: DashboardValueIndex,
  sharedBudget?: InterpolationBudget
): string {
  const sourceBytes = boundedUtf8ByteLength(
    source,
    Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes,
    `Panopticon q template exceeds the ${Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes}-byte output limit`
  );
  if (!source) {
    return '';
  }

  let projectedOutputBytes = sourceBytes;
  const budget = sharedBudget ?? { count: 0, replacementBytes: 0 };
  const parameterPattern = new RegExp(panopticonParameterPatternSource, 'g');
  const expanded = source.replace(
    parameterPattern,
    (token, name: string, delimiter: string | undefined, offset: number) => {
      if (offset > 0 && source[offset - 1] === '$') {
        return token;
      }
      incrementInterpolationCount(budget);
      validateParameterName(name);
      if (reservedPanopticonParameters.has(name)) {
        return token;
      }

      const joiner = delimiter === undefined ? ',' : delimiter;
      validateDelimiter(joiner, name);
      const lookup = panopticonParameterLookup(
        name,
        scopedVars,
        dashboardValueIndex
      );
      if (!lookup.found) {
        return token;
      }

      const replacement = formatExternalQValue(
        lookup.value,
        joiner,
        `Panopticon parameter "${name}"`
      );
      const replacementBytes = boundedUtf8ByteLength(
        replacement,
        Q_TEMPLATE_SUBSTITUTION_LIMITS.replacementBytes,
        `Panopticon parameter "${name}" exceeds the replacement limit`
      );
      budget.replacementBytes = checkedByteTotal(
        budget.replacementBytes,
        replacementBytes,
        Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes,
        'External q variable replacements exceed the q template substitution budget'
      );
      projectedOutputBytes = projectedReplacementTotal(
        projectedOutputBytes,
        boundedUtf8ByteLength(
          token,
          Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes,
          'Panopticon parameter token exceeds the q template limit'
        ),
        replacementBytes
      );
      return replacement;
    }
  );

  const expandedBytes = boundedUtf8ByteLength(
    expanded,
    Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes,
    `Panopticon q template exceeds the ${Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes}-byte output limit`
  );
  if (projectedOutputBytes !== expandedBytes) {
    throw new Error('Panopticon q template expansion length accounting failed');
  }
  return expanded;
}

function panopticonParameterLookup(
  name: string,
  scopedVars: ScopedVars | undefined,
  dashboardValueIndex: DashboardValueIndex
): ParameterLookup {
  const scoped = scopedValueLookup(name, scopedVars);
  if (scoped.found) {
    return scoped;
  }
  const dashboard = dashboardValueIndex.get(name);
  if (!dashboard) {
    return { found: false };
  }
  if (dashboard.duplicate) {
    throw new Error(`Panopticon parameter "${name}" has ambiguous dashboard sources`);
  }
  return { found: true, value: dashboard.value };
}

function standardGrafanaRawValueLookup(
  name: string,
  scopedVars: ScopedVars | undefined,
  dashboardValueIndex: DashboardValueIndex
): ParameterLookup {
  const scoped = scopedValueLookup(name, scopedVars);
  if (scoped.found && scoped.value !== null && scoped.value !== undefined) {
    return scoped;
  }
  const dashboard = dashboardValueIndex.get(name);
  if (!dashboard) {
    return { found: false };
  }
  if (dashboard.duplicate) {
    throw new Error(`Grafana variable "${name}" has ambiguous dashboard sources`);
  }
  return { found: true, value: dashboard.value };
}

function scopedValueLookup(
  name: string,
  scopedVars: ScopedVars | undefined
): ParameterLookup {
  if (!scopedVars) {
    return { found: false };
  }
  const scopedProperty = optionalOwnDataProperty(scopedVars, name);
  if (scopedProperty === undefined) {
    return { found: false };
  }
  if (
    !scopedProperty ||
    typeof scopedProperty !== 'object' ||
    Array.isArray(scopedProperty)
  ) {
    throw new Error(`Grafana variable "${name}" has a malformed scoped value`);
  }
  return {
    found: true,
    value: requiredOwnDataProperty(scopedProperty, 'value'),
  };
}

function buildDashboardValueIndex(
  dashboardVariables?: TypedVariableModel[]
): DashboardValueIndex {
  const index: DashboardValueIndex = new Map();
  if (dashboardVariables === undefined) {
    return index;
  }
  if (!Array.isArray(dashboardVariables)) {
    throw new Error('Grafana dashboard variable list is malformed');
  }
  const length = safeArrayLength(
    dashboardVariables,
    Q_TEMPLATE_SUBSTITUTION_LIMITS.dashboardVariables,
    `Grafana dashboard exceeds the ${Q_TEMPLATE_SUBSTITUTION_LIMITS.dashboardVariables}-variable scan limit`
  );
  for (let position = 0; position < length; position++) {
    const variable = requiredArrayItem(dashboardVariables, position);
    if (!variable || typeof variable !== 'object' || Array.isArray(variable)) {
      throw new Error('Grafana dashboard variable metadata is malformed');
    }
    const name = requiredOwnDataProperty(variable, 'name');
    if (typeof name !== 'string') {
      throw new Error('Grafana dashboard variable metadata is malformed');
    }
    if (
      name.length === 0 ||
      name.length > Q_TEMPLATE_SUBSTITUTION_LIMITS.parameterNameBytes ||
      !panopticonParameterNamePattern.test(name)
    ) {
      continue;
    }
    const current = optionalOwnDataProperty(variable, 'current');
    if (!current || typeof current !== 'object' || Array.isArray(current)) {
      continue;
    }
    const valueDescriptor = ownDataProperty(current, 'value');
    if (!valueDescriptor.found) {
      continue;
    }
    const existing = index.get(name);
    index.set(name, {
      duplicate: existing !== undefined,
      value: existing?.value ?? valueDescriptor.value,
    });
  }
  return index;
}

function validateRawExternalValue(value: unknown, label: string): void {
  formatExternalQValue(value, ',', label);
}

function formatExternalQValue(value: unknown, delimiter: string, label: string): string {
  if (!Array.isArray(value)) {
    return formatExternalQScalar(value, label);
  }
  const length = safeArrayLength(
    value,
    Q_TEMPLATE_SUBSTITUTION_LIMITS.arrayElements,
    `${label} contains more than ${Q_TEMPLATE_SUBSTITUTION_LIMITS.arrayElements} values`
  );
  if (length === 0) {
    throw new Error(`${label} must contain at least one value`);
  }

  const delimiterBytes = boundedUtf8ByteLength(
    delimiter,
    Q_TEMPLATE_SUBSTITUTION_LIMITS.delimiterBytes,
    `${label} delimiter exceeds the ${Q_TEMPLATE_SUBSTITUTION_LIMITS.delimiterBytes}-byte limit`
  );
  const output: string[] = [];
  let expansionBytes = 0;
  for (let index = 0; index < length; index++) {
    const item = formatExternalQScalar(
      requiredArrayItem(value, index),
      `${label} element ${index + 1}`
    );
    if (index > 0) {
      expansionBytes = checkedByteTotal(
        expansionBytes,
        delimiterBytes,
        Q_TEMPLATE_SUBSTITUTION_LIMITS.replacementBytes,
        `${label} exceeds the ${Q_TEMPLATE_SUBSTITUTION_LIMITS.replacementBytes}-byte replacement limit`
      );
    }
    expansionBytes = checkedByteTotal(
      expansionBytes,
      boundedUtf8ByteLength(
        item,
        Q_TEMPLATE_SUBSTITUTION_LIMITS.elementBytes,
        `${label} element ${index + 1} exceeds the value limit`
      ),
      Q_TEMPLATE_SUBSTITUTION_LIMITS.replacementBytes,
      `${label} exceeds the ${Q_TEMPLATE_SUBSTITUTION_LIMITS.replacementBytes}-byte replacement limit`
    );
    output.push(item);
  }
  return output.join(delimiter);
}

function formatExternalQScalar(value: unknown, label: string): string {
  let text: string;
  if (typeof value === 'string') {
    text = value;
  } else if (typeof value === 'number') {
    if (!Number.isFinite(value)) {
      throw new Error(`${label} must be a finite number`);
    }
    text = Object.is(value, -0) ? '-0' : String(value);
  } else {
    throw new Error(
      `${label} must be a string or finite number; booleans, nulls, objects, and nested arrays are not supported`
    );
  }

  const textBytes = boundedUtf8ByteLength(
    text,
    Q_TEMPLATE_SUBSTITUTION_LIMITS.elementBytes,
    `${label} exceeds the ${Q_TEMPLATE_SUBSTITUTION_LIMITS.elementBytes}-byte value limit`
  );
  if (textBytes === 0) {
    throw new Error(`${label} must not be empty`);
  }
  if (!isSafeQToken(text)) {
    throw new Error(
      `${label} must be one conservative ASCII q token: a dotted identifier, finite decimal, Grafana interval, or ISO/q date or time`
    );
  }
  return text;
}

function isSafeQToken(value: string): boolean {
  if (nonFiniteNumberTextPattern.test(value)) {
    return false;
  }
  if (safeDottedIdentifierPattern.test(value)) {
    return true;
  }
  if (finiteDecimalPattern.test(value)) {
    return Number.isFinite(Number(value));
  }
  return (
    grafanaIntervalPattern.test(value) ||
    isoDateOrTimestampPattern.test(value) ||
    qDateOrTimestampPattern.test(value) ||
    qTimePattern.test(value)
  );
}

function sameFlatExternalValue(expected: unknown, actual: unknown): boolean {
  if (typeof expected === 'string' || typeof expected === 'number') {
    return typeof actual === typeof expected && Object.is(expected, actual);
  }
  if (!Array.isArray(expected) || !Array.isArray(actual)) {
    return false;
  }
  const expectedLength = safeArrayLength(
    expected,
    Q_TEMPLATE_SUBSTITUTION_LIMITS.arrayElements,
    'Grafana raw variable array exceeds the comparison limit'
  );
  const actualLength = safeArrayLength(
    actual,
    Q_TEMPLATE_SUBSTITUTION_LIMITS.arrayElements,
    'Grafana formatter array exceeds the comparison limit'
  );
  if (expectedLength !== actualLength) {
    return false;
  }
  for (let index = 0; index < expectedLength; index++) {
    const expectedItem = requiredArrayItem(expected, index);
    const actualItem = requiredArrayItem(actual, index);
    if (
      (typeof expectedItem !== 'string' && typeof expectedItem !== 'number') ||
      typeof actualItem !== typeof expectedItem ||
      !Object.is(expectedItem, actualItem)
    ) {
      return false;
    }
  }
  return true;
}

function validateParameterName(name: string): void {
  if (
    !panopticonParameterNamePattern.test(name) ||
    name.length > Q_TEMPLATE_SUBSTITUTION_LIMITS.parameterNameBytes
  ) {
    throw new Error(
      `Panopticon parameter name must be at most ${Q_TEMPLATE_SUBSTITUTION_LIMITS.parameterNameBytes} bytes`
    );
  }
}

function validateDelimiter(delimiter: string, name: string): void {
  boundedUtf8ByteLength(
    delimiter,
    Q_TEMPLATE_SUBSTITUTION_LIMITS.delimiterBytes,
    `Panopticon parameter "${name}" delimiter exceeds the ${Q_TEMPLATE_SUBSTITUTION_LIMITS.delimiterBytes}-byte limit`
  );
  if (unsafeDelimiterPattern.test(delimiter)) {
    throw new Error(
      `Panopticon parameter "${name}" delimiter must not contain braces or control characters`
    );
  }
}

function formatterVariableName(variable: unknown): string | undefined {
  if (!variable || typeof variable !== 'object' || Array.isArray(variable)) {
    return undefined;
  }
  const descriptor = ownDataProperty(variable, 'name');
  if (
    !descriptor.found ||
    typeof descriptor.value !== 'string' ||
    descriptor.value.length === 0 ||
    descriptor.value.length > Q_TEMPLATE_SUBSTITUTION_LIMITS.parameterNameBytes ||
    !panopticonParameterNamePattern.test(descriptor.value)
  ) {
    return undefined;
  }
  return descriptor.value;
}

function assertNoSceneInterpolation(scopedVars: ScopedVars | undefined): void {
  if (scopedVars) {
    const sceneObject = ownDataProperty(scopedVars, '__sceneObject');
    if (sceneObject.found && sceneObject.value) {
      throw new Error('Grafana scene interpolation is not supported in executable q');
    }
    if (!sceneObject.found && hasProperty(scopedVars, '__sceneObject')) {
      throw new Error('Grafana scene interpolation is not supported in executable q');
    }
  }
  if (typeof window === 'undefined') {
    return;
  }
  const sceneContext = ownDataProperty(
    window as unknown as object,
    '__grafanaSceneContext'
  );
  if (!sceneContext.found) {
    if (hasProperty(window as unknown as object, '__grafanaSceneContext')) {
      throw new Error('Grafana scene interpolation is not supported in executable q');
    }
    return;
  }
  if (!sceneContext.value) {
    return;
  }
  if (typeof sceneContext.value !== 'object' || Array.isArray(sceneContext.value)) {
    throw new Error('Grafana scene interpolation state is malformed');
  }
  const isActive = ownDataProperty(sceneContext.value, 'isActive');
  if (!isActive.found || isActive.value !== false) {
    throw new Error('Grafana scene interpolation is not supported in executable q');
  }
}

function projectedReplacementTotal(
  currentBytes: number,
  tokenBytes: number,
  replacementBytes: number
): number {
  if (replacementBytes <= tokenBytes) {
    const reduction = tokenBytes - replacementBytes;
    if (!Number.isSafeInteger(reduction) || reduction < 0 || reduction > currentBytes) {
      throw new Error('Panopticon q template expansion length accounting failed');
    }
    return currentBytes - reduction;
  }
  return checkedByteTotal(
    currentBytes,
    replacementBytes - tokenBytes,
    Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes,
    `Panopticon q template exceeds the ${Q_TEMPLATE_SUBSTITUTION_LIMITS.outputBytes}-byte output limit`
  );
}

function checkedByteTotal(
  current: number,
  addition: number,
  limit: number,
  errorMessage: string
): number {
  if (
    !Number.isSafeInteger(current) ||
    !Number.isSafeInteger(addition) ||
    current < 0 ||
    addition < 0 ||
    addition > limit ||
    current > limit - addition
  ) {
    throw new Error(errorMessage);
  }
  return current + addition;
}

function incrementInterpolationCount(budget: InterpolationBudget): void {
  budget.count = checkedByteTotal(
    budget.count,
    1,
    Q_TEMPLATE_SUBSTITUTION_LIMITS.interpolations,
    `Executable q template exceeds the ${Q_TEMPLATE_SUBSTITUTION_LIMITS.interpolations}-interpolation limit`
  );
}

function boundedUtf8ByteLength(
  value: string,
  limit: number,
  errorMessage: string
): number {
  if (
    !Number.isSafeInteger(limit) ||
    limit < 0 ||
    value.length > limit
  ) {
    throw new Error(errorMessage);
  }
  const bytes = utf8Encoder.encode(value).byteLength;
  if (bytes > limit) {
    throw new Error(errorMessage);
  }
  return bytes;
}

function safeArrayLength(value: unknown[], limit: number, errorMessage: string): number {
  let descriptor: PropertyDescriptor | undefined;
  try {
    descriptor = Object.getOwnPropertyDescriptor(value, 'length');
  } catch {
    throw new Error(errorMessage);
  }
  if (
    !descriptor ||
    !('value' in descriptor) ||
    !Number.isSafeInteger(descriptor.value) ||
    descriptor.value < 0 ||
    descriptor.value > limit
  ) {
    throw new Error(errorMessage);
  }
  return descriptor.value;
}

function requiredArrayItem(value: unknown[], index: number): unknown {
  const descriptor = ownDataProperty(value, String(index));
  if (!descriptor.found) {
    throw new Error('Grafana variable arrays must be dense data arrays');
  }
  return descriptor.value;
}

function ownDataProperty(
  value: object,
  key: PropertyKey
): { found: boolean; value?: unknown } {
  let descriptor: PropertyDescriptor | undefined;
  try {
    descriptor = Object.getOwnPropertyDescriptor(value, key);
  } catch {
    throw new Error('Grafana interpolation metadata contains an unsafe property');
  }
  if (!descriptor) {
    return { found: false };
  }
  if (!('value' in descriptor)) {
    throw new Error('Grafana interpolation metadata contains an accessor property');
  }
  return { found: true, value: descriptor.value };
}

function requiredOwnDataProperty(value: object, key: PropertyKey): unknown {
  const property = ownDataProperty(value, key);
  if (!property.found) {
    throw new Error('Grafana interpolation metadata is missing a required property');
  }
  return property.value;
}

function optionalOwnDataProperty(value: object, key: PropertyKey): unknown {
  return ownDataProperty(value, key).value;
}

function hasProperty(value: object, key: PropertyKey): boolean {
  try {
    return key in value;
  } catch {
    throw new Error('Grafana interpolation metadata contains an unsafe property');
  }
}
