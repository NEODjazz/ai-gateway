// Run in the pinned LibreChat image, with the public profile mounted read-only.
// Never print configuration values; credentials are not required for validation.
const fs = require('node:fs');
const { configSchema } = require('librechat-data-provider');
const parsed = configSchema.safeParse(JSON.parse(fs.readFileSync(process.argv[2], 'utf8')));
if (!parsed.success) {
  process.stderr.write(JSON.stringify({
    valid: false,
    issues: parsed.error.issues.map(issue => ({ path: issue.path, code: issue.code })),
  }) + '\n');
  process.exit(1);
}
const { getOpenAIConfig } = require('@librechat/api');
const endpoint = parsed.data.endpoints.custom.find(item => item.name === 'AI Gateway');
const sdk = getOpenAIConfig('fixture', {
  modelOptions: { model: 'fixture' },
  addParams: endpoint.addParams,
}, endpoint.name);
if (sdk.llmConfig.maxRetries !== 0 || Object.hasOwn(sdk.llmConfig.modelKwargs || {}, 'maxRetries')) {
  throw new Error('Gateway retry policy must be consumed by the SDK, not forwarded as an API parameter');
}
process.stdout.write('PASS pinned LibreChat configuration schema and SDK retry policy\n');
