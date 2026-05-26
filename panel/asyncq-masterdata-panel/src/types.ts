export type AsyncQMasterDataViewMode = 'master' | 'freshness' | 'diagnostics';

export interface AsyncQMasterDataOptions {
  viewMode: AsyncQMasterDataViewMode;
  timeColumn?: string;
  datasourceUid?: string;
  warnAfterSeconds: number;
  criticalAfterSeconds: number;
  showCache: boolean;
  showControls: boolean;
  previewRows: number;
}

export const defaultOptions: AsyncQMasterDataOptions = {
  viewMode: 'master',
  timeColumn: '',
  datasourceUid: '',
  warnAfterSeconds: 60,
  criticalAfterSeconds: 300,
  showCache: true,
  showControls: true,
  previewRows: 10,
};
