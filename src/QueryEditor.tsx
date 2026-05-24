import React, { ChangeEvent, PureComponent, SyntheticEvent } from 'react';
import { InlineFieldRow, InlineField, LegacyForms, Input, InlineSwitch } from '@grafana/ui';
import { QueryEditorProps } from '@grafana/data';
import { DataSource } from './datasource';
import { MyDataSourceOptions, MyQuery } from './types';
const { FormField } = LegacyForms;

type Props = QueryEditorProps<DataSource, MyQuery, MyDataSourceOptions>;

export class QueryEditor extends PureComponent<Props> {
    onQueryTextChange = (event: ChangeEvent<HTMLInputElement>) => {
        const { onChange, query } = this.props;
        onChange({ ...query, queryText: event.target.value });
    };

    onTimeOutChange = (event: ChangeEvent<HTMLInputElement>) => {
        if((/^\d+$/.test(event.target.value) || event.target.value==="")){
            const { onChange, query } = this.props;
            onChange({ ...query, timeOut: parseInt(event.target.value, 10) });
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
        if((/^\d+$/.test(event.target.value) || event.target.value==="")){
            const { onChange, query } = this.props;
            onChange({ ...query, pollIntervalMs: parseInt(event.target.value, 10) });
        }
    };
    onMaxStreamRowsChange = (event: ChangeEvent<HTMLInputElement>) => {
        if((/^\d+$/.test(event.target.value) || event.target.value==="")){
            const { onChange, query } = this.props;
            onChange({ ...query, maxStreamRows: parseInt(event.target.value, 10) });
        }
    };
    onStreamRetentionSecondsChange = (event: ChangeEvent<HTMLInputElement>) => {
        if((/^\d+$/.test(event.target.value) || event.target.value==="")){
            const { onChange, query } = this.props;
            onChange({ ...query, streamRetentionMs: event.target.value === '' ? undefined : parseInt(event.target.value, 10) * 1000 });
        }
    };

    render() {
        const query = this.props.query;
        const { queryText, timeOut, useTimeColumn, includeKeyColumns, timeColumn, executionMode, compatibilityMode, deferredQueryWrapper, panopticonQueryWrapper, panopticonRequestFunction, streamName, pollIntervalMs, maxStreamRows, streamRetentionMs } = query;
        const mode = executionMode || 'sync';
        const compat = compatibilityMode || 'native';
        const streamRetentionSeconds = streamRetentionMs ? Math.round(streamRetentionMs / 1000) : '';
        return (
            <>
                <div className="gf-form" style={{paddingBottom: 4}}>
                    <span className="gf-form-label width-13">Mode</span>
                    <select className="gf-form-input width-15" value={mode} onChange={this.onExecutionModeChange}>
                        <option value="sync">Sync</option>
                        <option value="async">Helper Async</option>
                        <option value="pluginAsync">Plugin Async</option>
                        <option value="deferredAsync">Deferred Async</option>
                        <option value="stream">Stream</option>
                    </select>
                </div>
                <div className="gf-form" style={{paddingBottom: 4}}>
                    <span className="gf-form-label width-13">Compatibility</span>
                    <select className="gf-form-input width-15" value={compat} onChange={this.onCompatibilityModeChange}>
                        <option value="native">Native AsyncQ</option>
                        <option value="aquaq">AquaQ</option>
                        <option value="panopticon">Panopticon</option>
                    </select>
                </div>
                <div style={{paddingBottom: 4}}>
                <FormField
                    name="QueryTextInputField"
                    inputWidth={40}
                    labelWidth={13}
                    value={queryText || ''}
                    onChange={this.onQueryTextChange}
                    label="KDB Query"
                    tooltip="Please enter a KDB Query"
                />
                </div>
                {mode === 'deferredAsync' && <div style={{paddingBottom: 4}}>
                <FormField
                    name="DeferredWrapperInputField"
                    inputWidth={40}
                    labelWidth={13}
                    value={deferredQueryWrapper || ''}
                    onChange={this.onDeferredWrapperChange}
                    label="Wrapper"
                    tooltip="Deferred async wrapper containing exactly one {Query} placeholder."
                    placeholder=".gateway.defer[{Query}]"
                />
                </div>}
                {compat === 'panopticon' && <>
                <div style={{paddingBottom: 4}}>
                <FormField
                    name="PanopticonWrapperInputField"
                    inputWidth={40}
                    labelWidth={13}
                    value={panopticonQueryWrapper || ''}
                    onChange={this.onPanopticonQueryWrapperChange}
                    label="Pano Wrapper"
                    tooltip="Optional Panopticon wrapper expression. Use exactly one {Query}; supported macros include {TimeWindowStart}, {TimeWindowEnd}, {Snapshot}, {IntervalMs}, and {MaxDataPoints}."
                    placeholder=".pano.run[{Query};{TimeWindowStart};{TimeWindowEnd}]"
                />
                </div>
                <div style={{paddingBottom: 4}}>
                <FormField
                    name="PanopticonRequestFunctionInputField"
                    inputWidth={40}
                    labelWidth={13}
                    value={panopticonRequestFunction || ''}
                    onChange={this.onPanopticonRequestFunctionChange}
                    label="Pano Fn"
                    tooltip="Optional q function or lambda that accepts the full request dictionary. When set, the backend calls it instead of evaluating Query text directly."
                    placeholder="{[req] .pano.run req}"
                />
                </div>
                </>}
                {mode !== 'sync' && <div style={{paddingBottom: 4}}>
                <FormField
                    name="PollIntervalInputField"
                    inputWidth={15}
                    labelWidth={13}
                    value={pollIntervalMs || ''}
                    onChange={this.onPollIntervalChange}
                    label="Poll (ms)"
                    tooltip="For async mode this controls status polling. Streaming mode passes it as q-side metadata."
                />
                </div>}
                {mode === 'stream' && <>
                <div style={{paddingBottom: 4}}>
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
                <div style={{paddingBottom: 4}}>
                <FormField
                    name="MaxStreamRowsInputField"
                    inputWidth={15}
                    labelWidth={13}
                    value={maxStreamRows || ''}
                    onChange={this.onMaxStreamRowsChange}
                    label="Max Rows"
                    tooltip="Maximum rows retained in the browser for each streaming frame."
                />
                </div>
                <div style={{paddingBottom: 4}}>
                <FormField
                    name="StreamRetentionInputField"
                    inputWidth={15}
                    labelWidth={13}
                    value={streamRetentionSeconds}
                    onChange={this.onStreamRetentionSecondsChange}
                    label="Retention (s)"
                    tooltip="Optional browser-side streaming time window. Leave empty or 0 to retain by Max Rows only."
                />
                </div>
                </>}
                <div style={{paddingBottom: 4}}>
                <FormField
                    name="TimeoutTextInputField"
                    inputWidth={15}
                    labelWidth={13}
                    value={timeOut || ''}
                    onChange={this.onTimeOutChange}
                    label="Timeout (ms)"
                    tooltip="Please enter a Timeout in ms, default is 10,000 ms"
                />
                </div>
                <div style={{paddingBottom: 4}}>
                <InlineFieldRow>
                    <InlineField
                        label="Use Custom Time Column"
                        labelWidth={26}
                        tooltip="Grafana will default to using the left-most temporal column as the time axis. Select this to use a different temporal column as the time axis"
                        >
                        <InlineSwitch checked={useTimeColumn} label="Use Custom Time Column" onChange={this.onUseTimeColumnToggle}/>
                    </InlineField>
                    <InlineField
                        hidden={!useTimeColumn}
                        label="Time Column"
                        labelWidth={26}
                        tooltip="Name of temporal column to use as the time axis"
                        >
                        <Input
                            hidden={!useTimeColumn}
                            className="TimeColumnInputField"
                            width={30}
                            value={timeColumn || ''}
                            onChange={this.onTimeColumnChange}
                        />
                    </InlineField>
                </InlineFieldRow>
                <InlineField
                    label="Include Keys In Output"
                    labelWidth={26}
                    tooltip="If enabled, key columns will be projected and included in the output for grouped-series results">
                    <InlineSwitch checked={includeKeyColumns} onChange={this.onIncludeKeyColumnsToggle} />
                </InlineField>
                </div>
                </>
        );
    }
}
