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
const detailsStyle: React.CSSProperties = { marginTop: 6, marginBottom: 6 };
const summaryStyle: React.CSSProperties = { cursor: 'pointer', fontWeight: 500, marginBottom: 6 };

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

  onLegacyAsyncSubmitChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, legacyAsyncSubmit: event.target.value });
  };

  onLegacyAsyncStatusChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, legacyAsyncStatus: event.target.value });
  };

  onLegacyAsyncResultChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, legacyAsyncResult: event.target.value });
  };

  onLegacyAsyncCancelChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, legacyAsyncCancel: event.target.value });
  };

  onLegacyAsyncRequestModeChange = (event: ChangeEvent<HTMLSelectElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, legacyAsyncRequestMode: event.target.value as MyQuery['legacyAsyncRequestMode'] });
  };

  onLegacyAsyncJobIDPathChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, legacyAsyncJobIDPath: event.target.value });
  };

  onLegacyAsyncStatusPathChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, legacyAsyncStatusPath: event.target.value });
  };

  onLegacyAsyncProgressPathChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, legacyAsyncProgressPath: event.target.value });
  };

  onLegacyAsyncMessagePathChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, legacyAsyncMessagePath: event.target.value });
  };

  onLegacyAsyncErrorPathChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, legacyAsyncErrorPath: event.target.value });
  };

  onLegacyAsyncPayloadPathChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, legacyAsyncPayloadPath: event.target.value });
  };

  onLegacyAsyncQueuedValuesChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, legacyAsyncQueuedValues: event.target.value });
  };

  onLegacyAsyncRunningValuesChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, legacyAsyncRunningValues: event.target.value });
  };

  onLegacyAsyncDoneValuesChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, legacyAsyncDoneValues: event.target.value });
  };

  onLegacyAsyncErrorValuesChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, legacyAsyncErrorValues: event.target.value });
  };

  onLegacyAsyncCancelledValuesChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onChange, query } = this.props;
    onChange({ ...query, legacyAsyncCancelledValues: event.target.value });
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
            <option value="legacyAsync">Legacy Async</option>
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

  renderLegacyAsyncFields(mode: MyQuery['executionMode'], query: MyQuery) {
    if (mode !== 'legacyAsync') {
      return null;
    }
    return (
      <QuerySection title="Legacy Async Adapter">
        <div style={blockStyle}>
          <FormField
            name="LegacyAsyncSubmitInputField"
            inputWidth={40}
            labelWidth={13}
            value={query.legacyAsyncSubmit || ''}
            onChange={this.onLegacyAsyncSubmitChange}
            label="Submit Fn"
            placeholder=".gw.submit"
            tooltip="q function or lambda called once to submit the request. It must return a job id or an envelope containing one."
          />
        </div>
        <div style={blockStyle}>
          <FormField
            name="LegacyAsyncStatusInputField"
            inputWidth={40}
            labelWidth={13}
            value={query.legacyAsyncStatus || ''}
            onChange={this.onLegacyAsyncStatusChange}
            label="Status Fn"
            placeholder=".gw.status"
            tooltip="q function or lambda polled with the job id until it reaches a configured terminal status."
          />
        </div>
        <div style={blockStyle}>
          <FormField
            name="LegacyAsyncResultInputField"
            inputWidth={40}
            labelWidth={13}
            value={query.legacyAsyncResult || ''}
            onChange={this.onLegacyAsyncResultChange}
            label="Result Fn"
            placeholder=".gw.result"
            tooltip="Optional q function or lambda called with the job id when status is done. Not required when the terminal status envelope contains the result payload."
          />
        </div>
        <div style={blockStyle}>
          <FormField
            name="LegacyAsyncCancelInputField"
            inputWidth={40}
            labelWidth={13}
            value={query.legacyAsyncCancel || ''}
            onChange={this.onLegacyAsyncCancelChange}
            label="Cancel Fn"
            placeholder=".gw.cancel"
            tooltip="Optional q function or lambda called with the job id when Grafana cancels the panel query."
          />
        </div>
        <div className="gf-form" style={blockStyle}>
          <span className="gf-form-label width-13">Request</span>
          <select className="gf-form-input width-20" value={query.legacyAsyncRequestMode || 'requestDict'} onChange={this.onLegacyAsyncRequestModeChange}>
            <option value="requestDict">Request Dict</option>
            <option value="panopticonDict">Panopticon Dict</option>
            <option value="compiledQueryText">Compiled Query Text</option>
            <option value="queryText">Original Query Text</option>
          </select>
        </div>
        <details style={detailsStyle}>
          <summary style={summaryStyle}>Response mapping</summary>
          <InlineFieldRow>
            <InlineField label="Job ID" labelWidth={13} tooltip="Submit/status envelope path that contains the job id.">
              <Input width={16} value={query.legacyAsyncJobIDPath || ''} onChange={this.onLegacyAsyncJobIDPathChange} placeholder="jobId" />
            </InlineField>
            <InlineField label="Status" labelWidth={13} tooltip="Status envelope path.">
              <Input width={16} value={query.legacyAsyncStatusPath || ''} onChange={this.onLegacyAsyncStatusPathChange} placeholder="status" />
            </InlineField>
            <InlineField label="Progress" labelWidth={13} tooltip="Optional numeric progress envelope path.">
              <Input width={16} value={query.legacyAsyncProgressPath || ''} onChange={this.onLegacyAsyncProgressPathChange} placeholder="progress" />
            </InlineField>
          </InlineFieldRow>
          <InlineFieldRow>
            <InlineField label="Message" labelWidth={13} tooltip="Optional status message envelope path.">
              <Input width={16} value={query.legacyAsyncMessagePath || ''} onChange={this.onLegacyAsyncMessagePathChange} placeholder="message" />
            </InlineField>
            <InlineField label="Error" labelWidth={13} tooltip="Optional q-side error envelope path.">
              <Input width={16} value={query.legacyAsyncErrorPath || ''} onChange={this.onLegacyAsyncErrorPathChange} placeholder="error" />
            </InlineField>
            <InlineField label="Payload" labelWidth={13} tooltip="Result payload path in status or result envelopes. Raw table results can leave this at the default.">
              <Input width={16} value={query.legacyAsyncPayloadPath || ''} onChange={this.onLegacyAsyncPayloadPathChange} placeholder="result" />
            </InlineField>
          </InlineFieldRow>
          <InlineFieldRow>
            <InlineField label="Queued" labelWidth={13} tooltip="Comma-separated legacy statuses treated as queued.">
              <Input width={24} value={query.legacyAsyncQueuedValues || ''} onChange={this.onLegacyAsyncQueuedValuesChange} placeholder="queued,pending" />
            </InlineField>
            <InlineField label="Running" labelWidth={13} tooltip="Comma-separated legacy statuses treated as running.">
              <Input width={24} value={query.legacyAsyncRunningValues || ''} onChange={this.onLegacyAsyncRunningValuesChange} placeholder="running,executing" />
            </InlineField>
          </InlineFieldRow>
          <InlineFieldRow>
            <InlineField label="Done" labelWidth={13} tooltip="Comma-separated legacy statuses treated as done.">
              <Input width={24} value={query.legacyAsyncDoneValues || ''} onChange={this.onLegacyAsyncDoneValuesChange} placeholder="done,complete,completed" />
            </InlineField>
            <InlineField label="Error" labelWidth={13} tooltip="Comma-separated legacy statuses treated as error.">
              <Input width={24} value={query.legacyAsyncErrorValues || ''} onChange={this.onLegacyAsyncErrorValuesChange} placeholder="error,failed" />
            </InlineField>
            <InlineField label="Cancelled" labelWidth={13} tooltip="Comma-separated legacy statuses treated as cancelled.">
              <Input width={24} value={query.legacyAsyncCancelledValues || ''} onChange={this.onLegacyAsyncCancelledValuesChange} placeholder="cancelled,canceled" />
            </InlineField>
          </InlineFieldRow>
        </details>
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
        {this.renderLegacyAsyncFields(mode, query)}
        {mode === 'stream' && this.renderStreamFields(streamName, maxStreamRows, streamRetentionMs)}
        {this.renderPanopticonFields(compat, panopticonQueryWrapper, panopticonRequestFunction)}
        {this.renderAsyncFields(mode, pollIntervalMs)}
        {this.renderResultFields(timeOut, useTimeColumn, includeKeyColumns, timeColumn)}
      </>
    );
  }
}
