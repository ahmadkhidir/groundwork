// Diagnostic: mint an Auth.js session cookie for a given user using the installed @auth/core
// encode() and the local AUTH_SECRET. Prints "name=value" for curl -H "Cookie: ...".
//
// This is a plain CommonJS Node script by design (run directly with `node`, not through the TS
// pipeline), so the TS-only no-require-imports rule does not apply here.
/* eslint-disable @typescript-eslint/no-require-imports */
const fs = require("fs");
const path = require("path");
const { encode } = require("@auth/core/jwt");

function loadEnv(file) {
  const env = {};
  if (!fs.existsSync(file)) return env;
  for (const line of fs.readFileSync(file, "utf8").split(/\r?\n/)) {
    const m = line.match(/^\s*([A-Z0-9_]+)\s*=\s*(.*)\s*$/);
    if (m) env[m[1]] = m[2].replace(/^["']|["']$/g, "");
  }
  return env;
}

const webRoot = path.resolve(__dirname, "..");
const env = loadEnv(path.join(webRoot, ".env.local"));
const secret = env.AUTH_SECRET;
if (!secret) {
  console.error("AUTH_SECRET not found in web/.env.local");
  process.exit(1);
}

const userId = process.argv[2];
const email = process.argv[3] || "user@groundwork.com";
const name = process.argv[4] || "User";
const tier = process.argv[5] || "pro";
const salt = "authjs.session-token";

const token = {
  sub: userId,
  id: userId,
  email,
  name,
  tier,
  tierCheckedAt: Date.now(),
};

encode({ token, secret, salt, maxAge: 30 * 24 * 60 * 60 })
  .then((jwe) => console.log(`authjs.session-token=${jwe}`))
  .catch((err) => {
    console.error(err);
    process.exit(1);
  });
