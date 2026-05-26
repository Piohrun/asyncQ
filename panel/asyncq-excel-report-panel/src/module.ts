import { PanelPlugin } from '@grafana/data';

import { AsyncQExcelReportPanel } from './AsyncQExcelReportPanel';
import { AsyncQExcelReportOptions, defaultOptions } from './types';

export const plugin = new PanelPlugin<AsyncQExcelReportOptions>(AsyncQExcelReportPanel)
  .setDefaults(defaultOptions)
  .setPanelOptions((builder) =>
    builder
      .addTextInput({
        path: 'datasourceUid',
        name: 'Datasource UID',
        description: 'AsyncQ datasource UID used to generate the workbook.',
        defaultValue: defaultOptions.datasourceUid,
      })
      .addTextInput({
        path: 'reportId',
        name: 'Report ID',
        description: 'ID from the datasource Excel report catalog.',
        defaultValue: defaultOptions.reportId,
      })
      .addTextInput({
        path: 'buttonText',
        name: 'Button text',
        defaultValue: defaultOptions.buttonText,
      })
      .addBooleanSwitch({
        path: 'showReportName',
        name: 'Show report name',
        defaultValue: defaultOptions.showReportName,
      })
      .addBooleanSwitch({
        path: 'showFileNameInput',
        name: 'Show filename input',
        defaultValue: defaultOptions.showFileNameInput,
      })
  );
