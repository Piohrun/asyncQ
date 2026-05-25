import type { ScopedVars, TypedVariableModel } from '@grafana/data';

const panopticonParameterPattern = /\{([A-Za-z_][A-Za-z0-9_.-]*)(?::([^{}]*))?\}/g;

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

export function expandPanopticonDashboardParameters(
  input: string,
  scopedVars?: ScopedVars,
  dashboardVariables?: TypedVariableModel[]
): string {
  if (!input) {
    return input || '';
  }

  return input.replace(panopticonParameterPattern, (token, name: string, delimiter?: string) => {
    if (reservedPanopticonParameters.has(name)) {
      return token;
    }

    const value = panopticonParameterLookup(name, scopedVars, dashboardVariables);
    if (value === undefined || value === null) {
      return token;
    }

    return panopticonParameterValue(value, delimiter);
  });
}

function panopticonParameterLookup(
  name: string,
  scopedVars?: ScopedVars,
  dashboardVariables?: TypedVariableModel[]
): unknown {
  const scopedVar = scopedVars?.[name];
  if (scopedVar && scopedVar.value !== undefined && scopedVar.value !== null) {
    return scopedVar.value;
  }

  const variable = dashboardVariables?.find((item) => item.name === name);
  if (!variable || !('current' in variable)) {
    return undefined;
  }

  const current = variable.current as { value?: unknown };
  return current.value;
}

function panopticonParameterValue(value: unknown, delimiter?: string): string {
  if (Array.isArray(value)) {
    const joiner = delimiter === undefined ? ',' : delimiter;
    return value.map(panopticonSingleParameterValue).join(joiner);
  }
  return panopticonSingleParameterValue(value);
}

function panopticonSingleParameterValue(value: unknown): string {
  if (value === undefined || value === null) {
    return '';
  }
  return String(value);
}
