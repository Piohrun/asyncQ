import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

const markerPath = path.join(process.cwd(), 'demo/runtime/e2e-started-demo');

export default async function globalTeardown() {
  const marker = fs.existsSync(markerPath) ? fs.readFileSync(markerPath, 'utf8').trim() : '';
  if (marker === 'started') {
    execFileSync('./scripts/stop-demo-local.sh', { cwd: process.cwd(), stdio: 'inherit' });
  }
  if (fs.existsSync(markerPath)) {
    fs.unlinkSync(markerPath);
  }
}
