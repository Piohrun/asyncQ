import { execFileSync } from 'node:child_process';
import fs from 'node:fs';

import { expect, test } from '@playwright/test';

test('Excel reporting dashboard downloads a populated workbook', async ({ page }, testInfo) => {
  await page.goto('/d/asyncq-excel-report/asyncq-excel-reporting?orgId=1&from=now-5m&to=now&var-reportRows=5000&kiosk=tv');
  await expect(page.getByText('Excel report: stream')).toBeVisible();

  await page.getByLabel('Excel file name').first().fill('playwright-excel-report');

  const downloadPromise = page.waitForEvent('download', { timeout: 30_000 });
  await page.getByRole('button', { name: 'Download stream' }).evaluate((button) => {
    (button as HTMLButtonElement).click();
  });
  const download = await downloadPromise;

  expect(download.suggestedFilename()).toBe('playwright-excel-report.xlsx');
  const workbookPath = testInfo.outputPath('playwright-excel-report.xlsx');
  await download.saveAs(workbookPath);

  expect(fs.statSync(workbookPath).size).toBeGreaterThan(1000);
  expect(unzipList(workbookPath)).toContain('xl/workbook.xml');

  const workbookXml = unzipEntry(workbookPath, 'xl/workbook.xml');
  const summarySheet = unzipEntry(workbookPath, 'xl/worksheets/sheet2.xml');
  const tradesSheet = unzipEntry(workbookPath, 'xl/worksheets/sheet3.xml');
  expect(workbookXml).toContain('Summary');
  expect(workbookXml).toContain('Trades');
  expect(summarySheet).toContain('sym');
  expect(summarySheet).toContain('lastPrice');
  expect(tradesSheet).toContain('price');
});

function unzipList(filePath: string): string {
  return execFileSync('unzip', ['-Z1', filePath], { encoding: 'utf8' });
}

function unzipEntry(filePath: string, entry: string): string {
  return execFileSync('unzip', ['-p', filePath, entry], { encoding: 'utf8', maxBuffer: 20 * 1024 * 1024 });
}
