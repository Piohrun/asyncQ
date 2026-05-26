export interface AsyncQExcelReportOptions {
  datasourceUid: string;
  reportId: string;
  buttonText: string;
  showReportName: boolean;
  showFileNameInput: boolean;
}

export const defaultOptions: AsyncQExcelReportOptions = {
  datasourceUid: '',
  reportId: 'demo-market-report',
  buttonText: 'Download Excel',
  showReportName: true,
  showFileNameInput: true,
};

export interface ExcelReportCatalog {
  reports?: ExcelReportDefinition[];
}

export interface ExcelReportDefinition {
  id: string;
  name?: string;
  description?: string;
  outputName?: string;
  metadata?: Record<string, any>;
}
