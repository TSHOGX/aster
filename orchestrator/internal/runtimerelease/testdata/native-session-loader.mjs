// Test-only loader: execute the exact Go-patched module against pinned native
// dependencies without modifying the installed package or its relative imports.
import { registerHooks } from 'node:module';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
const source = readFileSync(process.env.ASTER_TEST_NATIVE_SESSION, 'utf8');
registerHooks({
  load(url, context, nextLoad) {
    if (url.endsWith('/pi-coding-agent/dist/core/session-manager.js')) {
      return { format: 'module', source, shortCircuit: true };
    }
    if (url.endsWith('/pi-coding-agent/dist/index.js')) {
      return { format: 'module', source: readFileSync(join(dirname(process.env.ASTER_TEST_NATIVE_SESSION), '../index.js'), 'utf8'), shortCircuit: true };
    }
    return nextLoad(url, context);
  },
});
