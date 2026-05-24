import { defaults } from 'lodash';
import React, { ChangeEvent, PureComponent, SyntheticEvent, FormEvent } from 'react';
import {InlineField, InlineSwitch, LegacyForms} from '@grafana/ui';
import { DataSourcePluginOptionsEditorProps } from '@grafana/data';
import {defaultConfig, MyDataSourceOptions, MySecureJsonData} from './types';
import {TextArea }  from '@grafana/ui';
const { FormField, SecretFormField } = LegacyForms;

interface Props extends DataSourcePluginOptionsEditorProps<MyDataSourceOptions> {}

interface State {}

export class ConfigEditor extends PureComponent<Props, State> {

  state = {
    displayTLS:false
  }

  onHostChange = (event: ChangeEvent<HTMLInputElement>) => {

    const { onOptionsChange, options } = this.props;
    const jsonData = {
      ...options.jsonData,
      host: event.target.value,
    };
    // @ts-ignore
    onOptionsChange({ ...options, jsonData });
  }

  onPortChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onOptionsChange, options } = this.props;

    if((/^\d+$/.test(event.target.value) || event.target.value==="")){
      const jsonData = {
        ...options.jsonData,
        port: parseInt(event.target.value, 10),
      };
      onOptionsChange({ ...options, jsonData });
    }
  }
  onTimeoutChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onOptionsChange, options } = this.props;

    if((/^\d+$/.test(event.target.value) || event.target.value==="")){
      const jsonData = {
        ...options.jsonData,
        timeout: event.target.value,
      };
      onOptionsChange({ ...options, jsonData });
    }
  }
  onUsernameChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onOptionsChange, options } = this.props;
    const { secureJsonData } = options;
    onOptionsChange({
      ...options,
      secureJsonData: {
        ...secureJsonData,
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
    const { secureJsonData } = options;
    onOptionsChange({
      ...options,
      secureJsonData: {
        ...secureJsonData,
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

  onTlsToggle = (e: SyntheticEvent) => {

    const { onOptionsChange, options } = this.props;
    const jsonData = {
      ...options.jsonData,
      withTLS: !options.jsonData.withTLS
    };
    // @ts-ignore
    onOptionsChange({ ...options, jsonData });
  };

  onSkipTlsToggle = (e: SyntheticEvent) => {

    const { onOptionsChange, options } = this.props;
    const jsonData = {
      ...options.jsonData,
      skipVerifyTLS: !options.jsonData.skipVerifyTLS
    };
    // @ts-ignore
    onOptionsChange({ ...options, jsonData });
  };

  onCaCertToggle = (e: SyntheticEvent) => {

    const { onOptionsChange, options } = this.props;
    const jsonData = {
      ...options.jsonData,
      withCACert: !options.jsonData.withCACert
    };
    // @ts-ignore
    onOptionsChange({ ...options, jsonData });
  };
  onEnableAsyncToggle = (e: SyntheticEvent) => {
    const { onOptionsChange, options } = this.props;
    const jsonData = {
      ...options.jsonData,
      enableAsync: !(options.jsonData.enableAsync !== false)
    };
    onOptionsChange({ ...options, jsonData });
  };

  onEnableStreamingToggle = (e: SyntheticEvent) => {
    const { onOptionsChange, options } = this.props;
    const jsonData = {
      ...options.jsonData,
      enableStreaming: !(options.jsonData.enableStreaming !== false)
    };
    onOptionsChange({ ...options, jsonData });
  };

  onDiagnosticsToggle = (e: SyntheticEvent) => {
    const { onOptionsChange, options } = this.props;
    const nextEnabled = !options.jsonData.diagnosticsEnabled;
    const jsonData = {
      ...options.jsonData,
      diagnosticsEnabled: nextEnabled,
      diagnosticsLogQueryText: nextEnabled ? options.jsonData.diagnosticsLogQueryText : false,
    };
    onOptionsChange({ ...options, jsonData });
  };

  onDiagnosticsLogQueryTextToggle = (e: SyntheticEvent) => {
    const { onOptionsChange, options } = this.props;
    const jsonData = {
      ...options.jsonData,
      diagnosticsLogQueryText: !options.jsonData.diagnosticsLogQueryText
    };
    onOptionsChange({ ...options, jsonData });
  };

  onDefaultExecutionModeChange = (event: ChangeEvent<HTMLSelectElement>) => {
    const { onOptionsChange, options } = this.props;
    const jsonData = {
      ...options.jsonData,
      executionMode: event.target.value as MyDataSourceOptions['executionMode'],
    };
    onOptionsChange({ ...options, jsonData });
  };

  onCompatibilityModeChange = (event: ChangeEvent<HTMLSelectElement>) => {
    const { onOptionsChange, options } = this.props;
    const jsonData = {
      ...options.jsonData,
      compatibilityMode: event.target.value as MyDataSourceOptions['compatibilityMode'],
    };
    onOptionsChange({ ...options, jsonData });
  };

  onDeferredWrapperChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onOptionsChange, options } = this.props;
    const jsonData = {
      ...options.jsonData,
      deferredQueryWrapper: event.target.value,
    };
    onOptionsChange({ ...options, jsonData });
  };

  onPanopticonQueryWrapperChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onOptionsChange, options } = this.props;
    const jsonData = {
      ...options.jsonData,
      panopticonQueryWrapper: event.target.value,
    };
    onOptionsChange({ ...options, jsonData });
  };

  onPanopticonRequestFunctionChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onOptionsChange, options } = this.props;
    const jsonData = {
      ...options.jsonData,
      panopticonRequestFunction: event.target.value,
    };
    onOptionsChange({ ...options, jsonData });
  };

  onAsyncMaxJobsChange = (event: ChangeEvent<HTMLInputElement>) => {
    const { onOptionsChange, options } = this.props;
    if((/^\d+$/.test(event.target.value) || event.target.value==="")){
      const jsonData = {
        ...options.jsonData,
        asyncMaxJobs: parseInt(event.target.value, 10),
      };
      onOptionsChange({ ...options, jsonData });
    }
  };

  onTlsCertificateChange = (event: FormEvent<HTMLTextAreaElement>) => {
    const { onOptionsChange, options } = this.props;
    const { secureJsonData } = options;
    onOptionsChange({
      ...options,
      secureJsonData: {
        ...secureJsonData,
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
    const { secureJsonData } = options;
    onOptionsChange({
      ...options,
      secureJsonData: {
        ...secureJsonData,
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
    const { secureJsonData } = options;
    onOptionsChange({
      ...options,
      secureJsonData: {
        ...secureJsonData,
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




  renderTLS = () => {
    const { options } = this.props;
    const { secureJsonFields } = options;
    const secureJsonData = (options.secureJsonData || {}) as MySecureJsonData;

    return (
        <>
          <div className="gf-form">
            {secureJsonFields.tlsKey ? <SecretFormField
                name="TLSKeyInputField"
                isConfigured={(secureJsonFields && secureJsonFields.tlsKey) as boolean}
                value={secureJsonData.tlsKey || ''}
                label="TLS Key"
                placeholder="TLS Key"
                labelWidth={7}
                inputWidth={20}
                onReset={this.onTlsKeyReset}
                //onChange={this.onTlsKeyChange}
            /> :
            <InlineField label="TLS Key" labelWidth={14} grow={true}>
              <TextArea
                style={{width: 320}}
                placeholder="TLS Key"
                value={secureJsonData.tlsKey || ''} 
                name="TLSKeyInputField"
                onChange={this.onTlsKeyChange}/>
            </InlineField>}
          </div>

          <div className="gf-form">
            {secureJsonFields.tlsCertificate ?
                <SecretFormField
                    name="TLSCertInputField"
                    isConfigured={(secureJsonFields && secureJsonFields.tlsCertificate) as boolean}
                    value={secureJsonData.tlsCertificate || ''}
                    label="TLS Certificate"
                    placeholder="TLS Certificate"
                    labelWidth={7}
                    inputWidth={20}
                    onReset={this.onTlsCertificateReset}
                    //onChange={this.onTlsCertificateChange}
                /> :
                <InlineField label="TLS Certificate" labelWidth={14} grow={true}>
                  <TextArea
                    style={{width: 320}}
                    placeholder="TLS Certificate"
                    value={secureJsonData.tlsCertificate}
                    name="TLSCertInputField"
                    onChange={this.onTlsCertificateChange}/>
                </InlineField>
            }
          </div>
          {options.jsonData.withCACert &&
          <div className="gf-form">
            {secureJsonFields.caCert ?
              <SecretFormField
                name="TLSCAInputField"
                isConfigured={(secureJsonFields && secureJsonFields.caCert) as boolean}
                value={secureJsonData.caCert || ''}
                label="CA Certificate"
                placeholder="CA Certificate"
                labelWidth={7}
                inputWidth={20}
                onReset={this.onCaCertReset}
                //onChange={this.onCaCertChange}
            />:
              <InlineField label="CA Certificate" labelWidth={14} grow={true}>
                <TextArea
                  style={{width: 320}}
                  placeholder="CA Certificate"
                  value={secureJsonData.caCert}
                  name="TLSCAInputField"
                  onChange={this.onCaCertChange}/>
              </InlineField>}
          </div>}
        </>
    )
  }

  render() {
    const { options } = defaults(this.props, defaultConfig);
    const { jsonData, secureJsonFields } = options;
    const secureJsonData = (options.secureJsonData || {}) as MySecureJsonData;
    return (
        <div className="gf-form-group">

          <div className="gf-form">
            <FormField
                name="HostInputField"
                label="Host"
                labelWidth={7}
                inputWidth={20}
                onChange={this.onHostChange}
                value={jsonData.host || ''}
                placeholder="Please enter host URL"
            />
          </div>
          <div className="gf-form">
            <FormField
                name="PortInputField"
                label="Port"
                labelWidth={7}
                inputWidth={20}
                onChange={this.onPortChange}
                value={jsonData.port || ''}
                placeholder="Please enter host port"
            />
          </div>
          <div className="gf-form">

            <SecretFormField
                name="UsernameInputField"
                isConfigured={(secureJsonFields && secureJsonFields.username) as boolean}
                value={secureJsonData.username || ''}
                label="Username"
                placeholder="Username"
                labelWidth={7}
                inputWidth={20}
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
                labelWidth={7}
                inputWidth={20}
                onReset={this.onResetPassword}
                onChange={this.onPasswordChange}
            />
          </div>
          {!options.jsonData.withTLS &&
          <div className="gf-form">
            <FormField
                name="TimeoutInputField"
                label="Timeout (ms)"
                labelWidth={7}
                inputWidth={20}
                onChange={this.onTimeoutChange}
                value={jsonData.timeout || ''}
                placeholder="Please set timeout"
            />
          </div>}


          {options.jsonData.withTLS && <>{this.renderTLS()}</>}
          <div className="gf-form">
            <InlineField
                label="TLS Client Auth"
                labelWidth={14}>
              <InlineSwitch checked={options.jsonData.withTLS} onChange={this.onTlsToggle} />
            </InlineField>
            {options.jsonData.withTLS && <>
              <InlineField
                  label="Skip TLS Verify"
                  labelWidth={14}>
                <InlineSwitch checked={options.jsonData.skipVerifyTLS} onChange={this.onSkipTlsToggle} />
              </InlineField>
              <InlineField
                  label="With CA Cert"
                  labelWidth={14}>
                <InlineSwitch checked={options.jsonData.withCACert} onChange={this.onCaCertToggle} />
              </InlineField>
            </>}
          </div>
          <div className="gf-form">
            <InlineField
                label="Async Queries"
                labelWidth={14}>
              <InlineSwitch checked={options.jsonData.enableAsync !== false} onChange={this.onEnableAsyncToggle} />
            </InlineField>
            <InlineField
                label="Streaming"
                labelWidth={14}>
              <InlineSwitch checked={options.jsonData.enableStreaming !== false} onChange={this.onEnableStreamingToggle} />
            </InlineField>
          </div>
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
          <div className="gf-form">
            <FormField
              name="DeferredWrapperInputField"
              label="Deferred Wrapper"
              labelWidth={14}
              inputWidth={40}
              onChange={this.onDeferredWrapperChange}
              value={jsonData.deferredQueryWrapper || ''}
              placeholder=".gateway.defer[{Query}]"
              tooltip="Datasource default wrapper for Deferred Async mode. It must contain exactly one {Query} placeholder."
            />
          </div>
          <div className="gf-form">
            <FormField
              name="PanopticonQueryWrapperInputField"
              label="Pano Wrapper"
              labelWidth={14}
              inputWidth={40}
              onChange={this.onPanopticonQueryWrapperChange}
              value={jsonData.panopticonQueryWrapper || ''}
              placeholder=".pano.run[{Query};{TimeWindowStart};{TimeWindowEnd}]"
              tooltip="Datasource default Panopticon wrapper expression. It must contain exactly one {Query} placeholder when set."
            />
          </div>
          <div className="gf-form">
            <FormField
              name="PanopticonRequestFunctionInputField"
              label="Pano Fn"
              labelWidth={14}
              inputWidth={40}
              onChange={this.onPanopticonRequestFunctionChange}
              value={jsonData.panopticonRequestFunction || ''}
              placeholder="{[req] .pano.run req}"
              tooltip="Optional q function or lambda that accepts the full request dictionary in Panopticon compatibility mode."
            />
          </div>
          <div className="gf-form">
            <FormField
              name="AsyncMaxJobsInputField"
              label="Async Max Jobs"
              labelWidth={14}
              inputWidth={20}
              onChange={this.onAsyncMaxJobsChange}
              value={jsonData.asyncMaxJobs || ''}
              placeholder="16"
              tooltip="Maximum plugin-managed async jobs for this datasource instance."
            />
          </div>
          <div className="gf-form">
            <InlineField
              label="Diagnostics"
              labelWidth={14}
              tooltip="Writes structured backend logs with request IDs, ref IDs, execution modes, query hashes, kdb+ result shapes, frame schemas, and errors. Query text is not logged unless enabled separately."
            >
              <InlineSwitch checked={!!options.jsonData.diagnosticsEnabled} onChange={this.onDiagnosticsToggle} />
            </InlineField>
            <InlineField
              label="Log Query Text"
              labelWidth={14}
              tooltip="Writes raw query text and wrapper text to backend logs. Enable only in trusted environments where query text does not contain sensitive data."
              disabled={!options.jsonData.diagnosticsEnabled}
            >
              <InlineSwitch
                checked={!!options.jsonData.diagnosticsLogQueryText}
                disabled={!options.jsonData.diagnosticsEnabled}
                onChange={this.onDiagnosticsLogQueryTextToggle}
              />
            </InlineField>
          </div>
        </div>
    );
  }
}
