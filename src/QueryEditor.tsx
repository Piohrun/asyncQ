import React, { ChangeEvent, FormEvent, PureComponent, ReactNode, SyntheticEvent } from 'react';
import { InlineFieldRow, InlineField, LegacyForms, Input, InlineSwitch, TextArea } from '@grafana/ui';
import { QueryEditorProps } from '@grafana/data';
import { DataSource } from './datasource';
import { MyDataSourceOptions, MyQuery } from './types';
const { FormField } = LegacyForms;

type Props = QueryEditorProps<DataSource, MyQuery, MyDataSourceOptions>;

const sectionStyle: React.CSSProperties = { marginTop: 12 };
const headingStyle: React.CSSProperties = { fontWeight: 500, marginBottom: 4 };
const blockStyle: React.CSSProperties = { paddingBottom: 4 };

function QuerySection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div style={sectionStyle}>
      <div style={headingStyle}>{title}</div>
      {children}
    </div>
  );
}

export class QueryEditor extends PureComponent<Props> {
  onQueryTextChange = (event: FormEvent<HTMLTextAreaElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, queryText: event.currentTarget.value });
  };

  onTimeOutChange = (event: ChangeEvent<HTMLInputElement>) => {
    if (/^\d+$/.test(event.target.value) || event.target.value === '') {
      const { onChange, query } = this.props;
      onChange({ ...query, timeOut: event.target.value === '' ? undefined : parseInt(event.target.value, 10) });
    }
  };

  onUseTimeColumnToggle = (event: SyntheticEvent<HTMLInputElement, Event>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, useTimeColumn: !query.useTimeColumn });
  };

  onTimeColumnChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, timeColumn: event.target.value });
  };

  onIncludeKeyColumnsToggle = (event: SyntheticEvent<HTMLInputElement, Event>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, includeKeyColumns: !query.includeKeyColumns });
  };

  onExecutionModeChange = (event: ChangeEvent<HTMLSelectElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, executionMode: event.target.value as MyQuery['executionMode'] });
  };

  onCompatibilityModeChange = (event: ChangeEvent<HTMLSelectElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, compatibilityMode: event.target.value as MyQuery['compatibilityMode'] });
  };

  onDeferredWrapperChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, deferredQueryWrapper: event.target.value });
  };

  onPanopticonQueryWrapperChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, panopticonQueryWrapper: event.target.value });
  };

  onPanopticonRequestFunctionChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, panopticonRequestFunction: event.target.value });
  };

  onStreamNameChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, streamName: event.target.value });
  };

  onPollIntervalChange = (event: ChangeEvent<HTMLInputElement>) => {
    if (/^\d+$/.test(event.target.value) || event.target.value === '') {
      const { onChange, query } = this.props;
      onChange({ ...query, pollIntervalMs: event.target.value === '' ? undefined : parseInt(event.target.value, 10) });
    }
  };

  onMaxStreamRowsChange = (event: ChangeEvent<HTMLInputElement>) => {
    if (/^\d+$/.test(event.target.value) || event.target.value === '') {
      const { onChange, query } = this.props;
      onChange({ ...query, maxStreamRows: event.target.value === '' ? undefined : parseInt(event.target.value, 10) });
    }
  };

  onStreamRetentionSecondsChange = (event: ChangeEvent<HTMLInputElement>) => {
    if (/^\d+$/.test(event.target.value) || event.target.value === '') {
      const { onChange, query } = this.props;
      onChange({ ...query, streamRetentionMs: event.target.value === '' ? undefined : parseInt(event.target.value, 10) * 1000 });
    }
  };

  onQueryCacheModeChange = (event: ChangeEvent<HTMLSelectElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, queryCacheMode: event.target.value as MyQuery['queryCacheMode'] });
  };

  onQueryCacheKeyModeChange = (event: ChangeEvent<HTMLSelectElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, queryCacheKeyMode: event.target.value as MyQuery['queryCacheKeyMode'] });
  };

  onQueryCacheTTLChange = (event: ChangeEvent<HTMLInputElement>) => {
    if (/^\d+$/.test(event.target.value) || event.target.value === '') {
      const { onChange, query } = this.props;
      onChange({ ...query, queryCacheTTLSeconds: event.target.value === '' ? undefined : parseInt(event.target.value, 10) });
    }
  };

  onQueryCacheStaleTTLChange = (event: ChangeEvent<HTMLInputElement>) => {
    if (/^\d+$/.test(event.target.value) || event.target.value === '') {
      const { onChange, query } = this.props;
      onChange({ ...query, queryCacheStaleTTLSeconds: event.target.value === '' ? undefined : parseInt(event.target.value, 10) });
    }
  };

  onQueryCacheTimeBucketChange = (event: ChangeEvent<HTMLInputElement>) => {
    if (/^\d+$/.test(event.target.value) || event.target.value === '') {
      const { onChange, query } = this.props;
      onChange({ ...query, queryCacheTimeBucketSeconds: event.target.value === '' ? undefined : parseInt(event.target.value, 10) });
    }
  };

  renderExecutionFields(mode: MyQuery['executionMode'], compat: MyQuery['compatibilityMode']) {
    return (
      <QuerySection title="Execution">
        <div className="gf-form" style={blockStyle}>
          <span className="gf-form-label width-13">Mode</span>
          <select className="gf-form-input width-18" value={mode || 'sync'} onChange={this.onExecutionModeChange}>
            <option value="sync">Sync</option>
            <option value="async">Helper Async</option>
            <option value="pluginAsync">Plugin Async</option>
            <option value="deferredAsync">Deferred Async</option>
            <option value="stream">Stream</option>
          </select>
          <span className="gf-form-label width-13">Compatibility</span>
          <select className="gf-form-input width-18" value={compat || 'native'} onChange={this.onCompatibilityModeChange}>
            <option value="native">Native AsyncQ</option>
            <option value="aquaq">AquaQ</option>
            <option value="panopticon">Panopticon</option>
          </select>
        </div>
      </QuerySection>
    );
  }

  renderQueryText(queryText?: string) {
    return (
      <QuerySection title="Query">
        <InlineField label="KDB Query" labelWidth={13} grow tooltip="q expression or function call sent to kdb+">
          <TextArea
            name="QueryTextInputField"
            rows={4}
            value={queryText || ''}
            onChange={this.onQueryTextChange}
            placeholder=".my.gateway.query[]"
          />
        </InlineField>
      </QuerySection>
    );
  }

  renderAsyncFields(mode: MyQuery['executionMode'], pollIntervalMs?: number) {
    if (mode === 'sync') {
      return null;
    }
    return (
      <QuerySection title={mode === 'stream' ? 'Stream Status' : 'Async Status'}>
        <div style={blockStyle}>
          <FormField
            name="PollIntervalInputField"
            inputWidth={15}
            labelWidth={13}
            value={pollIntervalMs || ''}
            onChange={this.onPollIntervalChange}
            label="Poll (ms)"
            placeholder="1000"
            tooltip="Async status polling interval. In Stream mode this is also passed to q as request metadata."
          />
        </div>
      </QuerySection>
    );
  }

  renderDeferredFields(mode: MyQuery['executionMode'], deferredQueryWrapper?: string) {
    if (mode !== 'deferredAsync') {
      return null;
    }
    return (
      <QuerySection title="Deferred Async">
        <div style={blockStyle}>
          <FormField
            name="DeferredWrapperInputField"
            inputWidth={48}
            labelWidth={13}
            value={deferredQueryWrapper || ''}
            onChange={this.onDeferredWrapperChange}
            label="Wrapper"
            tooltip="Deferred async wrapper containing exactly one {Query} placeholder."
            placeholder=".gateway.defer[{Query}]"
          />
        </div>
      </QuerySection>
    );
  }

  renderStreamFields(streamName?: string, maxStreamRows?: number, streamRetentionMs?: number) {
    const streamRetentionSeconds = streamRetentionMs ? Math.round(streamRetentionMs / 1000) : '';
    return (
      <QuerySection title="Streaming">
        <div style={blockStyle}>
          <FormField
            name="StreamNameInputField"
            inputWidth={30}
            labelWidth={13}
            value={streamName || ''}
            onChange={this.onStreamNameChange}
            label="Stream Name"
            tooltip="Optional stable stream channel name. Leave empty to create a unique stream per query run."
          />
        </div>
        <InlineFieldRow>
          <InlineField label="Max Rows" labelWidth={13} tooltip="Maximum rows retained in the browser for each streaming frame.">
            <Input width={15} value={maxStreamRows || ''} onChange={this.onMaxStreamRowsChange} placeholder="1000" />
          </InlineField>
          <InlineField label="Retention (s)" labelWidth={13} tooltip="Optional browser-side streaming time window. Leave empty or 0 to retain by Max Rows only.">
            <Input width={15} value={streamRetentionSeconds} onChange={this.onStreamRetentionSecondsChange} placeholder="0" />
          </InlineField>
        </InlineFieldRow>
      </QuerySection>
    );
  }

  renderCacheFields(
    mode: MyQuery['executionMode'],
    queryCacheMode?: MyQuery['queryCacheMode'],
    queryCacheKeyMode?: MyQuery['queryCacheKeyMode'],
    queryCacheTTLSeconds?: number,
    queryCacheStaleTTLSeconds?: number,
    queryCacheTimeBucketSeconds?: number
  ) {
    if (mode !== 'sync') {
      return null;
    }
    return (
      <QuerySection title="Cache">
        <div className="gf-form" style={blockStyle}>
          <span className="gf-form-label width-13">Mode</span>
          <select className="gf-form-input width-18" value={queryCacheMode || 'default'} onChange={this.onQueryCacheModeChange}>
            <option value="default">Datasource Default</option>
            <option value="enabled">Enabled</option>
            <option value="disabled">Disabled</option>
            <option value="bypass">Bypass</option>
            <option value="refresh">Refresh</option>
          </select>
          <span className="gf-form-label width-13">Key</span>
          <select className="gf-form-input width-18" value={queryCacheKeyMode || 'default'} onChange={this.onQueryCacheKeyModeChange}>
            <option value="default">Datasource Default</option>
            <option value="strict">Strict</option>
            <option value="shared">Shared</option>
          </select>
        </div>
        <InlineFieldRow>
          <InlineField label="TTL (s)" labelWidth={13} tooltip="Optional per-query cache TTL. Blank uses the datasource cache TTL.">
            <Input width={15} value={queryCacheTTLSeconds ?? ''} onChange={this.onQueryCacheTTLChange} placeholder="default" />
          </InlineField>
          <InlineField label="Stale TTL (s)" labelWidth={13} tooltip="Optional stale-while-revalidate window. Stale results are returned immediately while the backend refreshes the cache for the next query.">
            <Input width={15} value={queryCacheStaleTTLSeconds ?? ''} onChange={this.onQueryCacheStaleTTLChange} placeholder="default" />
          </InlineField>
          <InlineField label="Bucket (s)" labelWidth={13} tooltip="Optional per-query time range bucket. Use 0 for exact time ranges.">
            <Input width={15} value={queryCacheTimeBucketSeconds ?? ''} onChange={this.onQueryCacheTimeBucketChange} placeholder="default" />
          </InlineField>
        </InlineFieldRow>
      </QuerySection>
    );
  }

  renderPanopticonFields(compat: MyQuery['compatibilityMode'], panopticonQueryWrapper?: string, panopticonRequestFunction?: string) {
    if (compat !== 'panopticon') {
      return null;
    }
    return (
      <QuerySection title="Panopticon">
        <div style={blockStyle}>
          <FormField
            name="PanopticonWrapperInputField"
            inputWidth={48}
            labelWidth={13}
            value={panopticonQueryWrapper || ''}
            onChange={this.onPanopticonQueryWrapperChange}
            label="Wrapper"
            tooltip="Optional Panopticon wrapper expression. Use exactly one {Query}; supported macros include {TimeWindowStart}, {IntervalMs}, and Grafana-backed dashboard parameters such as {symbol}."
            placeholder=".pano.run[{Query};{TimeWindowStart};{TimeWindowEnd}]"
          />
        </div>
        <div style={blockStyle}>
          <FormField
            name="PanopticonRequestFunctionInputField"
            inputWidth={48}
            labelWidth={13}
            value={panopticonRequestFunction || ''}
            onChange={this.onPanopticonRequestFunctionChange}
            label="Request Fn"
            tooltip="Optional q function or lambda that accepts the full request dictionary. When set, the backend calls it instead of evaluating Query text directly."
            placeholder="{[req] .pano.run req}"
          />
        </div>
      </QuerySection>
    );
  }

  renderResultFields(timeOut?: number, useTimeColumn?: boolean, includeKeyColumns?: boolean, timeColumn?: string) {
    return (
      <QuerySection title="Result">
        <div style={blockStyle}>
          <FormField
            name="TimeoutTextInputField"
            inputWidth={15}
            labelWidth={13}
            value={timeOut || ''}
            onChange={this.onTimeOutChange}
            label="Timeout (ms)"
            placeholder="10000"
            tooltip="Backend query timeout in milliseconds. Blank uses the backend default."
          />
        </div>
        <InlineFieldRow>
          <InlineField
            label="Custom Time Column"
            labelWidth={26}
            tooltip="Use a named temporal column as the time axis instead of Grafana's default temporal column detection."
          >
            <InlineSwitch checked={!!useTimeColumn} onChange={this.onUseTimeColumnToggle} />
          </InlineField>
          <InlineField hidden={!useTimeColumn} label="Time Column" labelWidth={13} tooltip="Name of temporal column to use as the time axis">
            <Input hidden={!useTimeColumn} className="TimeColumnInputField" width={30} value={timeColumn || ''} onChange={this.onTimeColumnChange} placeholder="time" />
          </InlineField>
        </InlineFieldRow>
        <InlineField label="Include Keys" labelWidth={26} tooltip="Include key columns in grouped-table output.">
          <InlineSwitch checked={!!includeKeyColumns} onChange={this.onIncludeKeyColumnsToggle} />
        </InlineField>
      </QuerySection>
    );
  }

  render() {
    const query = this.props.query;
    const {
      queryText,
      timeOut,
      useTimeColumn,
      includeKeyColumns,
      timeColumn,
      executionMode,
      compatibilityMode,
      deferredQueryWrapper,
      panopticonQueryWrapper,
      panopticonRequestFunction,
      streamName,
      pollIntervalMs,
      maxStreamRows,
      streamRetentionMs,
      queryCacheMode,
      queryCacheKeyMode,
      queryCacheTTLSeconds,
      queryCacheStaleTTLSeconds,
      queryCacheTimeBucketSeconds,
    } = query;
    const mode = executionMode || 'sync';
    const compat = compatibilityMode || 'native';

    return (
      <>
        {this.renderExecutionFields(mode, compat)}
        {this.renderQueryText(queryText)}
        {this.renderCacheFields(mode, queryCacheMode, queryCacheKeyMode, queryCacheTTLSeconds, queryCacheStaleTTLSeconds, queryCacheTimeBucketSeconds)}
        {this.renderDeferredFields(mode, deferredQueryWrapper)}
        {mode === 'stream' && this.renderStreamFields(streamName, maxStreamRows, streamRetentionMs)}
        {this.renderPanopticonFields(compat, panopticonQueryWrapper, panopticonRequestFunction)}
        {this.renderAsyncFields(mode, pollIntervalMs)}
        {this.renderResultFields(timeOut, useTimeColumn, includeKeyColumns, timeColumn)}
      </>
    );
  }
}
