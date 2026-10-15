// Disposable integration fixture only. Keeps real OIDC HTTPS validation enabled.
// Receives no developer credentials; never logs URLs, headers, or request bodies.
const fs = require('node:fs');
const http = require('node:http');
const https = require('node:https');

function port(name) {
  const value = Number(process.env[name]);
  if (!Number.isInteger(value) || value < 1 || value > 65535) {
    throw new Error('Invalid fixture port');
  }
  return value;
}

const upstreamPort = port('UPSTREAM_HTTP_PORT');
const certificateDirectory = process.env.FIXTURE_CERT_DIR || '/fixture';
const server = https.createServer({
  cert: fs.readFileSync(`${certificateDirectory}/ca.crt`),
  key: fs.readFileSync(`${certificateDirectory}/ca.key`),
}, (request, response) => {
  const upstream = http.request({
    hostname: '127.0.0.1', port: upstreamPort,
    method: request.method, path: request.url,
    headers: { ...request.headers, 'x-forwarded-proto': 'https' },
    timeout: 10000,
  }, (result) => {
    response.writeHead(result.statusCode, result.headers);
    result.on('error', () => response.destroy());
    result.pipe(response);
  });
  upstream.on('timeout', () => upstream.destroy());
  upstream.on('error', () => {
    if (!response.headersSent) response.writeHead(502);
    response.end();
  });
  request.on('aborted', () => upstream.destroy());
  response.on('close', () => upstream.destroy());
  request.pipe(upstream);
});
server.requestTimeout = 15000;
server.headersTimeout = 10000;
server.listen(port('FIXTURE_TLS_PORT'), '0.0.0.0', () => {
  if (process.env.FIXTURE_READY_SIGNAL === 'true') process.stdout.write('ready\n');
});
process.on('SIGTERM', () => {
  server.close();
  server.closeAllConnections();
});
