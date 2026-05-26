import { PanelPlugin } from '@grafana/data';

import { AsyncQMasterDataPanel } from './AsyncQMasterDataPanel';
import { AsyncQMasterDataOptions, defaultOptions } from './types';

export const plugin = new PanelPlugin<AsyncQMasterDataOptions>(AsyncQMasterDataPanel)
  .setDefaults(defaultOptions)
  .setPanelOptions((builder) =>
    builder
      .addRadio({
        path: 'viewMode',
        name: 'View mode',
        defaultValue: defaultOptions.viewMode,
        settings: {
          options: [
            { value: 'master', label: 'Master data' },
            { value: 'freshness', label: 'Freshness' },
            { value: 'diagnostics', label: 'Diagnostics' },
          ],
        },
      })
      .addTextInput({
        path: 'timeColumn',
        name: 'Time column',
        defaultValue: defaultOptions.timeColumn,
      })
      .addTextInput({
        path: 'datasourceUid',
        name: 'Datasource UID',
        description: 'Optional AsyncQ datasource UID for cache controls when no query frame is available.',
        defaultValue: defaultOptions.datasourceUid,
      })
      .addNumberInput({
        path: 'warnAfterSeconds',
        name: 'Warn age seconds',
        defaultValue: defaultOptions.warnAfterSeconds,
        settings: { min: 1, integer: true },
      })
      .addNumberInput({
        path: 'criticalAfterSeconds',
        name: 'Critical age seconds',
        defaultValue: defaultOptions.criticalAfterSeconds,
        settings: { min: 1, integer: true },
      })
      .addBooleanSwitch({
        path: 'showCache',
        name: 'Show cache',
        defaultValue: defaultOptions.showCache,
      })
      .addBooleanSwitch({
        path: 'showControls',
        name: 'Cache controls',
        defaultValue: defaultOptions.showControls,
      })
      .addNumberInput({
        path: 'previewRows',
        name: 'Preview rows',
        defaultValue: defaultOptions.previewRows,
        settings: { min: 1, max: 50, integer: true },
        showIf: (options) => options.viewMode !== 'diagnostics',
      })
  );
