import { execFileSync } from 'node:child_process';
import fs from 'node:fs';

import { expect, test } from '@playwright/test';

test('Excel reporting dashboard downloads a populated workbook', async ({ page }, testInfo) => {
  const queryState = { prices: false, trades: false };

  page.on('response', async (response) => {
    if (!response.url().includes('/api/ds/query') || !response.ok()) {
      return;
    }
    const body = await response.json().catch(() => undefined);
    for (const result of Object.values((body as any)?.results || {})) {
      for (const frame of (result as any)?.frames || []) {
        const fieldNames = ((frame as any)?.schema?.fields || []).map((field: any) => String(field.name));
        const values = (frame as any)?.data?.values || [];
        const rowCount = Array.isArray(values[0]) ? values[0].length : 0;
        if (rowCount > 0 && fieldNames.includes('lastPrice') && fieldNames.includes('trades')) {
          queryState.prices = true;
        }
        if (rowCount > 0 && fieldNames.includes('price') && fieldNames.includes('size')) {
          queryState.trades = true;
        }
      }
    }
  });

  await page.goto('/d/asyncq-excel-report/asyncq-excel-reporting?orgId=1&from=now-5m&to=now&kiosk=tv');
  await expect(page.getByText('Excel report')).toBeVisible();
  await expect.poll(() => queryState.prices && queryState.trades).toBe(true);

  await page.getByLabel('Excel file name').fill('playwright-excel-report');

  const downloadPromise = page.waitForEvent('download', { timeout: 30_000 });
  await page.getByRole('button', { name: 'Download report' }).evaluate((button) => {
    (button as HTMLButtonElement).click();
  });
  const download = await downloadPromise;

  expect(download.suggestedFilename()).toBe('playwright-excel-report.xlsx');
  const workbookPath = testInfo.outputPath('playwright-excel-report.xlsx');
  await download.saveAs(workbookPath);

  expect(fs.statSync(workbookPath).size).toBeGreaterThan(1000);
  expect(unzipList(workbookPath)).toContain('xl/workbook.xml');

  const workbookXml = unzipEntry(workbookPath, 'xl/workbook.xml');
  const sharedStrings = unzipEntry(workbookPath, 'xl/sharedStrings.xml');
  expect(workbookXml).toContain('Summary');
  expect(workbookXml).toContain('Trades');
  expect(sharedStrings).toContain('sym');
  expect(sharedStrings).toContain('lastPrice');
  expect(sharedStrings).toContain('price');
});

function unzipList(filePath: string): string {
  return execFileSync('unzip', ['-Z1', filePath], { encoding: 'utf8' });
}

function unzipEntry(filePath: string, entry: string): string {
  return execFileSync('unzip', ['-p', filePath, entry], { encoding: 'utf8' });
}
