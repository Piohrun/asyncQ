import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

const baseURL = process.env.ASYNCQ_E2E_BASE_URL || 'http://localhost:3000';
const markerPath = path.join(process.cwd(), 'demo/runtime/e2e-started-demo');

export default async function globalSetup() {
  if (await grafanaHealthy()) {
    writeMarker('reused');
    return;
  }

  execFileSync('./scripts/start-demo-local.sh', { cwd: process.cwd(), stdio: 'inherit' });
  writeMarker('started');
  await waitForGrafana();
}

async function waitForGrafana() {
  const deadline = Date.now() + 180_000;
  while (Date.now() < deadline) {
    if (await grafanaHealthy()) {
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }
  throw new Error(`Grafana demo did not become healthy at ${baseURL}`);
}

async function grafanaHealthy(): Promise<boolean> {
  try {
    const response = await fetch(`${baseURL}/api/health`);
    return response.ok;
  } catch {
    return false;
  }
}

function writeMarker(value: 'started' | 'reused') {
  fs.mkdirSync(path.dirname(markerPath), { recursive: true });
  fs.writeFileSync(markerPath, value);
}
