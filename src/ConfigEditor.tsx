import { defaults } from 'lodash';
import React, { ChangeEvent, FormEvent, PureComponent, ReactNode, SyntheticEvent } from 'react';
import { InlineField, InlineSwitch, LegacyForms, TextArea } from '@grafana/ui';
import { DataSourcePluginOptionsEditorProps } from '@grafana/data';
import { defaultConfig, MyDataSourceOptions, MySecureJsonData } from './types';
const { FormField, SecretFormField } = LegacyForms;

interface Props extends DataSourcePluginOptionsEditorProps<MyDataSourceOptions> {}

const sectionStyle: React.CSSProperties = { marginTop: 16 };
const headingStyle: React.CSSProperties = { fontWeight: 500, marginBottom: 4 };

function ConfigSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div style={sectionStyle}>
      <div style={headingStyle}>{title}</div>
      {children}
    </div>
  );
}

export class ConfigEditor extends PureComponent<Props> {
  updateJsonData = (patch: Partial<MyDataSourceOptions>) => {
    const { onOptionsChange, options } = this.props;
    onOptionsChange({
      ...options,
      jsonData: {
        ...options.jsonData,
        ...patch,
      },
    });
  };

  onHostChange = (event: ChangeEvent<HTMLInputElement>) => {
    this.updateJsonData({ host: event.target.value });
  };

  onPortChange = (event: ChangeEvent<HTMLInputElement>) => {
    if (/^\d+$/.test(event.target.value) || event.target.value === '') {
      this.updateJsonData({ port: event.target.value === '' ? undefined : parseInt(event.target.value, 10) });
    }
  };

  onTimeoutChange = (event: ChangeEvent<HTMLInputElement>) => {
    if (/^\d+$/.test(event.target.value) || event.target.value === '') {
      this.updateJsonData({ timeout: event.target.value });
    }
  };

  onUsernameChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onOptionsChange, options } = this.props;
    onOptionsChange({
      ...options,
      secureJsonData: {
        ...options.secureJsonData,
        username: event.target.value,
      },
    });
  };

  onResetUsername = () => {
    const { onOptionsChange, options } = this.props;
    onOptionsChange({
      ...options,
      secureJsonFields: {
        ...options.secureJsonFields,
        username: false,
      },
      secureJsonData: {
        ...options.secureJsonData,
        username: '',
      },
    });
  };

  onPasswordChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onOptionsChange, options } = this.props;
    onOptionsChange({
      ...options,
      secureJsonData: {
        ...options.secureJsonData,
        password: event.target.value,
      },
    });
  };

  onResetPassword = () => {
    const { onOptionsChange, options } = this.props;
    onOptionsChange({
      ...options,
      secureJsonFields: {
        ...options.secureJsonFields,
        password: false,
      },
      secureJsonData: {
        ...options.secureJsonData,
        password: '',
      },
    });
  };

  onTlsToggle = (event: SyntheticEvent) => {
    this.updateJsonData({ withTLS: !this.props.options.jsonData.withTLS });
  };

  onSkipTlsToggle = (event: SyntheticEvent) => {
    this.updateJsonData({ skipVerifyTLS: !this.props.options.jsonData.skipVerifyTLS });
  };

  onCaCertToggle = (event: SyntheticEvent) => {
    this.updateJsonData({ withCACert: !this.props.options.jsonData.withCACert });
  };

  onEnableAsyncToggle = (event: SyntheticEvent) => {
    this.updateJsonData({ enableAsync: !(this.props.options.jsonData.enableAsync !== false) });
  };

  onEnableStreamingToggle = (event: SyntheticEvent) => {
    this.updateJsonData({ enableStreaming: !(this.props.options.jsonData.enableStreaming !== false) });
  };

  onDiagnosticsToggle = (event: SyntheticEvent) => {
    const nextEnabled = !this.props.options.jsonData.diagnosticsEnabled;
    this.updateJsonData({
      diagnosticsEnabled: nextEnabled,
      diagnosticsLogQueryText: nextEnabled ? this.props.options.jsonData.diagnosticsLogQueryText : false,
    });
  };

  onDiagnosticsLogQueryTextToggle = (event: SyntheticEvent) => {
    this.updateJsonData({ diagnosticsLogQueryText: !this.props.options.jsonData.diagnosticsLogQueryText });
  };

  onDefaultExecutionModeChange = (event: ChangeEvent<HTMLSelectElement>) => {
    this.updateJsonData({ executionMode: event.target.value as MyDataSourceOptions['executionMode'] });
  };

  onCompatibilityModeChange = (event: ChangeEvent<HTMLSelectElement>) => {
    this.updateJsonData({ compatibilityMode: event.target.value as MyDataSourceOptions['compatibilityMode'] });
  };

  onDeferredWrapperChange = (event: ChangeEvent<HTMLInputElement>) => {
    this.updateJsonData({ deferredQueryWrapper: event.target.value });
  };

  onPanopticonQueryWrapperChange = (event: ChangeEvent<HTMLInputElement>) => {
    this.updateJsonData({ panopticonQueryWrapper: event.target.value });
  };

  onPanopticonRequestFunctionChange = (event: ChangeEvent<HTMLInputElement>) => {
    this.updateJsonData({ panopticonRequestFunction: event.target.value });
  };

  onAsyncMaxJobsChange = (event: ChangeEvent<HTMLInputElement>) => {
    if (/^\d+$/.test(event.target.value) || event.target.value === '') {
      this.updateJsonData({ asyncMaxJobs: event.target.value === '' ? undefined : parseInt(event.target.value, 10) });
    }
  };

  onSyncMaxConnectionsChange = (event: ChangeEvent<HTMLInputElement>) => {
    if (/^\d+$/.test(event.target.value) || event.target.value === '') {
      this.updateJsonData({ syncMaxConnections: event.target.value === '' ? undefined : parseInt(event.target.value, 10) });
    }
  };

  onQueryCacheToggle = (event: SyntheticEvent) => {
    this.updateJsonData({ queryCacheEnabled: !(this.props.options.jsonData.queryCacheEnabled !== false) });
  };

  onQueryCacheDiskToggle = (event: SyntheticEvent) => {
    this.updateJsonData({ queryCacheDiskEnabled: !(this.props.options.jsonData.queryCacheDiskEnabled !== false) });
  };

  onQueryCacheControlToggle = (event: SyntheticEvent) => {
    this.updateJsonData({ queryCacheControlEnabled: !(this.props.options.jsonData.queryCacheControlEnabled !== false) });
  };

  onQueryCacheTTLChange = (event: ChangeEvent<HTMLInputElement>) => {
    if (/^\d+$/.test(event.target.value) || event.target.value === '') {
      this.updateJsonData({ queryCacheTTLSeconds: event.target.value === '' ? undefined : parseInt(event.target.value, 10) });
    }
  };

  onQueryCacheMaxEntriesChange = (event: ChangeEvent<HTMLInputElement>) => {
    if (/^\d+$/.test(event.target.value) || event.target.value === '') {
      this.updateJsonData({ queryCacheMaxEntries: event.target.value === '' ? undefined : parseInt(event.target.value, 10) });
    }
  };

  onQueryCacheTimeBucketChange = (event: ChangeEvent<HTMLInputElement>) => {
    if (/^\d+$/.test(event.target.value) || event.target.value === '') {
      this.updateJsonData({ queryCacheTimeBucketSeconds: event.target.value === '' ? undefined : parseInt(event.target.value, 10) });
    }
  };

  onQueryCacheStaleTTLChange = (event: ChangeEvent<HTMLInputElement>) => {
    if (/^\d+$/.test(event.target.value) || event.target.value === '') {
      this.updateJsonData({ queryCacheStaleTTLSeconds: event.target.value === '' ? undefined : parseInt(event.target.value, 10) });
    }
  };

  onQueryCacheKeyModeChange = (event: ChangeEvent<HTMLSelectElement>) => {
    this.updateJsonData({ queryCacheKeyMode: event.target.value as MyDataSourceOptions['queryCacheKeyMode'] });
  };

  onQueryCacheDiskPathChange = (event: ChangeEvent<HTMLInputElement>) => {
    this.updateJsonData({ queryCacheDiskPath: event.target.value });
  };

  onQueryCacheDiskMaxBytesChange = (event: ChangeEvent<HTMLInputElement>) => {
    if (/^\d+$/.test(event.target.value) || event.target.value === '') {
      this.updateJsonData({ queryCacheDiskMaxBytes: event.target.value === '' ? undefined : parseInt(event.target.value, 10) });
    }
  };

  onQueryCacheDiskMaxEntriesChange = (event: ChangeEvent<HTMLInputElement>) => {
    if (/^\d+$/.test(event.target.value) || event.target.value === '') {
      this.updateJsonData({ queryCacheDiskMaxEntries: event.target.value === '' ? undefined : parseInt(event.target.value, 10) });
    }
  };

  onTlsCertificateChange = (event: FormEvent<HTMLTextAreaElement>) => {
    const { onOptionsChange, options } = this.props;
    onOptionsChange({
      ...options,
      secureJsonData: {
        ...options.secureJsonData,
        tlsCertificate: event.currentTarget.value,
      },
    });
  };

  onTlsCertificateReset = () => {
    const { onOptionsChange, options } = this.props;
    onOptionsChange({
      ...options,
      secureJsonFields: {
        ...options.secureJsonFields,
        tlsCertificate: false,
      },
      secureJsonData: {
        ...options.secureJsonData,
        tlsCertificate: '',
      },
    });
  };

  onTlsKeyChange = (event: FormEvent<HTMLTextAreaElement>) => {
    const { onOptionsChange, options } = this.props;
    onOptionsChange({
      ...options,
      secureJsonData: {
        ...options.secureJsonData,
        tlsKey: event.currentTarget.value,
      },
    });
  };

  onTlsKeyReset = () => {
    const { onOptionsChange, options } = this.props;
    onOptionsChange({
      ...options,
      secureJsonFields: {
        ...options.secureJsonFields,
        tlsKey: false,
      },
      secureJsonData: {
        ...options.secureJsonData,
        tlsKey: '',
      },
    });
  };

  onCaCertChange = (event: FormEvent<HTMLTextAreaElement>) => {
    const { onOptionsChange, options } = this.props;
    onOptionsChange({
      ...options,
      secureJsonData: {
        ...options.secureJsonData,
        caCert: event.currentTarget.value,
      },
    });
  };

  onCaCertReset = () => {
    const { onOptionsChange, options } = this.props;
    onOptionsChange({
      ...options,
      secureJsonFields: {
        ...options.secureJsonFields,
        caCert: false,
      },
      secureJsonData: {
        ...options.secureJsonData,
        caCert: '',
      },
    });
  };

  renderSecretFields(secureJsonFields: Record<string, boolean>, secureJsonData: MySecureJsonData) {
    return (
      <>
        <div className="gf-form">
          <SecretFormField
            name="UsernameInputField"
            isConfigured={(secureJsonFields && secureJsonFields.username) as boolean}
            value={secureJsonData.username || ''}
            label="Username"
            placeholder="Username"
            labelWidth={10}
            inputWidth={22}
            onReset={this.onResetUsername}
            onChange={this.onUsernameChange}
          />
        </div>
        <div className="gf-form">
          <SecretFormField
            name="PasswordInputField"
            isConfigured={(secureJsonFields && secureJsonFields.password) as boolean}
            value={secureJsonData.password || ''}
            label="Password"
            placeholder="Password"
            labelWidth={10}
            inputWidth={22}
            onReset={this.onResetPassword}
            onChange={this.onPasswordChange}
          />
        </div>
      </>
    );
  }

  renderTLSCertificates(jsonData: MyDataSourceOptions, secureJsonFields: Record<string, boolean>, secureJsonData: MySecureJsonData) {
    if (!jsonData.withTLS) {
      return null;
    }

    return (
      <>
        <div className="gf-form">
          {secureJsonFields.tlsKey ? (
            <SecretFormField
              name="TLSKeyInputField"
              isConfigured={(secureJsonFields && secureJsonFields.tlsKey) as boolean}
              value={secureJsonData.tlsKey || ''}
              label="TLS Key"
              placeholder="TLS Key"
              labelWidth={10}
              inputWidth={22}
              onReset={this.onTlsKeyReset}
            />
          ) : (
            <InlineField label="TLS Key" labelWidth={14} grow>
              <TextArea style={{ width: 360 }} placeholder="TLS Key" value={secureJsonData.tlsKey || ''} name="TLSKeyInputField" onChange={this.onTlsKeyChange} />
            </InlineField>
          )}
        </div>
        <div className="gf-form">
          {secureJsonFields.tlsCertificate ? (
            <SecretFormField
              name="TLSCertInputField"
              isConfigured={(secureJsonFields && secureJsonFields.tlsCertificate) as boolean}
              value={secureJsonData.tlsCertificate || ''}
              label="TLS Certificate"
              placeholder="TLS Certificate"
              labelWidth={10}
              inputWidth={22}
              onReset={this.onTlsCertificateReset}
            />
          ) : (
            <InlineField label="TLS Certificate" labelWidth={14} grow>
              <TextArea style={{ width: 360 }} placeholder="TLS Certificate" value={secureJsonData.tlsCertificate || ''} name="TLSCertInputField" onChange={this.onTlsCertificateChange} />
            </InlineField>
          )}
        </div>
        {jsonData.withCACert && (
          <div className="gf-form">
            {secureJsonFields.caCert ? (
              <SecretFormField
                name="TLSCAInputField"
                isConfigured={(secureJsonFields && secureJsonFields.caCert) as boolean}
                value={secureJsonData.caCert || ''}
                label="CA Certificate"
                placeholder="CA Certificate"
                labelWidth={10}
                inputWidth={22}
                onReset={this.onCaCertReset}
              />
            ) : (
              <InlineField label="CA Certificate" labelWidth={14} grow>
                <TextArea style={{ width: 360 }} placeholder="CA Certificate" value={secureJsonData.caCert || ''} name="TLSCAInputField" onChange={this.onCaCertChange} />
              </InlineField>
            )}
          </div>
        )}
      </>
    );
  }

  renderConnection(jsonData: MyDataSourceOptions, secureJsonFields: Record<string, boolean>, secureJsonData: MySecureJsonData) {
    return (
      <ConfigSection title="Connection">
        <div className="gf-form">
          <FormField name="HostInputField" label="Host" labelWidth={10} inputWidth={28} onChange={this.onHostChange} value={jsonData.host || ''} placeholder="localhost" />
        </div>
        <div className="gf-form">
          <FormField name="PortInputField" label="Port" labelWidth={10} inputWidth={12} onChange={this.onPortChange} value={jsonData.port ?? ''} placeholder="5000" />
          <FormField name="TimeoutInputField" label="Timeout (ms)" labelWidth={12} inputWidth={12} onChange={this.onTimeoutChange} value={jsonData.timeout || ''} placeholder="5000" />
        </div>
        {this.renderSecretFields(secureJsonFields, secureJsonData)}
      </ConfigSection>
    );
  }

  renderTLS(jsonData: MyDataSourceOptions, secureJsonFields: Record<string, boolean>, secureJsonData: MySecureJsonData) {
    return (
      <ConfigSection title="TLS">
        <div className="gf-form">
          <InlineField label="Client Auth" labelWidth={14}>
            <InlineSwitch checked={!!jsonData.withTLS} onChange={this.onTlsToggle} />
          </InlineField>
          {jsonData.withTLS && (
            <>
              <InlineField label="Skip Verify" labelWidth={14}>
                <InlineSwitch checked={!!jsonData.skipVerifyTLS} onChange={this.onSkipTlsToggle} />
              </InlineField>
              <InlineField label="CA Cert" labelWidth={14}>
                <InlineSwitch checked={!!jsonData.withCACert} onChange={this.onCaCertToggle} />
              </InlineField>
            </>
          )}
        </div>
        {this.renderTLSCertificates(jsonData, secureJsonFields, secureJsonData)}
      </ConfigSection>
    );
  }

  renderCapabilities(jsonData: MyDataSourceOptions) {
    return (
      <ConfigSection title="Capabilities">
        <div className="gf-form">
          <InlineField label="Async Queries" labelWidth={14}>
            <InlineSwitch checked={jsonData.enableAsync !== false} onChange={this.onEnableAsyncToggle} />
          </InlineField>
          <InlineField label="Streaming" labelWidth={14}>
            <InlineSwitch checked={jsonData.enableStreaming !== false} onChange={this.onEnableStreamingToggle} />
          </InlineField>
        </div>
        <div className="gf-form">
          <FormField
            name="SyncMaxConnectionsInputField"
            label="Sync Max Connections"
            labelWidth={18}
            inputWidth={12}
            onChange={this.onSyncMaxConnectionsChange}
            value={jsonData.syncMaxConnections ?? ''}
            placeholder="4"
            tooltip="Maximum reusable synchronous kdb+ IPC connections for this datasource instance. Set to 1 for strict legacy serial behavior."
          />
          <FormField
            name="AsyncMaxJobsInputField"
            label="Async Max Jobs"
            labelWidth={14}
            inputWidth={12}
            onChange={this.onAsyncMaxJobsChange}
            value={jsonData.asyncMaxJobs ?? ''}
            placeholder="16"
            tooltip="Maximum plugin-managed async jobs for this datasource instance."
          />
        </div>
        <div className="gf-form">
          <InlineField label="Query Cache" labelWidth={18} tooltip="Caches successful sync query results in this datasource instance. Disable for writeback/action queries or panels that must always hit kdb+.">
            <InlineSwitch checked={!!jsonData.queryCacheEnabled} onChange={this.onQueryCacheToggle} />
          </InlineField>
          <FormField
            name="QueryCacheTTLInputField"
            label="Cache TTL (s)"
            labelWidth={14}
            inputWidth={12}
            onChange={this.onQueryCacheTTLChange}
            value={jsonData.queryCacheTTLSeconds ?? ''}
            placeholder="60"
            tooltip="How long a successful sync query result can be reused before the plugin asks kdb+ again."
          />
          <FormField
            name="QueryCacheStaleTTLInputField"
            label="Stale TTL (s)"
            labelWidth={14}
            inputWidth={12}
            onChange={this.onQueryCacheStaleTTLChange}
            value={jsonData.queryCacheStaleTTLSeconds ?? ''}
            placeholder="0"
            tooltip="Optional stale-while-revalidate window. Stale results return immediately while the backend refreshes the cache for the next query."
          />
          <FormField
            name="QueryCacheMaxEntriesInputField"
            label="Cache Entries"
            labelWidth={14}
            inputWidth={12}
            onChange={this.onQueryCacheMaxEntriesChange}
            value={jsonData.queryCacheMaxEntries ?? ''}
            placeholder="128"
            tooltip="Maximum cached sync query results kept in memory for this datasource instance."
          />
        </div>
        <div className="gf-form">
          <span className="gf-form-label width-18">Cache Key</span>
          <select className="gf-form-input width-14" value={jsonData.queryCacheKeyMode || 'strict'} onChange={this.onQueryCacheKeyModeChange}>
            <option value="strict">Strict</option>
            <option value="shared">Shared</option>
          </select>
          <FormField
            name="QueryCacheTimeBucketInputField"
            label="Cache Time Bucket (s)"
            labelWidth={18}
            inputWidth={12}
            onChange={this.onQueryCacheTimeBucketChange}
            value={jsonData.queryCacheTimeBucketSeconds ?? ''}
            placeholder="0"
            tooltip="Rounds query time ranges in the cache key. Keep 0 for exact time ranges; use values like 5, 30, or 60 for relative now-based dashboards that should reuse warm results briefly."
          />
        </div>
        <div className="gf-form">
          <InlineField
            label="Disk Cache"
            labelWidth={18}
            tooltip="Persists successful sync query results on the local Grafana server so dashboard reloads can reuse warm results after plugin restarts. Disable for writeback/action queries."
          >
            <InlineSwitch checked={!!jsonData.queryCacheDiskEnabled} onChange={this.onQueryCacheDiskToggle} />
          </InlineField>
          <FormField
            name="QueryCacheDiskMaxEntriesInputField"
            label="Disk Entries"
            labelWidth={14}
            inputWidth={12}
            onChange={this.onQueryCacheDiskMaxEntriesChange}
            value={jsonData.queryCacheDiskMaxEntries ?? ''}
            placeholder="10000"
            tooltip="Maximum cached sync query result files kept on disk for this datasource instance."
          />
          <FormField
            name="QueryCacheDiskMaxBytesInputField"
            label="Disk Bytes"
            labelWidth={14}
            inputWidth={16}
            onChange={this.onQueryCacheDiskMaxBytesChange}
            value={jsonData.queryCacheDiskMaxBytes ?? ''}
            placeholder="1073741824"
            tooltip="Approximate maximum disk cache size in bytes for this datasource instance."
          />
        </div>
        <div className="gf-form">
          <FormField
            name="QueryCacheDiskPathInputField"
            label="Disk Path"
            labelWidth={18}
            inputWidth={52}
            onChange={this.onQueryCacheDiskPathChange}
            value={jsonData.queryCacheDiskPath || ''}
            placeholder="default Grafana data/cache directory"
            tooltip="Optional server-side root directory. The plugin adds a datasource-specific subdirectory under this path."
          />
        </div>
        <div className="gf-form">
          <InlineField
            label="Cache Controls"
            labelWidth={18}
            tooltip="Allows Admin and Editor users to clear memory/disk cache through datasource resource calls and the AsyncQ master data panel. Viewers can still read cache status."
          >
            <InlineSwitch checked={jsonData.queryCacheControlEnabled !== false} onChange={this.onQueryCacheControlToggle} />
          </InlineField>
        </div>
      </ConfigSection>
    );
  }

  renderDefaults(jsonData: MyDataSourceOptions) {
    const showDeferredWrapper = jsonData.executionMode === 'deferredAsync' || !!jsonData.deferredQueryWrapper;
    const showPanopticonDefaults = jsonData.compatibilityMode === 'panopticon' || !!jsonData.panopticonQueryWrapper || !!jsonData.panopticonRequestFunction;

    return (
      <ConfigSection title="Query Defaults">
        <div className="gf-form">
          <span className="gf-form-label width-14">Default Mode</span>
          <select className="gf-form-input width-20" value={jsonData.executionMode || 'sync'} onChange={this.onDefaultExecutionModeChange}>
            <option value="sync">Sync</option>
            <option value="async">Helper Async</option>
            <option value="pluginAsync">Plugin Async</option>
            <option value="deferredAsync">Deferred Async</option>
            <option value="stream">Stream</option>
          </select>
          <span className="gf-form-label width-14">Compatibility</span>
          <select className="gf-form-input width-20" value={jsonData.compatibilityMode || 'native'} onChange={this.onCompatibilityModeChange}>
            <option value="native">Native AsyncQ</option>
            <option value="aquaq">AquaQ</option>
            <option value="panopticon">Panopticon</option>
          </select>
        </div>
        {showDeferredWrapper && (
          <div className="gf-form">
            <FormField
              name="DeferredWrapperInputField"
              label="Deferred Wrapper"
              labelWidth={14}
              inputWidth={48}
              onChange={this.onDeferredWrapperChange}
              value={jsonData.deferredQueryWrapper || ''}
              placeholder=".gateway.defer[{Query}]"
              tooltip="Datasource default wrapper for Deferred Async mode. It must contain exactly one {Query} placeholder."
            />
          </div>
        )}
        {showPanopticonDefaults && (
          <>
            <div className="gf-form">
              <FormField
                name="PanopticonQueryWrapperInputField"
                label="Pano Wrapper"
                labelWidth={14}
                inputWidth={48}
                onChange={this.onPanopticonQueryWrapperChange}
                value={jsonData.panopticonQueryWrapper || ''}
                placeholder=".pano.run[{Query};{TimeWindowStart};{TimeWindowEnd}]"
                tooltip="Datasource default Panopticon wrapper expression. It must contain exactly one {Query} placeholder when set. Grafana-backed dashboard parameters such as {symbol} are expanded in Panopticon mode."
              />
            </div>
            <div className="gf-form">
              <FormField
                name="PanopticonRequestFunctionInputField"
                label="Pano Fn"
                labelWidth={14}
                inputWidth={48}
                onChange={this.onPanopticonRequestFunctionChange}
                value={jsonData.panopticonRequestFunction || ''}
                placeholder="{[req] .pano.run req}"
                tooltip="Optional q function or lambda that accepts the full request dictionary in Panopticon compatibility mode."
              />
            </div>
          </>
        )}
      </ConfigSection>
    );
  }

  renderDiagnostics(jsonData: MyDataSourceOptions) {
    return (
      <ConfigSection title="Diagnostics">
        <div className="gf-form">
          <InlineField
            label="Diagnostics"
            labelWidth={14}
            tooltip="Writes structured backend logs with request IDs, ref IDs, execution modes, query hashes, kdb+ result shapes, frame schemas, and errors. Query text is not logged unless enabled separately."
          >
            <InlineSwitch checked={!!jsonData.diagnosticsEnabled} onChange={this.onDiagnosticsToggle} />
          </InlineField>
          <InlineField
            label="Log Query Text"
            labelWidth={14}
            tooltip="Writes raw query text and wrapper text to backend logs. Enable only in trusted environments where query text does not contain sensitive data."
            disabled={!jsonData.diagnosticsEnabled}
          >
            <InlineSwitch checked={!!jsonData.diagnosticsLogQueryText} disabled={!jsonData.diagnosticsEnabled} onChange={this.onDiagnosticsLogQueryTextToggle} />
          </InlineField>
        </div>
      </ConfigSection>
    );
  }

  render() {
    const options = this.props.options;
    const jsonData = defaults({}, options.jsonData, defaultConfig) as MyDataSourceOptions;
    const secureJsonFields = (options.secureJsonFields || {}) as Record<string, boolean>;
    const secureJsonData = (options.secureJsonData || {}) as MySecureJsonData;

    return (
      <div className="gf-form-group">
        {this.renderConnection(jsonData, secureJsonFields, secureJsonData)}
        {this.renderTLS(jsonData, secureJsonFields, secureJsonData)}
        {this.renderCapabilities(jsonData)}
        {this.renderDefaults(jsonData)}
        {this.renderDiagnostics(jsonData)}
      </div>
    );
  }
}
