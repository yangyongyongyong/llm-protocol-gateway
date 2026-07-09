import { readFileSync } from "node:fs";
import { startProxy, getProxyPort } from "./proxy.ts";
import { getCursorModels } from "./models.ts";

const tokenFile = process.env.CURSOR_TOKEN_FILE;
if (!tokenFile) {
  console.log(JSON.stringify({ event: "error", message: "CURSOR_TOKEN_FILE is required" }));
  process.exit(1);
}

function readToken(): string {
  return readFileSync(tokenFile, "utf8").trim();
}

try {
  const token = readToken();
  if (!token) {
    throw new Error("cursor access token file is empty");
  }
  const models = await getCursorModels(token);
  await startProxy(async () => readToken(), models);
  const port = getProxyPort();
  if (!port) {
    throw new Error("cursor bridge failed to bind a port");
  }
  console.log(JSON.stringify({ event: "ready", port }));
} catch (error) {
  const message = error instanceof Error ? error.message : String(error);
  console.log(JSON.stringify({ event: "error", message }));
  process.exit(1);
}
